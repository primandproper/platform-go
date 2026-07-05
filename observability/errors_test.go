package observability

import (
	"errors"
	"testing"

	loggingnoop "github.com/primandproper/platform-go/v4/observability/logging/noop"
	"github.com/primandproper/platform-go/v4/observability/tracing"

	"github.com/shoenig/test"
	"google.golang.org/grpc/codes"
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
