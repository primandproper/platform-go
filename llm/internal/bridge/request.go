package bridge

import (
	"encoding/base64"
	"fmt"
	"strings"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/llm"

	anyllm "github.com/mozilla-ai/any-llm-go"
)

// toolResultErrorPrefix marks a failed tool result. any-llm-go's normalized
// message shape has no is_error flag — its Anthropic provider hardcodes false
// — so the fact that a tool failed reaches the model as text or not at all.
const toolResultErrorPrefix = "error: "

// Params translates a platform request into any-llm-go's normalized parameter
// shape, using model in place of the request's own (the provider resolves that,
// since only it knows its default).
//
// It rejects malformed requests here rather than letting the provider reject
// them over the network: every error it returns matches llm.ErrInvalidRequest.
func Params(req *llm.CompletionRequest, model string) (anyllm.CompletionParams, error) {
	if req == nil {
		return anyllm.CompletionParams{}, invalidRequest("nil completion request")
	}

	if len(req.Messages) == 0 {
		return anyllm.CompletionParams{}, invalidRequest("completion request has no messages")
	}

	messages, err := Messages(req.Messages, req.System)
	if err != nil {
		return anyllm.CompletionParams{}, err
	}

	tools, err := Tools(req.Tools)
	if err != nil {
		return anyllm.CompletionParams{}, err
	}

	params := anyllm.CompletionParams{
		Model:             model,
		Messages:          messages,
		Temperature:       req.Temperature,
		TopP:              req.TopP,
		MaxTokens:         req.MaxTokens,
		Stop:              req.StopSequences,
		Tools:             tools,
		ParallelToolCalls: req.ParallelToolCalls,
		ResponseFormat:    responseFormat(req.ResponseFormat),
		Seed:              req.Seed,
	}

	if req.ReasoningEffort != "" {
		params.ReasoningEffort = anyllm.ReasoningEffort(req.ReasoningEffort)
	}

	if choice, ok := toolChoice(req.ToolChoice); ok {
		params.ToolChoice = choice
	}

	return params, nil
}

// Messages translates platform messages into any-llm-go's, prepending the
// system prompt as a leading system message. any-llm-go's Anthropic provider
// hoists that message back out into the top-level system parameter, so the one
// shape reaches both providers correctly.
//
// One platform message can become several: a tool message answering three calls
// becomes three wire messages, because OpenAI's shape carries exactly one
// tool_call_id per message.
func Messages(msgs []llm.Message, system string) ([]anyllm.Message, error) {
	out := make([]anyllm.Message, 0, len(msgs)+1)
	if system != "" {
		out = append(out, anyllm.Message{Role: anyllm.RoleSystem, Content: system})
	}

	for i := range msgs {
		converted, err := message(&msgs[i])
		if err != nil {
			return nil, platformerrors.Wrapf(err, "message %d", i)
		}

		out = append(out, converted...)
	}

	return out, nil
}

// message translates one platform message into the one or more wire messages it
// corresponds to.
func message(msg *llm.Message) ([]anyllm.Message, error) {
	switch msg.Role {
	case llm.RoleUser:
		return userMessage(msg)
	case llm.RoleAssistant:
		return assistantMessage(msg)
	case llm.RoleTool:
		return toolMessages(msg)
	default:
		return nil, invalidRequest("unknown message role %q", msg.Role)
	}
}

// userMessage renders a user turn. Text-only content goes over as a plain
// string, which is what both providers' non-multimodal paths expect; anything
// with an image becomes a content part list.
func userMessage(msg *llm.Message) ([]anyllm.Message, error) {
	var (
		text     strings.Builder
		parts    []anyllm.ContentPart
		hasImage bool
	)

	for i := range msg.Content {
		part := &msg.Content[i]

		switch part.Type {
		case llm.PartText:
			text.WriteString(part.Text)
			parts = append(parts, anyllm.ContentPart{Type: "text", Text: part.Text})
		case llm.PartImage:
			url, err := imageURL(part.Image)
			if err != nil {
				return nil, err
			}

			hasImage = true
			parts = append(parts, anyllm.ContentPart{Type: "image_url", ImageURL: &anyllm.ImageURL{URL: url}})
		default:
			return nil, invalidRequest("user message cannot carry a %q part", part.Type)
		}
	}

	if hasImage {
		return []anyllm.Message{{Role: anyllm.RoleUser, Content: parts}}, nil
	}

	return []anyllm.Message{{Role: anyllm.RoleUser, Content: text.String()}}, nil
}

