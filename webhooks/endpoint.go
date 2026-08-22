package webhooks

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/primandproper/platform-go/v13/cryptography/requestsigning"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

// DefaultContentType is the Content-Type deliveries carry when an Endpoint does
// not set one.
const DefaultContentType = "application/json"

var (
	// ErrInvalidEndpointURL indicates a URL that is unparseable, not absolute,
	// or not https.
	ErrInvalidEndpointURL = platformerrors.New("invalid webhook endpoint URL")

	// ErrDisallowedEndpointHost indicates a URL whose host resolves somewhere a
	// webhook must not reach — loopback, link-local, or private address space.
	// See CheckEndpointURL for why this is enforced at registration.
	ErrDisallowedEndpointHost = platformerrors.New("webhook endpoint host is not publicly routable")

	// ErrReservedHeader indicates an Endpoint whose static headers would
	// overwrite one this package sets.
	ErrReservedHeader = platformerrors.New("webhook endpoint sets a reserved header")

	// ErrNoEvents indicates an endpoint subscribing to nothing, which is never
	// what the registrant meant.
	ErrNoEvents = platformerrors.New("webhook endpoint subscribes to no events")
)

// reservedHeaders are the headers this package sets on every request. An
// Endpoint's static Headers may not contain them: a subscriber that could
// overwrite its own signature header would be authenticating deliveries against
// a value it chose.
var reservedHeaders = []string{
	requestsigning.SignatureHeader,
	requestsigning.TimestampHeader,
	EventTypeHeader,
	DeliveryIDHeader,
	AttemptHeader,
	"Content-Type",
}

// CheckEndpointURL reports whether u is acceptable as a delivery target.
//
// This is SSRF prevention, and it is worth being explicit about what it does
// and does not buy. A webhook endpoint is a URL supplied by a user that the
// server will then make authenticated requests to, which is the textbook shape
// of a server-side request forgery: point it at 169.254.169.254 and the
// delivery worker fetches cloud instance credentials on the attacker's behalf,
// or point it at an internal admin service and the worker reaches something the
// attacker cannot.
//
// So: https only, and no host that resolves into loopback, link-local, private,
// or otherwise non-global address space.
//
// The check runs at registration, where a rejection can be reported to whoever
// submitted the URL, and again at delivery, because registration alone is not
// sound: DNS is mutable, and a name that resolved publicly when it was
// registered can resolve to 127.0.0.1 by the time the worker dials it.
//
// What this does not close is DNS rebinding. Resolution and connection are
// separate steps, and an attacker controlling the authoritative server can
// return a public address to this lookup and a private one to the dial moments
// later. Closing that needs the checked IP pinned into the dial itself — a
// custom DialContext that resolves once and refuses anything else — which this
// package does not do, because it would mean owning the transport rather than
// accepting the caller's. Deployments where that gap matters should supply a
// pinning transport via WithHTTPClient; this function raises the cost without
// claiming to eliminate it.
func CheckEndpointURL(ctx context.Context, rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return platformerrors.Wrapf(ErrInvalidEndpointURL, "parsing %q: %v", rawURL, err)
	}

	// https only. Plaintext delivery would put the payload — and the headers
	// that authenticate it — on the wire in clear, and a signature does not make
	// a payload confidential.
	if parsed.Scheme != "https" {
		return platformerrors.Wrapf(ErrInvalidEndpointURL, "scheme %q is not https", parsed.Scheme)
	}

	if parsed.Host == "" {
		return platformerrors.Wrapf(ErrInvalidEndpointURL, "no host in %q", rawURL)
	}

	if parsed.User != nil {
		// Credentials in the URL would be logged everywhere the URL is, and the
		// signature is how a subscriber authenticates us — not basic auth.
		return platformerrors.Wrapf(ErrInvalidEndpointURL, "userinfo is not permitted in an endpoint URL")
	}

	host := parsed.Hostname()

	// A literal IP is checked directly; a name is checked against everything it
	// currently resolves to.
	if ip := net.ParseIP(host); ip != nil {
		return checkIP(ip, host)
	}

	// Resolved through the context-aware resolver rather than net.LookupIP: this
	// runs on the delivery path, and a name whose authoritative server is
	// blackholed would otherwise hang a worker goroutine for the resolver's own
	// timeout with nothing able to cancel it.
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return platformerrors.Wrapf(ErrInvalidEndpointURL, "resolving %q: %v", host, err)
	}

	if len(ips) == 0 {
		return platformerrors.Wrapf(ErrInvalidEndpointURL, "%q resolves to no addresses", host)
	}

	// Every resolved address must be acceptable, not merely one of them. A name
	// that returns both a public and a loopback address would otherwise pass
	// here and then be dialed at whichever the resolver returned first.
	for i := range ips {
		if err = checkIP(ips[i].IP, host); err != nil {
			return err
		}
	}

	return nil
}

