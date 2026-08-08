package client

import (
	"bytes"
	"context"
	"io"
	"maps"
	"net/http"
	"net/url"
	"reflect"
	"sync"

	platformerrors "github.com/primandproper/platform-go/v10/errors"
	"github.com/primandproper/platform-go/v10/routing/internal/routeplan"
)

// operation is one Endpoint resolved against its input type: the plain pattern
// to fill, and the plan saying where each field of an In belongs on the wire.
//
// It is the mirror of what registration builds on the server. The same plan the
// server reads a request with is the one written here, which is what keeps the
// two ends from drifting: there is no second description of the route to keep in
// step.
type operation struct {
	plan    *routeplan.Plan
	pattern string
	method  string
}

// operationKey identifies a resolved operation. The input type is part of it
// because the plan is a fact about the input type, and the method and pattern
// because the plan is cross-checked against the path parameters they imply.
//
//nolint:unused // every field is read by the map's own equality, never by name
type operationKey struct {
	in      reflect.Type
	method  string
	pattern string
}

// operations memoizes the reflection, which is the expensive part of a call and
// depends on nothing that changes between calls.
var operations sync.Map

func resolve(method, pattern string, in reflect.Type) (*operation, error) {
	key := operationKey{in: in, method: method, pattern: pattern}
	if cached, ok := operations.Load(key); ok {
		if op, isOperation := cached.(*operation); isOperation {
			return op, nil
		}
	}

	plain, pathParams, err := routeplan.ParsePath(pattern)
	if err != nil {
		return nil, platformerrors.Wrap(err, "reading the endpoint's pattern")
	}

	plan, err := routeplan.New(in, pathParams, method)
	if err != nil {
		return nil, platformerrors.Wrap(err, "planning the endpoint's input")
	}

	op := &operation{plan: plan, pattern: plain, method: method}
	operations.Store(key, op)

	return op, nil
}

// request turns an input value into the HTTP request the server would bind it
// back out of.
func (o *operation) request(ctx context.Context, c *Client, in any) (*http.Request, error) {
	rv := reflect.ValueOf(in)

	pathValues := map[string]string{}
	query := c.base.Query()
	header := http.Header{}

	var cookies []*http.Cookie

	for i := range o.plan.Params {
		pf := &o.plan.Params[i]

		text, present, err := o.param(rv, pf)
		if err != nil {
			return nil, err
		}

		if !present {
			continue
		}

		switch pf.In {
		case routeplan.InPath:
			pathValues[pf.Name] = text
		case routeplan.InQuery:
			query.Set(pf.Name, text)
		case routeplan.InHeader:
			header.Set(pf.Name, text)
		case routeplan.InCookie:
			cookies = append(cookies, &http.Cookie{Name: pf.Name, Value: text})
		}
	}

	target, err := o.url(c, pathValues, query)
	if err != nil {
		return nil, err
	}

	body, err := o.body(ctx, c, rv)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, o.method, target, body)
	if err != nil {
		return nil, platformerrors.Wrap(err, "building the request")
	}

	maps.Copy(req.Header, header)

	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}

	req.Header.Set("Accept", c.codec.ContentType())
	if o.plan.SendsBody() {
		req.Header.Set("Content-Type", c.codec.ContentType())
	}

	return req, nil
}

// param renders one input field as the string its request carries.
//
// An empty rendering is treated as absent, because that is how the server reads
// it: bind skips a parameter whose raw value is the empty string, so sending one
// would put a header or a query key on the wire that changes nothing. A path
// parameter has no such option — it is part of the address — so an empty or
// unreachable one is an error rather than a request to a URL with a hole in it.
func (o *operation) param(rv reflect.Value, pf *routeplan.ParamField) (text string, present bool, err error) {
	fv, reachable := pf.Value(rv)
	if reachable {
		text, present, err = routeplan.FormatScalar(fv)
		if err != nil {
			return "", false, platformerrors.Wrapf(err, "rendering %s parameter %q", pf.In, pf.Name)
		}
	}

	if !reachable || !present || text == "" {
		if pf.Required {
			return "", false, platformerrors.Wrapf(
				platformerrors.ErrEmptyInputParameter,
				"%s %s: %s parameter %q has no value", o.method, o.pattern, pf.In, pf.Name,
			)
		}

		return "", false, nil
	}

	return text, true, nil
}

// url assembles the request URL from the client's base, the filled pattern, and
// the query.
func (o *operation) url(c *Client, pathValues map[string]string, query url.Values) (string, error) {
	path, escaped, err := routeplan.FillPath(o.pattern, pathValues)
	if err != nil {
		return "", platformerrors.Wrap(err, "filling the endpoint's path")
	}

	target := *c.base
	target.Path = c.base.Path + path

	// RawPath is only consulted when it differs from Path — that is the
	// contract net/url states — so setting it unconditionally would leave a
	// value net/url is entitled to ignore. Setting it only when the escaping
	// changed something is what makes a path parameter containing a slash
	// arrive as one segment rather than two.
	if rawPath := c.base.EscapedPath() + escaped; rawPath != target.Path {
		target.RawPath = rawPath
	} else {
		target.RawPath = ""
	}

	target.RawQuery = query.Encode()

	return target.String(), nil
}

// body marshals the request body: the input value with every parameter field
// zeroed, so that a value the request already carries in the path, the query, a
// header, or a cookie is not also sent in the body.
//
// Zeroing rather than omitting keeps the body's shape exactly what the server's
// decoder expects, json tags and all. It is safe because of the order the server
// binds in — body first, then parameters over the top — so every field zeroed
// here is one the server is about to overwrite from the request anyway.
//
// A field Detach refuses is left as it is: the copy still shares that memory
// with the caller, and sending one redundant value is a smaller wrong than
// mutating an input the caller passed by value and still holds.
func (o *operation) body(ctx context.Context, c *Client, rv reflect.Value) (io.Reader, error) {
	if !o.plan.SendsBody() || !rv.IsValid() {
		return http.NoBody, nil
	}

	payload := reflect.New(rv.Type())
	payload.Elem().Set(rv)

	for i := range o.plan.Params {
		fv, ok := o.plan.Params[i].Detach(payload.Elem())
		if ok && fv.CanSet() {
			fv.SetZero()
		}
	}

	encoded, err := c.codec.Marshal(ctx, payload.Elem().Interface())
	if err != nil {
		return nil, platformerrors.Wrap(err, "marshaling the request body")
	}

	// A *bytes.Reader rather than any io.Reader: net/http reads its length and
	// installs a GetBody from it, which is what lets a retrying transport send
	// the request a second time.
	return bytes.NewReader(encoded), nil
}
