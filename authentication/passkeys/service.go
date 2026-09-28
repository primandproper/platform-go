package passkeys

import (
	"bytes"
	"context"
	"fmt"

	"github.com/primandproper/primitives-go/v2/authentication/webauthn"
	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/identifiers"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	"github.com/primandproper/primitives-go/v2/observability/tracing"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/go-webauthn/webauthn/protocol"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// serviceLayerName scopes the service's spans, logger and instruments.
const serviceLayerName = serviceName + "_service"

// EnrollmentGate decides whether the user a registration is for may add a
// passkey right now, and answers a non-nil error to refuse.
//
// It is the re-authentication seam. Adding a passkey is adding a way into an
// account, and a live session proves only that somebody signed in once — the
// laptop left unlocked in between is exactly who a gate is for, and it is why
// authentication/signin's UpdatePassword asks for the current password again.
// Only the consumer's sessions know when the person last proved who they are,
// so the answer is theirs: a gate typically refuses unless the session was
// authenticated within the last few minutes, or admits a user who holds no
// credential at all yet because they have just registered.
//
// Its error is returned from the registration unchanged, so a consumer's own
// "re-authenticate first" sentinel reaches its transport as itself.
type EnrollmentGate func(ctx context.Context, scope tenancy.Scope, userID string) error

// AdmitEveryEnrollment is the gate for a deployment that has decided a live
// session is proof enough to add a passkey. It is a named choice rather than a
// default — see ErrNoEnrollmentGate.
func AdmitEveryEnrollment(context.Context, tenancy.Scope, string) error { return nil }

// UsernameResolver answers which WebAuthn user handle a username names, for a
// named login.
//
// It is the second of the two questions this package cannot answer for itself
// — [UserResolver] is the first, the other direction — and it is a function for
// the same reason: this package does not import identity. A username that names
// nobody answers [ErrUnknownUsername], wrapped or not; any other error is the
// deployment being unwell and fails the ceremony as itself.
type UsernameResolver func(ctx context.Context, scope tenancy.Scope, username string) (handle []byte, err error)

// AlternativeSignIn answers whether a user can sign in some way other than
// with a passkey — whether they hold a password, in most deployments. It is
// handed the archive's own transaction, so a consumer reading its credentials
// table reads it where the archive is being decided.
type AlternativeSignIn func(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, userID string) (bool, error)

// Service runs the passkey ceremonies over a [Store], a relying party and a
// [UserSource]: registration, the named and the discoverable login, and the
// list and archive a settings page offers.
//
// # What it adds over the pieces
//
// Every piece of a passkey flow was already somewhere — the protocol in
// primitives-go's authentication/webauthn, the ceremony in flight in
// authentication/webauthnsessions, the credential in [Store] — and what nothing
// did was run them in the order a flow requires. So every consumer wrote the
// same three hundred lines, and got the same four things wrong: a login that
// answered an unknown username with an error, an enrollment a live session was
// enough for, an archive that left somebody with no way in, and nothing
// recorded about any of it. This type is those lines, with those four decided.
//
// # What it does not do
//
// Mint anything. A finished login answers with the credential that proved
// somebody, and a session or a token for them is the caller's to issue —
// authentication/signin's IssueForPrincipal is the door for that.
//
// # Transactions
//
// A write takes the caller's database.Tx and a read takes an executor, which is
// this module's store convention, with one exception that is the protocol's:
// a finished login writes the sign count back in a transaction of its own,
// which commits before the call returns. See [Service.FinishLogin].
type Service struct {
	client      database.Client
	store       Store
	rp          *webauthn.RelyingParty
	users       *UserSource
	hooks       Hooks
	gate        EnrollmentGate
	usernames   UsernameResolver
	alternative AlternativeSignIn
	o11y        observability.Observer

	instruments *metrics.OperationSet

	// What the options wrote, kept only until the observer is built from it.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	unguarded bool
}

// NewService builds the ceremony service.
//
// The client is kept for the one transaction the service opens itself and for
// the clock a login is recorded against. The relying party runs the protocol
// and keeps the ceremony state between the two requests of each ceremony;
// authentication/webauthnsessions/config builds one. The user source is the
// consumer's answer to who a handle names.
//
// An enrollment gate is required — [WithEnrollmentGate], or
// [AdmitEveryEnrollment] by name — and its absence is ErrNoEnrollmentGate. The
// last-credential guard is on unless [WithoutLastCredentialGuard] says
// otherwise. Hooks default to [NoopHooks], and observability to nothing.
func NewService(
	client database.Client,
	store Store,
	rp *webauthn.RelyingParty,
	users *UserSource,
	opts ...ServiceOption,
) (*Service, error) {
	switch {
	case client == nil:
		return nil, ErrNilDatabaseClient
	case store == nil:
		return nil, ErrNilStore
	case rp == nil:
		return nil, ErrNilRelyingParty
	case users == nil:
		return nil, ErrNilUserSource
	}

	s := &Service{
		client: client,
		store:  store,
		rp:     rp,
		users:  users,
		hooks:  NoopHooks{},
	}

	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}

	if s.gate == nil {
		return nil, ErrNoEnrollmentGate
	}

	instruments, err := metrics.NewOperationSet(s.metricsProvider, serviceLayerName)
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating passkey service instruments")
	}

	s.instruments = instruments
	s.o11y = observability.NewObserver(serviceLayerName, s.logger, s.tracerProvider)

	return s, nil
}

