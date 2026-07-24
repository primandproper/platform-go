package random_test

import (
	"context"
	"fmt"

	"github.com/primandproper/platform-go/v6/random"
)

func ExampleGenerateHexEncodedString() {
	s, err := random.GenerateHexEncodedString(context.Background(), 16)
	if err != nil {
		panic(err)
	}

	// Output is non-deterministic, but always 32 hex characters (16 bytes * 2)
	fmt.Println(len(s))
	// Output: 32
}

func ExampleGenerateRawBytes() {
	b, err := random.GenerateRawBytes(context.Background(), 8)
	if err != nil {
		panic(err)
	}

	fmt.Println(len(b))
	// Output: 8
}
