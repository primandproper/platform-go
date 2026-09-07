package observability

import (
	"errors"
	"strings"
	"testing"

	loggingnoop "github.com/primandproper/platform-go/v14/observability/logging/noop"
	"github.com/primandproper/platform-go/v14/observability/tracing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestPrepareAndLogError(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		err := errors.New("blah")
		logger := loggingnoop.NewLogger()
		_, span := tracing.StartSpan(ctx)

		test.Error(t, PrepareAndLogError(err, logger, span, "things and %s", "stuff"))
	})

	T.Run("with nil error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()
		_, span := tracing.StartSpan(ctx)

		test.NoError(t, PrepareAndLogError(nil, logger, span, "things and %s", "stuff"))
	})

	T.Run("with nil span", func(t *testing.T) {
		t.Parallel()

		err := errors.New("blah")
		logger := loggingnoop.NewLogger()

		test.Error(t, PrepareAndLogError(err, logger, nil, "things and %s", "stuff"))
	})

	T.Run("with nil logger", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		err := errors.New("blah")
		_, span := tracing.StartSpan(ctx)

		test.Error(t, PrepareAndLogError(err, nil, span, "things and %s", "stuff"))
	})

	T.Run("with empty description", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		err := errors.New("blah")
		logger := loggingnoop.NewLogger()
		_, span := tracing.StartSpan(ctx)

		test.Error(t, PrepareAndLogError(err, logger, span, ""))
	})
}

func TestPrepareError(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		err := errors.New("blah")
		_, span := tracing.StartSpan(ctx)

		test.Error(t, PrepareError(err, span, "things and %s", "stuff"))
	})

	T.Run("with nil error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		_, span := tracing.StartSpan(ctx)

		test.NoError(t, PrepareError(nil, span, "things and %s", "stuff"))
	})

	T.Run("with nil span", func(t *testing.T) {
		t.Parallel()

		err := errors.New("blah")

		test.Error(t, PrepareError(err, nil, "things and %s", "stuff"))
	})

	T.Run("with empty description", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		err := errors.New("blah")
		_, span := tracing.StartSpan(ctx)

		actual := PrepareError(err, span, "")
		test.Error(t, actual)
		test.Eq(t, err, actual)
	})
}

func TestAcknowledgeError(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		err := errors.New("blah")
		logger := loggingnoop.NewLogger()
		_, span := tracing.StartSpan(ctx)

		AcknowledgeError(err, logger, span, "things and %s", "stuff")
	})

	T.Run("with nil span", func(t *testing.T) {
		t.Parallel()

		err := errors.New("blah")
		logger := loggingnoop.NewLogger()

		AcknowledgeError(err, logger, nil, "things and %s", "stuff")
	})

	T.Run("with nil logger", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		err := errors.New("blah")
		_, span := tracing.StartSpan(ctx)

		AcknowledgeError(err, nil, span, "things and %s", "stuff")
	})

	T.Run("with empty description", func(t *testing.T) {
		t.Parallel()

		err := errors.New("blah")
		logger := loggingnoop.NewLogger()

		AcknowledgeError(err, logger, nil, "")
	})
}

func TestPrepareAndLogGRPCStatus(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		err := errors.New("blah")
		logger := loggingnoop.NewLogger()
		_, span := tracing.StartSpan(ctx)

		test.Error(t, PrepareAndLogGRPCStatus(err, logger, span, codes.Internal, "things and %s", "stuff"))
	})

	T.Run("with nil error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()
		_, span := tracing.StartSpan(ctx)

		test.NoError(t, PrepareAndLogGRPCStatus(nil, logger, span, codes.Internal, "things and %s", "stuff"))
	})

	T.Run("with nil span", func(t *testing.T) {
		t.Parallel()

		err := errors.New("blah")
		logger := loggingnoop.NewLogger()

		test.Error(t, PrepareAndLogGRPCStatus(err, logger, nil, codes.Internal, "things and %s", "stuff"))
	})

	T.Run("with nil logger", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		err := errors.New("blah")
		_, span := tracing.StartSpan(ctx)

		test.Error(t, PrepareAndLogGRPCStatus(err, nil, span, codes.Internal, "things and %s", "stuff"))
	})

	T.Run("with empty description", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		err := errors.New("blah")
		logger := loggingnoop.NewLogger()
		_, span := tracing.StartSpan(ctx)

		test.Error(t, PrepareAndLogGRPCStatus(err, logger, span, codes.Internal, ""))
	})
}

func TestGRPCStatusError(T *testing.T) {
	T.Parallel()

	T.Run("nothing to report", func(t *testing.T) {
		t.Parallel()

		test.Nil(t, GRPCStatusError(nil, codes.Internal, "doing the thing"))
	})

	T.Run("answers to both idioms", func(t *testing.T) {
		t.Parallel()

		sentinel := errors.New("the widget is not in this catalog")

		err := GRPCStatusError(sentinel, codes.FailedPrecondition, "fetching the widget")
		must.Error(t, err)

		// The chain, for errors.Is.
		test.True(t, errors.Is(err, sentinel))

		// The status, for status.Code — and the message is the one given, not
		// the error's text.
		st, ok := status.FromError(err)
		must.True(t, ok)
		test.EqOp(t, codes.FailedPrecondition, st.Code())
		test.EqOp(t, "fetching the widget", st.Message())
	})

	T.Run("an empty message falls back to the code", func(t *testing.T) {
		t.Parallel()

		err := GRPCStatusError(errors.New("blah"), codes.DataLoss, "")
		must.Error(t, err)

		test.EqOp(t, codes.DataLoss.String(), status.Convert(err).Message())
	})
}

func TestPrepareAndLogGRPCStatus_keepsTheChain(T *testing.T) {
	T.Parallel()

	// The regression this function was rewritten for: it used to return
	// status.Errorf(code, "%v", wrapped), which is a *status.Error whose only
	// content is a string, and the sentinel was gone before any interceptor saw
	// it.
	T.Run("the sentinel is still there", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		sentinel := errors.New("the widget is not in this catalog")
		logger := loggingnoop.NewLogger()
		_, span := tracing.StartSpan(ctx)

		err := PrepareAndLogGRPCStatus(sentinel, logger, span, codes.FailedPrecondition, "fetching widget %s", "abc")
		must.Error(t, err)

		test.True(t, errors.Is(err, sentinel))
		test.EqOp(t, codes.FailedPrecondition, status.Code(err))
	})

	T.Run("the description is the message and the chain is not", func(t *testing.T) {
		t.Parallel()

		inner := errors.New("scanning widget_catalog_entries")

		err := PrepareAndLogGRPCStatus(inner, nil, nil, codes.Internal, "fetching the widget")
		must.Error(t, err)

		msg := status.Convert(err).Message()
		test.EqOp(t, "fetching the widget", msg)
		test.False(t, strings.Contains(msg, "widget_catalog_entries"))

		// Error() is still the whole chain, for the log line and the details.
		test.True(t, strings.Contains(err.Error(), "widget_catalog_entries"))
	})
}
