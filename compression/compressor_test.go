package compression

import (
	"encoding/base64"
	"testing"

	"github.com/primandproper/platform-go/v13/encoding"
	"github.com/primandproper/platform-go/v13/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

type whatever struct {
	Name string `json:"name"`
}

func TestNewCompressor(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		comp, err := NewCompressor(AlgorithmZstd)
		must.NoError(t, err)
		must.NotNil(t, comp)
	})

	T.Run("s2", func(t *testing.T) {
		t.Parallel()

		comp, err := NewCompressor(AlgorithmS2)
		must.NoError(t, err)
		must.NotNil(t, comp)
	})

	T.Run("from config string", func(t *testing.T) {
		t.Parallel()

		const configValue = "zstd"

		comp, err := NewCompressor(Algorithm(configValue))
		must.NoError(t, err)
		must.NotNil(t, comp)
	})

	T.Run("invalid algo", func(t *testing.T) {
		t.Parallel()

		comp, err := NewCompressor(Algorithm(t.Name()))
		must.Error(t, err)
		must.Nil(t, comp)
	})
}

func Test_compressor_CompressBytes(T *testing.T) {
	T.Parallel()

	T.Run("zstandard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		comp, err := NewCompressor(AlgorithmZstd)
		must.NoError(t, err)

		x := &whatever{
			Name: "testing",
		}

		encoder := encoding.NewServerEncoderDecoder(encoding.ContentTypeJSON)

		expected := "KLUv_QQAkQAAeyJuYW1lIjoidGVzdGluZyJ9h21pXw=="
		compressed, err := comp.CompressBytes(encoder.MustEncodeJSON(ctx, x))
		test.NoError(t, err)
		actual := base64.URLEncoding.EncodeToString(compressed)

		test.EqOp(t, expected, actual)
	})

	T.Run("s2", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		comp, err := NewCompressor(AlgorithmS2)
		must.NoError(t, err)

		x := &whatever{
			Name: "testing",
		}

		encoder := encoding.NewServerEncoderDecoder(encoding.ContentTypeJSON)

		expected := "_wYAAFMyc1R3TwEWAAC9a5gJeyJuYW1lIjoidGVzdGluZyJ9"
		compressed, err := comp.CompressBytes(encoder.MustEncodeJSON(ctx, x))
		test.NoError(t, err)
		actual := base64.URLEncoding.EncodeToString(compressed)

		test.EqOp(t, expected, actual)
	})

	T.Run("invalid algo", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		comp, err := NewCompressor(AlgorithmS2)
		must.NoError(t, err)

		comp.algo = "invalid"

		x := &whatever{
			Name: "testing",
		}

		encoder := encoding.NewServerEncoderDecoder(encoding.ContentTypeJSON)

		compressed, err := comp.CompressBytes(encoder.MustEncodeJSON(ctx, x))
		test.Error(t, err)
		test.Nil(t, compressed)
	})
}

