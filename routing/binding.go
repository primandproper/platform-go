package routing

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	httpx "github.com/primandproper/platform-go/v13/errors/http"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// Parameter locations, matching the struct-tag names swaggest reflects.
const (
	inPath   = "path"
	inQuery  = "query"
	inHeader = "header"
	inCookie = "cookie"
)

// textUnmarshaler mirrors encoding.TextUnmarshaler so param field types that
// parse themselves (uuid.UUID, time.Time, ...) can be detected and used.
type textUnmarshaler interface {
	UnmarshalText(text []byte) error
}

// paramField describes one non-body input field bound from the request: a path,
// query, header, or cookie parameter.
type paramField struct {
	typ      reflect.Type
	in       string
	name     string
	index    []int
	required bool
}

// bindPlan is the cached, per-route plan for populating an In value from a
// request. It is built once at registration and reused on every request.
type bindPlan struct {
	params []paramField
	// rawBody is the field index of the input's RawBody field, or nil if it has
	// none. rawBodies counts them, so a second one is a registration panic
	// rather than a coin flip over which field receives the body.
	rawBody   []int
	rawBodies int
	// maxBody bounds the request body in bytes, or 0 for no bound. It is
	// resolved at registration from the route and Router settings.
	maxBody   int64
	allowBody bool
	hasBody   bool
}

// newBindPlan reflects the input type In, builds its binding plan, and
// cross-checks the route's typed path parameters against the input's `path`
// fields. It panics on a static mismatch (a path param with no matching field,
// or an incompatible declared type) — a programmer error caught at boot.
func newBindPlan[In any](pathParams []ParamSpec, method string) *bindPlan {
	plan := &bindPlan{allowBody: methodAllowsBody(method)}

	t := derefType(reflect.TypeFor[In]())
	if t.Kind() == reflect.Struct {
		collectFields(t, nil, plan)
	}

	for i := range pathParams {
		pp := pathParams[i]

		pf, ok := findParam(plan, inPath, pp.Name)
		if !ok {
			panic(fmt.Sprintf(
				"routing: path parameter %q has no matching `path:%q` field on input type %s",
				pp.Name, pp.Name, t,
			))
		}

		if !tokenMatchesType(pp.Token, pf.typ) {
			panic(fmt.Sprintf(
				"routing: path parameter %q declared as %q but input field %s is %s",
				pp.Name, pp.Token, pf.name, pf.typ,
			))
		}
	}

	checkRawBody(plan, t, method)

	return plan
}

// checkRawBodyTag rejects a RawBody field that also claims a JSON name. Binding
// ignores the tag — the field receives the whole body either way — but the
// reflector does not: a named field is a property of the request schema, so the
// operation would document the document as one field of an object it is not
// inside, next to the raw body this package adds. json:"-" is allowed, being the
// same statement the bare field already makes.
func checkRawBodyTag(t reflect.Type, f *reflect.StructField) {
	name, ok := f.Tag.Lookup("json")
	if !ok || strings.Split(name, ",")[0] == "-" {
		return
	}

	panic(fmt.Sprintf(
		"routing: RawBody field %s.%s carries a json tag; the whole body is this field, so it has no name within one",
		t, f.Name,
	))
}

// checkRawBody rejects the three ways a RawBody field cannot mean anything, all
// of them decidable from the input type and the method. Like the path-parameter
// checks above, they panic at registration: each one is a route that could never
// serve a request correctly, and boot is the only moment at which saying so
// costs nobody a response.
func checkRawBody(plan *bindPlan, t reflect.Type, method string) {
	switch {
	case plan.rawBodies == 0:
		return
	case plan.rawBodies > 1:
		panic(fmt.Sprintf(
			"routing: input type %s has %d RawBody fields; the request body is one document, so at most one field can receive it",
			t, plan.rawBodies,
		))
	case plan.hasBody:
		panic(fmt.Sprintf(
			"routing: input type %s has both a RawBody field and body fields; the body is either the raw document or an object with fields, not both",
			t,
		))
	case !plan.allowBody:
		panic(fmt.Sprintf(
			"routing: input type %s has a RawBody field on a %s route, which carries no request body",
			t, method,
		))
	}
}

func findParam(plan *bindPlan, in, name string) (paramField, bool) {
	for i := range plan.params {
		if plan.params[i].in == in && plan.params[i].name == name {
			return plan.params[i], true
		}
	}

	return paramField{}, false
}

