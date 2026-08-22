// Package jsonl implements eventcapture.Sink as an append-only,
// size-rotated, newline-delimited JSON file. Each record is marshaled as one
// line; the record's own JSON tags define the wire shape, so the sink
// prescribes nothing about content. Rotated files are renamed
// path.<timestamp> with a fixed-width stamp whose lexical order is
// chronological order, and the oldest are pruned so at most MaxFiles rotated
// siblings are retained alongside the live file.
package jsonl

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/eventcapture"
	"github.com/primandproper/platform-go/v13/observability/logging"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

const (
	// DefaultMaxBytes is the rotation threshold when Config.MaxBytes is unset.
	DefaultMaxBytes int64 = 64 << 20
	// DefaultMaxFiles is the retained rotated-file count when Config.MaxFiles
	// is unset.
	DefaultMaxFiles = 8

	// rotatedLayout stamps rotated files. Fixed-width and
	// second-plus-nanosecond precise, so lexical order of rotated names is
	// chronological order and two rotations can never collide on a name.
	rotatedLayout = "20060102T150405.000000000"
)

// Config configures a JSONL sink.
type Config struct {
	// Path is the live file's location; parent directories are created as
	// needed.
	Path string `env:"PATH" json:"path,omitempty" yaml:"path,omitempty"`
	// MaxBytes is the size at which the live file rotates aside.
	MaxBytes int64 `env:"MAX_BYTES" json:"maxBytes,omitempty" yaml:"maxBytes,omitempty"`
	// MaxFiles is how many rotated files are retained; older ones are pruned.
	MaxFiles int `env:"MAX_FILES" json:"maxFiles,omitempty" yaml:"maxFiles,omitempty"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates a Config struct.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Path, validation.Required),
	)
}

// EnsureDefaults fills unset knobs with the package defaults.
func (cfg *Config) EnsureDefaults() {
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = DefaultMaxBytes
	}
	if cfg.MaxFiles <= 0 {
		cfg.MaxFiles = DefaultMaxFiles
	}
}

var _ eventcapture.Sink = (*Sink)(nil)

// Sink appends records to a newline-delimited JSON file, rotating it by
// size. Writes go through a bufio.Writer; Flush (called on the Recorder's
// tick) makes the file tail-able, and Close flushes and closes it.
type Sink struct {
	clock    clock.Clock
	f        *os.File
	w        *bufio.Writer
	logger   logging.Logger
	path     string
	written  int64
	maxBytes int64
	maxFiles int
	mu       sync.Mutex
}

// Option configures a Sink.
type Option func(*Sink)

// WithClock swaps the clock used to stamp rotated files. Tests generally do
// not need it: under testing/synctest the default clock already runs on
// bubble time, which makes the stamps deterministic.
func WithClock(c clock.Clock) Option {
	return func(s *Sink) {
		if c != nil {
			s.clock = c
		}
	}
}

// WithLogger attaches a logger. Rotation moves capture data to a new path and
// pruning deletes it outright; without a logger both happen invisibly, and a
// prune that keeps failing looks exactly like one that is working until the
// disk fills.
func WithLogger(logger logging.Logger) Option {
	return func(s *Sink) {
		s.logger = logging.NewNamedLogger(logger, "jsonl_capture_sink")
	}
}

// NewSink opens (creating if needed, appending if present) the JSONL file at
// cfg.Path. The live file's byte count is resumed from its current size, so
// rotation thresholds survive a restart.
func NewSink(cfg *Config, opts ...Option) (*Sink, error) {
	if cfg == nil {
		return nil, errors.New("nil jsonl sink config provided")
	}
	if cfg.Path == "" {
		return nil, errors.New("jsonl sink path is required")
	}
	cfg.EnsureDefaults()

	s := &Sink{
		path:     filepath.Clean(cfg.Path),
		maxBytes: cfg.MaxBytes,
		maxFiles: cfg.MaxFiles,
		clock:    clock.NewClock(),
		logger:   logging.NewNamedLogger(nil, "jsonl_capture_sink"),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}

	if dir := filepath.Dir(s.path); dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, errors.Wrap(err, "creating sink directory")
		}
	}
	if err := s.openLocked(); err != nil {
		return nil, err
	}

	return s, nil
}

// Write marshals one record and appends it as a line, rotating first if the
// line would push the live file past MaxBytes. A line larger than MaxBytes on
// its own is still written (to a fresh file) rather than lost.
func (s *Sink) Write(record any) error {
	line, err := json.Marshal(record)
	if err != nil {
		return errors.Wrap(err, "marshaling record")
	}
	line = append(line, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.f == nil {
		return errors.New("sink is closed")
	}
	if s.written > 0 && s.written+int64(len(line)) > s.maxBytes {
		if err = s.rotateLocked(); err != nil {
			return err
		}
	}

	n, err := s.w.Write(line)
	s.written += int64(n)
	if err != nil {
		return errors.Wrap(err, "writing record")
	}

	return nil
}

// Flush pushes buffered lines to the OS so the file can be tailed between
// rotations.
func (s *Sink) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.w == nil {
		return nil
	}

	return s.w.Flush()
}

// Close flushes and closes the live file. Safe to call more than once.
func (s *Sink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.f == nil {
		return nil
	}

	flushErr := s.w.Flush()
	closeErr := s.f.Close()
	s.f, s.w = nil, nil
	if flushErr != nil {
		return flushErr
	}

	return closeErr
}

// openLocked opens the live file for appending and resumes the byte count
// from its current size, so rotation thresholds survive a restart.
func (s *Sink) openLocked() error {
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return errors.Wrap(err, "opening sink file")
	}

	info, err := f.Stat()
	if err != nil {
		return errors.Wrap(err, "stating sink file")
	}

	s.f = f
	s.w = bufio.NewWriter(f)
	s.written = info.Size()

	return nil
}

// rotateLocked closes the live file, renames it aside with a timestamp,
// reopens a fresh one, and prunes the oldest rotated siblings beyond
// MaxFiles.
func (s *Sink) rotateLocked() error {
	if err := s.w.Flush(); err != nil {
		return errors.Wrap(err, "flushing before rotation")
	}
	if err := s.f.Close(); err != nil {
		return errors.Wrap(err, "closing before rotation")
	}
	s.f, s.w = nil, nil

	rotated := s.path + "." + s.clock.Now().UTC().Format(rotatedLayout)
	if err := os.Rename(s.path, rotated); err != nil {
		return errors.Wrap(err, "rotating sink file")
	}

	s.logger.WithValues(map[string]any{"rotated_to": rotated, "bytes": s.written}).Info("rotated capture file")

	if err := s.openLocked(); err != nil {
		return err
	}
	s.pruneLocked()

	return nil
}

// pruneLocked deletes the oldest rotated files until at most MaxFiles remain.
// The rotation stamp is fixed-width, so lexical order is age order. Prune
// failures do not fail the write — losing old capture files beats failing a
// write — but they are logged, because a prune that never succeeds is
// indistinguishable from one that is working right up until the disk fills.
func (s *Sink) pruneLocked() {
	rotated, err := filepath.Glob(s.path + ".*")
	if err != nil {
		s.logger.Error("listing rotated capture files for pruning", err)

		return
	}
	if len(rotated) <= s.maxFiles {
		return
	}

	sort.Strings(rotated)
	for _, old := range rotated[:len(rotated)-s.maxFiles] {
		if removeErr := os.Remove(old); removeErr != nil {
			// Best effort; the next rotation retries.
			s.logger.WithValue("path", old).Error("pruning rotated capture file", removeErr)
		}
	}
}
