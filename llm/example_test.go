package llm_test

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/primandproper/platform-go/v13/llm"
	llmnoop "github.com/primandproper/platform-go/v13/llm/noop"
)

func Example() {
	provider := llmnoop.NewProvider()

	resp, err := provider.Completion(context.Background(), &llm.CompletionRequest{
		Model:  "example-model",
		System: "You are terse.",
		Messages: []llm.Message{
			llm.UserText("Hello!"),
		},
	})
	if err != nil {
		panic(err)
	}

	fmt.Printf("provider: %s\n", provider.Name())
	fmt.Printf("text: %q\n", resp.Text())
	fmt.Printf("tool uses: %d\n", len(resp.ToolUses()))
	// Output:
	// provider: noop
	// text: ""
	// tool uses: 0
}

// ExampleProvider_Stream shows the loop shape every consumer of a stream
// writes, including the Close that releases the response body.
func ExampleProvider_Stream() {
	provider := llmnoop.NewProvider()

	stream, err := provider.Stream(context.Background(), &llm.CompletionRequest{
		Messages: []llm.Message{llm.UserText("Hello!")},
	})
	if err != nil {
		panic(err)
	}
	defer func() { _ = stream.Close() }()

	for stream.Next() {
		switch event := stream.Current(); event.Type {
		case llm.EventTextDelta:
			fmt.Print(event.Text)
		case llm.EventToolUse:
			fmt.Printf("tool: %s\n", event.ToolUse.Name)
		case llm.EventDone:
			fmt.Printf("done: %s\n", event.StopReason)
		}
	}

	if err = stream.Err(); err != nil {
		panic(err)
	}
	// Output: done: end_turn
}

// ExampleToolResultMessage shows one turn of a tool-calling loop: the model's
// whole content goes back into the transcript, then the results.
func ExampleToolResultMessage() {
	resp := &llm.CompletionResponse{
		StopReason: llm.StopReasonToolUse,
		Content: []llm.Part{
			{Type: llm.PartText, Text: "Let me look that up."},
			{Type: llm.PartToolUse, ToolUse: &llm.ToolUse{
				ID:    "call_1",
				Name:  "get_weather",
				Input: json.RawMessage(`{"city":"Paris"}`),
			}},
		},
	}

	uses := resp.ToolUses()

	results := make([]llm.ToolResult, 0, len(uses))
	for i := range uses {
		results = append(results, llm.ToolResult{ToolUseID: uses[i].ID, Content: "17C and raining"})
	}

	messages := []llm.Message{
		llm.UserText("What is the weather in Paris?"),
		{Role: llm.RoleAssistant, Content: resp.Content},
		llm.ToolResultMessage(results...),
	}

	for i := range messages {
		fmt.Printf("%s: %d part(s)\n", messages[i].Role, len(messages[i].Content))
	}
	// Output:
	// user: 1 part(s)
	// assistant: 2 part(s)
	// tool: 1 part(s)
}