func Test_compressor_DecompressBytes(T *testing.T) {
	T.Parallel()

	algorithms := []Algorithm{
		AlgorithmZstd,
		AlgorithmS2,
	}

	for _, a := range algorithms {
		T.Run(string(a), func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			comp, err := NewCompressor(a)
			must.NoError(t, err)

			x := &whatever{
				Name: "testing",
			}

			encoder := encoding.NewServerEncoderDecoder(encoding.ContentTypeJSON)

			compressed, err := comp.CompressBytes(encoder.MustEncodeJSON(ctx, x))
			test.NoError(t, err)

			decompressed, err := comp.DecompressBytes(compressed)
			test.NoError(t, err)

			var y *whatever
			must.NoError(t, encoder.DecodeBytes(ctx, decompressed, &y))

			test.Eq(t, x, y)
		})
	}

	T.Run("with invalid algo", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		comp, err := NewCompressor(AlgorithmZstd)
		must.NoError(t, err)

		x := &whatever{
			Name: "testing",
		}

		encoder := encoding.NewServerEncoderDecoder(encoding.ContentTypeJSON)

		compressed, err := comp.CompressBytes(encoder.MustEncodeJSON(ctx, x))
		test.NoError(t, err)

		comp.algo = "invalid"

		decompressed, err := comp.DecompressBytes(compressed)
		test.Error(t, err)
		test.Nil(t, decompressed)
	})

	T.Run("with invalid zstd data", func(t *testing.T) {
		t.Parallel()

		comp, err := NewCompressor(AlgorithmZstd)
		must.NoError(t, err)

		decompressed, err := comp.DecompressBytes([]byte("not valid zstd data"))
		test.Error(t, err)
		test.Nil(t, decompressed)
	})

	T.Run("with invalid s2 data", func(t *testing.T) {
		t.Parallel()

		comp, err := NewCompressor(AlgorithmS2)
		must.NoError(t, err)

		decompressed, err := comp.DecompressBytes([]byte("not valid s2 data"))
		test.Error(t, err)
		test.Nil(t, decompressed)
	})

	// A small, highly-compressible payload that expands well past a tiny cap must be
	// rejected rather than allocating the full decompressed size (decompression bomb guard).
	for _, a := range algorithms {
		T.Run(string(a)+" rejects output larger than the cap", func(t *testing.T) {
			t.Parallel()

			const maxOut = 4 << 10 // 4 KiB
			// 1 MiB of zeros compresses to a tiny payload but expands past the cap.
			bomb := make([]byte, 1<<20)

			packer, err := NewCompressor(a)
			must.NoError(t, err)
			compressed, err := packer.CompressBytes(bomb)
			must.NoError(t, err)
			must.True(t, len(compressed) < maxOut)

			capped, err := NewCompressor(a, WithMaxDecompressedBytes(maxOut))
			must.NoError(t, err)

			decompressed, err := capped.DecompressBytes(compressed)
			test.Error(t, err)
			test.Nil(t, decompressed)
		})

		T.Run(string(a)+" allows output within the cap", func(t *testing.T) {
			t.Parallel()

			payload := []byte("a modest payload well under the configured cap")

			packer, err := NewCompressor(a)
			must.NoError(t, err)
			compressed, err := packer.CompressBytes(payload)
			must.NoError(t, err)

			capped, err := NewCompressor(a, WithMaxDecompressedBytes(1<<20))
			must.NoError(t, err)

			decompressed, err := capped.DecompressBytes(compressed)
			test.NoError(t, err)
			test.Eq(t, payload, decompressed)
		})
	}
}

func TestCompressor_DecompressBytes_boundsConcatenatedFrames(T *testing.T) {
	T.Parallel()

	// zstd's WithDecoderMaxMemory bounds one frame. N concatenated frames each
	// under the cap decompress to N times it, which walks straight around the
	// documented limit — so the total is enforced on bytes actually produced.
	T.Run("zstd", func(t *testing.T) {
		t.Parallel()

		const limit = 1 << 16

		c, err := NewCompressor(AlgorithmZstd, WithMaxDecompressedBytes(limit))
		must.NoError(t, err)

		// Four frames, each comfortably under the limit on its own, together over it.
		frame, err := c.CompressBytes(make([]byte, limit/2))
		must.NoError(t, err)

		var concatenated []byte
		for range 4 {
			concatenated = append(concatenated, frame...)
		}

		out, err := c.DecompressBytes(concatenated)
		test.ErrorIs(t, err, ErrDecompressedTooLarge)
		test.Nil(t, out)
	})

	T.Run("s2", func(t *testing.T) {
		t.Parallel()

		const limit = 1 << 16

		c, err := NewCompressor(AlgorithmS2, WithMaxDecompressedBytes(limit))
		must.NoError(t, err)

		frame, err := c.CompressBytes(make([]byte, limit/2))
		must.NoError(t, err)

		var concatenated []byte
		for range 4 {
			concatenated = append(concatenated, frame...)
		}

		out, err := c.DecompressBytes(concatenated)
		test.ErrorIs(t, err, ErrDecompressedTooLarge)
		test.Nil(t, out)
	})
}

func TestErrInvalidAlgorithm(T *testing.T) {
	T.Parallel()

	// Algorithm is a runtime-config selection seam like any provider name, so
	// startup code that branches on the one platform sentinel catches a bad
	// compression algorithm too.
	T.Run("wraps the platform unknown-provider sentinel", func(t *testing.T) {
		t.Parallel()

		test.ErrorIs(t, ErrInvalidAlgorithm, errors.ErrUnknownProvider)
	})

	T.Run("an unknown algorithm reports both", func(t *testing.T) {
		t.Parallel()

		c, err := NewCompressor("brotli")
		test.Nil(t, c)
		test.ErrorIs(t, err, ErrInvalidAlgorithm)
		test.ErrorIs(t, err, errors.ErrUnknownProvider)
	})
}
