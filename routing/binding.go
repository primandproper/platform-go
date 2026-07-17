package routing

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	httpx "github.com/primandproper/platform-go/v5/errors/http"

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

// bindPlan is the cached, per-input-type plan for populating an In value from a
// request. It is built once at registration and reused on every request.
type bindPlan struct {
	params    []paramField
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

	return plan
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
func (p *bindPlan) bind(ctx context.Context, r *Router, req *http.Request, dest reflect.Value) error {
	if p.hasBody && p.allowBody {
		if err := r.enc.DecodeRequest(ctx, req, dest.Addr().Interface()); err != nil {
			return &bindError{code: httpx.ErrDecodingRequestInput, msg: "could not decode request body", err: err}
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
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return err
		}
		fv.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return err
		}
		fv.SetUint(n)
	case reflect.Float32, reflect.Float64:
		f, err := strconv.ParseFloat(raw, 64)
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
}

func (e *bindError) Error() string {
	if e.err != nil {
		return e.msg + ": " + e.err.Error()
	}

	return e.msg
}

func (e *bindError) Unwrap() error { return e.err }