// begin opens one operation's span and counts its attempt, and hands back the
// function that records its latency, counts a failure and closes the span.
func (s *Service) begin(
	ctx context.Context,
	name string,
	opts ...observability.BeginOption,
) (context.Context, observability.Operation, func(error)) {
	ctx, op := s.o11y.Begin(ctx, opts...)
	attr := metric.WithAttributes(attribute.String(serviceName+".operation", name))

	s.instruments.Attempt(ctx, attr)
	stop := op.Time(ctx, nil, s.instruments.Latency, attr)

	return ctx, op, func(err error) {
		if err != nil {
			s.instruments.Failed(ctx, attr)
		}

		stop()
		op.End()
	}
}

// BeginRegistration issues the options a browser needs to create a passkey
// for the user behind handle.
//
// The handle is the WebAuthn user handle the consumer assigns that user — the
// value its [UserResolver] maps back — and the transport serving a signed-in
// user derives it from the session rather than from the request. The
// [EnrollmentGate] runs here, so a refused enrollment is refused before the
// browser asks anybody to touch a key.
//
// Every passkey this service registers is discoverable: the options require a
// resident credential, which is what lets the named login omit the credential
// list an unknown username could otherwise be told apart by — see
// [Service.BeginLogin]. The user's live passkeys are excluded, so an
// authenticator already enrolled declines rather than registering twice.
func (s *Service) BeginRegistration(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	handle []byte,
) (creation *protocol.CredentialCreation, err error) {
	ctx, op, done := s.begin(ctx, "begin_registration", observability.WithValue(scopeKey, scope.String()))
	defer func() { done(err) }()

	u, err := s.enrollee(ctx, q, scope, handle)
	if err != nil {
		return nil, op.Error(err, "beginning passkey registration")
	}

	op.Set(userKey, u.identity.UserID)

	if creation, err = s.rp.BeginRegistration(ctx, u, discoverableRegistration(u.credentials)); err != nil {
		return nil, op.Error(err, "beginning passkey registration")
	}

	return creation, nil
}

