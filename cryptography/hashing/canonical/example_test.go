package canonical_test

import (
	"fmt"

	"github.com/primandproper/platform-go/v13/cryptography/hashing/canonical"
)

// recipe is a value whose digest must not depend on how Go happens to declare
// it.
type recipe struct {
	Name     string   `json:"name"`
	Steps    []string `json:"steps"`
	Servings int      `json:"servings"`
}

// ExampleSum shows the property the package exists for: semantically identical
// values digest identically. The struct declares Name first and Servings last;
// the map has no declaration order at all. Neither fact reaches the digest.
func ExampleSum() {
	fromStruct, err := canonical.Sum(recipe{
		Name:     "brine",
		Steps:    []string{"dissolve", "chill"},
		Servings: 4,
	})
	if err != nil {
		panic(err)
	}

	fromMap, err := canonical.Sum(map[string]any{
		"servings": 4,
		"name":     "brine",
		"steps":    []string{"dissolve", "chill"},
	})
	if err != nil {
		panic(err)
	}

	fmt.Println(fromStruct == fromMap)
	fmt.Println(fromStruct)
	// Output:
	// true
	// fe9264ed911b8e1f02172d8774abfda8fe8a9b61016e81ddc5d8681c8ec473b2
}

// document carries its own content hash — a field that cannot participate in
// its own computation.
type document struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	Hash  string `json:"hash"`
}

// ExampleWithoutKeys stamps a document with its own content hash. Excluding
// the hash field by name means verification re-hashes the document exactly as
// stored, with no hand-built copy that silently under-hashes the day someone
// adds a field.
func ExampleWithoutKeys() {
	doc := document{Title: "runbook", Body: "restart the thing"}

	digest, err := canonical.Sum(doc, canonical.WithoutKeys("hash"))
	if err != nil {
		panic(err)
	}
	doc.Hash = digest

	verified, err := canonical.Sum(doc, canonical.WithoutKeys("hash"))
	if err != nil {
		panic(err)
	}

	fmt.Println(verified == doc.Hash)
	// Output: true
}

// ExampleMarshal exposes the exact bytes a digest is computed over: object
// keys sorted by byte order, no insignificant whitespace, and array order left
// alone — slice order is treated as semantic, so [3,1,2] is not [1,2,3].
func ExampleMarshal() {
	canon, err := canonical.Marshal(map[string]any{
		"z": 1,
		"a": map[string]any{"nested": true, "b": []int{3, 1, 2}},
	})
	if err != nil {
		panic(err)
	}

	fmt.Println(string(canon))
	// Output: {"a":{"b":[3,1,2],"nested":true},"z":1}
}
