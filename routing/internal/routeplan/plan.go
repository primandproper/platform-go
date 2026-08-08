package routeplan

import (
	"fmt"
	"net/http"
	"reflect"
	"strings"
)

// Parameter locations, matching the struct-tag names swaggest reflects.
const (
	InPath   = "path"
	InQuery  = "query"
	InHeader = "header"
	InCookie = "cookie"
)

type (
	// TextUnmarshaler mirrors encoding.TextUnmarshaler so param field types that
	// parse themselves (uuid.UUID, time.Time, ...) can be detected and used.
	TextUnmarshaler interface {
		UnmarshalText(text []byte) error
	}

	// TextMarshaler mirrors encoding.TextMarshaler — the same detection in the
	// direction a client writes, so a type that parses itself out of a request
	// also renders itself back into one.
	TextMarshaler interface {
		MarshalText() ([]byte, error)
	}

	// ParamField describes one non-body input field carried outside the body: a
	// path, query, header, or cookie parameter.
	ParamField struct {
		Typ      reflect.Type
		In       string
		Name     string
		Index    []int
		Required bool
	}

	// Plan is the cached, per-input-type plan for moving values between an In
	// value and a request. It is built once — at registration on the server, at
	// first call on the client — and reused for every request after.
	Plan struct {
		Params    []ParamField
		AllowBody bool
		HasBody   bool
	}
)

// New reflects the input type t, builds its plan, and cross-checks the route's
// typed path parameters against the input's `path` fields. A path parameter with
// no matching field, or one whose declared token cannot hold the field's type, is
// a static mismatch between the pattern and the input type and is reported here.
func New(t reflect.Type, pathParams []ParamSpec, method string) (*Plan, error) {
	plan := &Plan{AllowBody: methodAllowsBody(method)}

	t = DerefType(t)
	if t.Kind() == reflect.Struct {
		collectFields(t, nil, plan)
	}

	for i := range pathParams {
		pp := pathParams[i]

		pf, ok := plan.Find(InPath, pp.Name)
		if !ok {
			return nil, fmt.Errorf(
				"path parameter %q has no matching `path:%q` field on input type %s",
				pp.Name, pp.Name, t,
			)
		}

		if !TokenMatchesType(pp.Token, pf.Typ) {
			return nil, fmt.Errorf(
				"path parameter %q declared as %q but input field %s is %s",
				pp.Name, pp.Token, pf.Name, pf.Typ,
			)
		}
	}

	return plan, nil
}

// SendsBody reports whether an In of this plan's type contributes a request
// body: it has body fields, and the method carries one.
func (p *Plan) SendsBody() bool { return p.HasBody && p.AllowBody }

// Find returns the param field bound at a location under a given name.
func (p *Plan) Find(in, name string) (ParamField, bool) {
	for i := range p.Params {
		if p.Params[i].In == in && p.Params[i].Name == name {
			return p.Params[i], true
		}
	}

	return ParamField{}, false
}

// Value walks f.Index into v (a value of the plan's input type) and returns the
// field it names.
//
// It reports false when a nil embedded pointer sits along the way: the field is
// not reachable, so there is no value to read. reflect.Value.FieldByIndex panics
// in that case, which is the right answer for a destination being populated and
// the wrong one for a source being read.
func (f *ParamField) Value(v reflect.Value) (reflect.Value, bool) {
	for _, i := range f.Index {
		for v.Kind() == reflect.Pointer {
			if v.IsNil() {
				return reflect.Value{}, false
			}

			v = v.Elem()
		}

		if v.Kind() != reflect.Struct {
			return reflect.Value{}, false
		}

		v = v.Field(i)
	}

	return v, true
}

// Detach walks f.Index into v — which must be addressable — replacing every
// pointer it crosses with a pointer to a fresh copy of what that pointer pointed
// at, and returns the settable field at the end.
//
// The copying is the point. A struct embedded by pointer is shared with whatever
// the caller still holds, so writing through it in what is supposed to be a
// private copy of an input would reach back out and change the caller's value.
// Detach severs that sharing one level at a time, so the write lands only on the
// copy.
//
// Like Value, it reports false when a nil pointer sits along the way: there is
// nothing shared to sever and no field to write.
//
// It also reports false for a pointer it is not allowed to replace, which in
// practice means a struct embedded by pointer under an unexported name. reflect
// hands out the fields promoted through such an embed as writable but refuses to
// let the embedded pointer itself be reassigned, so the sharing cannot be
// severed — and writing without severing it would change the caller's value,
// which is the one thing this must not do. A caller that cannot detach should
// leave the field alone.
func (f *ParamField) Detach(v reflect.Value) (reflect.Value, bool) {
	for _, i := range f.Index {
		for v.Kind() == reflect.Pointer {
			if v.IsNil() || !v.CanSet() {
				return reflect.Value{}, false
			}

			fresh := reflect.New(v.Type().Elem())
			fresh.Elem().Set(v.Elem())
			v.Set(fresh)

			v = v.Elem()
		}

		if v.Kind() != reflect.Struct {
			return reflect.Value{}, false
		}

		v = v.Field(i)
	}

	return v, true
}

// collectFields walks a struct type, recording param fields (path/query/header/
// cookie) and noting whether any field contributes to the request body.
func collectFields(t reflect.Type, index []int, plan *Plan) {
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
			plan.Params = append(plan.Params, ParamField{
				Index:    idx,
				In:       in,
				Name:     name,
				Typ:      f.Type,
				Required: in == InPath,
			})

			continue
		}

		if f.Anonymous && DerefType(f.Type).Kind() == reflect.Struct {
			collectFields(DerefType(f.Type), idx, plan)

			continue
		}

		if isBodyField(&f) {
			plan.HasBody = true
		}
	}
}

// paramLocation returns the location and parameter name for a field, if it
// carries a path/query/header/cookie tag. Path takes precedence, then query,
// header, cookie.
func paramLocation(tag reflect.StructTag) (in, name string, ok bool) {
	for _, loc := range []string{InPath, InQuery, InHeader, InCookie} {
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

// methodAllowsBody reports whether an HTTP method conventionally carries a request
// body that the layer should attempt to decode.
func methodAllowsBody(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}