// FinishRegistration verifies the attestation a browser returned and stores the
// passkey it produced, on the caller's transaction, under friendlyName.
//
// The [EnrollmentGate] runs again. A gate's answer can change in the minute a
// ceremony takes — a re-authentication window closing is the ordinary case —
// and this is the write the gate exists to guard.
//
// [Hooks.AfterRegisterPasskey] runs on tx after the write. Its error is
// returned, and returning it out of the transaction callback is what rolls the
// registration back; a caller that swallows it commits the passkey without its
// record.
func (s *Service) FinishRegistration(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	handle []byte,
	friendlyName string,
	response []byte,
) (registered *Credential, err error) {
	ctx, op, done := s.begin(ctx, "finish_registration", observability.WithValue(scopeKey, scope.String()))
	defer func() { done(err) }()

	if tx == nil {
		return nil, op.Error(ErrNilExecutor, "finishing passkey registration")
	}

	if len(response) == 0 {
		return nil, op.Error(ErrEmptyCeremonyResponse, "finishing passkey registration")
	}

	u, err := s.enrollee(ctx, tx, scope, handle)
	if err != nil {
		return nil, op.Error(err, "finishing passkey registration")
	}

	op.Set(userKey, u.identity.UserID)

	proven, err := s.rp.FinishRegistrationBody(ctx, u, bytes.NewReader(response))
	if err != nil {
		return nil, op.Error(err, "verifying passkey attestation")
	}

	transports := make([]string, 0, len(proven.Transport))
	for _, t := range proven.Transport {
		transports = append(transports, string(t))
	}

	registered, err = s.store.CreateCredential(ctx, tx, scope, &Credential{
		ID:            identifiers.New(),
		BelongsToUser: u.identity.UserID,
		FriendlyName:  friendlyName,
		Transports:    transports,
		CredentialID:  proven.ID,
		PublicKey:     proven.PublicKey,
		SignCount:     proven.Authenticator.SignCount,
	})
	if err != nil {
		return nil, op.Error(err, "storing registered passkey")
	}

	op.Set(rowIDKey, registered.ID)

	if err = s.hooks.AfterRegisterPasskey(ctx, tx, scope, registered); err != nil {
		return nil, op.Error(err, "running passkey registration hook")
	}

	return registered, nil
}

// enrollee resolves the user a registration is for, holds them to the
// enrollment gate, and assembles them with their live passkeys.
func (s *Service) enrollee(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	handle []byte,
) (*user, error) {
	if q == nil {
		return nil, ErrNilExecutor
	}

	resolved, err := s.users.User(ctx, q, scope, handle, AuthenticatorFlags{})
	if err != nil {
		return nil, err
	}

	u, ok := resolved.(*user)
	if !ok {
		return nil, platformerrors.Newf("passkey user source answered a %T", resolved)
	}

	if err = s.gate(ctx, scope, u.identity.UserID); err != nil {
		return nil, err
	}

	return u, nil
}

// discoverableRegistration requires a resident credential and excludes the
// ones the user already holds.
//
// It is spelled against the protocol's options struct rather than through
// go-webauthn's option constructors, which webauthn.RegistrationOption is an
// alias of: the struct is the specification's, and naming the library's
// package for two assignments would import it a second time to say less.
func discoverableRegistration(existing []webauthn.Credential) webauthn.RegistrationOption {
	exclusions := make([]protocol.CredentialDescriptor, 0, len(existing))
	for i := range existing {
		exclusions = append(exclusions, existing[i].Descriptor())
	}

	return func(o *protocol.PublicKeyCredentialCreationOptions) {
		o.AuthenticatorSelection.ResidentKey = protocol.ResidentKeyRequirementRequired
		o.AuthenticatorSelection.RequireResidentKey = protocol.ResidentKeyRequired()
		o.CredentialExcludeList = exclusions
	}
}

// omitAllowedCredentials strips the credential list from a named login's
// options. See Service.BeginLogin for why.
func omitAllowedCredentials(o *protocol.PublicKeyCredentialRequestOptions) {
	o.AllowedCredentials = nil
}

// BeginLogin issues the options a browser needs to sign in as username.
//
// # An unknown username is answered, not refused
//
// A username that names nobody — and one whose owner has no live passkey —
// gets discoverable options rather than an error, which is the posture
// authentication/passwordreset takes towards an address nobody holds. An error
// here would be a username-existence oracle built out of the login page.
//
// Answering is not enough by itself, because the options a known user gets
// would ordinarily list their credentials and the ones an unknown username gets
// could not. So no named login lists them: the ceremony is bound to the user's
// handle, the authenticator offers whatever passkeys it holds for this relying
// party, and [Service.FinishLogin] refuses one that answers for somebody else.
// That is what makes the two answers the same shape on the wire, and it is why
// [Service.BeginRegistration] registers discoverable credentials only: a
// passkey the authenticator cannot find without being told its ID is one a
// named login can no longer reach. A passkey registered some other way,
// without a resident key, signs in through neither login here.
//
// An empty username is ErrEmptyUsername: that is the calling code being wrong,
// not a guess about who exists.
func (s *Service) BeginLogin(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	username string,
) (assertion *protocol.CredentialAssertion, err error) {
	ctx, op, done := s.begin(ctx, "begin_login", observability.WithValue(scopeKey, scope.String()))
	defer func() { done(err) }()

	u, err := s.namedUser(ctx, q, scope, username, AuthenticatorFlags{})
	if err != nil && !platformerrors.Is(err, ErrUnknownUsername) {
		return nil, op.Error(err, "beginning passkey login")
	}

	if err != nil || len(u.credentials) == 0 {
		if assertion, err = s.rp.BeginDiscoverableLogin(ctx); err != nil {
			return nil, op.Error(err, "beginning passkey login")
		}

		return assertion, nil
	}

	if assertion, err = s.rp.BeginLogin(ctx, u, omitAllowedCredentials); err != nil {
		return nil, op.Error(err, "beginning passkey login")
	}

	return assertion, nil
}

