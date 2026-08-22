package webauthn

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/primandproper/platform-go/v13/clock"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/metrics"

	"github.com/go-webauthn/webauthn/protocol"
	gowebauthn "github.com/go-webauthn/webauthn/webauthn"
)

// serviceName names the loggers, spans, and instruments this package emits.
const serviceName = "webauthn"

// challengeKey is the span and log key the ceremony's challenge is recorded
// under. The challenge is public — it travels to the browser and back in the
// clear — and it is the only handle that ties a Begin to its Finish in a log
// aggregator, which is what makes a failed ceremony diagnosable at all.
const challengeKey = "webauthn.challenge"

// RelyingParty runs the registration and login ceremonies for one Relying
// Party, keeping each ceremony's state in a SessionStore between the request
// that begins it and the request that finishes it.
//
// It is exported, and returned by NewRelyingParty, so a caller depends on what
// it built rather than on an interface. There is no interface: this is the
// protocol, and a second implementation of it would be a second WebAuthn.
type RelyingParty struct {
	webauthn *gowebauthn.WebAuthn
	store    SessionStore
	clock    clock.Clock
	o11y     observability.Observer

	instruments *metrics.OperationSet

	ceremonyTimeout time.Duration
}

// NewRelyingParty builds a RelyingParty over a session store.
//
// The store is required and has no default, for the reason ErrNilStore gives:
// the map every WebAuthn example starts from works until there are two
// replicas, and then fails a fraction of logins in a way that reads as a client
// bug.
//
// The context is for validating the config, which is done here rather than left
// to a composition root — an RPID that is not a domain, or an origin list that
// is empty, is a service that cannot register a passkey, and finding that out
// at the first registration is finding it out from a user.
func NewRelyingParty(ctx context.Context, cfg *Config, store SessionStore, opts ...Option) (*RelyingParty, error) {
	if cfg == nil {
		return nil, platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil webauthn config")
	}

	if store == nil {
		return nil, ErrNilStore
	}

	cfg.EnsureDefaults()

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, platformerrors.Wrap(err, "validating webauthn config")
	}

	o := newOptions(opts)

	w, err := gowebauthn.New(cfg.protocolConfig())
	if err != nil {
		return nil, platformerrors.Wrap(err, "building webauthn relying party")
	}

	instruments, err := metrics.NewOperationSet(o.metricsProvider, serviceName)
	if err != nil {
		return nil, err
	}

	return &RelyingParty{
		webauthn:        w,
		store:           store,
		clock:           o.clock,
		o11y:            observability.NewObserver(serviceName, o.logger, o.tracerProvider),
		instruments:     instruments,
		ceremonyTimeout: cfg.CeremonyTimeout,
	}, nil
}

// BeginRegistration issues the credential creation options a browser needs to
// register a new passkey for user, and stores the ceremony's state.
//
// The state is not returned. It is stored under the challenge and read back by
// FinishRegistration from the challenge the client echoes, which is what lets
// the two requests land on different replicas — and what stops a caller from
// round-tripping the ceremony's state through the client, where it would be
// the client deciding what challenge it had been given.
//
// A deployment that wants usernameless (discoverable) login registers resident
// credentials, which is a per-ceremony option:
//
//	rp.BeginRegistration(ctx, user,
//		gowebauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired))
func (rp *RelyingParty) BeginRegistration(
	ctx context.Context,
	user User,
	opts ...RegistrationOption,
) (*protocol.CredentialCreation, error) {
	ctx, op := rp.o11y.Begin(ctx)
	defer op.End()
	defer op.Time(ctx, nil, rp.instruments.Latency)()

	rp.instruments.Attempt(ctx)

	if user == nil {
		return nil, rp.failed(ctx, op.Error(ErrNilUser, "beginning webauthn registration"))
	}

	creation, session, err := rp.webauthn.BeginRegistration(user, opts...)
	if err != nil {
		return nil, rp.failed(ctx, op.Error(err, "beginning webauthn registration"))
	}

	if err = rp.save(ctx, op, session); err != nil {
		return nil, err
	}

	return creation, nil
}

