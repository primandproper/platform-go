package client

import (
	"context"
	"io"
	"net/http"
	"reflect"

	platformerrors "github.com/primandproper/platform-go/v10/errors"
	httpx "github.com/primandproper/platform-go/v10/errors/http"
	"github.com/primandproper/platform-go/v10/routing"
)

// Call invokes an Endpoint on the service c addresses and returns its typed
// output.
//
// The input is rendered into the request the endpoint's own binding would read
// it back out of: path parameters filled into the pattern, query, header, and
// cookie fields placed where their tags say, and the remaining fields marshaled
// as the body. The response is decoded into Out, unwrapped from the platform
// envelope when the service sends one.
//
// A non-2xx response is returned as an *Error, which unwraps to the platform
// sentinel behind the service's error code where the code names one — so a
// caller matches a remote failure with errors.Is exactly as it would a local
// one. Everything else — a transport failure, a body that would not decode — is
// returned wrapped, with Out at its zero value.
//
// It is a function rather than a method because it is generic in the endpoint's
// types and Go interface methods cannot be, which is the same reason routing
// registers with functions.
func Call[In, Out any](ctx context.Context, c *Client, ep routing.Endpoint[In, Out], in In) (Out, error) {
	var out Out

	op, err := resolve(ep.Method, ep.Pattern, reflect.TypeFor[In]())
	if err != nil {
		return out, err
	}

	ctx, operation := c.o11y.BeginCustom(ctx, op.method+" "+op.pattern)
	defer operation.End()

	status, body, err := c.exchange(ctx, op, in)
	if err != nil {
		operation.Acknowledge(err, "calling %s %s", op.method, op.pattern)

		return out, err
	}

	out, err = decode[Out](ctx, c, status, body)
	if err != nil {
		operation.Acknowledge(err, "reading the response to %s %s", op.method, op.pattern)

		return out, err
	}

	return out, nil
}

// exchange runs the half of a call that does not depend on Out: build the
// request, send it, read the body, and turn a refusal into an *Error. It is
// non-generic so that this work is compiled once rather than once per endpoint.
func (c *Client) exchange(ctx context.Context, op *operation, in any) (status int, body []byte, err error) {
	req, err := op.request(ctx, c, in)
	if err != nil {
		return 0, nil, err
	}

	// G704: the URL is this client's own base plus a pattern the endpoint
	// declares, with every caller-supplied value escaped into a path segment, a
	// query value, a header, or a cookie. None of them can move the request to
	// another host.
	res, err := c.httpClient.Do(req) //nolint:gosec // the host comes from the client's base URL, never from the input
	if err != nil {
		return 0, nil, platformerrors.Wrapf(err, "calling %s %s", op.method, op.pattern)
	}
	defer func() {
		// Drained before closing so the connection can be reused: net/http
		// discards a connection whose body was left unread, which turns a client
		// that stops reading early into one that opens a socket per call.
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, c.maxResponseBytes)) //nolint:errcheck // best-effort drain for connection reuse
		_ = res.Body.Close()                                                     //nolint:errcheck // best-effort close of a body already read
	}()

	body, err = c.readBody(res)
	if err != nil {
		return 0, nil, err
	}

	if res.StatusCode < http.StatusOK || res.StatusCode >= http.StatusMultipleChoices {
		return 0, nil, c.newError(ctx, op, res.StatusCode, body)
	}

	return res.StatusCode, body, nil
}

// readBody reads a response body, refusing one over the client's cap rather than
// truncating it: a truncated body decodes into a value that is quietly missing
// fields, which is worse than a failed call.
func (c *Client) readBody(res *http.Response) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(res.Body, c.maxResponseBytes+1))
	if err != nil {
		return nil, platformerrors.Wrap(err, "reading the response body")
	}

	if int64(len(body)) > c.maxResponseBytes {
		return nil, platformerrors.Newf("response body exceeded %d bytes", c.maxResponseBytes)
	}

	return body, nil
}

// decode reads a successful response into Out.
//
// An empty body is not an error. A route whose Out is routing.Empty writes only
// a status by design, and a 204 says the same thing whatever Out is; in both
// cases the zero value is the answer.
func decode[Out any](ctx context.Context, c *Client, status int, body []byte) (Out, error) {
	var out Out

	if len(body) == 0 || status == http.StatusNoContent || isEmpty[Out]() {
		return out, nil
	}

	if !c.envelope {
		if err := c.codec.Unmarshal(ctx, body, &out); err != nil {
			return out, platformerrors.Wrap(err, "decoding the response")
		}

		return out, nil
	}

	var envelope httpx.APIResponse[Out]
	if err := c.codec.Unmarshal(ctx, body, &envelope); err != nil {
		return out, platformerrors.Wrap(err, "decoding the response envelope")
	}

	return envelope.Data, nil
}

// isEmpty reports whether T is routing.Empty, the placeholder for a route with
// no response body.
func isEmpty[T any]() bool {
	var t T
	_, ok := any(t).(routing.Empty)

	return ok
}
