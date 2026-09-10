package http

import (
	"context"
	"encoding/json"
	nethttp "net/http"
	"testing"

	"github.com/primandproper/platform-go/v14/dataprivacy"
	dataprivacymock "github.com/primandproper/platform-go/v14/dataprivacy/mock"

	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/filtering"
	"github.com/primandproper/primitives-go/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// theSubject is who most of these tests are.
var theSubject = dataprivacy.Subject{ID: "subject_1", Type: dataprivacy.SubjectUser}

// decodeReceipt reads a Receipt out of a response body.
//
// The router envelopes what a handler returns, so the receipt is under "data".
func decodeReceipt(t *testing.T, body []byte) *Receipt {
	t.Helper()

	var envelope struct {
		Data Receipt `json:"data"`
	}

	must.NoError(t, json.Unmarshal(body, &envelope))
	must.NotNil(t, envelope.Data.Request)

	return &envelope.Data
}

func TestNew(T *testing.T) {
	T.Parallel()

	T.Run("rejects a nil service", func(t *testing.T) {
		t.Parallel()

		_, err := New(nil, WithSubjectResolver(subjectFromContext))

		test.ErrorIs(t, err, ErrNilService)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})

	// The resolver has no default and there is no name for going without one: a
	// surface that served every subject's privacy requests to whoever asked
	// would be the export endpoint working on the wrong person.
	T.Run("requires a subject resolver", func(t *testing.T) {
		t.Parallel()

		_, err := New(&dataprivacymock.ServiceMock{})

		test.ErrorIs(t, err, ErrNilSubjectResolver)
	})

	T.Run("builds with a resolver", func(t *testing.T) {
		t.Parallel()

		handlers, err := New(&dataprivacymock.ServiceMock{}, WithSubjectResolver(subjectFromContext))

		must.NoError(t, err)
		must.NotNil(t, handlers)
	})
}

func TestHandlers_submit(T *testing.T) {
	T.Parallel()

	T.Run("records an export and points at its progress", func(t *testing.T) {
		t.Parallel()

		svc := &dataprivacymock.ServiceMock{}
		svc.SubmitFunc = func(_ context.Context, subject dataprivacy.Subject, requestType dataprivacy.RequestType) (*dataprivacy.Request, error) {
			req := requestFor("req_1", subject, dataprivacy.StatusInProgress, "op_1")
			req.Type = requestType

			return req, nil
		}

		res := do(t, mount(t, svc, theSubject), nethttp.MethodPost, BasePath, `{"type":"export"}`)

		test.EqOp(t, nethttp.StatusAccepted, res.Code)

		receipt := decodeReceipt(t, res.Body.Bytes())
		test.EqOp(t, "req_1", receipt.Request.ID)
		test.EqOp(t, dataprivacy.RequestExport, receipt.Request.Type)
		test.EqOp(t, "/operations/op_1", receipt.Progress)
		test.EqOp(t, "/operations/op_1/events", receipt.Events)
	})

	// The submission is about whoever the resolver says it is about.
	T.Run("the subject comes from the resolver", func(t *testing.T) {
		t.Parallel()

		var seen dataprivacy.Subject

		svc := &dataprivacymock.ServiceMock{}
		svc.SubmitFunc = func(_ context.Context, subject dataprivacy.Subject, _ dataprivacy.RequestType) (*dataprivacy.Request, error) {
			seen = subject

			return requestFor("req_1", subject, dataprivacy.StatusInProgress, "op_1"), nil
		}

		res := do(t, mount(t, svc, theSubject), nethttp.MethodPost, BasePath, `{"type":"export"}`)

		test.EqOp(t, nethttp.StatusAccepted, res.Code)
		test.EqOp(t, theSubject.ID, seen.ID)
		test.EqOp(t, theSubject.Type, seen.Type)
	})

	// A subject in the body is not merely ignored: the input type has nowhere to
	// put one, so the request is refused rather than quietly performed on the
	// caller instead of on whoever they named.
	T.Run("a body naming a subject is refused", func(t *testing.T) {
		t.Parallel()

		svc := &dataprivacymock.ServiceMock{}
		svc.SubmitFunc = func(_ context.Context, _ dataprivacy.Subject, _ dataprivacy.RequestType) (*dataprivacy.Request, error) {
			t.Error("submitted a request whose body named a subject")

			return nil, nil
		}

		res := do(t, mount(t, svc, theSubject), nethttp.MethodPost, BasePath,
			`{"type":"export","subject":{"id":"somebody_else"}}`)

		test.EqOp(t, nethttp.StatusBadRequest, res.Code)
		test.SliceEmpty(t, svc.SubmitCalls())
	})

	// An erasure held for confirmation has no operation, and the receipt says so
	// by carrying no progress paths: the client is waiting on a person, not on a
	// worker.
	T.Run("an erasure awaiting confirmation carries no progress paths", func(t *testing.T) {
		t.Parallel()

		svc := &dataprivacymock.ServiceMock{}
		svc.SubmitFunc = func(_ context.Context, subject dataprivacy.Subject, _ dataprivacy.RequestType) (*dataprivacy.Request, error) {
			return requestFor("req_1", subject, dataprivacy.StatusAwaitingConfirmation, ""), nil
		}

		res := do(t, mount(t, svc, theSubject), nethttp.MethodPost, BasePath, `{"type":"erasure"}`)

		test.EqOp(t, nethttp.StatusAccepted, res.Code)

		receipt := decodeReceipt(t, res.Body.Bytes())
		test.EqOp(t, dataprivacy.StatusAwaitingConfirmation, receipt.Request.Status)
		test.EqOp(t, "", receipt.Progress)
		test.EqOp(t, "", receipt.Events)
	})

	T.Run("an unknown request type is a 400", func(t *testing.T) {
		t.Parallel()

		svc := &dataprivacymock.ServiceMock{}
		svc.SubmitFunc = func(_ context.Context, _ dataprivacy.Subject, requestType dataprivacy.RequestType) (*dataprivacy.Request, error) {
			return nil, platformerrors.Wrapf(dataprivacy.ErrUnknownRequestType, "dataprivacy request type %q", requestType)
		}

		res := do(t, mount(t, svc, theSubject), nethttp.MethodPost, BasePath, `{"type":"rummage"}`)

		test.EqOp(t, nethttp.StatusBadRequest, res.Code)
	})

	T.Run("a resolver that fails fails the request", func(t *testing.T) {
		t.Parallel()

		svc := &dataprivacymock.ServiceMock{}
		svc.SubmitFunc = func(_ context.Context, _ dataprivacy.Subject, _ dataprivacy.RequestType) (*dataprivacy.Request, error) {
			t.Error("the service was called without a subject")

			return nil, nil
		}

		handlers, router := build(t, svc)
		handlers.Mount(router)
		must.NoError(t, router.Err())

		// No asSubject wrapper, so the resolver finds nothing on the context.
		res := do(t, router.Handler(), nethttp.MethodPost, BasePath, `{"type":"export"}`)

		test.EqOp(t, nethttp.StatusInternalServerError, res.Code)
	})

	// A resolver that returns nobody has not decided that everybody may read
	// everything; it has failed to say who is asking, and the request stops.
	T.Run("a resolver that names nobody fails the request", func(t *testing.T) {
		t.Parallel()

		svc := &dataprivacymock.ServiceMock{}
		svc.SubmitFunc = func(_ context.Context, _ dataprivacy.Subject, _ dataprivacy.RequestType) (*dataprivacy.Request, error) {
			t.Error("the service was called for an empty subject")

			return nil, nil
		}

		res := do(t, mount(t, svc, dataprivacy.Subject{}), nethttp.MethodPost, BasePath, `{"type":"export"}`)

		test.EqOp(t, nethttp.StatusBadRequest, res.Code)
	})
}

func TestHandlers_list(T *testing.T) {
	T.Parallel()

	T.Run("lists the calling subject's requests", func(t *testing.T) {
		t.Parallel()

		var seen dataprivacy.Subject

		svc := &dataprivacymock.ServiceMock{}
		svc.ListFunc = func(
			_ context.Context,
			subject dataprivacy.Subject,
			_ *filtering.QueryFilter,
		) (*filtering.QueryFilteredResult[dataprivacy.Request], error) {
			seen = subject

			return &filtering.QueryFilteredResult[dataprivacy.Request]{
				Data: []*dataprivacy.Request{requestFor("req_1", subject, dataprivacy.StatusCompleted, "op_1")},
			}, nil
		}

		res := do(t, mount(t, svc, theSubject), nethttp.MethodGet, BasePath, "")

		test.EqOp(t, nethttp.StatusOK, res.Code)
		test.EqOp(t, theSubject.ID, seen.ID)
		test.StrContains(t, res.Body.String(), `"req_1"`)
	})

	T.Run("passes the cursor and limit through", func(t *testing.T) {
		t.Parallel()

		var seen *filtering.QueryFilter

		svc := &dataprivacymock.ServiceMock{}
		svc.ListFunc = func(
			_ context.Context,
			subject dataprivacy.Subject,
			filter *filtering.QueryFilter,
		) (*filtering.QueryFilteredResult[dataprivacy.Request], error) {
			seen = filter

			return &filtering.QueryFilteredResult[dataprivacy.Request]{}, nil
		}

		res := do(t, mount(t, svc, theSubject), nethttp.MethodGet, BasePath+"?cursor=req_9&limit=3", "")

		test.EqOp(t, nethttp.StatusOK, res.Code)
		must.NotNil(t, seen)
		must.NotNil(t, seen.Cursor)
		test.EqOp(t, "req_9", *seen.Cursor)
		must.NotNil(t, seen.MaxResponseSize)
		test.EqOp(t, uint16(3), *seen.MaxResponseSize)
	})

	// An absent cursor and limit are the default window rather than a zero one.
	T.Run("no parameters is the default filter", func(t *testing.T) {
		t.Parallel()

		filter := filterFrom(listInput{})

		must.NotNil(t, filter)
		test.Nil(t, filter.Cursor)
		test.Eq(t, filtering.DefaultQueryFilter().MaxResponseSize, filter.MaxResponseSize)
	})
}

func TestHandlers_get(T *testing.T) {
	T.Parallel()

	T.Run("serves the subject's own request", func(t *testing.T) {
		t.Parallel()

		req := requestFor("req_1", theSubject, dataprivacy.StatusInProgress, "op_1")

		res := do(t, mount(t, serviceReturning(req), theSubject), nethttp.MethodGet, BasePath+"/req_1", "")

		test.EqOp(t, nethttp.StatusOK, res.Code)

		receipt := decodeReceipt(t, res.Body.Bytes())
		test.EqOp(t, "req_1", receipt.Request.ID)
		test.EqOp(t, "/operations/op_1", receipt.Progress)
	})

	// The one that matters: a 403 for a request that exists and a 404 for one
	// that does not is an oracle telling whoever is guessing identifiers which of
	// their guesses are real. Both answers have to be the same.
	T.Run("somebody else's request is a 404, not a 403", func(t *testing.T) {
		t.Parallel()

		req := requestFor("req_1", theSubject, dataprivacy.StatusInProgress, "op_1")
		handler := mount(t, serviceReturning(req), dataprivacy.Subject{ID: "subject_2"})

		theirs := do(t, handler, nethttp.MethodGet, BasePath+"/req_1", "")
		missing := do(t, handler, nethttp.MethodGet, BasePath+"/req_nope", "")

		test.EqOp(t, nethttp.StatusNotFound, theirs.Code)
		test.EqOp(t, nethttp.StatusNotFound, missing.Code)
		test.EqOp(t, theirs.Body.String(), missing.Body.String())
	})

	// The read of one row applies the listing's own rule: a caller who names no
	// scope is asking about everything requested in their name.
	T.Run("an unscoped subject reads their scoped request", func(t *testing.T) {
		t.Parallel()

		scoped := dataprivacy.Subject{ID: theSubject.ID, Scope: tenancy.Of("account_1"), Type: dataprivacy.SubjectUser}
		req := requestFor("req_1", scoped, dataprivacy.StatusCompleted, "op_1")

		res := do(t, mount(t, serviceReturning(req), theSubject), nethttp.MethodGet, BasePath+"/req_1", "")

		test.EqOp(t, nethttp.StatusOK, res.Code)
	})

	// The other direction does not hold: a caller confined to one scope does not
	// read another's row, even about themselves.
	T.Run("a scoped subject does not read another scope's request", func(t *testing.T) {
		t.Parallel()

		other := dataprivacy.Subject{ID: theSubject.ID, Scope: tenancy.Of("account_2"), Type: dataprivacy.SubjectUser}
		req := requestFor("req_1", other, dataprivacy.StatusCompleted, "op_1")

		caller := dataprivacy.Subject{ID: theSubject.ID, Scope: tenancy.Of("account_1"), Type: dataprivacy.SubjectUser}

		res := do(t, mount(t, serviceReturning(req), caller), nethttp.MethodGet, BasePath+"/req_1", "")

		test.EqOp(t, nethttp.StatusNotFound, res.Code)
	})
}

func TestHandlers_confirm(T *testing.T) {
	T.Parallel()

	// A plain link click is a GET, and that is how the mail delivers it.
	T.Run("a GET confirms, because that is what a link click is", func(t *testing.T) {
		t.Parallel()

		req := requestFor("req_1", theSubject, dataprivacy.StatusAwaitingConfirmation, "")

		svc := serviceReturning(req)
		svc.ConfirmFunc = func(_ context.Context, requestID string) (*dataprivacy.Request, error) {
			return requestFor(requestID, theSubject, dataprivacy.StatusInProgress, "op_1"), nil
		}

		res := do(t, mount(t, svc, theSubject), nethttp.MethodGet, BasePath+"/req_1"+ConfirmSuffix, "")

		test.EqOp(t, nethttp.StatusOK, res.Code)

		receipt := decodeReceipt(t, res.Body.Bytes())
		test.EqOp(t, dataprivacy.StatusInProgress, receipt.Request.Status)
		test.EqOp(t, "/operations/op_1", receipt.Progress)
	})

	T.Run("confirming twice is a conflict", func(t *testing.T) {
		t.Parallel()

		req := requestFor("req_1", theSubject, dataprivacy.StatusInProgress, "op_1")

		svc := serviceReturning(req)
		svc.ConfirmFunc = func(_ context.Context, requestID string) (*dataprivacy.Request, error) {
			return nil, platformerrors.Wrapf(dataprivacy.ErrNotAwaitingConfirmation, "dataprivacy request %q", requestID)
		}

		res := do(t, mount(t, svc, theSubject), nethttp.MethodGet, BasePath+"/req_1"+ConfirmSuffix, "")

		test.EqOp(t, nethttp.StatusConflict, res.Code)
	})

	// The guard stands in front of the write, so somebody else's confirmation
	// link never reaches the service at all.
	T.Run("somebody else's request is a 404 and confirms nothing", func(t *testing.T) {
		t.Parallel()

		req := requestFor("req_1", theSubject, dataprivacy.StatusAwaitingConfirmation, "")

		svc := serviceReturning(req)
		svc.ConfirmFunc = func(_ context.Context, _ string) (*dataprivacy.Request, error) {
			t.Error("confirmed somebody else's request")

			return nil, nil
		}

		res := do(t, mount(t, svc, dataprivacy.Subject{ID: "subject_2"}),
			nethttp.MethodGet, BasePath+"/req_1"+ConfirmSuffix, "")

		test.EqOp(t, nethttp.StatusNotFound, res.Code)
		test.SliceEmpty(t, svc.ConfirmCalls())
	})
}

func TestHandlers_cancel(T *testing.T) {
	T.Parallel()

	T.Run("withdraws an unconfirmed erasure outright", func(t *testing.T) {
		t.Parallel()

		req := requestFor("req_1", theSubject, dataprivacy.StatusAwaitingConfirmation, "")

		svc := serviceReturning(req)
		svc.CancelFunc = func(_ context.Context, requestID string) (*dataprivacy.Request, error) {
			return requestFor(requestID, theSubject, dataprivacy.StatusCancelled, ""), nil
		}

		res := do(t, mount(t, svc, theSubject), nethttp.MethodPost, BasePath+"/req_1"+CancelSuffix, "")

		test.EqOp(t, nethttp.StatusOK, res.Code)

		receipt := decodeReceipt(t, res.Body.Bytes())
		test.EqOp(t, dataprivacy.StatusCancelled, receipt.Request.Status)
	})

	// Cancelling in-flight work is a request rather than a kill, and the response
	// says so: the request is still in progress, and the operation is where the
	// answer arrives.
	T.Run("an in-progress request stays in progress", func(t *testing.T) {
		t.Parallel()

		req := requestFor("req_1", theSubject, dataprivacy.StatusInProgress, "op_1")

		svc := serviceReturning(req)
		svc.CancelFunc = func(_ context.Context, _ string) (*dataprivacy.Request, error) {
			return req, nil
		}

		res := do(t, mount(t, svc, theSubject), nethttp.MethodPost, BasePath+"/req_1"+CancelSuffix, "")

		test.EqOp(t, nethttp.StatusOK, res.Code)

		receipt := decodeReceipt(t, res.Body.Bytes())
		test.EqOp(t, dataprivacy.StatusInProgress, receipt.Request.Status)
		test.EqOp(t, "/operations/op_1", receipt.Progress)
		test.EqOp(t, "/operations/op_1/events", receipt.Events)
	})

	T.Run("somebody else's request is a 404 and cancels nothing", func(t *testing.T) {
		t.Parallel()

		req := requestFor("req_1", theSubject, dataprivacy.StatusInProgress, "op_1")

		svc := serviceReturning(req)
		svc.CancelFunc = func(_ context.Context, _ string) (*dataprivacy.Request, error) {
			t.Error("cancelled somebody else's request")

			return nil, nil
		}

		res := do(t, mount(t, svc, dataprivacy.Subject{ID: "subject_2"}),
			nethttp.MethodPost, BasePath+"/req_1"+CancelSuffix, "")

		test.EqOp(t, nethttp.StatusNotFound, res.Code)
		test.SliceEmpty(t, svc.CancelCalls())
	})
}

func TestHandlers_Mount(T *testing.T) {
	T.Parallel()

	T.Run("registers all five routes", func(t *testing.T) {
		t.Parallel()

		handlers, router := build(t, &dataprivacymock.ServiceMock{})

		routes := handlers.Mount(router)

		must.NoError(t, router.Err())
		test.SliceLen(t, 5, routes)

		paths := map[string]string{}
		for _, route := range routes {
			paths[route.Method+" "+route.Path] = route.OperationID
		}

		for _, want := range []string{
			nethttp.MethodPost + " " + BasePath,
			nethttp.MethodGet + " " + BasePath,
			nethttp.MethodGet + " " + BasePath + "/{requestID}",
			nethttp.MethodGet + " " + BasePath + "/{requestID}" + ConfirmSuffix,
			nethttp.MethodPost + " " + BasePath + "/{requestID}" + CancelSuffix,
		} {
			_, ok := paths[want]
			test.True(t, ok, test.Sprintf("route %q was not registered", want))
		}
	})

	// The deployment that renders its own confirmation page mounts the other
	// four and leaves this one route off.
	T.Run("the confirm route can be left unmounted", func(t *testing.T) {
		t.Parallel()

		req := requestFor("req_1", theSubject, dataprivacy.StatusAwaitingConfirmation, "")
		svc := serviceReturning(req)

		handlers, router := build(t, svc)
		handlers.MountSubmit(router)
		handlers.MountList(router)
		handlers.MountGet(router)
		handlers.MountCancel(router)
		must.NoError(t, router.Err())

		handler := asSubject(theSubject, router.Handler())

		test.EqOp(t, nethttp.StatusOK, do(t, handler, nethttp.MethodGet, BasePath+"/req_1", "").Code)
		test.EqOp(t, nethttp.StatusNotFound,
			do(t, handler, nethttp.MethodGet, BasePath+"/req_1"+ConfirmSuffix, "").Code)
	})

	T.Run("mounts under a base path of the consumer's choosing", func(t *testing.T) {
		t.Parallel()

		req := requestFor("req_1", theSubject, dataprivacy.StatusCompleted, "op_1")

		handler := mount(t, serviceReturning(req), theSubject, WithBasePath("/gdpr"))

		test.EqOp(t, nethttp.StatusOK, do(t, handler, nethttp.MethodGet, "/gdpr/req_1", "").Code)
	})
}

func TestHandlers_receipt(T *testing.T) {
	T.Parallel()

	T.Run("nil request, nil receipt", func(t *testing.T) {
		t.Parallel()

		handlers, err := New(&dataprivacymock.ServiceMock{}, WithSubjectResolver(subjectFromContext))
		must.NoError(t, err)

		test.Nil(t, handlers.receipt(nil))
	})

	// The paths are built from operations/http's own constants, so a consumer
	// that mounts that surface elsewhere says so once and the receipts follow.
	T.Run("follows the operations surface to where it is mounted", func(t *testing.T) {
		t.Parallel()

		handlers, err := New(&dataprivacymock.ServiceMock{},
			WithSubjectResolver(subjectFromContext),
			WithOperationsBasePath("/v1/operations"))
		must.NoError(t, err)

		receipt := handlers.receipt(requestFor("req_1", theSubject, dataprivacy.StatusInProgress, "op_1"))

		test.EqOp(t, "/v1/operations/op_1", receipt.Progress)
		test.EqOp(t, "/v1/operations/op_1/events", receipt.Events)
	})
}

func TestOwns(T *testing.T) {
	T.Parallel()

	req := requestFor("req_1", dataprivacy.Subject{ID: "subject_1", Scope: tenancy.Of("account_1")}, dataprivacy.StatusCompleted, "op_1")

	cases := map[string]struct {
		req     *dataprivacy.Request
		subject dataprivacy.Subject
		want    bool
	}{
		"same subject, no scope named":  {subject: dataprivacy.Subject{ID: "subject_1"}, req: req, want: true},
		"same subject, same scope":      {subject: dataprivacy.Subject{ID: "subject_1", Scope: tenancy.Of("account_1")}, req: req, want: true},
		"same subject, other scope":     {subject: dataprivacy.Subject{ID: "subject_1", Scope: tenancy.Of("account_2")}, req: req, want: false},
		"different subject":             {subject: dataprivacy.Subject{ID: "subject_2"}, req: req, want: false},
		"no request":                    {subject: dataprivacy.Subject{ID: "subject_1"}, req: nil, want: false},
		"empty subject matches nothing": {subject: dataprivacy.Subject{}, req: req, want: false},
	}

	for name, testCase := range cases {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			test.EqOp(t, testCase.want, owns(testCase.subject, testCase.req))
		})
	}
}
