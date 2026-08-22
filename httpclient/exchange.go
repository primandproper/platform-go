package httpclient

import (
	"bytes"
	"context"
	"io"
	"maps"
	"net/http"

	"github.com/primandproper/platform-go/v13/encoding"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

// codecs is every encoding an exchange can speak, one client-side codec per
// content type the encoding package implements.
//
// It is built from encoding.ContentTypes rather than from a list written out
// here, so an encoding added there is reachable from an exchange without this
// package being touched, and so the set this package speaks cannot drift from
// the set that package implements. A lookup that misses is the only definition
// of "unsupported" this package has.
//
// They are the encoding package's client seam rather than encoding/json and its
// counterparts directly, which is what the rest of this module reaches for
// whenever a value has to become bytes; the JSON codec's output is
// byte-for-byte json.Marshal's. None of them is the ServerEncoderDecoder, which
// is about writing responses and has no business here.
//
// No observability is attached, because there is nothing here for it to say
// that the caller's own span does not already cover — a marshal of a struct the
// caller just built, and an unmarshal of a body the transport already traced.
var codecs = buildCodecs()

func buildCodecs() map[encoding.ContentType]encoding.Codec {
	built := make(map[encoding.ContentType]encoding.Codec, len(encoding.ContentTypes))

	for _, contentType := range encoding.ContentTypes {
		built[contentType] = encoding.NewClientEncoder(contentType)
	}

	return built
}

// Doer sends a prepared request and returns the response. *http.Client is the
// implementation callers pass, and the one the rest of this package builds.
//
// It is an interface rather than *http.Client for two reasons, both about what
// can be substituted for a client: BaseURLClient wraps one so a call site can
// name a path instead of a URL, and a test can answer an exchange without
// standing up a server. Neither is a place to reimplement a client — a Doer
// that is not, eventually, an *http.Client built by this package has none of
// the retrying, breaking, or limiting that everything below assumes has already
// happened.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// NoContent is the response type for an exchange whose reply body is not read
// at all:
//
//	_, err := httpclient.Exchange[httpclient.NoContent](ctx, client, http.MethodDelete, url, nil)
//
// It exists because "decode the body into Out" and "there is no body" are
// different requests, and the alternative — inferring one from an empty body —
// would turn a server that answered 200 with nothing into a silent zero value
// for every caller who did expect a body.
//
// The body is closed but not read, so a server that sent one anyway costs this
// exchange its connection and nothing else.
type NoContent struct{}

// Exchange performs one encoded exchange over doer: encode in, send it to url,
// check the status, and decode the response into Out.
//
//	claim, err := httpclient.Exchange[ClaimResponse](ctx, client, http.MethodPost, url, request)
//
// # The encoding is a choice, and JSON is only its default
//
// WithContentType names the encoding, over any content type the encoding
// package implements — JSON, XML, TOML, YAML, CBOR, Ecoji. Unnamed, it is
// DefaultContentType, which is JSON because that is what the overwhelming
// majority of these calls speak, not because this package treats JSON as the
// encoding and the rest as exceptions.
//
// A content type this package cannot speak is an error rather than a fallback,
// the same answer encoding.ParseContentType gives, and for the same reason:
// silently standing in for JSON would turn a typo into a request that reaches a
// real server and is misunderstood by it.
//
//	doc, err := httpclient.Exchange[Manifest](ctx, client, http.MethodGet, url, nil,
//		httpclient.WithContentType(encoding.ContentTypeCBOR),
//	)
//
// # The exchange itself
//
// A nil in sends no body at all — not an encoded null — and sets no
// Content-Type. Anything else is marshaled, and the bytes are held rather than
// streamed, so the request carries a GetBody and a retrying client can replay
// it.
//
// Every request asks for its own content type back in Accept, and the reply is
// decoded with that same codec whatever the response's Content-Type says. That
// leniency is deliberate: a server that answers JSON while labeling it
// text/plain is common, and reading the response header instead would refuse
// precisely the case the leniency exists for. The codec is the caller's
// statement of what it expects, not a guess at what arrived.
//
// A status outside 2xx is a *StatusError carrying the status, the request path,
// and a bounded prefix of the body — see WithErrorBodyLimit. Out is the zero
// value for every error this returns, including that one: a failed exchange
// decodes nothing, so there is no half-populated value to mistake for an
// answer.
//
// # It adds no resilience of its own
//
// Retrying, circuit breaking, rate limiting, and caching belong to the
// transports doer was built with, and are already finished by the time this
// reads a status. A helper that retried on top of them would give a client
// configured with WithRetryPolicy two nested loops and a caller no way to
// predict how many requests one call makes.
//
// What the error does carry is the classification those transports already use.
// A *StatusError whose status DefaultRetryClassification calls terminal matches
// retry.ErrUnretryable, so a caller wrapping a whole operation — several
// exchanges, or an exchange plus the work around it — in a retry.Policy of its
// own stops on a 400 and keeps trying a 429, without restating the rule.
func Exchange[Out any](ctx context.Context, doer Doer, method, url string, in any, opts ...ExchangeOption) (Out, error) {
	var out Out

	if doer == nil {
		return out, platformerrors.Wrap(platformerrors.ErrNilInputParameter, "performing an exchange without a client")
	}

	cfg, err := newExchangeConfig(opts)
	if err != nil {
		return out, err
	}

	req, err := buildRequest(ctx, cfg, method, url, in)
	if err != nil {
		return out, err
	}

	resp, err := doer.Do(req)
	if err != nil {
		// The error a client returns is a *url.Error already naming the method
		// and the URL, so restating either here would only say it twice.
		return out, platformerrors.Wrap(err, "sending the request")
	}

	defer func() {
		_ = resp.Body.Close() //nolint:errcheck // best-effort close; the bytes that mattered are already read.
	}()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return out, newStatusError(req, resp, cfg.errorBodyLimit)
	}

	// The zero value of Out is what a NoContent exchange returns, and asking it
	// what type it is costs less than the read it saves.
	if _, isEmpty := any(out).(NoContent); isEmpty {
		return out, nil
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return out, platformerrors.Wrapf(err, "reading the %s %s response", req.Method, req.URL.Path)
	}

	if err = cfg.codec.Unmarshal(ctx, raw, &out); err != nil {
		var zero Out

		return zero, platformerrors.Wrapf(err, "decoding the %s %s response", req.Method, req.URL.Path)
	}

	return out, nil
}

// buildRequest builds the request an exchange sends: the encoded body, the two
// headers that describe it, and whatever the caller added on top.
func buildRequest(ctx context.Context, cfg *exchangeConfig, method, url string, in any) (*http.Request, error) {
	body := io.Reader(http.NoBody)

	if in != nil {
		raw, err := cfg.codec.Marshal(ctx, in)
		if err != nil {
			return nil, platformerrors.Wrapf(err, "encoding the %s request body as %s", method, cfg.codec.ContentType())
		}

		// A bytes.Reader is what makes http.NewRequestWithContext populate
		// GetBody, and GetBody is what lets the retry transport send a second
		// attempt. Handing it an unseekable reader would produce a request that
		// silently declines to be retried.
		body = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, platformerrors.Wrapf(err, "building the %s request", method)
	}

	req.Header.Set("Accept", cfg.codec.ContentType())

	if in != nil {
		req.Header.Set("Content-Type", cfg.codec.ContentType())
	}

	// Last, so a caller that has something else to say about either header —
	// a vendor media type, a service that rejects a bare application/json —
	// says it and is believed.
	maps.Copy(req.Header, cfg.header)

	return req, nil
}
