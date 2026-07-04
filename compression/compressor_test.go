package compression

import (
	"encoding/base64"
	"testing"

	"github.com/primandproper/platform-go/v3/encoding"
	loggingnoop "github.com/primandproper/platform-go/v3/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/v3/observability/tracing/noop"

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

		encoder := encoding.ProvideServerEncoderDecoder(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), encoding.ContentTypeJSON)

		expected := "KLUv_QQAmQAAeyJuYW1lIjoidGVzdGluZyJ9Ch6HXww="
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

		encoder := encoding.ProvideServerEncoderDecoder(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), encoding.ContentTypeJSON)

		expected := "_wYAAFMyc1R3TwEXAABui7jXeyJuYW1lIjoidGVzdGluZyJ9Cg=="
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

		comp.(*compressor).algo = "invalid"

		x := &whatever{
			Name: "testing",
		}

		encoder := encoding.ProvideServerEncoderDecoder(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), encoding.ContentTypeJSON)

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

			encoder := encoding.ProvideServerEncoderDecoder(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), encoding.ContentTypeJSON)

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

		encoder := encoding.ProvideServerEncoderDecoder(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), encoding.ContentTypeJSON)

		compressed, err := comp.CompressBytes(encoder.MustEncodeJSON(ctx, x))
		test.NoError(t, err)

		comp.(*compressor).algo = "invalid"

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