// namedUser resolves a username to the user a named login is for. A username
// that names nobody is ErrUnknownUsername, whatever the resolver wrapped it in;
// any other failure is the deployment's and is returned as itself.
func (s *Service) namedUser(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	username string,
	flags AuthenticatorFlags,
) (*user, error) {
	if s.usernames == nil {
		return nil, ErrNoUsernameResolver
	}

	if q == nil {
		return nil, ErrNilExecutor
	}

	if username == "" {
		return nil, ErrEmptyUsername
	}

	handle, err := s.usernames(ctx, scope, username)
	if err != nil {
		if platformerrors.Is(err, ErrUnknownUsername) {
			return nil, ErrUnknownUsername
		}

		return nil, err
	}

	resolved, err := s.users.User(ctx, q, scope, handle, flags)
	if err != nil {
		return nil, err
	}

	u, ok := resolved.(*user)
	if !ok {
		return nil, platformerrors.Newf("passkey user source answered a %T", resolved)
	}

	return u, nil
}

// BeginDiscoverableLogin issues the options for a login where nobody has said
// who they are yet: the passkey the person chooses names them.
func (s *Service) BeginDiscoverableLogin(ctx context.Context) (assertion *protocol.CredentialAssertion, err error) {
	ctx, op, done := s.begin(ctx, "begin_discoverable_login")
	defer func() { done(err) }()

	if assertion, err = s.rp.BeginDiscoverableLogin(ctx); err != nil {
		return nil, op.Error(err, "beginning discoverable passkey login")
	}

	return assertion, nil
}

// FinishLogin verifies the assertion a browser returned for a named login and
// answers with the credential that proved the user — BelongsToUser is who
// they are, ID is the row — as [Store.RecordUse] left it.
//
// Every refusal wraps ErrLoginFailed, and a username that names nobody is
// refused the same way a signature that does not verify is, so the answer
// tells a caller nothing about which usernames exist. Each one runs
// [Hooks.AfterFailedPasskeyLogin]; see it for what happens when that fails.
//
// # The sign count commits here
//
// The sign count is written back in a transaction this method opens, and it
// has committed by the time the method returns. That is deliberate rather than
// a gap in the store convention: the count is the component's own protocol,
// and the caller's next step — minting a session or a token in a transaction
// of its own — must not be able to roll it back. A login whose bookkeeping
// failed is a login that did not happen, so a failed write is this method's
// error. The opposite order is harmless: a count that advanced for a login the
// caller then failed to mint a token for is a count the authenticator had
// already advanced, and the next assertion compares against the truth.
//
// An assertion whose counter did not advance past the stored one is refused
// with ErrSignCountRegressed and leaves the stored count alone.
func (s *Service) FinishLogin(
	ctx context.Context,
	scope tenancy.Scope,
	username string,
	response []byte,
) (proven *Credential, err error) {
	ctx, op, done := s.begin(ctx, "finish_login", observability.WithValue(scopeKey, scope.String()))
	defer func() { done(err) }()

	parsed, err := parseAssertion(response)
	if err != nil {
		return nil, op.Error(err, "finishing passkey login")
	}

	u, err := s.namedUser(ctx, s.client.Reader(), scope, username, parsed.flags)
	if platformerrors.Is(err, ErrUnknownUsername) {
		return nil, op.Error(s.refuse(ctx, scope, parsed, "", err), "finishing passkey login")
	}

	if err != nil {
		return nil, op.Error(err, "finishing passkey login")
	}

	op.Set(userKey, u.identity.UserID)

	credential, err := s.rp.FinishLoginBody(ctx, u, bytes.NewReader(response))
	if err != nil {
		return nil, op.Error(s.refuse(ctx, scope, parsed, u.identity.UserID, err), "finishing passkey login")
	}

	if proven, err = s.recordUse(ctx, scope, parsed, u.identity.UserID, credential); err != nil {
		return nil, op.Error(err, "finishing passkey login")
	}

	return proven, nil
}

