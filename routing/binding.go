package routing

import (
	"context"
	"fmt"
	"net/http"
	"reflect"

	httpx "github.com/primandproper/platform-go/v10/errors/http"
	"github.com/primandproper/platform-go/v10/routing/internal/routeplan"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// newBindPlan reflects the input type In, builds its binding plan, and
// cross-checks the route's typed path parameters against the input's `path`
// fields. It panics on a static mismatch (a path param with no matching field,
// or an incompatible declared type) — a programmer error caught at boot.
func newBindPlan[In any](pathParams []ParamSpec, method string) *routeplan.Plan {
	plan, err := routeplan.New(reflect.TypeFor[In](), pathParams, method)
	if err != nil {
		panic(fmt.Sprintf("routing: %s", err))
	}

	return plan
}

// bind populates dest (an addressable value of the input type) from the request
// according to plan: body first (when applicable), then path/query/header/cookie
// params (which overwrite any body-provided values), then validation.
func bind(ctx context.Context, plan *routeplan.Plan, r *Router, req *http.Request, dest reflect.Value) error {
	if plan.SendsBody() {
		if err := r.enc.DecodeRequest(ctx, req, dest.Addr().Interface()); err != nil {
			return &bindError{code: httpx.ErrDecodingRequestInput, msg: "could not decode request body", err: err}
		}
	}

	for i := range plan.Params {
		pf := &plan.Params[i]

		raw, present := rawParam(r.backend, req, pf)
		if !present || raw == "" {
			if pf.Required {
				return &bindError{
					code: httpx.ErrValidatingRequestInput,
					msg:  fmt.Sprintf("missing required %s parameter %q", pf.In, pf.Name),
				}
			}

			continue
		}

		if err := routeplan.SetScalar(dest.FieldByIndex(pf.Index), raw); err != nil {
			return &bindError{
				code: httpx.ErrValidatingRequestInput,
				msg:  fmt.Sprintf("invalid %s parameter %q", pf.In, pf.Name),
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
func rawParam(backend Backend, req *http.Request, pf *routeplan.ParamField) (string, bool) {
	switch pf.In {
	case routeplan.InPath:
		v := backend.PathValue(req, pf.Name)

		return v, v != ""
	case routeplan.InQuery:
		q := req.URL.Query()
		if !q.Has(pf.Name) {
			return "", false
		}

		return q.Get(pf.Name), true
	case routeplan.InHeader:
		v := req.Header.Get(pf.Name)

		return v, v != ""
	case routeplan.InCookie:
		c, err := req.Cookie(pf.Name)
		if err != nil {
			return "", false
		}

		return c.Value, true
	default:
		return "", false
	}
}

// bindError is a client-facing binding failure carrying the platform error code
// used to derive the HTTP status and response envelope.
type bindError struct {
	err  error
	msg  string
	code httpx.ErrorCode
}

// ErrorCode returns the platform error code for this binding failure, which is
// how an ErrorEncoder distinguishes a body it could not decode from input that
// failed validation without matching on this unexported type.
func (e *bindError) ErrorCode() httpx.ErrorCode { return e.code }

func (e *bindError) Error() string {
	if e.err != nil {
		return e.msg + ": " + e.err.Error()
	}

	return e.msg
}

func (e *bindError) Unwrap() error { return e.err }
