package bridge

import (
	"errors"
	"testing"

	"github.com/primandproper/platform-go/v13/llm"

	anyllm "github.com/mozilla-ai/any-llm-go"
	anyllmerrors "github.com/mozilla-ai/any-llm-go/errors"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// textChunk builds a chunk carrying one text delta.
func textChunk(text string) anyllm.ChatCompletionChunk {
	return anyllm.ChatCompletionChunk{
		Choices: []anyllm.ChunkChoice{{Delta: anyllm.ChunkDelta{Content: text}}},
	}
}

// toolChunk builds a chunk carrying one tool call delta.
func toolChunk(id, name, args string) anyllm.ChatCompletionChunk {
	return anyllm.ChatCompletionChunk{
		Choices: []anyllm.ChunkChoice{{Delta: anyllm.ChunkDelta{
			ToolCalls: []anyllm.ToolCall{{
				ID:       id,
				Type:     "function",
				Function: anyllm.FunctionCall{Name: name, Arguments: args},
			}},
		}}},
	}
}

// finishChunk builds the terminal chunk, carrying the finish reason and usage.
func finishChunk(reason string, usage *anyllm.Usage) anyllm.ChatCompletionChunk {
	return anyllm.ChatCompletionChunk{
		Choices: []anyllm.ChunkChoice{{FinishReason: reason}},
		Usage:   usage,
	}
}

// upstream stands in for a provider's CompletionStream: it feeds the chunks and
// then the error, closing both channels the way any-llm-go's providers do.
func upstream(t *testing.T, sequence []anyllm.ChatCompletionChunk, err error) (chunkCh <-chan anyllm.ChatCompletionChunk, errCh <-chan error) {
	t.Helper()

	chunks := make(chan anyllm.ChatCompletionChunk)
	errs := make(chan error, 1)

	go func() {
		defer close(chunks)
		defer close(errs)

		for i := range sequence {
			chunks <- sequence[i]
		}

		if err != nil {
			errs <- err
		}
	}()

	return chunks, errs
}

// collect drains a stream, returning every event it yielded.
func collect(t *testing.T, stream llm.Stream) []llm.Event {
	t.Helper()

	var events []llm.Event
	for stream.Next() {
		events = append(events, stream.Current())
	}

	return events
}

// newStream wires an upstream to a Stream, asserting that finish runs exactly
// once by the time the test is over.
func newStream(t *testing.T, chunks []anyllm.ChatCompletionChunk, upstreamErr error) (stream llm.Stream, finishes *int) {
	t.Helper()

	count := 0
	chunkCh, errCh := upstream(t, chunks, upstreamErr)
	out := Stream(chunkCh, errCh, func(error, *llm.Usage) { count++ })

	t.Cleanup(func() {
		must.NoError(t, out.Close())
		test.EqOp(t, 1, count, test.Sprint("finish must run exactly once"))
	})

	return out, &count
}

func TestStream(T *testing.T) {
	T.Parallel()

	T.Run("yields text deltas and then done", func(t *testing.T) {
		t.Parallel()

		stream, _ := newStream(t, []anyllm.ChatCompletionChunk{
			textChunk("Hel"),
			textChunk("lo"),
			finishChunk(anyllm.FinishReasonStop, &anyllm.Usage{
				PromptTokens:     3,
				CompletionTokens: 2,
				TotalTokens:      5,
			}),
		}, nil)

		events := collect(t, stream)
		must.NoError(t, stream.Err())

		must.SliceLen(t, 3, events)
		test.EqOp(t, llm.EventTextDelta, events[0].Type)
		test.EqOp(t, "Hel", events[0].Text)
		test.EqOp(t, llm.EventTextDelta, events[1].Type)
		test.EqOp(t, "lo", events[1].Text)

		test.EqOp(t, llm.EventDone, events[2].Type)
		test.EqOp(t, llm.StopReasonEndTurn, events[2].StopReason)
		must.NotNil(t, events[2].Usage)
		test.EqOp(t, 3, events[2].Usage.InputTokens)
		test.EqOp(t, 2, events[2].Usage.OutputTokens)
		test.EqOp(t, 5, events[2].Usage.TotalTokens)
	})

	T.Run("yields reasoning deltas", func(t *testing.T) {
		t.Parallel()

		stream, _ := newStream(t, []anyllm.ChatCompletionChunk{
			{Choices: []anyllm.ChunkChoice{{Delta: anyllm.ChunkDelta{
				Reasoning: &anyllm.Reasoning{Content: "hmm"},
			}}}},
			// An empty reasoning delta is not an event.
			{Choices: []anyllm.ChunkChoice{{Delta: anyllm.ChunkDelta{
				Reasoning: &anyllm.Reasoning{},
			}}}},
			textChunk("answer"),
			finishChunk(anyllm.FinishReasonStop, nil),
		}, nil)

		events := collect(t, stream)

		must.SliceLen(t, 3, events)
		test.EqOp(t, llm.EventThinkingDelta, events[0].Type)
		test.EqOp(t, "hmm", events[0].Text)
		test.EqOp(t, llm.EventTextDelta, events[1].Type)
		test.EqOp(t, llm.EventDone, events[2].Type)
		test.Nil(t, events[2].Usage)
	})

	T.Run("ignores the role-only opening chunk", func(t *testing.T) {
		t.Parallel()

		// Anthropic's first chunk carries only the assistant role, which is
		// not something a consumer needs to be told.
		stream, _ := newStream(t, []anyllm.ChatCompletionChunk{
			{Choices: []anyllm.ChunkChoice{{Delta: anyllm.ChunkDelta{Role: anyllm.RoleAssistant}}}},
			finishChunk(anyllm.FinishReasonStop, nil),
		}, nil)

		events := collect(t, stream)

		must.SliceLen(t, 1, events)
		test.EqOp(t, llm.EventDone, events[0].Type)
	})

	T.Run("with an upstream that yields nothing at all", func(t *testing.T) {
		t.Parallel()

		stream, _ := newStream(t, nil, nil)

		events := collect(t, stream)
		must.NoError(t, stream.Err())

		must.SliceLen(t, 1, events)
		test.EqOp(t, llm.EventDone, events[0].Type)
	})

	T.Run("stops at an upstream error without yielding done", func(t *testing.T) {
		t.Parallel()

		stream, _ := newStream(t, []anyllm.ChatCompletionChunk{textChunk("par")},
			anyllmerrors.NewRateLimitError("anthropic", errors.New("429")))

		events := collect(t, stream)

		// The text that did arrive is still delivered — a failure partway
		// through is not a reason to throw away what the model already said.
		must.SliceLen(t, 1, events)
		test.EqOp(t, "par", events[0].Text)

		// And the error is normalized, so the consumer never sees anyllm's.
		must.ErrorIs(t, stream.Err(), llm.ErrRateLimited)

		_, ok := errors.AsType[*llm.RateLimitError](stream.Err())
		test.True(t, ok)
	})

	T.Run("stays stopped after an error", func(t *testing.T) {
		t.Parallel()

		stream, _ := newStream(t, nil, errors.New("boom"))

		test.False(t, stream.Next())
		test.False(t, stream.Next())
		must.Error(t, stream.Err())
	})

	T.Run("stays stopped after done", func(t *testing.T) {
		t.Parallel()

		stream, _ := newStream(t, []anyllm.ChatCompletionChunk{finishChunk(anyllm.FinishReasonStop, nil)}, nil)

		must.True(t, stream.Next())
		test.EqOp(t, llm.EventDone, stream.Current().Type)
		test.False(t, stream.Next())
		test.False(t, stream.Next())
	})

	T.Run("translates the finish reason", func(t *testing.T) {
		t.Parallel()

		stream, _ := newStream(t, []anyllm.ChatCompletionChunk{
			finishChunk(anyllm.FinishReasonLength, nil),
		}, nil)

		events := collect(t, stream)

		must.SliceLen(t, 1, events)
		test.EqOp(t, llm.StopReasonMaxTokens, events[0].StopReason)
	})
}

func TestStream_ToolCalls(T *testing.T) {
	T.Parallel()

	T.Run("accumulates OpenAI's raw fragments", func(t *testing.T) {
		t.Parallel()

		// OpenAI passes deltas through untouched: the ID and name arrive once,
		// then the arguments come as bare JSON fragments.
		stream, _ := newStream(t, []anyllm.ChatCompletionChunk{
			toolChunk("call_1", "get_weather", `{"ci`),
			toolChunk("", "", `ty":"Pa`),
			toolChunk("", "", `ris"}`),
			finishChunk(anyllm.FinishReasonToolCalls, nil),
		}, nil)

		events := collect(t, stream)
		must.NoError(t, stream.Err())

		must.SliceLen(t, 2, events)
		test.EqOp(t, llm.EventToolUse, events[0].Type)
		must.NotNil(t, events[0].ToolUse)
		test.EqOp(t, "call_1", events[0].ToolUse.ID)
		test.EqOp(t, "get_weather", events[0].ToolUse.Name)
		test.EqOp(t, `{"city":"Paris"}`, string(events[0].ToolUse.Input))

		test.EqOp(t, llm.EventDone, events[1].Type)
		test.EqOp(t, llm.StopReasonToolUse, events[1].StopReason)
	})

	T.Run("collapses Anthropic's cumulative re-emissions", func(t *testing.T) {
		t.Parallel()

		// Anthropic accumulates internally and re-emits the whole call every
		// time. Replaying those verbatim would hand the consumer three tool
		// calls where the model made one.
		stream, _ := newStream(t, []anyllm.ChatCompletionChunk{
			toolChunk("toolu_1", "get_weather", `{"ci`),
			toolChunk("toolu_1", "get_weather", `{"city":"Pa`),
			toolChunk("toolu_1", "get_weather", `{"city":"Paris"}`),
			finishChunk(anyllm.FinishReasonToolCalls, nil),
		}, nil)

		events := collect(t, stream)

		must.SliceLen(t, 2, events)
		test.EqOp(t, llm.EventToolUse, events[0].Type)
		must.NotNil(t, events[0].ToolUse)
		test.EqOp(t, "toolu_1", events[0].ToolUse.ID)
		test.EqOp(t, `{"city":"Paris"}`, string(events[0].ToolUse.Input))
		test.EqOp(t, llm.EventDone, events[1].Type)
	})

	T.Run("appends when a repeated ID carries a fragment rather than the whole call", func(t *testing.T) {
		t.Parallel()

		// An OpenAI-compatible server that echoes the ID on every delta is
		// neither of the two known framings. The prefix test is what tells the
		// cases apart: these fragments do not start with what came before, so
		// they are continuations and not re-emissions.
		stream, _ := newStream(t, []anyllm.ChatCompletionChunk{
			toolChunk("call_1", "get_weather", `{"ci`),
			toolChunk("call_1", "get_weather", `ty":"Pa`),
			toolChunk("call_1", "get_weather", `ris"}`),
			finishChunk(anyllm.FinishReasonToolCalls, nil),
		}, nil)

		events := collect(t, stream)

		must.SliceLen(t, 2, events)
		test.EqOp(t, `{"city":"Paris"}`, string(events[0].ToolUse.Input))
	})

	T.Run("yields the same events for both providers' framings", func(t *testing.T) {
		t.Parallel()

		// This is the whole point of the accumulator: a consumer must not have
		// to know which backend it is talking to.
		fragmented := []anyllm.ChatCompletionChunk{
			toolChunk("call_1", "search", `{"q"`),
			toolChunk("", "", `:"go"}`),
			finishChunk(anyllm.FinishReasonToolCalls, nil),
		}
		cumulative := []anyllm.ChatCompletionChunk{
			toolChunk("call_1", "search", `{"q"`),
			toolChunk("call_1", "search", `{"q":"go"}`),
			finishChunk(anyllm.FinishReasonToolCalls, nil),
		}

		fragmentedStream, _ := newStream(t, fragmented, nil)
		cumulativeStream, _ := newStream(t, cumulative, nil)

		test.Eq(t, collect(t, fragmentedStream), collect(t, cumulativeStream))
	})

	T.Run("emits one event per call when several are made", func(t *testing.T) {
		t.Parallel()

		stream, _ := newStream(t, []anyllm.ChatCompletionChunk{
			toolChunk("call_1", "a", `{"x"`),
			toolChunk("", "", `:1}`),
			toolChunk("call_2", "b", `{"y":2}`),
			finishChunk(anyllm.FinishReasonToolCalls, nil),
		}, nil)

		events := collect(t, stream)

		must.SliceLen(t, 3, events)

		// The first call is emitted as soon as the second begins, rather than
		// being held to the end of the stream — a consumer can start running it
		// while the model is still writing the next one.
		test.EqOp(t, llm.EventToolUse, events[0].Type)
		test.EqOp(t, "call_1", events[0].ToolUse.ID)
		test.EqOp(t, `{"x":1}`, string(events[0].ToolUse.Input))

		test.EqOp(t, llm.EventToolUse, events[1].Type)
		test.EqOp(t, "call_2", events[1].ToolUse.ID)
		test.EqOp(t, `{"y":2}`, string(events[1].ToolUse.Input))

		test.EqOp(t, llm.EventDone, events[2].Type)
	})

	T.Run("keeps each emitted call distinct", func(t *testing.T) {
		t.Parallel()

		// Every event must own its ToolUse: pointers into a slice that later
		// reallocates, or a single reused address, would leave every event
		// pointing at the last call.
		stream, _ := newStream(t, []anyllm.ChatCompletionChunk{
			toolChunk("call_1", "a", `{}`),
			toolChunk("call_2", "b", `{}`),
			toolChunk("call_3", "c", `{}`),
			toolChunk("call_4", "d", `{}`),
			finishChunk(anyllm.FinishReasonToolCalls, nil),
		}, nil)

		events := collect(t, stream)
		must.SliceLen(t, 5, events)

		for i, name := range []string{"a", "b", "c", "d"} {
			test.EqOp(t, name, events[i].ToolUse.Name)
		}
	})

	T.Run("interleaves text and tool calls in arrival order", func(t *testing.T) {
		t.Parallel()

		stream, _ := newStream(t, []anyllm.ChatCompletionChunk{
			textChunk("Let me check. "),
			toolChunk("call_1", "a", `{}`),
			toolChunk("call_2", "b", `{}`),
			finishChunk(anyllm.FinishReasonToolCalls, nil),
		}, nil)

		events := collect(t, stream)

		must.SliceLen(t, 4, events)
		test.EqOp(t, llm.EventTextDelta, events[0].Type)
		test.EqOp(t, llm.EventToolUse, events[1].Type)
		test.EqOp(t, llm.EventToolUse, events[2].Type)
		test.EqOp(t, llm.EventDone, events[3].Type)
	})

	T.Run("carries a name that arrives after the first fragment", func(t *testing.T) {
		t.Parallel()

		stream, _ := newStream(t, []anyllm.ChatCompletionChunk{
			toolChunk("call_1", "", `{}`),
			toolChunk("call_1", "late_name", `{}`),
			finishChunk(anyllm.FinishReasonToolCalls, nil),
		}, nil)

		events := collect(t, stream)

		must.SliceLen(t, 2, events)
		test.EqOp(t, "late_name", events[0].ToolUse.Name)
	})

	T.Run("carries a name on an ID-less continuation", func(t *testing.T) {
		t.Parallel()

		stream, _ := newStream(t, []anyllm.ChatCompletionChunk{
			toolChunk("call_1", "", `{"x"`),
			toolChunk("", "late_name", `:1}`),
			finishChunk(anyllm.FinishReasonToolCalls, nil),
		}, nil)

		events := collect(t, stream)

		must.SliceLen(t, 2, events)
		test.EqOp(t, "late_name", events[0].ToolUse.Name)
		test.EqOp(t, `{"x":1}`, string(events[0].ToolUse.Input))
	})

	T.Run("ignores a continuation with no call to continue", func(t *testing.T) {
		t.Parallel()

		// A fragment before any call has been announced has nowhere to go, and
		// inventing an anonymous call for it would put a tool call the model
		// never made in front of the consumer.
		stream, _ := newStream(t, []anyllm.ChatCompletionChunk{
			toolChunk("", "", `{"orphan":true}`),
			finishChunk(anyllm.FinishReasonStop, nil),
		}, nil)

		events := collect(t, stream)

		must.SliceLen(t, 1, events)
		test.EqOp(t, llm.EventDone, events[0].Type)
	})

	T.Run("emits an unfinished call when the stream ends mid-arguments", func(t *testing.T) {
		t.Parallel()

		// A response truncated at max_tokens leaves invalid JSON in Input.
		// llm.ToolUse documents that; dropping the call instead would hide from
		// the consumer that the model tried.
		stream, _ := newStream(t, []anyllm.ChatCompletionChunk{
			toolChunk("call_1", "a", `{"x"`),
			finishChunk(anyllm.FinishReasonLength, nil),
		}, nil)

		events := collect(t, stream)

		must.SliceLen(t, 2, events)
		test.EqOp(t, `{"x"`, string(events[0].ToolUse.Input))
		test.EqOp(t, llm.StopReasonMaxTokens, events[1].StopReason)
	})
}

func TestStream_Close(T *testing.T) {
	T.Parallel()

	T.Run("stops the stream and runs finish once", func(t *testing.T) {
		t.Parallel()

		finishes := 0
		chunkCh, errCh := upstream(t, []anyllm.ChatCompletionChunk{
			textChunk("one"),
			textChunk("two"),
			textChunk("three"),
			finishChunk(anyllm.FinishReasonStop, nil),
		}, nil)
		stream := Stream(chunkCh, errCh, func(error, *llm.Usage) { finishes++ })

		must.True(t, stream.Next())
		test.EqOp(t, "one", stream.Current().Text)

		must.NoError(t, stream.Close())
		test.EqOp(t, 1, finishes)

		test.False(t, stream.Next())
		must.NoError(t, stream.Err())

		// Closing again neither re-runs finish nor errors.
		must.NoError(t, stream.Close())
		test.EqOp(t, 1, finishes)
	})

	T.Run("releases a producer blocked mid-stream", func(t *testing.T) {
		t.Parallel()

		// The channels any-llm-go hands back are unbuffered, so a consumer that
		// walks away leaves the producing goroutine parked on a send forever.
		// Close has to drain what is left; -race and the goroutine leak checker
		// are what actually police this.
		done := make(chan struct{})
		chunkCh := make(chan anyllm.ChatCompletionChunk)
		errCh := make(chan error, 1)

		go func() {
			defer close(done)
			defer close(chunkCh)
			defer close(errCh)

			for range 100 {
				chunkCh <- textChunk("x")
			}
		}()

		stream := Stream(chunkCh, errCh, func(error, *llm.Usage) {})

		must.True(t, stream.Next())
		must.NoError(t, stream.Close())

		<-done
	})

	T.Run("drops events decoded but not yet yielded", func(t *testing.T) {
		t.Parallel()

		// One chunk carrying reasoning, text, and a finish reason decodes into
		// several events at once. Closing after the first must not keep handing
		// out the rest.
		chunkCh, errCh := upstream(t, []anyllm.ChatCompletionChunk{{
			Choices: []anyllm.ChunkChoice{{
				Delta: anyllm.ChunkDelta{
					Reasoning: &anyllm.Reasoning{Content: "hmm"},
					Content:   "answer",
				},
				FinishReason: anyllm.FinishReasonStop,
			}},
		}}, nil)
		stream := Stream(chunkCh, errCh, func(error, *llm.Usage) {})

		must.True(t, stream.Next())
		test.EqOp(t, llm.EventThinkingDelta, stream.Current().Type)

		must.NoError(t, stream.Close())
		test.False(t, stream.Next())
	})

	T.Run("after the stream already finished", func(t *testing.T) {
		t.Parallel()

		finishes := 0
		chunkCh, errCh := upstream(t, []anyllm.ChatCompletionChunk{
			finishChunk(anyllm.FinishReasonStop, nil),
		}, nil)
		stream := Stream(chunkCh, errCh, func(error, *llm.Usage) { finishes++ })

		test.SliceLen(t, 1, collect(t, stream))
		test.EqOp(t, 1, finishes)

		must.NoError(t, stream.Close())
		test.EqOp(t, 1, finishes)
	})

	T.Run("reports the terminal error to finish", func(t *testing.T) {
		t.Parallel()

		var finishErr error
		finishes := 0

		chunkCh, errCh := upstream(t, nil, anyllmerrors.NewAuthenticationError("openai", errors.New("bad key")))
		stream := Stream(chunkCh, errCh, func(err error, _ *llm.Usage) {
			finishes++
			finishErr = err
		})

		test.SliceEmpty(t, collect(t, stream))
		test.EqOp(t, 1, finishes)
		must.ErrorIs(t, finishErr, llm.ErrAuthentication)

		must.NoError(t, stream.Close())
		test.EqOp(t, 1, finishes)
	})

	T.Run("with no finish hook", func(t *testing.T) {
		t.Parallel()

		chunkCh, errCh := upstream(t, []anyllm.ChatCompletionChunk{
			finishChunk(anyllm.FinishReasonStop, nil),
		}, nil)
		stream := Stream(chunkCh, errCh, nil)

		test.SliceLen(t, 1, collect(t, stream))
		must.NoError(t, stream.Close())
	})
}
