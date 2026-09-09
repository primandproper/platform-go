package http

import (
	"testing"

	operationshttp "github.com/primandproper/platform-go/v14/operations/http"

	"github.com/primandproper/primitives-go/observability/logging"
	"github.com/primandproper/primitives-go/observability/tracing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNewOptions(T *testing.T) {
	T.Parallel()

	T.Run("defaults", func(t *testing.T) {
		t.Parallel()

		o := newOptions(nil)

		test.EqOp(t, BasePath, o.basePath)
		test.EqOp(t, operationshttp.BasePath, o.operationsPath)
		test.Eq(t, []string{"privacy"}, o.tags)
		test.Nil(t, o.resolver)
		test.Nil(t, o.logger)
		test.Nil(t, o.tracerProvider)
	})

	T.Run("skips nil options", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{nil, WithBasePath("/gdpr"), nil})

		test.EqOp(t, "/gdpr", o.basePath)
	})
}

func TestOptions(T *testing.T) {
	T.Parallel()

	T.Run("WithSubjectResolver", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{WithSubjectResolver(subjectFromContext)})

		must.NotNil(t, o.resolver)
	})

	// An empty path is ignored rather than honored: a surface has to be mounted
	// somewhere, and the empty string is a config that failed to say where.
	T.Run("an empty base path is ignored", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{WithBasePath("")})

		test.EqOp(t, BasePath, o.basePath)
	})

	T.Run("WithOperationsBasePath", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{WithOperationsBasePath("/v1/operations")})

		test.EqOp(t, "/v1/operations", o.operationsPath)
	})

	T.Run("an empty operations base path is ignored", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{WithOperationsBasePath("")})

		test.EqOp(t, operationshttp.BasePath, o.operationsPath)
	})

	T.Run("WithTags replaces the default", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{WithTags("gdpr", "privacy")})

		test.Eq(t, []string{"gdpr", "privacy"}, o.tags)
	})

	T.Run("WithLogger and WithTracerProvider", func(t *testing.T) {
		t.Parallel()

		logger := logging.EnsureLogger(nil)
		provider := tracing.EnsureTracerProvider(nil)

		o := newOptions([]Option{WithLogger(logger), WithTracerProvider(provider)})

		must.NotNil(t, o.logger)
		must.NotNil(t, o.tracerProvider)
	})

	// Absent means noop rather than nil: a caller that names no observability
	// gets handlers that observe nowhere, not handlers that panic on the first
	// span.
	T.Run("handlers built with no observability still serve", func(t *testing.T) {
		t.Parallel()

		handlers, err := New(serviceReturning(nil), WithSubjectResolver(subjectFromContext))

		must.NoError(t, err)
		must.NotNil(t, handlers.o11y)
	})
}
