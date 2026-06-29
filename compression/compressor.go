package compression

import (
	"bytes"
	"io"

	"github.com/primandproper/platform-go/v2/errors"

	"github.com/klauspost/compress/s2"
	"github.com/klauspost/compress/zstd"
)

const (
	algoZstd algo = "zstd"
	algoS2   algo = "s2"
)

var (
	// ErrInvalidAlgorithm is returned when an unsupported compression algorithm is requested.
	ErrInvalidAlgorithm = errors.New("invalid compression algorithm")
)

type (
	algo string

	// Compressor compresses and decompresses byte slices.
	Compressor interface {
		CompressBytes(in []byte) ([]byte, error)
		DecompressBytes(in []byte) ([]byte, error)
	}
)

type compressor struct {
	algo algo
}

// NewCompressor returns a new Compressor.
func NewCompressor(a algo) (Compressor, error) {
	switch a {
	case algoZstd, algoS2:
		return &compressor{algo: a}, nil
	default:
		return nil, ErrInvalidAlgorithm
	}
}

func (c *compressor) CompressBytes(in []byte) ([]byte, error) {
	switch c.algo {
	case algoZstd:
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
	case algoS2:
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
	case algoZstd:
		d, err := zstd.NewReader(bytes.NewReader(in))
		if err != nil {
			return nil, err
		}
		defer d.Close()

		var b bytes.Buffer
		if _, err = io.Copy(&b, d); err != nil {
			return nil, err
		}

		return b.Bytes(), nil
	case algoS2:
		dec := s2.NewReader(bytes.NewReader(in))

		var b bytes.Buffer
		if _, err := io.Copy(&b, dec); err != nil {
			return nil, err
		}

		return b.Bytes(), nil
	default:
		return nil, errors.Newf("unsupported compression algorithm: %s", c.algo)
	}
}
