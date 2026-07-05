package encoding

import (
	"testing"

	loggingnoop "github.com/primandproper/platform-go/v4/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/v4/observability/tracing/noop"

	"github.com/shoenig/test"
)

func Test_clientEncoder_ContentType(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		e := ProvideClientEncoder(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), ContentTypeJSON)

		test.NotEq(t, "", e.ContentType())
	})
}

func Test_buildContentType(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		test.NotNil(t, buildContentType("test"))
	})
}

func TestContentTypeToString(T *testing.T) {
	T.Parallel()

	T.Run("with JSON", func(t *testing.T) {
		t.Parallel()

		test.NotEq(t, "", ContentTypeToString(ContentTypeJSON))
	})

	T.Run("with XML", func(t *testing.T) {
		t.Parallel()

		test.NotEq(t, "", ContentTypeToString(ContentTypeXML))
	})

	T.Run("with Emoji", func(t *testing.T) {
		t.Parallel()

		test.NotEq(t, "", ContentTypeToString(ContentTypeEmoji))
	})

	T.Run("with invalid input", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "", ContentTypeToString(nil))
	})
}

func Test_contentTypeFromString(T *testing.T) {
	T.Parallel()

	T.Run("with JSON", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, ContentTypeJSON, contentTypeFromString(contentTypeJSON))
	})

	T.Run("with XML", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, ContentTypeXML, contentTypeFromString(contentTypeXML))
	})

	T.Run("with TOML", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, ContentTypeTOML, contentTypeFromString(contentTypeTOML))
	})

	T.Run("with YAML", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, ContentTypeYAML, contentTypeFromString(contentTypeYAML))
	})

	T.Run("with Emoji", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, ContentTypeEmoji, contentTypeFromString(contentTypeEmoji))
	})

	T.Run("with unknown defaults to JSON", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, ContentTypeJSON, contentTypeFromString("unknown"))
	})

	T.Run("ignores charset parameter", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, ContentTypeXML, contentTypeFromString("application/xml; charset=utf-8"))
		test.EqOp(t, ContentTypeJSON, contentTypeFromString("application/json; charset=utf-8"))
	})
}