// FinishRegistration verifies an attestation response carried by an HTTP
// request and returns the credential to store against the user.
//
// The credential is returned rather than stored: where a passkey lives, and
// what else it is stored beside, is the application's. What this owes it is a
// credential that has been verified against a challenge this server issued,
// has not been answered before, and is still inside its ceremony window.
func (rp *RelyingParty) FinishRegistration(ctx context.Context, user User, r *http.Request) (*Credential, error) {
	ctx, op := rp.o11y.Begin(ctx)
	defer op.End()
	defer op.Time(ctx, nil, rp.instruments.Latency)()

	rp.instruments.Attempt(ctx)

	parsed, err := protocol.ParseCredentialCreationResponse(r)
	if err != nil {
		return nil, rp.failed(ctx, op.Error(err, "parsing webauthn attestation response"))
	}

	return rp.createCredential(ctx, op, user, parsed)
}

// FinishRegistrationBody is FinishRegistration for a caller that is not
// serving HTTP — a gRPC handler, a message consumer — and holds the
// attestation response as bytes:
//
//	rp.FinishRegistrationBody(ctx, user, bytes.NewReader(req.GetAttestationResponse()))
//
// It exists so that such a caller does not have to forge an *http.Request
// around its own payload to reach the verification.
func (rp *RelyingParty) FinishRegistrationBody(ctx context.Context, user User, body io.Reader) (*Credential, error) {
	ctx, op := rp.o11y.Begin(ctx)
	defer op.End()
	defer op.Time(ctx, nil, rp.instruments.Latency)()

	rp.instruments.Attempt(ctx)

	if body == nil {
		return nil, rp.failed(ctx, op.Error(ErrNilResponse, "parsing webauthn attestation response"))
	}

	parsed, err := protocol.ParseCredentialCreationResponseBody(body)
	if err != nil {
		return nil, rp.failed(ctx, op.Error(err, "parsing webauthn attestation response"))
	}

	return rp.createCredential(ctx, op, user, parsed)
}

// BeginLogin issues the assertion options for a known user, and stores the
// ceremony's state. Use BeginDiscoverableLogin when the user is not known yet.
func (rp *RelyingParty) BeginLogin(
	ctx context.Context,
	user User,
	opts ...LoginOption,
) (*protocol.CredentialAssertion, error) {
	ctx, op := rp.o11y.Begin(ctx)
	defer op.End()
	defer op.Time(ctx, nil, rp.instruments.Latency)()

	rp.instruments.Attempt(ctx)

	if user == nil {
		return nil, rp.failed(ctx, op.Error(ErrNilUser, "beginning webauthn login"))
	}

	assertion, session, err := rp.webauthn.BeginLogin(user, opts...)
	if err != nil {
		return nil, rp.failed(ctx, op.Error(err, "beginning webauthn login"))
	}

	if err = rp.save(ctx, op, session); err != nil {
		return nil, err
	}

	return assertion, nil
}

// FinishLogin verifies an assertion response carried by an HTTP request
// against a known user, and returns the credential that answered it.
//
// The returned credential carries the authenticator's sign count, which the
// application is expected to write back: a count that goes backwards is how a
// cloned authenticator announces itself, and nothing can notice that unless the
// last one was stored.
func (rp *RelyingParty) FinishLogin(ctx context.Context, user User, r *http.Request) (*Credential, error) {
	ctx, op := rp.o11y.Begin(ctx)
	defer op.End()
	defer op.Time(ctx, nil, rp.instruments.Latency)()

	rp.instruments.Attempt(ctx)

	parsed, err := protocol.ParseCredentialRequestResponse(r)
	if err != nil {
		return nil, rp.failed(ctx, op.Error(err, "parsing webauthn assertion response"))
	}

	return rp.validateLogin(ctx, op, user, parsed)
}

// FinishLoginBody is FinishLogin for a caller holding the assertion response as
// bytes rather than as an HTTP request.
func (rp *RelyingParty) FinishLoginBody(ctx context.Context, user User, body io.Reader) (*Credential, error) {
	ctx, op := rp.o11y.Begin(ctx)
	defer op.End()
	defer op.Time(ctx, nil, rp.instruments.Latency)()

	rp.instruments.Attempt(ctx)

	if body == nil {
		return nil, rp.failed(ctx, op.Error(ErrNilResponse, "parsing webauthn assertion response"))
	}

	parsed, err := protocol.ParseCredentialRequestResponseBody(body)
	if err != nil {
		return nil, rp.failed(ctx, op.Error(err, "parsing webauthn assertion response"))
	}

	return rp.validateLogin(ctx, op, user, parsed)
}

