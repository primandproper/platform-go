package files_test

import (
	"context"
	"fmt"
	"strings"

	"github.com/primandproper/platform-go/v4/encoding"
	"github.com/primandproper/platform-go/v4/files"
)

func ExampleLines() {
	for line, err := range files.Lines(strings.NewReader("alpha\nbeta\n")) {
		if err != nil {
			panic(err)
		}

		fmt.Println(line)
	}
	// Output:
	// alpha
	// beta
}

func ExampleChunks() {
	for chunk, err := range files.Chunks(strings.NewReader("a\nb\nc\n"), 2) {
		if err != nil {
			panic(err)
		}

		fmt.Println(chunk)
	}
	// Output:
	// [a b]
	// [c]
}

func ExampleSliceLines() {
	lines, err := files.SliceLines(strings.NewReader("0\n1\n2\n3\n4\n"), 1, 2)
	if err != nil {
		panic(err)
	}

	fmt.Println(lines)
	// Output: [1 2]
}

func ExampleDecode() {
	type config struct {
		Name string `json:"name"`
	}

	cfg, err := files.Decode[config](context.Background(), strings.NewReader(`{"name":"platform"}`), encoding.ContentTypeJSON)
	if err != nil {
		panic(err)
	}

	fmt.Println(cfg.Name)
	// Output: platform
}
