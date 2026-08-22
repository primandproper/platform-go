package bridge

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/primandproper/platform-go/v13/llm"
	"github.com/primandproper/platform-go/v13/pointer"

	anyllm "github.com/mozilla-ai/any-llm-go"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestParams(T *testing.T) {
	T.Parallel()

	T.Run("carries every scalar through", func(t *testing.T) {
		t.Parallel()

		params, err := Params(&llm.CompletionRequest{
			Model:             "ignored-in-favor-of-the-resolved-model",
			Messages:          []llm.Message{llm.UserText("hi")},
			System:            "be terse",
			Temperature:       pointer.To(0.25),
			TopP:              pointer.To(0.9),
			MaxTokens:         pointer.To(512),
			Seed:              pointer.To(7),
			ParallelToolCalls: pointer.To(false),
			StopSequences:     []string{"STOP"},
			ReasoningEffort:   llm.ReasoningEffortHigh,
		}, "resolved-model")
		must.NoError(t, err)

		// The provider resolves the model, so the request's own is not used.
		test.EqOp(t, "resolved-model", params.Model)
		must.NotNil(t, params.Temperature)
		test.EqOp(t, 0.25, *params.Temperature)
		must.NotNil(t, params.TopP)
		test.EqOp(t, 0.9, *params.TopP)
		must.NotNil(t, params.MaxTokens)
		test.EqOp(t, 512, *params.MaxTokens)
		must.NotNil(t, params.Seed)
		test.EqOp(t, 7, *params.Seed)
		must.NotNil(t, params.ParallelToolCalls)
		test.False(t, *params.ParallelToolCalls)
		test.Eq(t, []string{"STOP"}, params.Stop)
		test.EqOp(t, anyllm.ReasoningEffort("high"), params.ReasoningEffort)

		must.SliceLen(t, 2, params.Messages)
		test.EqOp(t, anyllm.RoleSystem, params.Messages[0].Role)
		test.EqOp(t, "be terse", params.Messages[0].ContentString())
	})

	T.Run("leaves unset optionals off the wire", func(t *testing.T) {
		t.Parallel()

		params, err := Params(&llm.CompletionRequest{
			Messages: []llm.Message{llm.UserText("hi")},
		}, "m")
		must.NoError(t, err)

		test.Nil(t, params.Temperature)
		test.Nil(t, params.TopP)
		test.Nil(t, params.MaxTokens)
		test.Nil(t, params.Seed)
		test.Nil(t, params.ParallelToolCalls)
		test.Nil(t, params.ResponseFormat)
		test.Nil(t, params.ToolChoice)
		test.SliceEmpty(t, params.Tools)
		test.EqOp(t, anyllm.ReasoningEffort(""), params.ReasoningEffort)
	})

	T.Run("with a nil request", func(t *testing.T) {
		t.Parallel()

		_, err := Params(nil, "m")
		must.ErrorIs(t, err, llm.ErrInvalidRequest)
	})

	T.Run("with no messages", func(t *testing.T) {
		t.Parallel()

		_, err := Params(&llm.CompletionRequest{}, "m")
		must.ErrorIs(t, err, llm.ErrInvalidRequest)
	})

	T.Run("with an unconvertible message", func(t *testing.T) {
		t.Parallel()

		_, err := Params(&llm.CompletionRequest{
			Messages: []llm.Message{{Role: "wizard"}},
		}, "m")
		must.ErrorIs(t, err, llm.ErrInvalidRequest)

		// The index has to survive, or an operator reading the log cannot tell
		// which of forty messages was the bad one.
		test.StrContains(t, err.Error(), "message 0")
	})

	T.Run("with an unconvertible tool", func(t *testing.T) {
		t.Parallel()

		_, err := Params(&llm.CompletionRequest{
			Messages: []llm.Message{llm.UserText("hi")},
			Tools:    []llm.Tool{{Description: "nameless"}},
		}, "m")
		must.ErrorIs(t, err, llm.ErrInvalidRequest)
	})
}

