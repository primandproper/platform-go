package bridge

import (
	"encoding/json"

	"github.com/primandproper/platform-go/v13/llm"

	anyllm "github.com/mozilla-ai/any-llm-go"
)

// Response translates a completion back into the platform's shape.
//
// Two things are lost on the way in, both upstream of here. any-llm-go
// collapses Anthropic's stop_sequence onto OpenAI's "stop", so a response that
// ended on a stop sequence reports llm.StopReasonEndTurn rather than
// llm.StopReasonStopSequence. And it flattens a response's content blocks into
// one text string plus sidecar tool calls, so the platform parts come back in
// canonical order — reasoning, then text, then tool calls — rather than the
// order the model emitted them in. Neither is recoverable without going around
// the library.
//
// Only the first choice is read. Nothing in the platform's surface asks for n>1
// completions, so a provider that returned more would be answering a question
// nobody posed.
func Response(resp *anyllm.ChatCompletion) *llm.CompletionResponse {
	if resp == nil {
		return nil
	}

	out := &llm.CompletionResponse{
		ID:    resp.ID,
		Model: resp.Model,
		Usage: usage(resp.Usage),
	}

	if len(resp.Choices) == 0 {
		return out
	}

	choice := &resp.Choices[0]
	out.StopReason = stopReason(choice.FinishReason)

	if choice.Message.Reasoning != nil && choice.Message.Reasoning.Content != "" {
		out.Content = append(out.Content, llm.Part{
			Type: llm.PartThinking,
			Text: choice.Message.Reasoning.Content,
		})
	}

	if text := choice.Message.ContentString(); text != "" {
		out.Content = append(out.Content, llm.Part{Type: llm.PartText, Text: text})
	}

	for i := range choice.Message.ToolCalls {
		out.Content = append(out.Content, toolUsePart(&choice.Message.ToolCalls[i]))
	}

	return out
}

// toolUsePart renders one tool call as a platform part.
func toolUsePart(call *anyllm.ToolCall) llm.Part {
	return llm.Part{
		Type: llm.PartToolUse,
		ToolUse: &llm.ToolUse{
			ID:    call.ID,
			Name:  call.Function.Name,
			Input: json.RawMessage(call.Function.Arguments),
		},
	}
}

// usage translates token accounting, preserving "the provider said nothing" as
// nil rather than flattening it to a zeroed struct.
func usage(u *anyllm.Usage) *llm.Usage {
	if u == nil {
		return nil
	}

	return &llm.Usage{
		InputTokens:     u.PromptTokens,
		OutputTokens:    u.CompletionTokens,
		ReasoningTokens: u.ReasoningTokens,
		TotalTokens:     u.TotalTokens,
	}
}

// stopReason translates OpenAI's finish reason vocabulary, which is what
// any-llm-go normalizes both providers onto.
func stopReason(reason string) llm.StopReason {
	switch reason {
	case anyllm.FinishReasonStop:
		return llm.StopReasonEndTurn
	case anyllm.FinishReasonLength:
		return llm.StopReasonMaxTokens
	case anyllm.FinishReasonToolCalls:
		return llm.StopReasonToolUse
	case anyllm.FinishReasonContentFilter:
		return llm.StopReasonContentFilter
	default:
		return llm.StopReasonEndTurn
	}
}