// checkIP rejects an address that is not globally routable.
//
// IsGlobalUnicast is necessary but not sufficient: it admits the RFC 1918
// private ranges and unique-local IPv6, which are precisely the internal
// networks this is meant to keep deliveries out of.
func checkIP(ip net.IP, host string) error {
	switch {
	case ip.IsLoopback():
		return platformerrors.Wrapf(ErrDisallowedEndpointHost, "%q resolves to loopback address %s", host, ip)
	case ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast():
		// 169.254.0.0/16 — the cloud instance metadata range.
		return platformerrors.Wrapf(ErrDisallowedEndpointHost, "%q resolves to link-local address %s", host, ip)
	case ip.IsPrivate():
		return platformerrors.Wrapf(ErrDisallowedEndpointHost, "%q resolves to private address %s", host, ip)
	case ip.IsUnspecified():
		return platformerrors.Wrapf(ErrDisallowedEndpointHost, "%q resolves to unspecified address %s", host, ip)
	case ip.IsMulticast(), ip.IsInterfaceLocalMulticast():
		return platformerrors.Wrapf(ErrDisallowedEndpointHost, "%q resolves to multicast address %s", host, ip)
	case !ip.IsGlobalUnicast():
		return platformerrors.Wrapf(ErrDisallowedEndpointHost, "%q resolves to non-global address %s", host, ip)
	default:
		return nil
	}
}

// URLChecker vets a delivery target. CheckEndpointURL is the implementation
// this package uses unless a caller replaces it.
//
// It is replaceable because a minority of deployments deliver webhooks to
// internal services on purpose — a sidecar, another service on the same private
// network — and for them CheckEndpointURL's refusal is not a safety property
// but a wall. Replacing it means owning the SSRF question yourself: the
// replacement is the only thing standing between a user-supplied URL and an
// authenticated request from inside your network, so it should be an allowlist
// of hosts you operate, not a function that returns nil.
type URLChecker func(ctx context.Context, rawURL string) error

// EnsureDefaults fills an Endpoint's optional fields.
func (e *Endpoint) EnsureDefaults() {
	if e == nil {
		return
	}

	if e.ContentType == "" {
		e.ContentType = DefaultContentType
	}
}

// Validate checks an Endpoint against the catalog it is being registered into
// and the URL policy it will be delivered under.
//
// The catalog is an argument because an endpoint is only meaningful relative to
// the set of events an application publishes: a subscription to an event that
// does not exist is a silent no-op forever. checkURL is an argument so that
// registration and delivery cannot apply different policies — an endpoint
// accepted here and refused by the worker would sit in the backlog until it
// died. A nil checkURL means CheckEndpointURL.
//
// The scope is checked here, with the other invariants, rather than being left to
// the store: an endpoint that says nothing about whose it is is one an
// application with tenants registered by accident, and the account it was meant
// for would never see a delivery. Say tenancy.Global() to mean it.
func (e *Endpoint) Validate(ctx context.Context, catalog Catalog, checkURL URLChecker) error {
	if e == nil {
		return ErrNilEndpoint
	}

	if err := e.Scope.Validate(); err != nil {
		return err
	}

	if checkURL == nil {
		checkURL = CheckEndpointURL
	}

	if err := checkURL(ctx, e.URL); err != nil {
		return err
	}

	if len(e.Secret.Current) == 0 {
		return ErrNoSigningSecret
	}

	if len(e.Events) == 0 {
		return ErrNoEvents
	}

	for _, event := range e.Events {
		if !catalog.Known(event) {
			return platformerrors.Wrapf(ErrUnknownEventType, "event type %q", event)
		}
	}

	for name := range e.Headers {
		if slices.ContainsFunc(reservedHeaders, func(reserved string) bool {
			return strings.EqualFold(name, reserved)
		}) {
			return platformerrors.Wrapf(ErrReservedHeader, "header %q", name)
		}
	}

	return nil
}

// applyHeaders writes the endpoint's static headers onto a request. Reserved
// headers are rejected at registration, so nothing here can overwrite one; the
// check is repeated defensively because an Endpoint can also arrive from a Store
// implementation this package did not validate.
func (e *Endpoint) applyHeaders(header http.Header) {
	for name, value := range e.Headers {
		if slices.ContainsFunc(reservedHeaders, func(reserved string) bool {
			return strings.EqualFold(name, reserved)
		}) {
			continue
		}

		header.Set(name, value)
	}
}
