package http

import (
	"context"
	"encoding/json"
	"io"
	nethttp "net/http"
	"strings"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/dataprivacy"
	dataprivacymock "github.com/primandproper/platform-go/v14/dataprivacy/mock"
	"github.com/primandproper/platform-go/v14/errormappers"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// artifactBody is what the Open mocks below hand back.
const artifactBody = `{"subject":{"id":"u1"},"sections":{}}`

// completedExport is an export row with an artifact to fetch.
func completedExport(id string, subject dataprivacy.Subject) *dataprivacy.Request {
	req := requestFor(id, subject, dataprivacy.StatusCompleted, "op-"+id)
	req.Type = dataprivacy.RequestExport
	req.ArtifactRef = "privacy-exports/" + id + ".json"
	req.ExpiresAt = time.Now().UTC().Add(7 * 24 * time.Hour)

	return req
}

// mountWithArtifact builds a router with the whole surface and the artifact
// route on it, under one subject.
func mountWithArtifact(t *testing.T, svc dataprivacy.Service, subject dataprivacy.Subject, opts ...Option) nethttp.Handler {
	t.Helper()

	errormappers.Register()

	handlers, router := build(t, svc, opts...)

	handlers.Mount(router)
	handlers.MountArtifact(router)

	must.NoError(t, router.Err())

	return asSubject(subject, router.Handler())
}

// downloading makes a Service mock whose Download answers err (or a URL, when
// err is nil) and whose Open answers artifactBody.
func downloading(req *dataprivacy.Request, err error) *dataprivacymock.ServiceMock {
	svc := serviceReturning(req)

	svc.DownloadFunc = func(context.Context, *tenancy.Scope, string) (string, error) {
		if err != nil {
			return "", err
		}

		return "https://storage.example/signed?sig=abc", nil
	}
	svc.OpenFunc = func(context.Context, *tenancy.Scope, string) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(artifactBody)), nil
	}

	return svc
}

func TestHandlers_artifact(T *testing.T) {
	T.Parallel()

	subject := dataprivacy.Subject{ID: "u1", Type: dataprivacy.SubjectUser}
	artifactPath := BasePath + "/r1" + ArtifactSuffix

	T.Run("storage that signs, for an unencrypted artifact, is a redirect nothing caches", func(t *testing.T) {
		t.Parallel()

		svc := downloading(completedExport("r1", subject), nil)

		res := do(t, mountWithArtifact(t, svc, subject), nethttp.MethodGet, artifactPath, "")

		must.EqOp(t, nethttp.StatusSeeOther, res.Code)
		test.EqOp(t, "https://storage.example/signed?sig=abc", res.Header().Get("Location"))
		test.EqOp(t, "no-store", res.Header().Get("Cache-Control"))
		test.SliceLen(t, 0, svc.OpenCalls(), test.Sprint("a signed URL was available and the bytes were proxied anyway"))
	})

	for name, refusal := range map[string]error{
		"an encrypted artifact":         dataprivacy.ErrArtifactEncrypted,
		"storage that cannot sign URLs": dataprivacy.ErrNoURLSigner,
	} {
		T.Run(name+" is streamed as a JSON attachment", func(t *testing.T) {
			t.Parallel()

			svc := downloading(completedExport("r1", subject), platformerrors.Wrap(refusal, "r1"))

			res := do(t, mountWithArtifact(t, svc, subject), nethttp.MethodGet, artifactPath, "")

			must.EqOp(t, nethttp.StatusOK, res.Code)
			test.EqOp(t, artifactBody, res.Body.String())
			test.EqOp(t, "application/json", res.Header().Get("Content-Type"))
			test.EqOp(t, `attachment; filename=r1.json`, res.Header().Get("Content-Disposition"))
			test.EqOp(t, "nosniff", res.Header().Get("X-Content-Type-Options"))
			test.EqOp(t, "private, no-store", res.Header().Get("Cache-Control"))
			test.SliceLen(t, 1, svc.OpenCalls())
		})
	}

	T.Run("a request with no artifact is a conflict in the platform's envelope", func(t *testing.T) {
		t.Parallel()

		svc := downloading(requestFor("r1", subject, dataprivacy.StatusInProgress, "op"),
			platformerrors.Wrap(dataprivacy.ErrArtifactUnavailable, "r1"))

		res := do(t, mountWithArtifact(t, svc, subject), nethttp.MethodGet, artifactPath, "")

		must.EqOp(t, nethttp.StatusConflict, res.Code)
		test.StrContains(t, res.Header().Get("Content-Type"), "application/json")
		test.EqOp(t, "nosniff", res.Header().Get("X-Content-Type-Options"))
		test.EqOp(t, "", res.Header().Get("Content-Disposition"))

		envelope := &struct {
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}{}
		must.NoError(t, json.Unmarshal(res.Body.Bytes(), envelope))
		must.NotNil(t, envelope.Error, must.Sprintf("the refusal is not the platform's error envelope: %s", res.Body.String()))
		test.EqOp(t, "privacy request has no downloadable artifact", envelope.Error.Message)
	})

	T.Run("a failure to open the artifact is a refusal rather than a truncated body", func(t *testing.T) {
		t.Parallel()

		svc := downloading(completedExport("r1", subject), platformerrors.Wrap(dataprivacy.ErrArtifactEncrypted, "r1"))
		svc.OpenFunc = func(context.Context, *tenancy.Scope, string) (io.ReadCloser, error) {
			return nil, platformerrors.New("bucket unwell")
		}

		res := do(t, mountWithArtifact(t, svc, subject), nethttp.MethodGet, artifactPath, "")

		test.EqOp(t, nethttp.StatusInternalServerError, res.Code)
		test.EqOp(t, "", res.Header().Get("Content-Disposition"))
	})

	T.Run("another subject's export is absent, and nothing about its artifact is asked", func(t *testing.T) {
		t.Parallel()

		svc := downloading(completedExport("r1", dataprivacy.Subject{ID: "somebody-else"}), nil)

		res := do(t, mountWithArtifact(t, svc, subject), nethttp.MethodGet, artifactPath, "")

		test.EqOp(t, nethttp.StatusNotFound, res.Code)
		test.SliceLen(t, 0, svc.DownloadCalls())
		test.SliceLen(t, 0, svc.OpenCalls())
	})

	T.Run("an unknown request is absent", func(t *testing.T) {
		t.Parallel()

		svc := downloading(nil, nil)

		res := do(t, mountWithArtifact(t, svc, subject), nethttp.MethodGet, BasePath+"/made-up"+ArtifactSuffix, "")

		test.EqOp(t, nethttp.StatusNotFound, res.Code)
		test.SliceLen(t, 0, svc.DownloadCalls())
	})

	T.Run("it answers a subject holding no grant", func(t *testing.T) {
		t.Parallel()

		svc := downloading(completedExport("r1", subject), nil)

		res := do(t, mountWithArtifact(t, svc, subject, WithEnforcer(enforcerGranting(t))), nethttp.MethodGet, artifactPath, "")

		test.EqOp(t, nethttp.StatusSeeOther, res.Code)
	})

	T.Run("a request with nobody on it is refused before anything is read", func(t *testing.T) {
		t.Parallel()

		errormappers.Register()

		svc := downloading(completedExport("r1", subject), nil)

		handlers, router := build(t, svc)
		handlers.MountArtifact(router)
		must.NoError(t, router.Err())

		res := do(t, router.Handler(), nethttp.MethodGet, artifactPath, "")

		test.Greater(t, 399, res.Code)
		test.True(t, untouched(svc))
		test.SliceLen(t, 0, svc.DownloadCalls())
	})

	T.Run("it is served at the base path the handlers were built with", func(t *testing.T) {
		t.Parallel()

		svc := downloading(completedExport("r1", subject), nil)

		res := do(t, mountWithArtifact(t, svc, subject, WithBasePath("/elsewhere")), nethttp.MethodGet, "/elsewhere/r1"+ArtifactSuffix, "")

		test.EqOp(t, nethttp.StatusSeeOther, res.Code)
	})
}

