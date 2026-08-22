package bridge

import (
	"encoding/json"
	"strings"

	"github.com/primandproper/platform-go/v13/llm"

	anyllm "github.com/mozilla-ai/any-llm-go"
)

var _ llm.Stream = (*stream)(nil)

// Stream adapts any-llm-go's chunk/error channel pair to llm.Stream.
//
// finish is invoked exactly once, when the stream stops for any reason:
// exhausted, failed, or closed. It carries the terminal error, nil when there
// was none. Providers use it to cancel the request context, record the stream's
// duration, and end their span — all of which have to happen when the consumer
// is done rather than when Stream returns, since a stream outlives the call
// that created it. A consumer that neither drains nor closes never triggers it,
// which is why llm.Stream documents Close as mandatory.
func Stream(chunks <-chan anyllm.ChatCompletionChunk, errs <-chan error, finish func(error, *llm.Usage)) llm.Stream {
	return &stream{
		chunks: chunks,
		errs:   errs,
		finish: finish,
	}
}

type stream struct {
	err     error
	chunks  <-chan anyllm.ChatCompletionChunk
	errs    <-chan error
	finish  func(error, *llm.Usage)
	toolIdx map[string]int
	usage   *llm.Usage
	current llm.Event
	reason  llm.StopReason
	pending []llm.Event
	tools   []llm.ToolUse
	flushed int
	drained bool
	done    bool
	closed  bool
}

// Next advances to the next event.
func (s *stream) Next() bool {
	for {
		if len(s.pending) > 0 {
			s.current, s.pending = s.pending[0], s.pending[1:]

			return true
		}

		if s.closed || s.done || s.err != nil {
			return false
		}

		if s.drained {
			// The upstream is exhausted: any tool call still accumulating is
			// complete by definition, and then the stream is over.
			s.flushTools()
			s.pending = append(s.pending, llm.Event{
				Type:       llm.EventDone,
				StopReason: s.reason,
				Usage:      s.usage,
			})
			s.done = true
			s.finishOnce(nil)

			continue
		}

		s.receive()
	}
}

// Current returns the event Next advanced to.
func (s *stream) Current() llm.Event {
	return s.current
}

// Err returns the error that stopped the stream.
func (s *stream) Err() error {
	return s.err
}

// Close releases the stream. It is idempotent, and safe after a failure or a
// full drain.
func (s *stream) Close() error {
	if s.closed {
		return nil
	}

	s.closed = true
	s.finishOnce(nil)

	// Events already decoded but not yet yielded are dropped. A consumer that
	// closed has said it is done, and handing it more events afterwards is a
	// surprise rather than a service.
	s.pending = nil

	// Whoever is still feeding these channels is blocked on an unbuffered send
	// as soon as we stop reading, and a blocked producer is a leaked goroutine
	// and a leaked HTTP body. Cancelling the request context (which finish
	// does) unblocks the send only once the producer notices, so drain what is
	// left in the background until both channels close.
	chunks, errs := s.chunks, s.errs
	s.chunks, s.errs = nil, nil

	if chunks != nil || errs != nil {
		go drain(chunks, errs)
	}

	return nil
}

// finishOnce runs the finish hook at most once.
//
// It hands over whatever token accounting the stream saw, which is only known
// once the stream is over — the provider reports usage in the final chunk. That
// is why it travels through here rather than being read at the call site: by the
// time Stream returns, there is nothing to read yet.
func (s *stream) finishOnce(err error) {
	if s.finish == nil {
		return
	}

	finish := s.finish
	s.finish = nil
	finish(err, s.usage)
}

// drain reads both channels to completion so the producing goroutine can exit.
func drain(chunks <-chan anyllm.ChatCompletionChunk, errs <-chan error) {
	for chunks != nil || errs != nil {
		select {
		case _, ok := <-chunks:
			if !ok {
				chunks = nil
			}
		case _, ok := <-errs:
			if !ok {
				errs = nil
			}
		}
	}
}

