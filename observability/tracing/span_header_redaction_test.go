package tracing

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v4/observability/keys"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// assertHeadersRedacted verifies that the given span carries a redacted attribute for
// each sensitive header, a real value for the benign header, and never leaks a secret.
func assertHeadersRedacted(t *testing.T, ros sdktrace.ReadOnlySpan, prefix, benignKey, benignValue string, secrets ...string) {
	t.Helper()

	attrs := map[string]string{}
	spanAttrs := ros.Attributes()
	for i := range spanAttrs {
		attrs[string(spanAttrs[i].Key)] = spanAttrs[i].Value.Emit()
	}

	authKey := fmt.Sprintf("%s.%s", prefix, "Authorization")
	cookieKey := fmt.Sprintf("%s.%s", prefix, http.CanonicalHeaderKey("cookie"))
	apiKey := fmt.Sprintf("%s.%s", prefix, http.CanonicalHeaderKey("x-api-key"))

	test.EqOp(t, redactedHeaderValue, attrs[authKey])
	test.EqOp(t, redactedHeaderValue, attrs[cookieKey])
	test.EqOp(t, redactedHeaderValue, attrs[apiKey])

	test.StrContains(t, attrs[fmt.Sprintf("%s.%s", prefix, benignKey)], benignValue)

	for _, secret := range secrets {
		for k, v := range attrs {
			if strings.Contains(v, secret) {
				t.Fatalf("secret %q leaked into span attribute %q: %q", secret, k, v)
			}
		}
	}
}

func TestAttachRequestToSpan_redactsSensitiveHeaders(T *testing.T) {
	T.Parallel()

	T.Run("sensitive request headers are redacted, benign headers retained", func(t *testing.T) {
		t.Parallel()

		tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
		_, span := tp.Tracer("test").Start(t.Context(), "test")

		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://example.com/x", http.NoBody)
		must.NoError(t, err)
		req.Header.Set("Authorization", "Bearer super-secret-token")
		req.Header.Set("Cookie", "session=super-secret-cookie")
		req.Header.Set("X-Api-Key", "super-secret-key")
		req.Header.Set("X-Trace-Context", "visible-value")

		AttachRequestToSpan(span, req)
		span.End()

		ros, ok := span.(sdktrace.ReadOnlySpan)
		must.True(t, ok)

		assertHeadersRedacted(t, ros, keys.RequestHeadersKey, "X-Trace-Context", "visible-value",
			"super-secret-token", "super-secret-cookie", "super-secret-key")
	})
}

func TestAttachResponseToSpan_redactsSensitiveHeaders(T *testing.T) {
	T.Parallel()

	T.Run("sensitive response headers are redacted, benign headers retained", func(t *testing.T) {
		t.Parallel()

		tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
		_, span := tp.Tracer("test").Start(t.Context(), "test")

		res := &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{},
		}
		res.Header.Set("Set-Cookie", "session=super-secret-cookie")
		res.Header.Set("X-Content-Type-Options", "nosniff")

		AttachResponseToSpan(span, res)
		span.End()

		ros, ok := span.(sdktrace.ReadOnlySpan)
		must.True(t, ok)

		attrs := map[string]string{}
		for _, kv := range ros.Attributes() {
			attrs[string(kv.Key)] = kv.Value.Emit()
		}

		setCookieKey := fmt.Sprintf("%s.%s", keys.ResponseHeadersKey, http.CanonicalHeaderKey("set-cookie"))
		test.EqOp(t, redactedHeaderValue, attrs[setCookieKey])

		benignKey := fmt.Sprintf("%s.%s", keys.ResponseHeadersKey, http.CanonicalHeaderKey("x-content-type-options"))
		test.StrContains(t, attrs[benignKey], "nosniff")

		for k, v := range attrs {
			if strings.Contains(v, "super-secret-cookie") {
				t.Fatalf("secret leaked into span attribute %q: %q", k, v)
			}
		}
	})
}