// collectFields walks a struct type, recording param fields (path/query/header/
// cookie) and noting whether any field contributes to the request body.
func collectFields(t reflect.Type, index []int, plan *bindPlan) {
	for i := range t.NumField() {
		f := t.Field(i)

		// skip unexported, non-embedded fields.
		if f.PkgPath != "" && !f.Anonymous {
			continue
		}

		idx := make([]int, 0, len(index)+1)
		idx = append(idx, index...)
		idx = append(idx, i)

		// Ahead of the tag lookup, because RawBody says where the field is bound
		// from by being the type it is. A tag on it would be a second, quieter
		// answer to a question the type has already settled.
		if f.Type == rawBodyType {
			checkRawBodyTag(t, &f)

			plan.rawBodies++
			plan.rawBody = idx

			continue
		}

		if in, name, ok := paramLocation(f.Tag); ok {
			plan.params = append(plan.params, paramField{
				index:    idx,
				in:       in,
				name:     name,
				typ:      f.Type,
				required: in == inPath,
			})

			continue
		}

		if f.Anonymous && derefType(f.Type).Kind() == reflect.Struct {
			collectFields(derefType(f.Type), idx, plan)

			continue
		}

		if isBodyField(&f) {
			plan.hasBody = true
		}
	}
}

// paramLocation returns the location and parameter name for a field, if it
// carries a path/query/header/cookie tag. Path takes precedence, then query,
// header, cookie.
func paramLocation(tag reflect.StructTag) (in, name string, ok bool) {
	for _, loc := range []string{inPath, inQuery, inHeader, inCookie} {
		if v, present := tag.Lookup(loc); present {
			n := strings.Split(v, ",")[0]
			if n == "" {
				continue
			}

			return loc, n, true
		}
	}

	return "", "", false
}

// isBodyField reports whether a non-param field contributes to the request body.
// A field with json:"-" is excluded; any other exported field counts.
func isBodyField(f *reflect.StructField) bool {
	if j, ok := f.Tag.Lookup("json"); ok {
		name := strings.Split(j, ",")[0]

		return name != "-"
	}

	return true
}

// bind populates dest (an addressable value of the input type) from the request:
// body first (when applicable), then path/query/header/cookie params (which
// overwrite any body-provided values), then validation.
//
// res is here only to be handed to http.MaxBytesReader, which needs it to stop
// a client that keeps sending after the limit is reached rather than reading a
// body this request has already refused.
func (p *bindPlan) bind(ctx context.Context, r *Router, res http.ResponseWriter, req *http.Request, dest reflect.Value) error {
	switch {
	case p.rawBody != nil:
		raw, err := readRawBody(res, req, p.maxBody)
		if err != nil {
			return err
		}

		dest.FieldByIndex(p.rawBody).SetBytes(raw)
	case p.hasBody && p.allowBody:
		if p.maxBody > 0 && req.Body != nil {
			req.Body = http.MaxBytesReader(res, req.Body, p.maxBody)
		}

		if err := r.enc.DecodeRequest(ctx, req, dest.Addr().Interface()); err != nil {
			return bodyError(err, p.maxBody, "could not decode request body")
		}
	}

	for i := range p.params {
		pf := &p.params[i]

		raw, present := rawParam(r.backend, req, pf)
		if !present || raw == "" {
			if pf.required {
				return &bindError{
					code: httpx.ErrValidatingRequestInput,
					msg:  fmt.Sprintf("missing required %s parameter %q", pf.in, pf.name),
				}
			}

			continue
		}

		if err := setScalar(dest.FieldByIndex(pf.index), raw); err != nil {
			return &bindError{
				code: httpx.ErrValidatingRequestInput,
				msg:  fmt.Sprintf("invalid %s parameter %q", pf.in, pf.name),
				err:  err,
			}
		}
	}

	if v, ok := dest.Addr().Interface().(validation.ValidatableWithContext); ok {
		if err := v.ValidateWithContext(ctx); err != nil {
			return &bindError{code: httpx.ErrValidatingRequestInput, msg: "invalid request input", err: err}
		}
	}

	return nil
}

// rawBodyType is the type a field must have to receive the unparsed body.
var rawBodyType = reflect.TypeFor[RawBody]()

// readRawBody reads the whole request body, bounded by limit when there is one.
// A body the request does not carry reads as no bytes rather than as a failure:
// a document-bodied route with an empty body is a request the handler gets to
// reject with its own message.
func readRawBody(res http.ResponseWriter, req *http.Request, limit int64) ([]byte, error) {
	if req.Body == nil {
		return nil, nil
	}

	var body io.Reader = req.Body
	if limit > 0 {
		body = http.MaxBytesReader(res, req.Body, limit)
	}

	raw, err := io.ReadAll(body)
	if err != nil {
		return nil, bodyError(err, limit, "could not read request body")
	}

	return raw, nil
}

