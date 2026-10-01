package grpc

import (
	"context"
	"errors"
	"testing"

	"github.com/shoenig/test"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestStreamBroken(T *testing.T) {
	T.Parallel()

	for name, tc := range map[string]struct {
		err  error
		want codes.Code
	}{
		"a client that went away":        {err: status.FromContextError(context.Canceled).Err(), want: codes.Canceled},
		"a deadline that passed":         {err: status.FromContextError(context.DeadlineExceeded).Err(), want: codes.DeadlineExceeded},
		"a transport error with no code": {err: errors.New("connection reset"), want: codes.Canceled},
		"a status that names nothing":    {err: status.Error(codes.Unknown, "?"), want: codes.Canceled},
	} {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			// Never Internal: a stream that broke is not the server's fault.
			test.EqOp(t, tc.want, streamBroken(tc.err))
		})
	}
}
