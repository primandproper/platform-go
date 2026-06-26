package encoding

import (
	"testing"

	loggingnoop "github.com/primandproper/platform-go/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/observability/tracing/noop"
)

type benchPayload struct {
	Name string `json:"name"`
	ID   int    `json:"id"`
}

func BenchmarkServerEncoderDecoder(b *testing.B) {
	ctx := b.Context()
	ed := ProvideServerEncoderDecoder(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), ContentTypeJSON)

	in := &benchPayload{Name: "benchmark", ID: 42}
	encoded := ed.MustEncodeJSON(ctx, in)

	b.Run("EncodeJSON", func(b *testing.B) {
		for b.Loop() {
			bytesSink = ed.MustEncodeJSON(ctx, in)
		}
	})

	b.Run("DecodeBytes", func(b *testing.B) {
		for b.Loop() {
			var out benchPayload
			_ = ed.DecodeBytes(ctx, encoded, &out)
		}
	})
}

var bytesSink []byte