func TestMessages(T *testing.T) {
	T.Parallel()

	T.Run("omits the system message when there is no system prompt", func(t *testing.T) {
		t.Parallel()

		msgs, err := Messages([]llm.Message{llm.UserText("hi")}, "")
		must.NoError(t, err)

		must.SliceLen(t, 1, msgs)
		test.EqOp(t, anyllm.RoleUser, msgs[0].Role)
	})

	T.Run("renders a text-only user message as a plain string", func(t *testing.T) {
		t.Parallel()

		msgs, err := Messages([]llm.Message{{
			Role: llm.RoleUser,
			Content: []llm.Part{
				{Type: llm.PartText, Text: "one "},
				{Type: llm.PartText, Text: "two"},
			},
		}}, "")
		must.NoError(t, err)

		must.SliceLen(t, 1, msgs)
		test.EqOp(t, "one two", msgs[0].ContentString())
		test.False(t, msgs[0].IsMultiModal())
	})

	T.Run("renders a user message with an image as content parts", func(t *testing.T) {
		t.Parallel()

		msgs, err := Messages([]llm.Message{{
			Role: llm.RoleUser,
			Content: []llm.Part{
				{Type: llm.PartText, Text: "what is this"},
				{Type: llm.PartImage, Image: &llm.Image{URL: "https://example.com/cat.png"}},
			},
		}}, "")
		must.NoError(t, err)

		must.SliceLen(t, 1, msgs)
		parts := msgs[0].ContentParts()
		must.SliceLen(t, 2, parts)
		test.EqOp(t, "text", parts[0].Type)
		test.EqOp(t, "what is this", parts[0].Text)
		test.EqOp(t, "image_url", parts[1].Type)
		must.NotNil(t, parts[1].ImageURL)
		test.EqOp(t, "https://example.com/cat.png", parts[1].ImageURL.URL)
	})

	T.Run("encodes inline image bytes as a data URI", func(t *testing.T) {
		t.Parallel()

		data := []byte{0x89, 0x50, 0x4e, 0x47}

		msgs, err := Messages([]llm.Message{{
			Role: llm.RoleUser,
			Content: []llm.Part{
				{Type: llm.PartImage, Image: &llm.Image{Data: data, MediaType: "image/png"}},
			},
		}}, "")
		must.NoError(t, err)

		parts := msgs[0].ContentParts()
		must.SliceLen(t, 1, parts)
		must.NotNil(t, parts[0].ImageURL)
		test.EqOp(t, "data:image/png;base64,"+base64.StdEncoding.EncodeToString(data), parts[0].ImageURL.URL)
	})

	T.Run("prefers inline data over a URL", func(t *testing.T) {
		t.Parallel()

		msgs, err := Messages([]llm.Message{{
			Role: llm.RoleUser,
			Content: []llm.Part{{Type: llm.PartImage, Image: &llm.Image{
				Data:      []byte("bytes"),
				MediaType: "image/jpeg",
				URL:       "https://example.com/ignored.png",
			}}},
		}}, "")
		must.NoError(t, err)

		parts := msgs[0].ContentParts()
		must.SliceLen(t, 1, parts)
		test.StrContains(t, parts[0].ImageURL.URL, "data:image/jpeg;base64,")
	})

	T.Run("renders an assistant message with tool calls and reasoning", func(t *testing.T) {
		t.Parallel()

		msgs, err := Messages([]llm.Message{{
			Role: llm.RoleAssistant,
			Content: []llm.Part{
				{Type: llm.PartThinking, Text: "let me check"},
				{Type: llm.PartText, Text: "checking"},
				{Type: llm.PartToolUse, ToolUse: &llm.ToolUse{
					ID:    "call_1",
					Name:  "lookup",
					Input: json.RawMessage(`{"q":"x"}`),
				}},
			},
		}}, "")
		must.NoError(t, err)

		must.SliceLen(t, 1, msgs)
		test.EqOp(t, anyllm.RoleAssistant, msgs[0].Role)
		test.EqOp(t, "checking", msgs[0].ContentString())
		must.NotNil(t, msgs[0].Reasoning)
		test.EqOp(t, "let me check", msgs[0].Reasoning.Content)
		must.SliceLen(t, 1, msgs[0].ToolCalls)
		test.EqOp(t, "call_1", msgs[0].ToolCalls[0].ID)
		test.EqOp(t, "function", msgs[0].ToolCalls[0].Type)
		test.EqOp(t, "lookup", msgs[0].ToolCalls[0].Function.Name)
		test.EqOp(t, `{"q":"x"}`, msgs[0].ToolCalls[0].Function.Arguments)
	})

	T.Run("leaves reasoning unset when there is none", func(t *testing.T) {
		t.Parallel()

		msgs, err := Messages([]llm.Message{llm.AssistantText("hi")}, "")
		must.NoError(t, err)

		must.SliceLen(t, 1, msgs)
		test.Nil(t, msgs[0].Reasoning)
		test.SliceEmpty(t, msgs[0].ToolCalls)
	})

	T.Run("splits a tool message into one wire message per result", func(t *testing.T) {
		t.Parallel()

		// OpenAI's shape carries exactly one tool_call_id per message, so three
		// answers cannot ride in one message however Anthropic-shaped the
		// platform's model is.
		msgs, err := Messages([]llm.Message{llm.ToolResultMessage(
			llm.ToolResult{ToolUseID: "a", Content: "1"},
			llm.ToolResult{ToolUseID: "b", Content: "2"},
			llm.ToolResult{ToolUseID: "c", Content: "3"},
		)}, "")
		must.NoError(t, err)

		must.SliceLen(t, 3, msgs)
		for i, id := range []string{"a", "b", "c"} {
			test.EqOp(t, anyllm.RoleTool, msgs[i].Role)
			test.EqOp(t, id, msgs[i].ToolCallID)
		}
		test.EqOp(t, "1", msgs[0].ContentString())
	})

	T.Run("marks a failed tool result in its content", func(t *testing.T) {
		t.Parallel()

		msgs, err := Messages([]llm.Message{llm.ToolResultMessage(
			llm.ToolResult{ToolUseID: "a", Content: "no such city", IsError: true},
		)}, "")
		must.NoError(t, err)

		must.SliceLen(t, 1, msgs)
		test.EqOp(t, toolResultErrorPrefix+"no such city", msgs[0].ContentString())
	})

	T.Run("with a rejected message", func(t *testing.T) {
		t.Parallel()

		for _, tc := range []struct {
			name string
			msg  llm.Message
		}{
			{
				name: "unknown role",
				msg:  llm.Message{Role: "wizard", Content: []llm.Part{{Type: llm.PartText}}},
			},
			{
				name: "tool result in a user message",
				msg: llm.Message{Role: llm.RoleUser, Content: []llm.Part{
					{Type: llm.PartToolResult, ToolResult: &llm.ToolResult{}},
				}},
			},
			{
				name: "tool use in a user message",
				msg: llm.Message{Role: llm.RoleUser, Content: []llm.Part{
					{Type: llm.PartToolUse, ToolUse: &llm.ToolUse{}},
				}},
			},
			{
				name: "image part with no image",
				msg:  llm.Message{Role: llm.RoleUser, Content: []llm.Part{{Type: llm.PartImage}}},
			},
			{
				name: "image with neither data nor URL",
				msg: llm.Message{Role: llm.RoleUser, Content: []llm.Part{
					{Type: llm.PartImage, Image: &llm.Image{}},
				}},
			},
			{
				name: "inline image data with no media type",
				msg: llm.Message{Role: llm.RoleUser, Content: []llm.Part{
					{Type: llm.PartImage, Image: &llm.Image{Data: []byte("x")}},
				}},
			},
			{
				name: "image in an assistant message",
				msg: llm.Message{Role: llm.RoleAssistant, Content: []llm.Part{
					{Type: llm.PartImage, Image: &llm.Image{URL: "https://example.com/x.png"}},
				}},
			},
			{
				name: "tool use part with no tool use",
				msg:  llm.Message{Role: llm.RoleAssistant, Content: []llm.Part{{Type: llm.PartToolUse}}},
			},
			{
				name: "text in a tool message",
				msg:  llm.Message{Role: llm.RoleTool, Content: []llm.Part{{Type: llm.PartText, Text: "x"}}},
			},
			{
				name: "tool result part with no tool result",
				msg:  llm.Message{Role: llm.RoleTool, Content: []llm.Part{{Type: llm.PartToolResult}}},
			},
			{
				name: "unknown part type",
				msg:  llm.Message{Role: llm.RoleUser, Content: []llm.Part{{Type: "hologram"}}},
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()

				_, err := Messages([]llm.Message{tc.msg}, "")
				must.ErrorIs(t, err, llm.ErrInvalidRequest)
			})
		}
	})
}

