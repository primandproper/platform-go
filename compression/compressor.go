package compression

import (
	"bytes"
	stderrors "errors"
	"io"

	"github.com/primandproper/platform-go/v6/errors"

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
	ErrInvalidAlgorithm = errors.New("invalid compression algorithm")
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
type Option func(*compressor)

// WithMaxDecompressedBytes overrides the maximum number of bytes DecompressBytes will
// produce for a single input. A value of 0 leaves the default (DefaultMaxDecompressedBytes)
// in place.
func WithMaxDecompressedBytes(n uint64) Option {
	return func(c *compressor) {
		if n > 0 {
			c.maxDecompressedBytes = n
		}
	}
}

type compressor struct {
	algo                 Algorithm
	maxDecompressedBytes uint64
}

// NewCompressor returns a new Compressor for the given Algorithm. An unknown or
// empty Algorithm yields ErrInvalidAlgorithm.
func NewCompressor(a Algorithm, opts ...Option) (Compressor, error) {
	switch a {
	case AlgorithmZstd, AlgorithmS2:
		c := &compressor{algo: a, maxDecompressedBytes: DefaultMaxDecompressedBytes}
		for _, opt := range opts {
			opt(c)
		}
		return c, nil
	default:
		return nil, ErrInvalidAlgorithm
	}
}

func (c *compressor) CompressBytes(in []byte) ([]byte, error) {
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
		return nil, errors.Newf("unsupported compression algorithm: %s", c.algo)
	}
}

func (c *compressor) DecompressBytes(in []byte) ([]byte, error) {
	switch c.algo {
	case AlgorithmZstd:
		// WithDecoderMaxMemory caps the decompressed output; the decoder returns an error
		// once a frame would exceed it, so a bomb fails instead of exhausting memory.
		d, err := zstd.NewReader(bytes.NewReader(in), zstd.WithDecoderMaxMemory(c.maxDecompressedBytes))
		if err != nil {
			return nil, err
		}
		defer d.Close()

		var b bytes.Buffer
		if _, err = io.Copy(&b, d); err != nil {
			return nil, err
		}

		return b.Bytes(), nil
	case AlgorithmS2:
		dec := s2.NewReader(bytes.NewReader(in))

		// s2's streaming reader has no built-in output cap, so bound it manually: copy at
		// most maxDecompressedBytes+1 and treat reaching the extra byte as an overflow.
		limit := int64(c.maxDecompressedBytes)
		var b bytes.Buffer
		n, err := io.CopyN(&b, dec, limit+1)
		if err != nil && !stderrors.Is(err, io.EOF) {
			return nil, err
		}
		if n > limit {
			return nil, ErrDecompressedTooLarge
		}

		return b.Bytes(), nil
	default:
		return nil, errors.Newf("unsupported compression algorithm: %s", c.algo)
	}
}
