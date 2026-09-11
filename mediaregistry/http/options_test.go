package http

import (
	"testing"
	"time"

	"github.com/primandproper/primitives-go/v2/observability/logging"
	loggingnoop "github.com/primandproper/primitives-go/v2/observability/logging/noop"
	tracingnoop "github.com/primandproper/primitives-go/v2/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestOptions(T *testing.T) {
	T.Parallel()

	T.Run("the defaults are a mounted base path, one tag, and the owner comparison", func(t *testing.T) {
		t.Parallel()

		o := newOptions(nil)

		test.EqOp(t, BasePath, o.basePath)
		test.Eq(t, []string{"uploads"}, o.tags)
		must.NotNil(t, o.entitlement)

		// No resolver, which is what New refuses over.
		test.Nil(t, o.resolver)

		entitled, err := o.entitlement(t.Context(), ownedCaller(), testObject())
		must.NoError(t, err)
		test.True(t, entitled)
	})

	T.Run("each option sets the field it names", func(t *testing.T) {
		t.Parallel()

		var logger logging.Logger = loggingnoop.NewLogger()
		tracerProvider := tracingnoop.NewTracerProvider()

		o := newOptions([]Option{
			WithCallerResolver(resolverFromContext),
			WithBasePath("/api/v1/files"),
			WithTags("files", "media"),
			WithLogger(logger),
			WithTracerProvider(tracerProvider),
		})

		must.NotNil(t, o.resolver)
		test.EqOp(t, "/api/v1/files", o.basePath)
		test.Eq(t, []string{"files", "media"}, o.tags)
		test.Eq(t, logger, o.logger)
		test.Eq(t, tracerProvider, o.tracerProvider)
	})

	T.Run("nil options are ignored", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{nil})

		test.EqOp(t, BasePath, o.basePath)
	})

	T.Run("an empty base path keeps the default rather than mounting at the root", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{WithBasePath("")})

		test.EqOp(t, BasePath, o.basePath)
	})
}

func TestModTimeOf(T *testing.T) {
	T.Parallel()

	T.Run("the registration time, which is every row registry writes today", func(t *testing.T) {
		t.Parallel()

		object := testObject()
		object.CreatedAt = time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)

		test.EqOp(t, object.CreatedAt, modTimeOf(object))
	})

	T.Run("the update time, for the day a statement assigns one", func(t *testing.T) {
		t.Parallel()

		updated := time.Date(2026, time.September, 8, 9, 30, 0, 0, time.UTC)

		object := testObject()
		object.CreatedAt = time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
		object.LastUpdatedAt = &updated

		test.EqOp(t, updated, modTimeOf(object))
	})
}
