package http

import (
	"context"
	"errors"
	"io"
	"mime"
	nethttp "net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/primandproper/platform-go/v14/dataprivacy"

	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/routing"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/swaggest/openapi-go/openapi3"
)

// ArtifactSuffix is appended to a request's path for its export's artifact.
const ArtifactSuffix = "/artifact"

// artifactOperationID is the OpenAPI operation ID of the artifact route. It is
// named once and used twice — in the document and in the Route MountArtifact
// returns — because those two are the same endpoint.
const artifactOperationID = "download_privacy_request_artifact"

// The headers the artifact route decides, spelled once. Each is a decision the
// package documentation argues for rather than a default it inherited.
const (
	cacheControlHeader = "Cache-Control"

	// artifactCacheControl keeps the artifact out of every cache, shared or
	// not. It is everything the application holds about a person, served on
	// the strength of who is asking, and a copy left in a browser's cache is a
	// copy nothing expires.
	artifactCacheControl = "private, no-store"

	// redirectCacheControl keeps the redirect out of every cache too. What it
	// points at is a signed URL with an expiry of its own, and a cached
	// redirect is a stale one handed to whoever asks next.
	redirectCacheControl = "no-store"

	contentDispositionHeader = "Content-Disposition"
	contentTypeHeader        = "Content-Type"
	locationHeader           = "Location"

	contentTypeOptionsHeader = "X-Content-Type-Options"
	contentTypeOptionsValue  = "nosniff"

	// artifactContentType is what Service.Open hands back, whatever the
	// artifact was stored under: canonical JSON, decompressed and decrypted.
	artifactContentType = "application/json"
)

// deliveryKey records which of the two answers the route gave.
const deliveryKey = "dataprivacy.artifact_delivery"

// The two deliveries.
const (
	deliveryRedirect = "redirect"
	deliveryProxy    = "proxy"
)

// MountArtifact registers the route an export's artifact is downloaded from. It
// is in OwnStandingRoutes and requires no grant: it serves a subject their own
// export, and nobody else's.
//
// It is not part of Mount, and that is deliberate rather than an oversight to
// correct. A route added to Mount is a route added to every deployment already
// calling it, which is a change to their surface they did not ask for; the
// deployment that wants this one says so.
//
// A completed export whose storage can sign URLs, and whose artifact is not
// encrypted, is answered 303 to a signed URL, so the bytes do not pass through
// the application. One whose artifact is encrypted, or whose storage cannot
// sign, is answered with the artifact itself — decrypted, decompressed, as the
// JSON it was built as — because that is the only way it reaches the subject at
// all. A request with no artifact to hand over is a conflict, through the error
// registry like every other refusal here.
//
// Call it before MountOpenAPI, so the spec the router serves includes it, and
// before the handlers answer anything: a Receipt names the artifact's path only
// once this has been called.
func (h *Handlers) MountArtifact(r *routing.Router) *routing.Route {
	pattern := h.artifactPattern()
	backend := r.Backend()

	// Registered on the Backend rather than through routing's typed
	// registration, for the reason mediaregistry/http's serve is: a typed
	// handler's output is encoded by the framework, and this answers with a
	// redirect or a file.
	r.Handle(nethttp.MethodGet, pattern, nethttp.HandlerFunc(func(res nethttp.ResponseWriter, req *nethttp.Request) {
		h.artifact(res, req, backend.PathValue(req, pathParam))
	}))

	h.describeArtifact(r, pattern)
	h.artifactMounted.Store(true)

	return &routing.Route{
		Method:      nethttp.MethodGet,
		Path:        pattern,
		OperationID: artifactOperationID,
	}
}