func TestTools(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		schema := map[string]any{"type": "object"}

		tools, err := Tools([]llm.Tool{{Name: "lookup", Description: "looks up", Schema: schema}})
		must.NoError(t, err)

		must.SliceLen(t, 1, tools)
		test.EqOp(t, "function", tools[0].Type)
		test.EqOp(t, "lookup", tools[0].Function.Name)
		test.EqOp(t, "looks up", tools[0].Function.Description)
		test.Eq(t, schema, tools[0].Function.Parameters)
	})

	T.Run("with no tools", func(t *testing.T) {
		t.Parallel()

		tools, err := Tools(nil)
		must.NoError(t, err)
		test.Nil(t, tools)
	})

	T.Run("with a nameless tool", func(t *testing.T) {
		t.Parallel()

		_, err := Tools([]llm.Tool{{Description: "anonymous"}})
		must.ErrorIs(t, err, llm.ErrInvalidRequest)
	})
}

func TestToolChoice(T *testing.T) {
	T.Parallel()

	T.Run("renders a mode as the bare string the providers switch on", func(t *testing.T) {
		t.Parallel()

		for _, mode := range []llm.ToolChoiceMode{llm.ToolChoiceAuto, llm.ToolChoiceRequired, llm.ToolChoiceNone} {
			choice, ok := toolChoice(&llm.ToolChoice{Mode: mode})
			must.True(t, ok)
			test.Eq[any](t, string(mode), choice)
		}
	})

	T.Run("names a specific tool", func(t *testing.T) {
		t.Parallel()

		choice, ok := toolChoice(&llm.ToolChoice{Mode: llm.ToolChoiceSpecific, Name: "lookup"})
		must.True(t, ok)

		// The providers type-switch on the value, not a pointer to it.
		specific, ok := choice.(anyllm.ToolChoice)
		must.True(t, ok)
		test.EqOp(t, "function", specific.Type)
		must.NotNil(t, specific.Function)
		test.EqOp(t, "lookup", specific.Function.Name)
	})

	T.Run("sends nothing when there is nothing to send", func(t *testing.T) {
		t.Parallel()

		for _, tc := range []struct {
			choice *llm.ToolChoice
			name   string
		}{
			{name: "nil", choice: nil},
			{name: "specific with no name", choice: &llm.ToolChoice{Mode: llm.ToolChoiceSpecific}},
			{name: "unknown mode", choice: &llm.ToolChoice{Mode: "telepathy"}},
			{name: "zero mode", choice: &llm.ToolChoice{}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()

				_, ok := toolChoice(tc.choice)
				test.False(t, ok)
			})
		}
	})

	T.Run("reaches the params", func(t *testing.T) {
		t.Parallel()

		params, err := Params(&llm.CompletionRequest{
			Messages:   []llm.Message{llm.UserText("hi")},
			ToolChoice: &llm.ToolChoice{Mode: llm.ToolChoiceRequired},
		}, "m")
		must.NoError(t, err)

		test.Eq(t, any("required"), params.ToolChoice)
	})
}

func TestResponseFormat(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		schema := map[string]any{"type": "object"}

		format := responseFormat(&llm.ResponseFormat{Name: "answer", Schema: schema, Strict: true})
		must.NotNil(t, format)

		test.EqOp(t, "json_schema", format.Type)
		must.NotNil(t, format.JSONSchema)
		test.EqOp(t, "answer", format.JSONSchema.Name)
		test.Eq(t, schema, format.JSONSchema.Schema)
		must.NotNil(t, format.JSONSchema.Strict)
		test.True(t, *format.JSONSchema.Strict)
	})

	T.Run("carries a false strict rather than dropping it", func(t *testing.T) {
		t.Parallel()

		format := responseFormat(&llm.ResponseFormat{Name: "answer"})
		must.NotNil(t, format)
		must.NotNil(t, format.JSONSchema.Strict)
		test.False(t, *format.JSONSchema.Strict)
	})

	T.Run("with no format", func(t *testing.T) {
		t.Parallel()

		test.Nil(t, responseFormat(nil))
	})
}
