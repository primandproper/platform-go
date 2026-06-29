package random

import (
	"testing"

	tracingnoop "github.com/primandproper/platform-go/v2/observability/tracing/noop"
)

func BenchmarkGenerator(b *testing.B) {
	g := NewGenerator(nil, tracingnoop.NewTracerProvider())
	ctx := b.Context()

	b.Run("HexEncodedString16", func(b *testing.B) {
		for b.Loop() {
			strSink, _ = g.GenerateHexEncodedString(ctx, 16)
		}
	})

	b.Run("RawBytes32", func(b *testing.B) {
		for b.Loop() {
			bytesSink, _ = g.GenerateRawBytes(ctx, 32)
		}
	})
}

var (
	strSink   string
	bytesSink []byte
)
