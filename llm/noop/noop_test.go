package noop

import (
	"testing"

	"github.com/primandproper/platform-go/v13/llm"

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

func TestProvider_Name(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		// A real name rather than the empty string, so a metric broken down by
		// provider keeps the dimension instead of losing it.
		test.EqOp(t, "noop", NewProvider().Name())
	})
}

func TestProvider_Capabilities(T *testing.T) {
	T.Parallel()

	T.Run("supports nothing", func(t *testing.T) {
		t.Parallel()

		test.Eq(t, llm.Capabilities{}, NewProvider().Capabilities())
	})
}

func TestProvider_Completion(T *testing.T) {
	T.Parallel()

	T.Run("returns an empty response and no error", func(t *testing.T) {
		t.Parallel()

		result, err := NewProvider().Completion(t.Context(), &llm.CompletionRequest{
			Model:    "test",
			Messages: []llm.Message{llm.UserText("hello")},
		})

		must.NoError(t, err)
		must.NotNil(t, result)
		test.EqOp(t, "", result.Text())
		test.SliceEmpty(t, result.ToolUses())
		test.EqOp(t, llm.StopReasonEndTurn, result.StopReason)
	})

	T.Run("accepts a nil request", func(t *testing.T) {
		t.Parallel()

		// Nothing is sent, so there is nothing to validate, and a provider that
		// exists to keep a credential-less service running should not be the
		// one thing that fails.
		result, err := NewProvider().Completion(t.Context(), nil)

		must.NoError(t, err)
		must.NotNil(t, result)
	})
}

func TestProvider_Stream(T *testing.T) {
	T.Parallel()

	T.Run("yields only done", func(t *testing.T) {
		t.Parallel()

		stream, err := NewProvider().Stream(t.Context(), &llm.CompletionRequest{
			Messages: []llm.Message{llm.UserText("hello")},
		})
		must.NoError(t, err)
		must.NotNil(t, stream)
		t.Cleanup(func() { must.NoError(t, stream.Close()) })

		var events []llm.Event
		for stream.Next() {
			events = append(events, stream.Current())
		}
		must.NoError(t, stream.Err())

		// One event rather than none, so a consumer's event loop reaches its
		// terminal case instead of special-casing this provider.
		must.SliceLen(t, 1, events)
		test.EqOp(t, llm.EventDone, events[0].Type)
		test.EqOp(t, llm.StopReasonEndTurn, events[0].StopReason)
	})
}