// FinishDiscoverableLogin verifies the assertion a browser returned for a
// discoverable login, and answers the way [Service.FinishLogin] does: with the
// credential that proved somebody, after the sign count has committed.
//
// The user is whoever the handle the authenticator returned resolves to
// through the [UserSource]. Refusals, the failed-login hook and the sign-count
// write are all FinishLogin's.
func (s *Service) FinishDiscoverableLogin(
	ctx context.Context,
	scope tenancy.Scope,
	response []byte,
) (proven *Credential, err error) {
	ctx, op, done := s.begin(ctx, "finish_discoverable_login", observability.WithValue(scopeKey, scope.String()))
	defer func() { done(err) }()

	parsed, err := parseAssertion(response)
	if err != nil {
		return nil, op.Error(err, "finishing discoverable passkey login")
	}

	// The handler records who it resolved, so a refusal after the lookup can
	// still tell the failed-login hook whose account was tried.
	var claimed string

	lookup := s.users.DiscoverableUserHandler(ctx, s.client.Reader(), scope, parsed.flags)
	handler := func(rawID, handle []byte) (webauthn.User, error) {
		resolved, lookupErr := lookup(rawID, handle)
		if u, ok := resolved.(*user); ok {
			claimed = u.identity.UserID
		}

		return resolved, lookupErr
	}

	_, credential, err := s.rp.FinishDiscoverableLoginBody(ctx, handler, bytes.NewReader(response))
	if err != nil {
		return nil, op.Error(s.refuse(ctx, scope, parsed, claimed, err), "finishing discoverable passkey login")
	}

	op.Set(userKey, claimed)

	if proven, err = s.recordUse(ctx, scope, parsed, claimed, credential); err != nil {
		return nil, op.Error(err, "finishing discoverable passkey login")
	}

	return proven, nil
}

// parsedAssertion is what a login reads off the response before the relying
// party verifies it: the flags the stored credentials must be rendered under,
// and the credential ID a refusal is reported against.
type parsedAssertion struct {
	credentialID []byte
	flags        AuthenticatorFlags
}

// parseAssertion reads the response once ahead of the relying party, which
// parses it again to verify it. The flags cannot come from anywhere else — see
// AuthenticatorFlags — and they are needed before verification starts.
func parseAssertion(response []byte) (*parsedAssertion, error) {
	if len(response) == 0 {
		return nil, ErrEmptyCeremonyResponse
	}

	parsed, err := protocol.ParseCredentialRequestResponseBytes(response)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrLoginFailed, err)
	}

	flags := parsed.Response.AuthenticatorData.Flags

	return &parsedAssertion{
		credentialID: parsed.RawID,
		flags: AuthenticatorFlags{
			BackupEligible: flags.HasBackupEligible(),
			BackupState:    flags.HasBackupState(),
		},
	}, nil
}

// refuse runs the failed-login hook on a transaction of its own and answers
// with the error the login returns: the refusal wrapped in ErrLoginFailed, or
// the hook's own failure in its place.
func (s *Service) refuse(
	ctx context.Context,
	scope tenancy.Scope,
	parsed *parsedAssertion,
	userID string,
	cause error,
) error {
	refusal := fmt.Errorf("%w: %w", ErrLoginFailed, cause)

	attempt := &FailedLogin{
		Cause:        refusal,
		UserID:       userID,
		CredentialID: parsed.credentialID,
	}

	if err := s.client.WithTransaction(ctx, func(tx database.Tx) error {
		return s.hooks.AfterFailedPasskeyLogin(ctx, tx, scope, attempt)
	}); err != nil {
		return platformerrors.Wrap(err, "recording a failed passkey login")
	}

	return refusal
}

