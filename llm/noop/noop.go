// Package noop is the llm.Provider that spends nothing: Completion returns an
// empty response that stopped at llm.StopReasonEndTurn, and Stream returns a
// stream carrying only llm.EventDone.
//
// Those shapes are chosen so a caller's loop runs rather than special-cases
// this provider. An agent loop that asks for tool calls receives none and
// terminates cleanly on the first turn; a consumer draining the stream sees a
// well-formed end instead of an immediate EOF. Capabilities reports everything
// false, so a caller that degrades deliberately can, and Name reports "noop" so
// a metric or log line broken down by provider keeps its dimension rather than
// losing it to the empty string.
//
// llm/config hands it back only when a config names it — never as a stand-in
// for a provider name it did not recognize, which is errors.ErrUnknownProvider
// there.
package noop

import (
	"context"

	"github.com/primandproper/platform-go/v13/llm"
)

var _ llm.Provider = (*Provider)(nil)

// name is what Name reports. It is a real provider name rather than the empty
// string so that a metric or a log line broken down by provider says "noop"
// instead of losing the dimension.
const name = "noop"

// Provider is a no-op Provider: it answers nothing, successfully.
//
// llm/config hands it back only when a config names it, never as a stand-in for
// a provider it did not recognize — an unknown provider name is
// errors.ErrUnknownProvider there. Reach for this where doing no LLM work is
// the intent, such as a test or an environment with no credentials to spend.
type Provider struct{}

// NewProvider returns a no-op Provider.
func NewProvider() *Provider {
	return &Provider{}
}

// Name implements llm.Provider.
func (*Provider) Name() string {
	return name
}

// Capabilities implements llm.Provider. Everything is false: Stream returns an
// empty stream rather than a streaming one, and no request feature is honored
// because no request is sent.
func (*Provider) Capabilities() llm.Capabilities {
	return llm.Capabilities{}
}

// Completion is a no-op that returns an empty response.
func (*Provider) Completion(context.Context, *llm.CompletionRequest) (*llm.CompletionResponse, error) {
	return &llm.CompletionResponse{StopReason: llm.StopReasonEndTurn}, nil
}

// Stream is a no-op that returns a stream yielding only llm.EventDone, so a
// consumer's event loop runs to completion instead of special-casing this
// provider.
func (*Provider) Stream(context.Context, *llm.CompletionRequest) (llm.Stream, error) {
	return llm.NewSliceStream(llm.Event{Type: llm.EventDone, StopReason: llm.StopReasonEndTurn}), nil
}
