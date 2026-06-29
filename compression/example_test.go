package compression_test

import (
	"fmt"

	"github.com/primandproper/platform-go/v2/compression"
)

func Example_roundTrip() {
	c, err := compression.NewCompressor("zstd")
	if err != nil {
		panic(err)
	}

	original := []byte("hello, world!")
	compressed, err := c.CompressBytes(original)
	if err != nil {
		panic(err)
	}

	decompressed, err := c.DecompressBytes(compressed)
	if err != nil {
		panic(err)
	}

	fmt.Println(string(decompressed))
	// Output: hello, world!
}
