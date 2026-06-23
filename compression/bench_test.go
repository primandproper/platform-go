package compression

import (
	"strings"
	"testing"

	"github.com/shoenig/test/must"
)

func BenchmarkCompressor(b *testing.B) {
	data := []byte(strings.Repeat("the quick brown fox ", 512))

	cases := []struct {
		name string
		algo algo
	}{
		{"zstd", algoZstd},
		{"s2", algoS2},
	}

	for i := range cases {
		tc := cases[i]
		comp, err := NewCompressor(tc.algo)
		must.NoError(b, err)

		compressed, err := comp.CompressBytes(data)
		must.NoError(b, err)

		b.Run(tc.name+"/Compress", func(b *testing.B) {
			for b.Loop() {
				_, _ = comp.CompressBytes(data)
			}
		})

		b.Run(tc.name+"/Decompress", func(b *testing.B) {
			for b.Loop() {
				_, _ = comp.DecompressBytes(compressed)
			}
		})
	}
}
