package jsonl

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	clockmock "github.com/primandproper/platform-go/v13/clock/mock"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

type record struct {
	Name string `json:"name"`
	Seq  int    `json:"seq"`
}

// newTestSink builds a Sink over path. The rotation tests run inside a
// synctest bubble, where the production clock reads bubble time, so a
// time.Sleep between writes stamps each rotated file distinctly.
func newTestSink(t *testing.T, path string, maxBytes int64, maxFiles int) *Sink {
	t.Helper()

	s, err := NewSink(&Config{Path: path, MaxBytes: maxBytes, MaxFiles: maxFiles})
	must.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	return s
}

// testCapturePath is the temp capture file a test's sinks write to.
func testCapturePath(t *testing.T) string {
	t.Helper()

	return filepath.Join(t.TempDir(), "capture.jsonl")
}

func readLines(t *testing.T, path string) []record {
	t.Helper()

	f, err := os.Open(path)
	must.NoError(t, err)
	defer func() { _ = f.Close() }()

	var out []record
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var r record
		must.NoError(t, json.Unmarshal(scanner.Bytes(), &r))
		out = append(out, r)
	}
	must.NoError(t, scanner.Err())

	return out
}

func rotatedSiblings(t *testing.T, path string) []string {
	t.Helper()

	rotated, err := filepath.Glob(path + ".*")
	must.NoError(t, err)

	return rotated
}

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("path is required", func(t *testing.T) {
		t.Parallel()

		test.Error(t, (&Config{}).ValidateWithContext(t.Context()))
	})

	T.Run("a path is enough", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, (&Config{Path: "/var/log/capture.jsonl"}).ValidateWithContext(t.Context()))
	})
}

func TestConfig_EnsureDefaults(T *testing.T) {
	T.Parallel()

	T.Run("fills unset knobs", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Path: "capture.jsonl"}
		cfg.EnsureDefaults()
		test.EqOp(t, DefaultMaxBytes, cfg.MaxBytes)
		test.EqOp(t, DefaultMaxFiles, cfg.MaxFiles)
	})

	T.Run("leaves set knobs alone", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Path: "capture.jsonl", MaxBytes: 17, MaxFiles: 3}
		cfg.EnsureDefaults()
		test.EqOp(t, int64(17), cfg.MaxBytes)
		test.EqOp(t, 3, cfg.MaxFiles)
	})
}

func TestNewSink(T *testing.T) {
	T.Parallel()

	T.Run("rejects nil config and empty path", func(t *testing.T) {
		t.Parallel()

		_, err := NewSink(nil)
		test.Error(t, err)

		_, err = NewSink(&Config{})
		test.Error(t, err)
	})

	T.Run("creates parent directories", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), "nested", "deeper", "capture.jsonl")
		s, err := NewSink(&Config{Path: path})
		must.NoError(t, err)
		must.NoError(t, s.Close())
	})

	T.Run("nil options are skipped", func(t *testing.T) {
		t.Parallel()

		s, err := NewSink(&Config{Path: testCapturePath(t)}, nil, WithLogger(loggingnoop.NewLogger()), WithClock(nil))
		must.NoError(t, err)
		must.NoError(t, s.Close())
	})

	T.Run("a parent path that is a file fails", func(t *testing.T) {
		t.Parallel()

		notADir := filepath.Join(t.TempDir(), "not-a-dir")
		must.NoError(t, os.WriteFile(notADir, []byte("x"), 0o600))

		_, err := NewSink(&Config{Path: filepath.Join(notADir, "capture.jsonl")})
		test.Error(t, err)
	})

	T.Run("a path that is a directory fails to open", func(t *testing.T) {
		t.Parallel()

		// The parent exists, so MkdirAll is a no-op and the failure comes from
		// opening the directory itself for appending.
		_, err := NewSink(&Config{Path: t.TempDir()})
		test.Error(t, err)
	})
}