// artifact reads the request as its subject, then hands over what it can.
//
// The confinement is the one every other route here applies — read, which
// reports somebody else's request as absent — and it runs before the Service
// is asked for anything about the artifact. Download and Open both read the
// request again, narrowed by the same scope; the subject comparison is the part
// only this package makes.
func (h *Handlers) artifact(res nethttp.ResponseWriter, req *nethttp.Request, requestID string) {
	ctx, span := h.o11y.Begin(req.Context(), observability.WithValue(requestIDKey, requestID))
	defer span.End()

	request, scope, err := h.read(ctx, requestID)
	if err != nil {
		h.refuse(ctx, res, span, span.Error(err, "reading dataprivacy request for its artifact"))

		return
	}

	signed, err := h.svc.Download(ctx, scope, request.ID)

	switch {
	case err == nil:
		span.Set(deliveryKey, deliveryRedirect)

		header := res.Header()
		header.Set(cacheControlHeader, redirectCacheControl)
		header.Set(locationHeader, signed)

		res.WriteHeader(nethttp.StatusSeeOther)
	case errors.Is(err, dataprivacy.ErrArtifactEncrypted), errors.Is(err, dataprivacy.ErrNoURLSigner):
		// Not a failure: the two ways Download says the bytes have to come
		// through here instead.
		span.Set(deliveryKey, deliveryProxy)

		h.proxyArtifact(ctx, res, span, request, scope)
	default:
		h.refuse(ctx, res, span, span.Error(err, "signing dataprivacy artifact URL"))
	}
}

// proxyArtifact writes the artifact itself, as an attachment.
func (h *Handlers) proxyArtifact(
	ctx context.Context,
	res nethttp.ResponseWriter,
	span observability.Operation,
	request *dataprivacy.Request,
	scope *tenancy.Scope,
) {
	opened, err := h.svc.Open(ctx, scope, request.ID)
	if err != nil {
		h.refuse(ctx, res, span, span.Error(err, "opening dataprivacy artifact"))

		return
	}

	defer func() {
		if closeErr := opened.Close(); closeErr != nil {
			span.Acknowledge(closeErr, "closing dataprivacy artifact")
		}
	}()

	// application/json, stated rather than sniffed, and as an attachment: the
	// body is a file for the subject to keep, not a document for a browser to
	// render, and nosniff holds the browser to the type this package states.
	header := res.Header()
	header.Set(contentTypeHeader, artifactContentType)
	header.Set(contentDispositionHeader, artifactDisposition(request.ID))
	header.Set(contentTypeOptionsHeader, contentTypeOptionsValue)
	header.Set(cacheControlHeader, artifactCacheControl)

	res.WriteHeader(nethttp.StatusOK)

	if _, err = io.Copy(res, opened); err != nil {
		// The status is already sent, so there is no refusal left to write: the
		// client sees a body that stopped. Worth a line, not an error — a client
		// that navigated away produces this too.
		span.Acknowledge(err, "writing dataprivacy artifact")
	}
}

// refuse writes the platform's error envelope under the status errors/http
// resolves for err.
//
// It renders through routing.DefaultErrorBody, which is what the typed routes
// beside it answer with, so a refusal here is the same envelope — trace ID
// included — as a refusal from any of them. The mapping is the registry's, not
// this route's, which is the stance the rest of the package takes:
// dataprivacy.HTTPMapper answers a request that is not the caller's 404 and one
// with no artifact 409, once the composition root has made its one call to
// errormappers.Register.
func (h *Handlers) refuse(ctx context.Context, res nethttp.ResponseWriter, span observability.Operation, err error) {
	status, body := routing.DefaultErrorBody(ctx, err)

	encoded, err := h.codec.Marshal(ctx, body)
	if err != nil {
		nethttp.Error(res, "could not encode the response", nethttp.StatusInternalServerError)

		return
	}

	header := res.Header()
	header.Del(contentDispositionHeader)
	header.Set(contentTypeHeader, h.codec.ContentType())
	header.Set(contentTypeOptionsHeader, contentTypeOptionsValue)
	header.Set(cacheControlHeader, artifactCacheControl)

	res.WriteHeader(status)

	// The body is the platform's own error envelope, encoded by the platform's
	// own codec and served under the content type that codec names. There is no
	// HTML context for it to escape into.
	//nolint:gosec // G705: see above.
	if _, err = res.Write(encoded); err != nil {
		span.Acknowledge(err, "writing the refusal")
	}
}

// artifactDisposition names the download after the request, which is what
// storage names it after too, and never after the subject — see
// dataprivacy's artifactPath for why.
func artifactDisposition(requestID string) string {
	return mime.FormatMediaType("attachment", map[string]string{"filename": requestID + ".json"})
}

// artifactPattern renders the path the artifact route is registered under.
func (h *Handlers) artifactPattern() string {
	return path.Join(h.basePath, "/{"+pathParam+"}", ArtifactSuffix)
}

