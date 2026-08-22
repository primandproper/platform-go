package uploads_test

import (
	"context"
	"fmt"
	"sort"

	"github.com/primandproper/platform-go/v13/uploads"
	"github.com/primandproper/platform-go/v13/uploads/objectstorage"
)

func newManager() uploads.UploadManager {
	m, err := objectstorage.NewUploadManager(
		context.Background(),
		&objectstorage.Config{BucketName: "example", Provider: objectstorage.MemoryProvider},
	)
	if err != nil {
		panic(err)
	}

	return m
}

func ExampleSaveFile() {
	ctx := context.Background()
	mgr := newManager()

	// Options set the stored metadata; without WithContentType the provider sniffs it from content.
	if err := uploads.SaveFile(ctx, mgr, "note.txt", []byte("hi"),
		uploads.WithContentType("text/plain"),
		uploads.WithCacheControl("max-age=60"),
	); err != nil {
		panic(err)
	}

	data, err := uploads.ReadFile(ctx, mgr, "note.txt")
	if err != nil {
		panic(err)
	}

	fmt.Println(string(data))
	// Output: hi
}

func ExampleListAll() {
	ctx := context.Background()
	mgr := newManager()

	for _, name := range []string{"a.txt", "b.txt"} {
		if err := uploads.SaveFile(ctx, mgr, name, []byte("x")); err != nil {
			panic(err)
		}
	}

	// ListAll drains the streaming Lister into a slice; prefer ranging List for large prefixes.
	objs, err := uploads.ListAll(ctx, mgr.(uploads.Lister), "")
	if err != nil {
		panic(err)
	}

	paths := make([]string, 0, len(objs))
	for i := range objs {
		paths = append(paths, objs[i].Path)
	}
	sort.Strings(paths)

	fmt.Println(paths)
	// Output: [a.txt b.txt]
}
