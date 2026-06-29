package llm_test

import (
	"context"
	"fmt"

	"github.com/primandproper/platform-go/v2/llm"
	llmnoop "github.com/primandproper/platform-go/v2/llm/noop"
)

func Example() {
	provider := llmnoop.NewProvider()

	result, err := provider.Completion(context.Background(), llm.CompletionParams{
		Model: "example-model",
		Messages: []llm.Message{
			{Role: "user", Content: "Hello!"},
		},
	})
	if err != nil {
		panic(err)
	}

	fmt.Printf("content: %q\n", result.Content)
	// Output: content: ""
}
