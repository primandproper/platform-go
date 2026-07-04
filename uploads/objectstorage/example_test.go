package objectstorage_test

import (
	"context"
	"fmt"
	"sort"

	loggingnoop "github.com/primandproper/platform-go/v2/observability/logging/noop"
	metricsnoop "github.com/primandproper/platform-go/v2/observability/metrics/noop"
	tracingnoop "github.com/primandproper/platform-go/v2/observability/tracing/noop"
	"github.com/primandproper/platform-go/v2/uploads"
	"github.com/primandproper/platform-go/v2/uploads/objectstorage"
)

func newExampleManager() *objectstorage.Uploader {
	m, err := objectstorage.NewUploadManager(
		context.Background(),
		loggingnoop.NewLogger(),
		tracingnoop.NewTracerProvider(),
		metricsnoop.NewMetricsProvider(),
		// The in-memory provider needs no credentials, so it's handy for examples and tests.
		&objectstorage.Config{BucketName: "example", Provider: objectstorage.MemoryProvider},
	)
	if err != nil {
		panic(err)
	}

	return m
}

func Example() {
	ctx := context.Background()
	mgr := newExampleManager()

	if err := uploads.SaveFile(ctx, mgr, "greeting.txt", []byte("hello world")); err != nil {
		panic(err)
	}

	data, err := uploads.ReadFile(ctx, mgr, "greeting.txt")
	if err != nil {
		panic(err)
	}

	fmt.Println(string(data))
	// Output: hello world
}

func ExampleUploader_List() {
	ctx := context.Background()
	mgr := newExampleManager()

	for _, name := range []string{"data/a.txt", "data/b.txt", "other/c.txt"} {
		if err := uploads.SaveFile(ctx, mgr, name, []byte("x")); err != nil {
			panic(err)
		}
	}

	// List streams objects lazily; break out of the loop to stop early.
	var paths []string
	for obj, err := range mgr.List(ctx, "data/") {
		if err != nil {
			panic(err)
		}

		paths = append(paths, obj.Path)
	}
	sort.Strings(paths)

	fmt.Println(paths)
	// Output: [data/a.txt data/b.txt]
}

func ExampleUploader_Attributes() {
	ctx := context.Background()
	mgr := newExampleManager()

	if err := uploads.SaveFile(ctx, mgr, "photo.png", []byte("hello world")); err != nil {
		panic(err)
	}

	attrs, err := mgr.Attributes(ctx, "photo.png")
	if err != nil {
		panic(err)
	}

	fmt.Println(attrs.Size)
	// Output: 11
}