func TestSink_WriteAndFlush(T *testing.T) {
	T.Parallel()

	T.Run("records land one JSON line each", func(t *testing.T) {
		t.Parallel()

		path := testCapturePath(t)
		s := newTestSink(t, path, DefaultMaxBytes, DefaultMaxFiles)

		must.NoError(t, s.Write(&record{Name: "a", Seq: 1}))
		must.NoError(t, s.Write(&record{Name: "b", Seq: 2}))
		must.NoError(t, s.Flush())

		lines := readLines(t, path)
		must.SliceLen(t, 2, lines)
		test.EqOp(t, "a", lines[0].Name)
		test.EqOp(t, 2, lines[1].Seq)
	})

	T.Run("writing to a closed sink errors", func(t *testing.T) {
		t.Parallel()

		s := newTestSink(t, testCapturePath(t), DefaultMaxBytes, DefaultMaxFiles)

		must.NoError(t, s.Close())
		// A second Close is a no-op.
		must.NoError(t, s.Close())

		test.Error(t, s.Write(&record{Name: "late"}))
		// Flushing a closed sink is a no-op rather than an error: Close already
		// flushed, so there is nothing left owed to the file.
		test.NoError(t, s.Flush())
	})

	T.Run("an unmarshalable record errors without touching the file", func(t *testing.T) {
		t.Parallel()

		path := testCapturePath(t)
		s := newTestSink(t, path, DefaultMaxBytes, DefaultMaxFiles)

		test.Error(t, s.Write(make(chan int)))
		must.NoError(t, s.Flush())
		test.SliceLen(t, 0, readLines(t, path))
	})

	T.Run("a write failure surfaces once the buffer has one", func(t *testing.T) {
		t.Parallel()

		s := newTestSink(t, testCapturePath(t), DefaultMaxBytes, DefaultMaxFiles)

		must.NoError(t, s.Write(&record{Name: "buffered", Seq: 1}))
		must.NoError(t, s.f.Close())
		// The failed flush leaves the buffered writer in a sticky error state.
		test.Error(t, s.Flush())

		test.Error(t, s.Write(&record{Name: "doomed", Seq: 2}))
	})

	T.Run("Close surfaces a flush failure", func(t *testing.T) {
		t.Parallel()

		s := newTestSink(t, testCapturePath(t), DefaultMaxBytes, DefaultMaxFiles)

		// Buffered, so the descriptor has not been touched yet.
		must.NoError(t, s.Write(&record{Name: "buffered"}))
		// Pull the descriptor out from under the buffer: the flush Close owes
		// the file now has nowhere to go.
		must.NoError(t, s.f.Close())

		test.Error(t, s.Close())
	})
}

func TestSink_Rotation(T *testing.T) {
	T.Parallel()

	T.Run("rotates by size and prunes to MaxFiles", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			path := testCapturePath(t)
			// Tiny threshold: every second write rotates.
			s := newTestSink(t, path, 40, 2)

			for seq := range 8 {
				must.NoError(t, s.Write(&record{Name: "rotate-me", Seq: seq}))
				// Distinct stamps for distinct rotations.
				time.Sleep(time.Second)
			}
			must.NoError(t, s.Flush())

			rotated := rotatedSiblings(t, path)
			test.SliceLen(t, 2, rotated)

			// The live file still has the newest record.
			lines := readLines(t, path)
			must.SliceLen(t, 1, lines)
			test.EqOp(t, 7, lines[0].Seq)
		})
	})

	T.Run("an oversized record is written whole to a fresh file", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			path := testCapturePath(t)
			s := newTestSink(t, path, 40, 4)

			must.NoError(t, s.Write(&record{Name: "small", Seq: 1}))
			time.Sleep(time.Second)

			big := record{Name: string(make([]byte, 200)), Seq: 2}
			must.NoError(t, s.Write(&big))
			must.NoError(t, s.Flush())

			// The small record rotated aside; the oversized one lives alone.
			test.SliceLen(t, 1, rotatedSiblings(t, path))
			lines := readLines(t, path)
			must.SliceLen(t, 1, lines)
			test.EqOp(t, 2, lines[0].Seq)
		})
	})

	T.Run("byte count resumes across a reopen", func(t *testing.T) {
		t.Parallel()

		path := testCapturePath(t)

		first, err := NewSink(&Config{Path: path, MaxBytes: 60, MaxFiles: 4})
		must.NoError(t, err)
		must.NoError(t, first.Write(&record{Name: "before-restart", Seq: 1}))
		must.NoError(t, first.Close())

		// A new sink over the same path inherits the existing bytes, so this
		// write pushes past MaxBytes and rotates rather than growing unbounded.
		second, err := NewSink(&Config{Path: path, MaxBytes: 60, MaxFiles: 4})
		must.NoError(t, err)
		t.Cleanup(func() { _ = second.Close() })

		must.NoError(t, second.Write(&record{Name: "after-restart", Seq: 2}))
		must.NoError(t, second.Flush())

		test.SliceLen(t, 1, rotatedSiblings(t, path))
		lines := readLines(t, path)
		must.SliceLen(t, 1, lines)
		test.EqOp(t, 2, lines[0].Seq)
	})

	T.Run("WithClock stamps the rotated name", func(t *testing.T) {
		t.Parallel()

		stamped := time.Date(2026, time.March, 4, 5, 6, 7, 0, time.UTC)
		c := &clockmock.ClockMock{NowFunc: func() time.Time { return stamped }}

		path := testCapturePath(t)
		s, err := NewSink(&Config{Path: path, MaxBytes: 40, MaxFiles: 4}, WithClock(c))
		must.NoError(t, err)
		t.Cleanup(func() { _ = s.Close() })

		must.NoError(t, s.Write(&record{Name: "first", Seq: 1}))
		must.NoError(t, s.Write(&record{Name: "second", Seq: 2}))

		rotated := rotatedSiblings(t, path)
		must.SliceLen(t, 1, rotated)
		test.EqOp(t, path+"."+stamped.Format(rotatedLayout), rotated[0])
		test.SliceLen(t, 1, c.NowCalls())
	})
}

