package consumererr

import (
	"context"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestSend(T *testing.T) {
	T.Parallel()

	T.Run("delivers on a buffered channel", func(t *testing.T) {
		t.Parallel()

		errs := make(chan error, 1)
		want := errors.New("boom")

		Send(t.Context(), errs, want)

		must.SliceLen(t, 1, []error{<-errs})
	})

	T.Run("a nil channel is a caller that wants no errors", func(t *testing.T) {
		t.Parallel()

		done := make(chan struct{})
		go func() {
			defer close(done)
			Send(t.Context(), nil, errors.New("boom"))
		}()

		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("Send hung on a nil channel")
		}
	})

	T.Run("unblocks when the context is done and nobody is draining", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(t.Context())
		errs := make(chan error) // unbuffered, never read

		done := make(chan struct{})
		go func() {
			defer close(done)
			Send(ctx, errs, errors.New("boom"))
		}()

		cancel()

		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("Send wedged against a channel nobody drains")
		}
	})

	T.Run("delivers even when the context is already canceled", func(t *testing.T) {
		t.Parallel()

		// This is the race the two-phase send exists for: a handler that cancels
		// its own context and then returns an error. A single select on both
		// cases would drop the error roughly half the time, so this asserts the
		// outcome a hundred times rather than once.
		for range 100 {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()

			errs := make(chan error, 1)
			Send(ctx, errs, errors.New("boom"))

			select {
			case got := <-errs:
				test.NotNil(t, got)
			default:
				t.Fatal("a canceled context dropped an error the channel could accept")
			}
		}
	})
}
