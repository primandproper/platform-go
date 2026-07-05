package tracing

import (
	"fmt"
	"net/http"
	"time"

	"github.com/primandproper/platform-go/v4/filtering"
	"github.com/primandproper/platform-go/v4/observability/keys"

	"github.com/mssola/useragent"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

func keyValueForValue(k string, x any) attribute.KeyValue {
	if x == nil {
		return attribute.String(k, "nil")
	}

	switch v := x.(type) {
	case bool:
		return attribute.Bool(k, v)
	case []bool:
		return attribute.BoolSlice(k, v)
	case int:
		return attribute.Int(k, v)
	case []int:
		return attribute.IntSlice(k, v)
	case uint8:
		return attribute.Int64(k, int64(v))
	case uint16:
		return attribute.Int64(k, int64(v))
	case uint32:
		return attribute.Int64(k, int64(v))
	case uint64:
		return attribute.String(k, fmt.Sprintf("%d", v))
	case int64:
		return attribute.Int64(k, v)
	case []int64:
		return attribute.Int64Slice(k, v)
	case float64:
		return attribute.Float64(k, v)
	case []float64:
		return attribute.Float64Slice(k, v)
	case string:
		return attribute.String(k, v)
	case []string:
		return attribute.StringSlice(k, v)
	case time.Time:
		return attribute.String(k, v.Format(time.RFC3339Nano))
	case fmt.Stringer:
		return attribute.Stringer(k, v)
	default:
		return attribute.String(k, fmt.Sprintf("%+v", x))
	}
}

func AttachToSpan[T any](span trace.Span, attachmentKey string, x T) {
	// IsRecording gates the SetAttributes call: a non-recording span (noop or
	// sampled-out) drops attributes anyway, and skipping the call avoids
	// allocating the variadic KeyValue slice on that path.
	if span != nil && span.IsRecording() {
		span.SetAttributes(keyValueForValue(attachmentKey, x))
	}
}

// ServicePermissionChecker is a minimal interface for attaching permission state to spans.
// Export it so the app can adapt authorization.ServiceRolePermissionChecker.
type ServicePermissionChecker interface {
	IsServiceAdmin() bool
}

// SessionContextDataForTracing is a minimal interface for attaching session context to spans.
// Export it so the app can adapt sessions.ContextData.
type SessionContextDataForTracing interface {
	GetUserID() string
	GetServicePermissions() ServicePermissionChecker
	GetActiveAccountID() string
}

// AttachSessionContextDataToSpan provides a consistent way to attach a SessionContextData object to a span.
func AttachSessionContextDataToSpan(span trace.Span, sessionCtxData SessionContextDataForTracing) {
	if sessionCtxData != nil {
		AttachToSpan(span, keys.RequesterIDKey, sessionCtxData.GetUserID())
		AttachToSpan(span, keys.ActiveAccountIDKey, sessionCtxData.GetActiveAccountID())
		if servicePerms := sessionCtxData.GetServicePermissions(); servicePerms != nil {
			AttachToSpan(span, keys.UserIsServiceAdminKey, servicePerms.IsServiceAdmin())
		}
	}
}

// redactedHeaderValue replaces the value of a sensitive header attached to a span.
const redactedHeaderValue = "[REDACTED]"

// sensitiveHeaders holds the canonicalized names of headers whose values must never
// be attached to a span, to avoid leaking credentials into the trace backend.
var sensitiveHeaders = map[string]struct{}{
	"Authorization":       {},
	"Proxy-Authorization": {},
	"Cookie":              {},
	"Set-Cookie":          {},
	"Api-Key":             {},
	"X-Api-Key":           {},
	"X-Auth-Token":        {},
	"X-Authentication":    {},
	"X-Csrf-Token":        {},
}

// attachHeadersToSpan attaches HTTP headers to a span, redacting the values of any
// sensitive headers (credentials, cookies, API keys) while still recording their presence.
func attachHeadersToSpan(span trace.Span, prefix string, header http.Header) {
	for k, v := range header {
		key := fmt.Sprintf("%s.%s", prefix, k)
		if _, sensitive := sensitiveHeaders[http.CanonicalHeaderKey(k)]; sensitive {
			AttachToSpan(span, key, redactedHeaderValue)
			continue
		}

		AttachToSpan(span, key, v)
	}
}

// AttachRequestToSpan attaches a given HTTP request to a span.
func AttachRequestToSpan(span trace.Span, req *http.Request) {
	if req != nil {
		AttachToSpan(span, keys.RequestURIKey, req.URL.String())
		AttachToSpan(span, keys.RequestMethodKey, req.Method)
		AttachUserAgentDataToSpan(span, req)

		attachHeadersToSpan(span, keys.RequestHeadersKey, req.Header)
	}
}

// AttachResponseToSpan attaches a given *http.Response to a span.
func AttachResponseToSpan(span trace.Span, res *http.Response) {
	if res != nil {
		AttachRequestToSpan(span, res.Request)
		// Guard the direct SetAttributes call the same way AttachToSpan does, so a nil
		// (or non-recording) span doesn't panic.
		if span != nil && span.IsRecording() {
			span.SetAttributes(attribute.Int(keys.ResponseStatusKey, res.StatusCode))
		}

		attachHeadersToSpan(span, keys.ResponseHeadersKey, res.Header)
	}
}

// AttachErrorToSpan attaches a given error to a span.
func AttachErrorToSpan(span trace.Span, description string, err error) {
	if err != nil {
		span.SetStatus(codes.Error, description)
		span.RecordError(
			err,
			trace.WithStackTrace(true),
			trace.WithTimestamp(time.Now()),
			trace.WithAttributes(attribute.String("error.description", description)),
		)
	}
}

// AttachQueryFilterToSpan attaches a given query filter to a span.
func AttachQueryFilterToSpan(span trace.Span, filter *filtering.QueryFilter) {
	if filter != nil {
		if filter.MaxResponseSize != nil {
			AttachToSpan(span, keys.FilterLimitKey, *filter.MaxResponseSize)
		}

		if filter.Cursor != nil {
			AttachToSpan(span, keys.FilterCursorKey, *filter.Cursor)
		}

		if filter.CreatedAfter != nil {
			AttachToSpan(span, keys.FilterCreatedAfterKey, *filter.CreatedAfter)
		}

		if filter.CreatedBefore != nil {
			AttachToSpan(span, keys.FilterCreatedBeforeKey, *filter.CreatedBefore)
		}

		if filter.UpdatedAfter != nil {
			AttachToSpan(span, keys.FilterUpdatedAfterKey, *filter.UpdatedAfter)
		}

		if filter.UpdatedBefore != nil {
			AttachToSpan(span, keys.FilterUpdatedBeforeKey, *filter.UpdatedBefore)
		}

		if filter.SortBy != nil {
			AttachToSpan(span, keys.FilterSortByKey, *filter.SortBy)
		}
	} else {
		AttachToSpan(span, keys.FilterIsNilKey, true)
	}
}

// AttachUserAgentDataToSpan attaches a given search query to a span.
func AttachUserAgentDataToSpan(span trace.Span, req *http.Request) {
	header := req.Header.Get("User-Agent")
	ua := useragent.New(header)

	if ua != nil {
		AttachToSpan(span, keys.UserAgentOSKey, ua.OS())
		AttachToSpan(span, keys.UserAgentMobileKey, ua.Mobile())
		AttachToSpan(span, keys.UserAgentBotKey, ua.Bot())
	}
}
