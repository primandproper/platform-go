package compression

import (
	"bytes"
	stderrors "errors"
	"io"

	"github.com/primandproper/platform-go/v13/errors"

	"github.com/klauspost/compress/s2"
	"github.com/klauspost/compress/zstd"
)

const (
	// AlgorithmZstd selects the Zstandard compression algorithm.
	AlgorithmZstd Algorithm = "zstd"
	// AlgorithmS2 selects the S2 compression algorithm.
	AlgorithmS2 Algorithm = "s2"

	// DefaultMaxDecompressedBytes bounds how many bytes DecompressBytes will produce for a
	// single input, guarding against decompression bombs (a small hostile payload that expands
	// to gigabytes). It matches zstd's own default decoder memory limit. Override per-Compressor
	// with WithMaxDecompressedBytes.
	DefaultMaxDecompressedBytes uint64 = 64 << 20 // 64 MiB
)

var (
	// ErrInvalidAlgorithm is returned when an unsupported compression algorithm is requested.
	//
	// Algorithm is a runtime-config selection seam like any provider name, so this
	// wraps errors.ErrUnknownProvider: startup code that branches on one sentinel
	// for "the config named something this build does not have" catches this too,
	// and a caller that wants to say "compression" specifically still can.
	ErrInvalidAlgorithm = errors.Wrap(errors.ErrUnknownProvider, "invalid compression algorithm")
	// ErrDecompressedTooLarge is returned when decompressing an input would exceed the
	// configured maximum decompressed size.
	ErrDecompressedTooLarge = errors.New("decompressed output exceeds configured maximum")
)

type (
	// Algorithm identifies a supported compression algorithm. It is a named
	// string type so callers can select an algorithm from a runtime config
	// string via a plain conversion (e.g. Algorithm(cfg.Algorithm)).
	Algorithm string

	// Compressor compresses and decompresses byte slices.
	Compressor interface {
		CompressBytes(in []byte) ([]byte, error)
		DecompressBytes(in []byte) ([]byte, error)
	}
)

// Option configures a Compressor.
type Option func(*StandardCompressor)

// WithMaxDecompressedBytes overrides the maximum number of bytes DecompressBytes will
// produce for a single input. A value of 0 leaves the default (DefaultMaxDecompressedBytes)
// in place.
func WithMaxDecompressedBytes(n uint64) Option {
	return func(c *StandardCompressor) {
		if n > 0 {
			c.maxDecompressedBytes = n
		}
	}
}

var _ Compressor = (*StandardCompressor)(nil)

// StandardCompressor is the Compressor for every supported Algorithm. It is
// exported, and returned by NewCompressor, so a caller can depend on the
// compressor it built rather than on the Compressor seam.
type StandardCompressor struct {
	algo                 Algorithm
	maxDecompressedBytes uint64
}

// NewCompressor returns a new Compressor for the given Algorithm. An unknown or
// empty Algorithm yields ErrInvalidAlgorithm.
func NewCompressor(a Algorithm, opts ...Option) (*StandardCompressor, error) {
	switch a {
	case AlgorithmZstd, AlgorithmS2:
		c := &StandardCompressor{algo: a, maxDecompressedBytes: DefaultMaxDecompressedBytes}
		for _, opt := range opts {
			opt(c)
		}
		return c, nil
	default:
		return nil, errors.Wrapf(ErrInvalidAlgorithm, "compression algorithm %q", a)
	}
}

func (c *StandardCompressor) CompressBytes(in []byte) ([]byte, error) {
	switch c.algo {
	case AlgorithmZstd:
		var b bytes.Buffer
		enc, err := zstd.NewWriter(&b)
		if err != nil {
			return nil, err
		}

		if _, err = io.Copy(enc, bytes.NewReader(in)); err != nil {
			return nil, err
		}

		if err = enc.Close(); err != nil {
			return nil, err
		}

		return b.Bytes(), nil
	case AlgorithmS2:
		var b bytes.Buffer
		enc := s2.NewWriter(&b)

		if _, err := io.Copy(enc, bytes.NewReader(in)); err != nil {
			return nil, err
		}

		if err := enc.Close(); err != nil {
			return nil, err
		}

		return b.Bytes(), nil
	default:
		return nil, errors.Wrapf(ErrInvalidAlgorithm, "compression algorithm %q", c.algo)
	}
}

// copyBounded drains r into a buffer, refusing to produce more than
// maxDecompressedBytes.
//
// It copies one byte past the limit and treats reaching that byte as the
// overflow signal, so the check is on bytes actually produced rather than on
// anything the input claims about itself.
func (c *StandardCompressor) copyBounded(r io.Reader) ([]byte, error) {
	limit := int64(c.maxDecompressedBytes)

	var b bytes.Buffer

	n, err := io.CopyN(&b, r, limit+1)
	if err != nil && !stderrors.Is(err, io.EOF) {
		return nil, err
	}

	if n > limit {
		return nil, ErrDecompressedTooLarge
	}

	return b.Bytes(), nil
}

func (c *StandardCompressor) DecompressBytes(in []byte) ([]byte, error) {
	switch c.algo {
	case AlgorithmZstd:
		// WithDecoderMaxMemory is a *per-frame* cap, not a total-output one: N
		// concatenated frames each just under the limit decompress to N times it,
		// which is the whole bound walked straight around. It stays on as a cheap
		// early failure, and the copy below enforces the documented total.
		d, err := zstd.NewReader(bytes.NewReader(in), zstd.WithDecoderMaxMemory(c.maxDecompressedBytes))
		if err != nil {
			return nil, err
		}
		defer d.Close()

		return c.copyBounded(d)
	case AlgorithmS2:
		// s2's streaming reader has no output cap of its own, built-in or per-frame.
		return c.copyBounded(s2.NewReader(bytes.NewReader(in)))
	default:
		return nil, errors.Wrapf(ErrInvalidAlgorithm, "compression algorithm %q", c.algo)
	}
}
