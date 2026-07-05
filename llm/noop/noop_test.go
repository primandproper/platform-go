package noop

import (
	"testing"

	"github.com/primandproper/platform-go/v4/llm"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNewProvider(T *testing.T) {
	T.Parallel()

	T.Run("returns non-nil provider", func(t *testing.T) {
		t.Parallel()

		p := NewProvider()
		must.NotNil(t, p)
	})
}

func TestProvider_Completion(T *testing.T) {
	T.Parallel()

	T.Run("returns empty result and no error", func(t *testing.T) {
		t.Parallel()

		p := NewProvider()
		result, err := p.Completion(t.Context(), llm.CompletionParams{
			Model:    "test",
			Messages: []llm.Message{{Role: "user", Content: "hello"}},
		})

		must.NoError(t, err)
		must.NotNil(t, result)
		test.EqOp(t, "", result.Content)
	})
}
