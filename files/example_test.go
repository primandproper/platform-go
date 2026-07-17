package files_test

import (
	"context"
	"fmt"
	"strings"
	"testing/fstest"

	"github.com/primandproper/platform-go/v5/encoding"
	"github.com/primandproper/platform-go/v5/files"
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

// ExampleOpenFS reads a name-addressed file from an fs.FS. An embed.FS works identically — pass it
// to OpenFS (or NewReaderFS) instead of the fstest.MapFS used here for a self-contained example.
func ExampleOpenFS() {
	fsys := fstest.MapFS{"greeting.txt": {Data: []byte("hello\nworld\n")}}

	lines, err := files.OpenFS(fsys).Lines("greeting.txt")
	if err != nil {
		panic(err)
	}

	for line, err := range lines {
		if err != nil {
			panic(err)
		}

		fmt.Println(line)
	}
	// Output:
	// hello
	// world
}