// artifactPath is where one request's artifact is downloaded from, at this
// Handlers' base path.
func (h *Handlers) artifactPath(requestID string) string {
	return path.Join(h.basePath, url.PathEscape(requestID)) + ArtifactSuffix
}

// ArtifactLinkSigner is the link a completion notification carries when the
// artifact is downloaded through MountArtifact, for
// dataprivacy.WithFulfillerURLSigner.
//
// It exists for the deployment that encrypts its artifacts. There,
// dataprivacy.NewArtifactURLSigner declines — a signed URL to ciphertext is a
// file the subject cannot open — and the notification goes out with no link at
// all. This one links to the route instead, which decrypts, so the mail says
// where the export is rather than telling the subject to go and find it.
//
// publicBaseURL is where the router is reached from outside, with whatever path
// prefix a proxy puts in front of it and without the surface's own base path,
// which is appended — the default BasePath here, and the one a Handlers was
// built with from its own ArtifactLinkSigner method. The link is absolute
// because it leaves in a mail, where there is no request URL to resolve a
// relative one against.
//
// The expiry it reports is the artifact's own. The link is not signed and does
// not expire: the route authenticates whoever follows it, and it stops working
// when the sweep deletes the artifact, which is the moment the notification
// should say it will.
//
// It declines a request with no artifact, as the signer it replaces does.
func ArtifactLinkSigner(publicBaseURL string) func(ctx context.Context, req *dataprivacy.Request) (string, time.Time) {
	return artifactLinkSigner(publicBaseURL, BasePath)
}

// ArtifactLinkSigner is the package's ArtifactLinkSigner at the base path this
// Handlers was built with.
func (h *Handlers) ArtifactLinkSigner(publicBaseURL string) func(ctx context.Context, req *dataprivacy.Request) (string, time.Time) {
	return artifactLinkSigner(publicBaseURL, h.basePath)
}

func artifactLinkSigner(publicBaseURL, basePath string) func(ctx context.Context, req *dataprivacy.Request) (string, time.Time) {
	root := strings.TrimSuffix(publicBaseURL, "/")

	return func(_ context.Context, req *dataprivacy.Request) (string, time.Time) {
		if req == nil || req.ID == "" || req.ArtifactRef == "" {
			return "", time.Time{}
		}

		return root + path.Join(basePath, url.PathEscape(req.ID)) + ArtifactSuffix, req.ExpiresAt
	}
}

// describeArtifact writes the artifact route into the OpenAPI document by hand,
// because routing's reflector builds an operation from a typed handler and this
// is not one.
func (h *Handlers) describeArtifact(r *routing.Router, pattern string) {
	spec := r.Spec()
	if spec == nil {
		return
	}

	description := "Hands the calling subject their completed export. Where storage can sign URLs and the " +
		"artifact is not encrypted, the answer is a redirect to a short-lived signed URL; otherwise it is " +
		"the artifact itself, as JSON, as an attachment. A request belonging to somebody else is answered " +
		"404, and one with no artifact to hand over 409."

	op := openapi3.Operation{
		Tags:        h.tags,
		ID:          new(artifactOperationID),
		Summary:     new("Download a data-privacy request's artifact"),
		Description: &description,

		Parameters: []openapi3.ParameterOrRef{{
			Parameter: &openapi3.Parameter{
				Name:     pathParam,
				In:       openapi3.ParameterInPath,
				Required: new(true),
				Schema: &openapi3.SchemaOrRef{
					Schema: &openapi3.Schema{Type: new(openapi3.SchemaTypeString)},
				},
			},
		}},
	}

	op.Responses.MapOfResponseOrRefValues = map[string]openapi3.ResponseOrRef{
		"200": {
			Response: &openapi3.Response{
				Description: "The artifact, decrypted and decompressed.",
				Content: map[string]openapi3.MediaType{
					artifactContentType: {},
				},
			},
		},
		"303": {
			Response: &openapi3.Response{
				Description: "A signed URL the artifact is fetched from directly.",
			},
		},
	}

	// The reflector's own error accumulator is not reachable from here, so a
	// failure to add this is logged rather than swallowed: the route still
	// works, and the document is missing one entry.
	if err := spec.AddOperation(nethttp.MethodGet, pattern, op); err != nil {
		h.o11y.Logger().Error("describing the artifact route in the OpenAPI document", err)
	}
}
