package llm_test

import (
	"testing"

	"github.com/primandproper/platform-go/v6/llm"
	llmnoop "github.com/primandproper/platform-go/v6/llm/noop"

	"github.com/shoenig/test"
)

func TestNoopProvider_Completion(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		provider := llmnoop.NewProvider()

		result, err := provider.Completion(ctx, llm.CompletionParams{
			Model: "test",
			Messages: []llm.Message{
				{Role: "user", Content: "hello"},
			},
		})

		test.NoError(t, err)
		test.NotNil(t, result)
	})
}
