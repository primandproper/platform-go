package logging

import (
	"net/http"

	"go.opentelemetry.io/otel/trace"
)

// SpanInfo holds span and trace IDs extracted from a trace.Span.
type SpanInfo struct {
	SpanID  string
	TraceID string
}

// ExtractSpanInfo extracts span and trace IDs from a trace.Span. A nil span
// yields a zero-value SpanInfo rather than panicking, so WithSpan(nil) is safe.
func ExtractSpanInfo(span trace.Span) SpanInfo {
	if span == nil {
		return SpanInfo{}
	}

	spanCtx := span.SpanContext()
	return SpanInfo{
		SpanID:  spanCtx.SpanID().String(),
		TraceID: spanCtx.TraceID().String(),
	}
}

// RequestInfo holds HTTP request metadata extracted for logging.
type RequestInfo struct {
	Method    string
	Path      string
	Query     string
	RequestID string
}

// ExtractRequestInfo extracts logging-relevant fields from an HTTP request.
func ExtractRequestInfo(req *http.Request, requestIDFunc RequestIDFunc) RequestInfo {
	var info RequestInfo
	if req == nil {
		return info
	}

	info.Method = req.Method

	if req.URL != nil {
		info.Path = req.URL.Path
		info.Query = req.URL.RawQuery
	}

	if requestIDFunc != nil {
		info.RequestID = requestIDFunc(req)
	}

	return info
}