// recordUse writes a verified credential's sign count back in a transaction
// of its own, after refusing a counter that did not advance.
func (s *Service) recordUse(
	ctx context.Context,
	scope tenancy.Scope,
	parsed *parsedAssertion,
	userID string,
	verified *webauthn.Credential,
) (*Credential, error) {
	if verified.Authenticator.CloneWarning {
		return nil, s.refuse(ctx, scope, parsed, userID, ErrSignCountRegressed)
	}

	var proven *Credential

	if err := s.client.WithTransaction(ctx, func(tx database.Tx) error {
		stored, err := s.store.GetCredentialByCredentialID(ctx, tx, scope, verified.ID)
		if err != nil {
			return err
		}

		proven, err = s.store.RecordUse(ctx, tx, scope, stored.ID, verified.Authenticator.SignCount, s.client.CurrentTime())

		return err
	}); err != nil {
		return nil, platformerrors.Wrap(err, "recording passkey use")
	}

	return proven, nil
}

// ListCredentials reads a user's live passkeys, oldest first, for a settings
// page to render.
func (s *Service) ListCredentials(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	userID string,
) (credentials []*Credential, err error) {
	ctx, op, done := s.begin(ctx, "list_credentials",
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(userKey, userID),
	)
	defer func() { done(err) }()

	if credentials, err = s.store.GetCredentialsForUser(ctx, q, scope, userID); err != nil {
		return nil, op.Error(err, "listing passkeys")
	}

	return credentials, nil
}

// ArchiveCredential revokes one of a user's passkeys on the caller's
// transaction and answers with the row as the archive left it.
//
// The owner is part of the store's statement, so a passkey belonging to
// somebody else is ErrCredentialNotFound, exactly as one that does not exist is.
//
// # The last-credential guard
//
// A user whose only live passkey this is, and who has no other way in, is
// refused with ErrLastCredential rather than locked out of their own account —
// the person who registered with no password and signs in with nothing but
// this key. Whether they have another way in is [WithAlternativeSignIn]'s to
// answer; a service built without one assumes they have none, which refuses
// some archives a password would have made safe and never one that locks
// somebody out. [WithoutLastCredentialGuard] turns the guard off by name.
//
// The count is read before the archive and on the same transaction. Two
// archives of a user's last two passkeys racing on separate transactions can
// each see the other's row still live; the guard is for the person in front of
// a settings page, not a second writer.
//
// [Hooks.AfterArchivePasskey] runs on tx after the write, and a refusal from
// it rolls the archive back when it is returned out of the transaction
// callback.
func (s *Service) ArchiveCredential(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	credentialRowID, userID string,
) (archived *Credential, err error) {
	ctx, op, done := s.begin(ctx, "archive_credential",
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(rowIDKey, credentialRowID),
		observability.WithValue(userKey, userID),
	)
	defer func() { done(err) }()

	if tx == nil {
		return nil, op.Error(ErrNilExecutor, "archiving passkey")
	}

	if err = s.guardLastCredential(ctx, tx, scope, credentialRowID, userID); err != nil {
		return nil, op.Error(err, "archiving passkey")
	}

	if archived, err = s.store.ArchiveCredentialForUser(ctx, tx, scope, credentialRowID, userID); err != nil {
		return nil, op.Error(err, "archiving passkey")
	}

	if err = s.hooks.AfterArchivePasskey(ctx, tx, scope, archived); err != nil {
		return nil, op.Error(err, "running passkey archive hook")
	}

	return archived, nil
}

// guardLastCredential refuses an archive that would leave the user with no
// live passkey and no other way in. A row that is not among the user's live
// ones is left for the archive itself to report as not found.
func (s *Service) guardLastCredential(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	credentialRowID, userID string,
) error {
	if s.unguarded {
		return nil
	}

	live, err := s.store.GetCredentialsForUser(ctx, tx, scope, userID)
	if err != nil {
		return err
	}

	if len(live) != 1 || live[0].ID != credentialRowID {
		return nil
	}

	if s.alternative != nil {
		hasAlternative, altErr := s.alternative(ctx, tx, scope, userID)
		if altErr != nil {
			return platformerrors.Wrap(altErr, "checking for another way to sign in")
		}

		if hasAlternative {
			return nil
		}
	}

	return ErrLastCredential
}