// receive takes the next thing off whichever channel has one. A nil channel
// never becomes ready, so the select narrows to the live half once one side
// closes, and drained is only set when both have.
func (s *stream) receive() {
	select {
	case chunk, ok := <-s.chunks:
		if !ok {
			s.chunks = nil
			s.checkDrained()

			return
		}

		s.handle(&chunk)
	case err, ok := <-s.errs:
		if !ok {
			s.errs = nil
			s.checkDrained()

			return
		}

		s.err = NormalizeError(err)
		s.finishOnce(s.err)
	}
}

func (s *stream) checkDrained() {
	s.drained = s.chunks == nil && s.errs == nil
}

// handle turns one upstream chunk into zero or more platform events.
func (s *stream) handle(chunk *anyllm.ChatCompletionChunk) {
	if chunk.Usage != nil {
		s.usage = usage(chunk.Usage)
	}

	for i := range chunk.Choices {
		choice := &chunk.Choices[i]

		if choice.Delta.Reasoning != nil && choice.Delta.Reasoning.Content != "" {
			s.pending = append(s.pending, llm.Event{
				Type: llm.EventThinkingDelta,
				Text: choice.Delta.Reasoning.Content,
			})
		}

		if choice.Delta.Content != "" {
			s.pending = append(s.pending, llm.Event{
				Type: llm.EventTextDelta,
				Text: choice.Delta.Content,
			})
		}

		for j := range choice.Delta.ToolCalls {
			s.accumulateToolCall(&choice.Delta.ToolCalls[j])
		}

		if choice.FinishReason != "" {
			s.reason = stopReason(choice.FinishReason)
		}
	}
}

// accumulateToolCall folds one streamed tool call delta into the accumulator.
//
// The two providers disagree about what a delta is, and neither says which it
// is doing. OpenAI sends the ID and name once and then bare argument fragments;
// Anthropic sends the ID and name every time along with the arguments
// accumulated so far. any-llm-go drops the tool call index on the way through,
// so the ID is the only handle on identity, and a repeated ID is ambiguous:
// Anthropic re-sending the whole call, or an OpenAI-compatible server repeating
// the ID on a fragment.
//
// The prefix test settles it. Cumulative arguments always start with everything
// already seen for that call; a fragment does not, because it is the
// continuation rather than the whole. When nothing has been seen yet both rules
// agree, so the first delta needs no special case.
//
// The one case this cannot get right is a provider that interleaves fragments
// of several tool calls, since an ID-less fragment can then only be attributed
// to the most recently opened call. That needs the wire's tool call index, which
// any-llm-go drops, so it is not recoverable here at all. Neither provider does
// it today: both stream one call to completion before starting the next.
func (s *stream) accumulateToolCall(call *anyllm.ToolCall) {
	args := call.Function.Arguments

	if call.ID != "" {
		if idx, ok := s.toolIdx[call.ID]; ok {
			existing := string(s.tools[idx].Input)
			if strings.HasPrefix(args, existing) {
				s.tools[idx].Input = json.RawMessage(args)
			} else {
				s.tools[idx].Input = json.RawMessage(existing + args)
			}

			if call.Function.Name != "" {
				s.tools[idx].Name = call.Function.Name
			}

			return
		}

		// A call the accumulator has not seen means every earlier one is
		// finished, so they can be emitted now rather than held to the end.
		s.flushTools()

		if s.toolIdx == nil {
			s.toolIdx = map[string]int{}
		}

		s.toolIdx[call.ID] = len(s.tools)
		s.tools = append(s.tools, llm.ToolUse{
			ID:    call.ID,
			Name:  call.Function.Name,
			Input: json.RawMessage(args),
		})

		return
	}

	// No ID: a continuation of the call still open, which is the last one.
	if len(s.tools) == 0 {
		return
	}

	last := len(s.tools) - 1
	s.tools[last].Input = json.RawMessage(string(s.tools[last].Input) + args)

	if call.Function.Name != "" && s.tools[last].Name == "" {
		s.tools[last].Name = call.Function.Name
	}
}

// flushTools emits an event for every accumulated call not yet emitted.
func (s *stream) flushTools() {
	for i := s.flushed; i < len(s.tools); i++ {
		// Copied out, so that a later append reallocating s.tools cannot make
		// an already-emitted event point into a stale backing array.
		use := s.tools[i]
		s.pending = append(s.pending, llm.Event{Type: llm.EventToolUse, ToolUse: &use})
	}

	s.flushed = len(s.tools)
}