// assistantMessage renders an assistant turn being replayed to the model.
// Reasoning is carried, because a provider that requires thinking blocks to be
// echoed back rejects a transcript that dropped them.
func assistantMessage(msg *llm.Message) ([]anyllm.Message, error) {
	var (
		text      strings.Builder
		thinking  strings.Builder
		toolCalls []anyllm.ToolCall
	)

	for i := range msg.Content {
		part := &msg.Content[i]

		switch part.Type {
		case llm.PartText:
			text.WriteString(part.Text)
		case llm.PartThinking:
			thinking.WriteString(part.Text)
		case llm.PartToolUse:
			if part.ToolUse == nil {
				return nil, invalidRequest("tool_use part has no tool use")
			}

			toolCalls = append(toolCalls, anyllm.ToolCall{
				ID:   part.ToolUse.ID,
				Type: "function",
				Function: anyllm.FunctionCall{
					Name:      part.ToolUse.Name,
					Arguments: string(part.ToolUse.Input),
				},
			})
		default:
			return nil, invalidRequest("assistant message cannot carry a %q part", part.Type)
		}
	}

	out := anyllm.Message{
		Role:      anyllm.RoleAssistant,
		Content:   text.String(),
		ToolCalls: toolCalls,
	}

	if thinking.Len() > 0 {
		out.Reasoning = &anyllm.Reasoning{Content: thinking.String()}
	}

	return []anyllm.Message{out}, nil
}

// toolMessages renders a tool turn as one wire message per result.
func toolMessages(msg *llm.Message) ([]anyllm.Message, error) {
	out := make([]anyllm.Message, 0, len(msg.Content))
	for i := range msg.Content {
		part := &msg.Content[i]

		if part.Type != llm.PartToolResult {
			return nil, invalidRequest("tool message cannot carry a %q part", part.Type)
		}

		if part.ToolResult == nil {
			return nil, invalidRequest("tool_result part has no tool result")
		}

		content := part.ToolResult.Content
		if part.ToolResult.IsError {
			content = toolResultErrorPrefix + content
		}

		out = append(out, anyllm.Message{
			Role:       anyllm.RoleTool,
			Content:    content,
			ToolCallID: part.ToolResult.ToolUseID,
		})
	}

	return out, nil
}

// imageURL resolves an image to the single URL field any-llm-go carries,
// encoding inline bytes as a data URI.
func imageURL(img *llm.Image) (string, error) {
	if img == nil {
		return "", invalidRequest("image part has no image")
	}

	if len(img.Data) > 0 {
		if img.MediaType == "" {
			return "", invalidRequest("inline image data requires a media type")
		}

		return "data:" + img.MediaType + ";base64," + base64.StdEncoding.EncodeToString(img.Data), nil
	}

	if img.URL == "" {
		return "", invalidRequest("image part has neither data nor a URL")
	}

	return img.URL, nil
}

// Tools translates tool declarations. It returns nil for an empty list so that
// the parameter stays off the wire entirely.
func Tools(tools []llm.Tool) ([]anyllm.Tool, error) {
	if len(tools) == 0 {
		return nil, nil
	}

	out := make([]anyllm.Tool, 0, len(tools))
	for i := range tools {
		if tools[i].Name == "" {
			return nil, invalidRequest("tool has no name")
		}

		out = append(out, anyllm.Tool{
			Type: "function",
			Function: anyllm.Function{
				Name:        tools[i].Name,
				Description: tools[i].Description,
				Parameters:  tools[i].Schema,
			},
		})
	}

	return out, nil
}

// toolChoice renders a tool choice into the untyped shape any-llm-go's
// providers switch on: a mode string, or a providers.ToolChoice value naming
// one tool. It reports false when there is nothing to send.
func toolChoice(choice *llm.ToolChoice) (any, bool) {
	if choice == nil {
		return nil, false
	}

	switch choice.Mode {
	case llm.ToolChoiceAuto, llm.ToolChoiceRequired, llm.ToolChoiceNone:
		return string(choice.Mode), true
	case llm.ToolChoiceSpecific:
		if choice.Name == "" {
			return nil, false
		}

		return anyllm.ToolChoice{
			Type:     "function",
			Function: &anyllm.ToolChoiceFunction{Name: choice.Name},
		}, true
	default:
		return nil, false
	}
}

// responseFormat renders a structured output request.
func responseFormat(format *llm.ResponseFormat) *anyllm.ResponseFormat {
	if format == nil {
		return nil
	}

	strict := format.Strict

	return &anyllm.ResponseFormat{
		Type: "json_schema",
		JSONSchema: &anyllm.JSONSchema{
			Name:   format.Name,
			Schema: format.Schema,
			Strict: &strict,
		},
	}
}

// invalidRequest builds an error matching llm.ErrInvalidRequest.
func invalidRequest(format string, args ...any) error {
	return &normalizedError{
		sentinel: llm.ErrInvalidRequest,
		cause:    platformerrors.New(fmt.Sprintf(format, args...)),
	}
}
