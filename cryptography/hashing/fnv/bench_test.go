package fnv

import (
	"fmt"
	"strings"
	"testing"
)

func BenchmarkFNVHasher_Hash(b *testing.B) {
	hasher := NewFNVHasher()
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
