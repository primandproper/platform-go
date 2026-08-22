package httpclient

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/primandproper/platform-go/v13/charset"
	"github.com/primandproper/platform-go/v13/retry"
)

// StatusError is a response an exchange would not accept: a status outside 2xx.
//
// It carries what an operator reading the log line actually needs — which
// request, what the server said, and what the server said about it — and it
// carries the status code so a caller can branch on a 404 without parsing a
// string.
//
// Match it with errors.As. It also matches retry.ErrUnretryable under errors.Is
// for the statuses DefaultRetryClassification calls terminal, which is what
// lets a caller's own retry loop stop on a 400 and keep trying a 429 without
// writing that rule a second time.
type StatusError struct {
	// Method is the request method, so a log line says what was attempted and
	// not only where.
	Method string

	// Path is the request's path. Not the full URL: the host is the caller's
	// own configuration and adds nothing to a message about the response, and a
	// query string is exactly where a token or a customer identifier ends up —
	// which is a poor thing to put in a string destined for a log.
	Path string

	// Status is the status line as the server sent it, "404 Not Found".
	Status string

	// Body is the response body, whitespace-trimmed and cut to at most
	// WithErrorBodyLimit bytes on a rune boundary. Truncated says whether the
	// cut happened; Binary says whether there was a body that is not here.
	Body string

	// ContentType is the response's Content-Type header as the server sent it,
	// which may be empty. It is what makes a Binary error legible: the number of
	// bytes alone does not say whether the server answered CBOR or a proxy
	// answered a gzip stream.
	ContentType string

	// StatusCode is the response status code.
	StatusCode int

	// BodySize is how many bytes of the body were read — at most
	// WithErrorBodyLimit, plus the one byte used to detect truncation. It is not
	// the size of the body the server sent, which is the point: nothing here
	// reads far enough to know that.
	BodySize int

	// Truncated reports whether the server's body was longer than the limit, so
	// a reader knows the message ends because the bound was reached rather than
	// because the server had nothing more to say.
	Truncated bool

	// Binary reports that the server sent a body which is not text, so Body is
	// empty and BodySize and ContentType are all that is kept of it.
	//
	// An exchange over CBOR is the case this exists for. Its error bodies are
	// bytes, and a bounded prefix of them run through a string is mojibake in a
	// log line — the sort that arrives at a UTF-8 column or a JSON log encoder
	// and fails there, one layer away from anything that explains why.
	Binary bool
}

// newStatusError renders a refused response as an error, reading no more of its
// body than the limit allows.
//
// The bound is on the read and not merely on the string, which is the whole
// point of having one. A proxy's HTML error page runs to megabytes, and
// buffering it in order to throw all but 512 bytes of it away is the incident
// this is meant to prevent, not a smaller version of it. The cost is that the
// connection is not reusable when a body is left unread, which is a fair price
// on a path that has already failed.
//
// Whether the bytes are text is decided by looking at them rather than at the
// exchange's content type, because the two are routinely different: a CBOR
// endpoint's 502 comes from a proxy that has never heard of CBOR and answers
// HTML, and a JSON endpoint behind a misconfigured gateway can answer a gzip
// stream. What was actually read is the only thing that settles it.
func newStatusError(req *http.Request, resp *http.Response, limit int) *StatusError {
	// One byte past the limit, so a body that exactly fills it is not reported
	// as cut. The read error, if any, is discarded: the status is the finding
	// here, and "the server said 503 and then the connection died mid-body" is
	// still a 503.
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, int64(limit)+1)) //nolint:errcheck // the status is the finding; a short body does not change it.

	statusErr := &StatusError{
		Method:      req.Method,
		Path:        req.URL.Path,
		Status:      resp.Status,
		ContentType: resp.Header.Get("Content-Type"),
		StatusCode:  resp.StatusCode,
		BodySize:    len(raw),
		Truncated:   len(raw) > limit,
	}

	trimmed := strings.TrimSpace(string(raw))

	switch {
	case limit <= 0 || trimmed == "":
		// Nothing was to be kept, so there is nothing to report about it. A zero
		// limit is a caller saying this endpoint's failures are worthless or
		// sensitive, and answering it with a byte count would be answering a
		// question it declined to ask.
	case isText(trimmed):
		statusErr.Body = charset.TruncateUTF8(trimmed, limit)
	default:
		statusErr.Binary = true
	}

	return statusErr
}

// isText reports whether what was read is text, forgiving an incomplete rune at
// the very end.
//
// The forgiveness is the whole point. The read is bounded in bytes and not in
// runes, so a perfectly ordinary UTF-8 error document read up to the limit ends
// mid-character about three times in four — and judging that invalid would
// withhold exactly the long error bodies the bound exists to make affordable.
//
// Only the tail is forgiven, and only up to the longest rune. Real binary fails
// at its first byte far more often than at its last: a gzip stream opens with
// 0x1f 0x8b, and CBOR opens with a major-type byte that is a UTF-8 continuation.
// Trimming the end cannot rescue either.
func isText(s string) bool {
	for cut := range utf8.UTFMax {
		if cut > len(s) {
			break
		}

		if utf8.ValidString(s[:len(s)-cut]) {
			return true
		}
	}

	return false
}

func (e *StatusError) Error() string {
	if e.Binary {
		if e.ContentType == "" {
			return fmt.Sprintf("%s %s: server responded with %s: %d non-text bytes, unlabeled", e.Method, e.Path, e.Status, e.BodySize)
		}

		return fmt.Sprintf("%s %s: server responded with %s: %d non-text bytes of %s", e.Method, e.Path, e.Status, e.BodySize, e.ContentType)
	}

	if e.Body == "" {
		return fmt.Sprintf("%s %s: server responded with %s", e.Method, e.Path, e.Status)
	}

	ellipsis := ""
	if e.Truncated {
		ellipsis = "…"
	}

	return fmt.Sprintf("%s %s: server responded with %s: %s%s", e.Method, e.Path, e.Status, e.Body, ellipsis)
}

// Is reports retry.ErrUnretryable for a status another attempt cannot improve.
//
// It answers rather than wraps, because retry.ErrUnretryable is not what caused
// this error — it is a fact about it, and one that depends on the status code
// the caller can read for itself. The rule is terminalStatus, the same one
// DefaultRetryClassification hands the retry transport, so an outer loop and an
// inner one cannot come to different conclusions about the same response.
func (e *StatusError) Is(target error) bool {
	return target == retry.ErrUnretryable && terminalStatus(e.StatusCode)
}