// BeginDiscoverableLogin issues the assertion options for a login where the
// user is not known yet — the passkey names them — and stores the ceremony's
// state.
//
// The ceremony state it stores carries no user handle, and FinishDiscoverable
// Login refuses a session that does. That is the library's check, and it is
// what stops a discoverable assertion being answered against a ceremony that
// was begun for somebody in particular.
func (rp *RelyingParty) BeginDiscoverableLogin(
	ctx context.Context,
	opts ...LoginOption,
) (*protocol.CredentialAssertion, error) {
	ctx, op := rp.o11y.Begin(ctx)
	defer op.End()
	defer op.Time(ctx, nil, rp.instruments.Latency)()

	rp.instruments.Attempt(ctx)

	assertion, session, err := rp.webauthn.BeginDiscoverableLogin(opts...)
	if err != nil {
		return nil, rp.failed(ctx, op.Error(err, "beginning discoverable webauthn login"))
	}

	if err = rp.save(ctx, op, session); err != nil {
		return nil, err
	}

	return assertion, nil
}

// FinishDiscoverableLogin verifies an assertion response whose user is
// identified by the credential itself, and returns that user alongside the
// credential that answered.
//
// handler resolves the raw credential ID and user handle the authenticator
// returned into a User. It is the application's, because only the application
// knows where its credentials are stored, and it is called once per ceremony.
func (rp *RelyingParty) FinishDiscoverableLogin(
	ctx context.Context,
	handler DiscoverableUserHandler,
	r *http.Request,
) (user User, credential *Credential, err error) {
	ctx, op := rp.o11y.Begin(ctx)
	defer op.End()
	defer op.Time(ctx, nil, rp.instruments.Latency)()

	rp.instruments.Attempt(ctx)

	parsed, err := protocol.ParseCredentialRequestResponse(r)
	if err != nil {
		return nil, nil, rp.failed(ctx, op.Error(err, "parsing webauthn assertion response"))
	}

	return rp.validateDiscoverableLogin(ctx, op, handler, parsed)
}

// FinishDiscoverableLoginBody is FinishDiscoverableLogin for a caller holding
// the assertion response as bytes rather than as an HTTP request.
func (rp *RelyingParty) FinishDiscoverableLoginBody(
	ctx context.Context,
	handler DiscoverableUserHandler,
	body io.Reader,
) (user User, credential *Credential, err error) {
	ctx, op := rp.o11y.Begin(ctx)
	defer op.End()
	defer op.Time(ctx, nil, rp.instruments.Latency)()

	rp.instruments.Attempt(ctx)

	if body == nil {
		return nil, nil, rp.failed(ctx, op.Error(ErrNilResponse, "parsing webauthn assertion response"))
	}

	parsed, err := protocol.ParseCredentialRequestResponseBody(body)
	if err != nil {
		return nil, nil, rp.failed(ctx, op.Error(err, "parsing webauthn assertion response"))
	}

	return rp.validateDiscoverableLogin(ctx, op, handler, parsed)
}

// createCredential is the half of FinishRegistration that both entry points
// share: consume the ceremony, then verify the attestation against it.
func (rp *RelyingParty) createCredential(
	ctx context.Context,
	op observability.Operation,
	user User,
	parsed *protocol.ParsedCredentialCreationData,
) (*Credential, error) {
	if user == nil {
		return nil, rp.failed(ctx, op.Error(ErrNilUser, "finishing webauthn registration"))
	}

	session, err := rp.consume(ctx, op, parsed.Response.CollectedClientData.Challenge)
	if err != nil {
		return nil, err
	}

	credential, err := rp.webauthn.CreateCredential(user, *session, parsed)
	if err != nil {
		return nil, rp.failed(ctx, op.Error(err, "verifying webauthn attestation"))
	}

	return credential, nil
}

