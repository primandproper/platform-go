package httpclient

import (
	"net/http"

	"github.com/primandproper/platform-go/v13/encoding"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

// DefaultErrorBodyLimit is how much of a refused response's body a StatusError
// keeps when no other bound is named.
//
// 512 bytes is enough for every error shape a service designs on purpose — a
// JSON problem document, a validation list, a plain sentence — and short enough
// that the shapes nobody designed, an HTML error page from a load balancer or a
// stack trace from a framework's debug mode, cost one log line rather than a
// log budget.
const DefaultErrorBodyLimit = 512

// DefaultContentType is the encoding an exchange uses when WithContentType
// names none.
//
// It is JSON because that is what almost every service-to-service call in front
// of this package speaks, and for no stronger reason than that. It is a default
// and not a rule: every content type the encoding package implements is a peer
// here, reachable by naming it, and nothing in an exchange is written in terms
// of JSON specifically.
const DefaultContentType = encoding.ContentTypeJSON

// ExchangeOption customizes a single exchange. Options are applied in order, so
// a later one overrides an earlier one.
//
// It is per call rather than per client because what it governs is per call:
// the headers this request needs, the encoding this endpoint speaks, and how
// much of its error body is worth keeping. Everything durable about a client —
// its timeout, its transports, its observability — is an Option on
// NewHTTPClient, and nothing here is a second way to set any of it.
type ExchangeOption func(*exchangeConfig)

// exchangeConfig is the resolved configuration for one exchange.
type exchangeConfig struct {
	codec          encoding.Codec
	header         http.Header
	contentType    encoding.ContentType
	errorBodyLimit int
}

// newExchangeConfig resolves the options for one exchange, defaults first, and
// then resolves the codec the exchange will marshal and unmarshal through.
//
// The codec is looked up here rather than at each use so that an unsupported
// content type is refused before a request is built. Otherwise a bodiless GET
// would send an Accept header naming an encoding this package cannot read and
// fail at the decode, several layers and one wire request away from the option
// that caused it.
func newExchangeConfig(opts []ExchangeOption) (*exchangeConfig, error) {
	cfg := &exchangeConfig{
		contentType:    DefaultContentType,
		header:         http.Header{},
		errorBodyLimit: DefaultErrorBodyLimit,
	}

	for _, opt := range opts {
		if opt != nil {
			opt(cfg)
		}
	}

	codec, ok := codecs[cfg.contentType]
	if !ok {
		return nil, platformerrors.Wrapf(encoding.ErrUnsupportedContentType, "exchanging %q", cfg.contentType)
	}

	cfg.codec = codec

	return cfg, nil
}

// WithContentType selects the encoding an exchange speaks: the request body's
// Content-Type, the Accept it asks for, and the codec the response is decoded
// with.
//
//	doc, err := httpclient.Exchange[Manifest](ctx, client, http.MethodGet, url, nil,
//		httpclient.WithContentType(encoding.ContentTypeCBOR),
//	)
//
// Any content type the encoding package implements is accepted. One it does not
// is encoding.ErrUnsupportedContentType from the exchange, not a quiet fall back
// to DefaultContentType — unlike WithHeader and WithErrorBodyLimit, which ignore
// an argument that cannot mean anything. The difference is what the mistake
// costs: an ignored empty header name sends the request the caller meant, while
// a content type silently replaced by JSON sends a request some server will
// answer, wrongly, and nothing will say so.
//
// It sets both directions, which is what a service speaking one encoding wants.
// A caller that has to send one encoding and accept another sets the odd half
// with WithHeader, which is applied after these and wins.
func WithContentType(contentType encoding.ContentType) ExchangeOption {
	return func(c *exchangeConfig) {
		c.contentType = contentType
	}
}

// WithHeader sets a request header on the exchange, replacing any value the
// exchange would have set itself.
//
// It is the seam for the per-request headers an API asks for and a transport
// cannot know about: an Idempotency-Key, a tenant, a request identifier, a
// vendor media type in place of the Accept and Content-Type this package fills
// in. Credentials that hold for every call belong further down — in a transport
// wrapped by WithTransport, or in WithRequestSigning — rather than repeated at
// every call site, where the one that gets forgotten is the one that matters.
//
// An empty name is ignored.
func WithHeader(name, value string) ExchangeOption {
	return func(c *exchangeConfig) {
		if name != "" {
			c.header.Set(name, value)
		}
	}
}

// WithErrorBodyLimit bounds how many bytes of a refused response's body a
// StatusError keeps, and how many are read from the wire at all.
//
// A negative limit leaves DefaultErrorBodyLimit in place. Zero is a real
// answer, and the one to give an endpoint whose failures are known to be
// worthless or sensitive: the body is neither read nor kept, and the error
// reports the status alone.
func WithErrorBodyLimit(bytes int) ExchangeOption {
	return func(c *exchangeConfig) {
		if bytes >= 0 {
			c.errorBodyLimit = bytes
		}
	}
}