// bodyError renders a failed body read as a binding failure, separating the one
// cause that is not a malformed request from the ones that are.
//
// An over-limit body is answered 413 rather than the 400 its code maps to. The
// distinction is the whole point of having a limit: 400 tells a client its
// document was wrong, and it will send the same document again; 413 tells it the
// document was too big, which is the only thing it can act on.
func bodyError(err error, limit int64, msg string) error {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return &bindError{
			code:   httpx.ErrDecodingRequestInput,
			status: http.StatusRequestEntityTooLarge,
			msg:    tooLargeMessage(limit),
			err:    err,
		}
	}

	return &bindError{code: httpx.ErrDecodingRequestInput, msg: msg, err: err}
}

// tooLargeMessage is what a body over the bound is told, wherever it was refused.
// The binding step and LimitRequestBody refuse the same request for the same
// reason, and a client comparing the two should not be able to tell which one
// caught it.
func tooLargeMessage(limit int64) string {
	return fmt.Sprintf("request body exceeds the %d byte limit", limit)
}

// rawParam reads the raw string value of a parameter from the request. Path
// values come from the backend's PathValue; the rest from the standard request.
func rawParam(backend Backend, req *http.Request, pf *paramField) (string, bool) {
	switch pf.in {
	case inPath:
		v := backend.PathValue(req, pf.name)

		return v, v != ""
	case inQuery:
		q := req.URL.Query()
		if !q.Has(pf.name) {
			return "", false
		}

		return q.Get(pf.name), true
	case inHeader:
		v := req.Header.Get(pf.name)

		return v, v != ""
	case inCookie:
		c, err := req.Cookie(pf.name)
		if err != nil {
			return "", false
		}

		return c.Value, true
	default:
		return "", false
	}
}

// setScalar parses raw into fv. Types that implement encoding.TextUnmarshaler
// (uuid.UUID, time.Time, ...) parse themselves; otherwise the field's kind
// selects the strconv parser.
func setScalar(fv reflect.Value, raw string) error {
	if fv.Kind() == reflect.Pointer {
		if fv.IsNil() {
			fv.Set(reflect.New(fv.Type().Elem()))
		}

		fv = fv.Elem()
	}

	if fv.CanAddr() {
		if u, ok := fv.Addr().Interface().(textUnmarshaler); ok {
			return u.UnmarshalText([]byte(raw))
		}
	}

	switch fv.Kind() {
	case reflect.String:
		fv.SetString(raw)
	case reflect.Bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return err
		}
		fv.SetBool(b)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		// Parsed at the field's own width, not at 64 and then narrowed by SetInt.
		// Parsing wide and setting narrow wraps silently: ?count=300 into an int8
		// bound to 44, and the handler received a plausible number rather than the
		// 400 the request had earned.
		n, err := strconv.ParseInt(raw, 10, fv.Type().Bits())
		if err != nil {
			return err
		}
		fv.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(raw, 10, fv.Type().Bits())
		if err != nil {
			return err
		}
		fv.SetUint(n)
	case reflect.Float32, reflect.Float64:
		f, err := strconv.ParseFloat(raw, fv.Type().Bits())
		if err != nil {
			return err
		}
		fv.SetFloat(f)
	default:
		return fmt.Errorf("unsupported parameter kind %s", fv.Kind())
	}

	return nil
}

// bindError is a client-facing binding failure carrying the platform error code
// used to derive the HTTP status and response envelope.
type bindError struct {
	err  error
	msg  string
	code httpx.ErrorCode
	// status overrides the status the code maps to, for the failure whose code
	// and status disagree. Zero means the code decides, as it does for all but
	// the over-limit body.
	status int
}

// ErrorCode returns the platform error code for this binding failure, which is
// how an ErrorEncoder distinguishes a body it could not decode from input that
// failed validation without matching on this unexported type.
func (e *bindError) ErrorCode() httpx.ErrorCode { return e.code }

// httpStatus is the status this failure is sent as: the one it names, or the one
// its code maps to.
func (e *bindError) httpStatus() int {
	if e.status != 0 {
		return e.status
	}

	return httpx.HTTPStatusForCode(e.code)
}

func (e *bindError) Error() string {
	if e.err != nil {
		return e.msg + ": " + e.err.Error()
	}

	return e.msg
}

func (e *bindError) Unwrap() error { return e.err }
