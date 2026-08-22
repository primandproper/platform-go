package bridge

import (
	"testing"

	"github.com/primandproper/platform-go/v13/llm"

	anyllm "github.com/mozilla-ai/any-llm-go"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestResponse(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		out := Response(&anyllm.ChatCompletion{
			ID:    "resp-1",
			Model: "claude-sonnet-5",
			Choices: []anyllm.Choice{{
				FinishReason: anyllm.FinishReasonStop,
				Message: anyllm.Message{
					Role:    anyllm.RoleAssistant,
					Content: "hello",
				},
			}},
			Usage: &anyllm.Usage{
				PromptTokens:     10,
				CompletionTokens: 5,
				ReasoningTokens:  2,
				TotalTokens:      15,
			},
		})
		must.NotNil(t, out)

		test.EqOp(t, "resp-1", out.ID)
		test.EqOp(t, "claude-sonnet-5", out.Model)
		test.EqOp(t, llm.StopReasonEndTurn, out.StopReason)
		test.EqOp(t, "hello", out.Text())

		must.NotNil(t, out.Usage)
		test.EqOp(t, 10, out.Usage.InputTokens)
		test.EqOp(t, 5, out.Usage.OutputTokens)
		test.EqOp(t, 2, out.Usage.ReasoningTokens)
		test.EqOp(t, 15, out.Usage.TotalTokens)
	})

	T.Run("orders parts as reasoning, then text, then tool calls", func(t *testing.T) {
		t.Parallel()

		out := Response(&anyllm.ChatCompletion{
			Choices: []anyllm.Choice{{
				FinishReason: anyllm.FinishReasonToolCalls,
				Message: anyllm.Message{
					Content:   "on it",
					Reasoning: &anyllm.Reasoning{Content: "thinking"},
					ToolCalls: []anyllm.ToolCall{
						{ID: "call_1", Function: anyllm.FunctionCall{Name: "a", Arguments: `{"x":1}`}},
						{ID: "call_2", Function: anyllm.FunctionCall{Name: "b"}},
					},
				},
			}},
		})
		must.NotNil(t, out)

		must.SliceLen(t, 4, out.Content)
		test.EqOp(t, llm.PartThinking, out.Content[0].Type)
		test.EqOp(t, "thinking", out.Content[0].Text)
		test.EqOp(t, llm.PartText, out.Content[1].Type)
		test.EqOp(t, llm.PartToolUse, out.Content[2].Type)
		test.EqOp(t, llm.PartToolUse, out.Content[3].Type)

		test.EqOp(t, llm.StopReasonToolUse, out.StopReason)

		uses := out.ToolUses()
		must.SliceLen(t, 2, uses)
		test.EqOp(t, "call_1", uses[0].ID)
		test.EqOp(t, "a", uses[0].Name)
		test.EqOp(t, `{"x":1}`, string(uses[0].Input))
		test.EqOp(t, "call_2", uses[1].ID)
	})

	T.Run("omits empty text and reasoning rather than emitting empty parts", func(t *testing.T) {
		t.Parallel()

		out := Response(&anyllm.ChatCompletion{
			Choices: []anyllm.Choice{{
				Message: anyllm.Message{Content: "", Reasoning: &anyllm.Reasoning{}},
			}},
		})
		must.NotNil(t, out)

		test.SliceEmpty(t, out.Content)
	})

	T.Run("with no choices", func(t *testing.T) {
		t.Parallel()

		out := Response(&anyllm.ChatCompletion{ID: "resp-1"})
		must.NotNil(t, out)

		test.EqOp(t, "resp-1", out.ID)
		test.SliceEmpty(t, out.Content)
		test.EqOp(t, llm.StopReason(""), out.StopReason)
	})

	T.Run("preserves the absence of usage", func(t *testing.T) {
		t.Parallel()

		out := Response(&anyllm.ChatCompletion{Choices: []anyllm.Choice{{}}})
		must.NotNil(t, out)

		// nil rather than a zeroed struct, so that "the provider said nothing"
		// stays distinguishable from "the request cost nothing".
		test.Nil(t, out.Usage)
	})

	T.Run("with a nil response", func(t *testing.T) {
		t.Parallel()

		test.Nil(t, Response(nil))
	})
}

func TestStopReason(T *testing.T) {
	T.Parallel()

	T.Run("translates the finish reason vocabulary", func(t *testing.T) {
		t.Parallel()

		for _, tc := range []struct {
			finish string
			want   llm.StopReason
		}{
			{finish: anyllm.FinishReasonStop, want: llm.StopReasonEndTurn},
			{finish: anyllm.FinishReasonLength, want: llm.StopReasonMaxTokens},
			{finish: anyllm.FinishReasonToolCalls, want: llm.StopReasonToolUse},
			{finish: anyllm.FinishReasonContentFilter, want: llm.StopReasonContentFilter},
			// any-llm-go collapses Anthropic's stop_sequence onto "stop", so a
			// stop sequence is unrecoverable here and reads as end_turn. An
			// unrecognized reason lands in the same place rather than inventing
			// a stop reason the caller cannot act on.
			{finish: "wandered_off", want: llm.StopReasonEndTurn},
			{finish: "", want: llm.StopReasonEndTurn},
		} {
			t.Run(tc.finish, func(t *testing.T) {
				t.Parallel()

				test.EqOp(t, tc.want, stopReason(tc.finish))
			})
		}
	})
}