func TestReceipt_artifact(T *testing.T) {
	T.Parallel()

	subject := dataprivacy.Subject{ID: "u1"}

	read := func(t *testing.T, handler nethttp.Handler, id string) *Receipt {
		t.Helper()

		res := do(t, handler, nethttp.MethodGet, BasePath+"/"+id, "")
		must.EqOp(t, nethttp.StatusOK, res.Code, must.Sprint(res.Body.String()))

		out := &struct {
			Data *Receipt `json:"data"`
		}{}
		must.NoError(t, json.Unmarshal(res.Body.Bytes(), out))

		return out.Data
	}

	T.Run("a completed export names its artifact's path where the route is mounted", func(t *testing.T) {
		t.Parallel()

		got := read(t, mountWithArtifact(t, serviceReturning(completedExport("r1", subject)), subject), "r1")

		test.EqOp(t, BasePath+"/r1"+ArtifactSuffix, got.Artifact)
	})

	T.Run("and names none where it is not", func(t *testing.T) {
		t.Parallel()

		got := read(t, mount(t, serviceReturning(completedExport("r1", subject)), subject), "r1")

		test.EqOp(t, "", got.Artifact)
	})

	T.Run("a request with nothing to fetch names none", func(t *testing.T) {
		t.Parallel()

		for _, req := range []*dataprivacy.Request{
			requestFor("r1", subject, dataprivacy.StatusInProgress, "op"),
			requestFor("r1", subject, dataprivacy.StatusCompleted, "op"),
			requestFor("r1", subject, dataprivacy.StatusExpired, "op"),
		} {
			got := read(t, mountWithArtifact(t, serviceReturning(req), subject), "r1")

			test.EqOp(t, "", got.Artifact, test.Sprintf("a %s request named an artifact", req.Status))
		}
	})
}

func TestArtifactLinkSigner(T *testing.T) {
	T.Parallel()

	subject := dataprivacy.Subject{ID: "u1"}

	T.Run("links a completed export to the route, until its artifact expires", func(t *testing.T) {
		t.Parallel()

		req := completedExport("r1", subject)

		link, expires := ArtifactLinkSigner("https://api.example/v1/")(t.Context(), req)

		test.EqOp(t, "https://api.example/v1"+BasePath+"/r1"+ArtifactSuffix, link)
		test.EqOp(t, req.ExpiresAt, expires)
	})

	T.Run("declines a request with no artifact", func(t *testing.T) {
		t.Parallel()

		link, expires := ArtifactLinkSigner("https://api.example")(t.Context(), requestFor("r1", subject, dataprivacy.StatusCompleted, "op"))

		test.EqOp(t, "", link)
		test.True(t, expires.IsZero())

		link, _ = ArtifactLinkSigner("https://api.example")(t.Context(), nil)
		test.EqOp(t, "", link)
	})

	T.Run("a Handlers' own links at the base path it was built with", func(t *testing.T) {
		t.Parallel()

		handlers, _ := build(t, &dataprivacymock.ServiceMock{}, WithBasePath("/elsewhere"))

		link, _ := handlers.ArtifactLinkSigner("https://api.example")(t.Context(), completedExport("r1", subject))

		test.EqOp(t, "https://api.example/elsewhere/r1"+ArtifactSuffix, link)
	})
}
