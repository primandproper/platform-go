package jsonl_test

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/primandproper/platform-go/v13/eventcapture/jsonl"
)

// captured is one record's wire shape. The sink prescribes nothing about
// content — a record's own JSON tags define the line.
type captured struct {
	Route  string `json:"route"`
	Status int    `json:"status"`
}

// Example writes two records and shows the resulting file: one JSON object per
// line, appended in arrival order. Close flushes the buffered writer, which is
// what makes the trailing line visible; Flush does the same mid-run so the
// live file stays tail-able between rotations.
func Example() {
	dir, err := os.MkdirTemp("", "jsonl-example")
	if err != nil {
		panic(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	path := filepath.Join(dir, "capture.jsonl")

	sink, err := jsonl.NewSink(&jsonl.Config{Path: path})
	if err != nil {
		panic(err)
	}

	for _, record := range []captured{
		{Route: "/widgets", Status: 200},
		{Route: "/gadgets", Status: 500},
	} {
		if err = sink.Write(record); err != nil {
			panic(err)
		}
	}

	if err = sink.Close(); err != nil {
		panic(err)
	}

	contents, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}

	fmt.Print(string(contents))
	// Output:
	// {"route":"/widgets","status":200}
	// {"route":"/gadgets","status":500}
}
