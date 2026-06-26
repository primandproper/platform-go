package compression

import (
	"encoding/base64"
	"testing"

	"github.com/primandproper/platform-go/encoding"
	loggingnoop "github.com/primandproper/platform-go/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/observability/tracing/noop"

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

		comp, err := NewCompressor(algoZstd)
		must.NoError(t, err)
		must.NotNil(t, comp)
	})

	T.Run("s2", func(t *testing.T) {
		t.Parallel()

		comp, err := NewCompressor(algoS2)
		must.NoError(t, err)
		must.NotNil(t, comp)
	})

	T.Run("invalid algo", func(t *testing.T) {
		t.Parallel()

		comp, err := NewCompressor(algo(t.Name()))
		must.Error(t, err)
		must.Nil(t, comp)
	})
}

func Test_compressor_CompressBytes(T *testing.T) {
	T.Parallel()

	T.Run("zstandard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		comp, err := NewCompressor(algoZstd)
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
		comp, err := NewCompressor(algoS2)
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
		comp, err := NewCompressor(algoS2)
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

	algorithms := []algo{
		algoZstd,
		algoS2,
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
		comp, err := NewCompressor(algoZstd)
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

		comp, err := NewCompressor(algoZstd)
		must.NoError(t, err)

		decompressed, err := comp.DecompressBytes([]byte("not valid zstd data"))
		test.Error(t, err)
		test.Nil(t, decompressed)
	})

	T.Run("with invalid s2 data", func(t *testing.T) {
		t.Parallel()

		comp, err := NewCompressor(algoS2)
		must.NoError(t, err)

		decompressed, err := comp.DecompressBytes([]byte("not valid s2 data"))
		test.Error(t, err)
		test.Nil(t, decompressed)
	})
}
