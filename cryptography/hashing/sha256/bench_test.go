package sha256

import (
	"fmt"
	"strings"
	"testing"
)

func BenchmarkSHA256Hasher_Hash(b *testing.B) {
	hasher := NewSHA256Hasher()
	for _, size := range []int{16, 256, 4096} {
		content := strings.Repeat("a", size)
		b.Run(fmt.Sprintf("%dB", size), func(b *testing.B) {
			for b.Loop() {
				strSink, _ = hasher.Hash(content)
			}
		})
	}
}

var strSink string