func TestSink_RotationFailures(T *testing.T) {
	T.Parallel()

	T.Run("a rename failure surfaces on the write that triggered it", func(t *testing.T) {
		t.Parallel()

		// The only way this test makes the rename fail is by revoking write
		// permission on the directory, and root ignores permission bits. Left
		// unguarded the case does not merely lose its meaning, it fails: the
		// rename succeeds and no error surfaces. Any run in a root container —
		// the mutation gate's is one — would go red on it.
		if os.Geteuid() == 0 {
			t.Skip("root bypasses the directory permissions this case relies on")
		}

		dir := t.TempDir()
		path := filepath.Join(dir, "capture.jsonl")
		s := newTestSink(t, path, 40, 4)

		must.NoError(t, s.Write(&record{Name: "first", Seq: 1}))

		// Rotation renames within the parent directory, so revoking write
		// permission on it is enough to fail the rename.
		must.NoError(t, os.Chmod(dir, 0o500))
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

		test.Error(t, s.Write(&record{Name: "second", Seq: 2}))
	})

	T.Run("a flush failure surfaces before the rename", func(t *testing.T) {
		t.Parallel()

		path := testCapturePath(t)
		s := newTestSink(t, path, 40, 4)

		must.NoError(t, s.Write(&record{Name: "first", Seq: 1}))
		must.NoError(t, s.f.Close())

		test.Error(t, s.Write(&record{Name: "second", Seq: 2}))
	})

	T.Run("a prune listing failure does not fail the write", func(t *testing.T) {
		t.Parallel()

		// An unclosed bracket makes the glob pattern the pruner builds
		// malformed, so listing rotated siblings fails. Rotation itself still
		// has to succeed: losing old capture files beats failing a write.
		path := filepath.Join(t.TempDir(), "capture[.jsonl")
		s := newTestSink(t, path, 40, 1)

		must.NoError(t, s.Write(&record{Name: "first", Seq: 1}))
		must.NoError(t, s.Write(&record{Name: "second", Seq: 2}))
		must.NoError(t, s.Flush())

		lines := readLines(t, path)
		must.SliceLen(t, 1, lines)
		test.EqOp(t, 2, lines[0].Seq)
	})

	T.Run("an unremovable rotated file does not fail the write", func(t *testing.T) {
		t.Parallel()

		path := testCapturePath(t)

		// A non-empty directory occupying a rotated file's name: it sorts
		// oldest, so the pruner reaches for it first and os.Remove refuses.
		stale := path + ".00000101T000000.000000000"
		must.NoError(t, os.Mkdir(stale, 0o700))
		must.NoError(t, os.WriteFile(filepath.Join(stale, "occupant"), []byte("x"), 0o600))

		s := newTestSink(t, path, 40, 1)

		must.NoError(t, s.Write(&record{Name: "first", Seq: 1}))
		must.NoError(t, s.Write(&record{Name: "second", Seq: 2}))
		must.NoError(t, s.Flush())

		// The write landed, and the undeletable sibling is still there.
		lines := readLines(t, path)
		must.SliceLen(t, 1, lines)
		test.EqOp(t, 2, lines[0].Seq)
		_, err := os.Stat(stale)
		test.NoError(t, err)
	})
}