// validateLogin is the half of FinishLogin that both entry points share.
func (rp *RelyingParty) validateLogin(
	ctx context.Context,
	op observability.Operation,
	user User,
	parsed *protocol.ParsedCredentialAssertionData,
) (*Credential, error) {
	if user == nil {
		return nil, rp.failed(ctx, op.Error(ErrNilUser, "finishing webauthn login"))
	}

	session, err := rp.consume(ctx, op, parsed.Response.CollectedClientData.Challenge)
	if err != nil {
		return nil, err
	}

	credential, err := rp.webauthn.ValidateLogin(user, *session, parsed)
	if err != nil {
		return nil, rp.failed(ctx, op.Error(err, "verifying webauthn assertion"))
	}

	return credential, nil
}

// validateDiscoverableLogin is the half of FinishDiscoverableLogin that both
// entry points share.
func (rp *RelyingParty) validateDiscoverableLogin(
	ctx context.Context,
	op observability.Operation,
	handler DiscoverableUserHandler,
	parsed *protocol.ParsedCredentialAssertionData,
) (user User, credential *Credential, err error) {
	if handler == nil {
		return nil, nil, rp.failed(ctx, op.Error(ErrNilHandler, "finishing discoverable webauthn login"))
	}

	session, err := rp.consume(ctx, op, parsed.Response.CollectedClientData.Challenge)
	if err != nil {
		return nil, nil, err
	}

	if user, credential, err = rp.webauthn.ValidatePasskeyLogin(handler, *session, parsed); err != nil {
		return nil, nil, rp.failed(ctx, op.Error(err, "verifying discoverable webauthn assertion"))
	}

	return user, credential, nil
}

// save stores a ceremony's state for as long as the ceremony has left to run.
func (rp *RelyingParty) save(ctx context.Context, op observability.Operation, session *SessionData) error {
	op.Set(challengeKey, session.Challenge)

	if err := rp.store.Save(ctx, session, rp.ttl(session)); err != nil {
		return rp.failed(ctx, op.Error(err, "storing webauthn ceremony session"))
	}

	return nil
}

// consume reads a ceremony's state back and removes it, so that the challenge
// cannot be answered twice.
//
// The failure here is the ordinary one — an expired ceremony, a challenge this
// server never issued, a response replayed a second time — and it is recorded
// rather than merely returned, because those three are indistinguishable to the
// caller by design and only the log says which happened.
func (rp *RelyingParty) consume(
	ctx context.Context,
	op observability.Operation,
	challenge string,
) (*SessionData, error) {
	op.Set(challengeKey, challenge)

	session, err := rp.store.Consume(ctx, challenge)
	if err != nil {
		return nil, rp.failed(ctx, op.Error(err, "consuming webauthn ceremony session"))
	}

	return session, nil
}

// ttl is how long a ceremony's state has to outlive the request that issued it.
//
// It is taken from the deadline the library stamped rather than from the
// configured timeout, so that a per-ceremony option that shortens or lengthens
// the ceremony moves its stored state with it. The configured timeout is the
// fallback for a session with no deadline, which is what a caller building this
// package's SessionStore into their own go-webauthn configuration can produce.
//
// A deadline that has exactly arrived is no deadline: a ceremony stored for
// zero is a ceremony every store refuses, so the boundary falls on the side of
// the configured timeout. The now it is measured against comes from the clock
// rather than from time.Now, which is what lets a test sit on that boundary
// instead of near it.
func (rp *RelyingParty) ttl(session *SessionData) time.Duration {
	if remaining := session.Expires.Sub(rp.clock.Now()); remaining > 0 {
		return remaining
	}

	return rp.ceremonyTimeout
}

// failed counts a failed ceremony step and hands the error back unchanged.
//
// It takes the already-recorded error rather than the description, so that the
// description stays a literal at the call site: a helper that formatted it
// would be a printf wrapper whose format string is never constant, which is a
// vet failure and, more to the point, one more indirection between a log line
// and the code that emitted it.
//
// Errors is a subset of Requests rather than a series beside it, so this counts
// only the failure; the attempt was counted when the step began.
func (rp *RelyingParty) failed(ctx context.Context, err error) error {
	rp.instruments.Failed(ctx)

	return err
}
