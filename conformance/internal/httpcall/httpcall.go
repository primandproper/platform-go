// Package httpcall is how the suites asserting this module's HTTP surfaces make
// a request as a caller and read what the router answered.
//
// dataprivacy and operations are asserted over one router, and both read its
// answers the same way: a status, and the handler's value under data. One copy,
// so the two cannot come to disagree about what a request looks like or where
// the value sits in the body.
package httpcall

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// Envelope is how the router answers every route: the handler's value under
// data, beside details a deployment fills in.
type Envelope[T any] struct {
	Data T `json:"data"`
}

// Call makes one request as caller, at path under the caller's base URL, and
// returns the status and the whole body. A non-nil body is sent as JSON.
func Call(t *testing.T, caller *conformance.Subject, method, path string, body []byte) (status int, response []byte) {
	t.Helper()

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(t.Context(), method, URL(caller, path), reader)
	must.NoError(t, err)

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	res, err := caller.HTTP.Client.Do(req)
	must.NoError(t, err)

	defer func() { test.NoError(t, res.Body.Close()) }()

	response, err = io.ReadAll(res.Body)
	must.NoError(t, err)

	return res.StatusCode, response
}

// URL is path under caller's base URL.
func URL(caller *conformance.Subject, path string) string {
	return strings.TrimSuffix(caller.HTTP.BaseURL, "/") + path
}
