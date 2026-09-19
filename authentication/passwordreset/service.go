package passwordreset

import (
	"context"
	stderrors "errors"
	"time"

	"github.com/primandproper/platform-go/v14/identity"

	"github.com/primandproper/primitives-go/v2/authentication"
	"github.com/primandproper/primitives-go/v2/clock"
	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	"github.com/primandproper/primitives-go/v2/observability/tracing"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// flowName scopes the spans, logger and instruments the Service emits. It is
// separate from the store's so that a dashboard can tell "somebody asked for a
// reset" from "a row was written", which are the same event only when the flow
// is working.
const flowName = "password_reset_service"

// The names the flow labels its instruments with, one per operation. They are
// constants rather than literals at the call sites because a misspelled one is
// a second time series nobody notices until a dashboard is missing half its
// traffic.
const (
	opRequest  = "request_password_reset"
	opVerify   = "verify_password_reset"
	opComplete = "complete_password_reset"
)

// padKey records whether a reset request was held to its floor. It is on the
// span only and set only when it was not: a request whose context ended
// mid-pad answered faster than the floor promised, and nothing else in a trace
// would show it.
const padKey = "password_reset.padded"

// Directory is what this flow needs from identity, and nothing else.
//
// Two methods: the read that turns the address somebody typed into the user a
// link is for, and the credential write that lands the password they chose.
// identity.Store satisfies it, so a consumer passes theirs; a consumer whose
// directory is not that schema implements these two.
//
// It is narrow for the reason authentication/signin.Directory is narrow. This
// is the seam held by a component that answers unauthenticated requests naming
// other people's email addresses, and one that could also read the whole
// directory or archive a user is one that could be made to.
//
// The write is [github.com/primandproper/platform-go/v14/identity.CredentialStore]'s,
// and it is reached as a store rather than through identity's Service
// deliberately. identity.Service.UpdateUserPassword opens a transaction of its
// own, which is the one thing this flow cannot afford: [Store.Consume] decides
// who may change a password, and a password write that commits separately from
// the redemption authorizing it is exactly the window Consume's documentation
// refuses to leave open. So identity's AfterUpdateUserPassword hook does not
// fire here — a consumer who wants a companion write implements this interface
// over their own store and writes it on the Tx it is handed, which is the same
// transaction the redemption and the revocation are in.
type Directory interface {
	// GetUserByEmailAddress reads a live user by the address they registered
	// with. A user nobody has is ErrUserNotFound, and the flow answers a
	// request for one with success — see Service.Request.
	GetUserByEmailAddress(
		ctx context.Context,
		q database.SQLQueryExecutor,
		scope tenancy.Scope,
		emailAddress string,
	) (*identity.User, error)

	// UpdateUserPassword replaces the stored hash, stamps
	// PasswordLastChangedAt, and clears RequiresPasswordChange — the last of
	// which is what makes a forced password change terminate when the user
	// answers it through a reset link rather than through a sign-in.
	UpdateUserPassword(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		userID, hashedPassword string,
	) error
}

// Mail is what a Mailer is handed once a reset has been issued and committed.
//
// It carries everything a link needs and nothing that has to be read back out
// of the store, because the important half cannot be: Issuance.Secret exists
// once, in this value, and no read of any table will hand it over again. A
// Mailer that drops it has produced a reset nobody can complete.
type Mail struct {
	// User is who asked, redacted — so the address to send to, the name to
	// greet, and the locale a consumer keeps on a user are all here, and the
	// password hash and the second-factor secret are not.
	User *identity.User

	// Issuance is the token that was written and the secret that goes in the
	// link. Issuance.Token.ExpiresAt is the deadline worth putting in the
	// message: a link that has silently stopped working is the single most
	// common complaint about this whole flow.
	Issuance *Issuance
}

// Mailer delivers the one message this flow sends.
//
// It is an interface rather than a fixed template because the library cannot
// write the message. It does not know the URL a link points at — that is the
// consumer's front end, and the secret has to be embedded in it somewhere only
// they can decide — nor the tone, the language, or the branding. What it does
// know is when to send, and that is what this seam supplies: after the
// transaction that wrote the row has committed, and never from inside it.
//
// An error from a Mailer fails Service.Request. That is the opposite of how
// dataprivacy treats its Notifier, and the difference is what the mail is: an
// export is finished whether or not anybody was told, while a reset link that
// was never delivered is a request that accomplished nothing. The token stays
// committed — issuing again does not invalidate what is outstanding, see
// Store.Issue — so the caller's retry costs a row rather than a broken flow.
type Mailer interface {
	SendPasswordReset(ctx context.Context, mail *Mail) error
}

// MailerFunc adapts a function to Mailer.
type MailerFunc func(ctx context.Context, mail *Mail) error

// SendPasswordReset implements Mailer.
func (f MailerFunc) SendPasswordReset(ctx context.Context, mail *Mail) error {
	return f(ctx, mail)
}

// Service is the password reset flow over this package's Store: the ~120 lines
// every consumer writes, with the two properties they get wrong.
//
// It owns no table beyond the one Store already owns, and it holds no policy.
// Whether the new password is long enough, unusual enough or unlike the last
// four is the consumer's rule, applied before Complete is called; which engine
// hashes it is the consumer's Authenticator; what the mail says is the
// consumer's Mailer. What this adds is the order those are used in, and the two
// things that order is for:
//
// The redemption, the password write and the revocation of every other
// outstanding link commit together, in one transaction. Three transactions is
// what the hand-written version has, and the direction it fails in is not a
// bookkeeping error: a password write that commits over a revocation that then
// fails leaves live reset links for an account whose password has just changed.
// See Store.Consume, which is where the property is argued at length.
//
// The mail goes after the commit returns, never from inside it. A send inside
// the transaction is a link delivered for a reset that then rolled back, and
// the store has no way to take it back.
//
// A third property is about who asks rather than who answers: Request takes the
// same time and reports the same success for an address nobody holds as for one
// somebody does. That is the account-enumeration position Store.Issue's
// documentation already states, and it belongs here rather than in each
// consumer's handler, where it is one early return away from being lost.
type Service struct {
	client        database.Client
	tokens        Store
	directory     Directory
	authenticator authentication.Authenticator
	mailer        Mailer
	clk           clock.Clock
	o11y          observability.Observer

	instruments *metrics.OperationSet

	// What the options wrote, kept only until the observer is built from it.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	lifetime     time.Duration
	requestFloor time.Duration
}

// NewService builds the reset flow.
//
// The five positional dependencies are the ones it genuinely cannot build. The
// client, because the transaction the three writes share is opened on it. The
// token store, because this package ships one implementation and a consumer
// keeping short-lived credentials elsewhere passes another. The directory,
// because whose users these are is not this package's to decide. The
// authenticator, because which engine hashes a password is the one choice a
// flow like this must never make on a consumer's behalf. And the mailer,
// because a reset that cannot be delivered is not a reset.
//
// Everything else has a default, stated on the option that replaces it:
// DefaultTokenLifetime, DefaultRequestFloor, and the system clock.
// Observability is optional and defaults to nothing.
func NewService(
	client database.Client,
	tokens Store,
	directory Directory,
	authenticator authentication.Authenticator,
	mailer Mailer,
	opts ...ServiceOption,
) (*Service, error) {
	if client == nil {
		return nil, ErrNilDatabaseClient
	}

	if tokens == nil {
		return nil, ErrNilStore
	}

	if directory == nil {
		return nil, ErrNilDirectory
	}

	if authenticator == nil {
		return nil, ErrNilAuthenticator
	}

	if mailer == nil {
		return nil, ErrNilMailer
	}

	s := &Service{
		client:        client,
		tokens:        tokens,
		directory:     directory,
		authenticator: authenticator,
		mailer:        mailer,
		clk:           clock.NewClock(),
		lifetime:      DefaultTokenLifetime,
		requestFloor:  DefaultRequestFloor,
	}

	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}

	// A lifetime of zero would be an unset configuration field reaching the
	// store one request later, where it is ErrNonPositiveLifetime for every
	// reset anybody asks for. It is the same refusal, made at wiring time.
	if s.lifetime <= 0 {
		return nil, platformerrors.Wrapf(ErrNonPositiveLifetime,
			"password reset token lifetime %s", s.lifetime)
	}

	s.o11y = observability.NewObserver(flowName, s.logger, s.tracerProvider)

	instruments, err := metrics.NewOperationSet(s.metricsProvider, flowName)
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating password reset service instruments")
	}

	s.instruments = instruments

	return s, nil
}

// begin is the shape every operation here starts with: the span, the attempt
// counter and the latency timer, in one place.
//
// The returned func is deferred by the caller and is called with the error the
// operation is returning, which is why every return below assigns err first.
func (s *Service) begin(ctx context.Context, name string, values ...observability.BeginOption) (
	context.Context, observability.Operation, func(err error),
) {
	ctx, op := s.o11y.Begin(ctx, values...)

	attr := flowAttr(name)
	s.instruments.Attempt(ctx, attr)

	stop := op.Time(ctx, nil, s.instruments.Latency, attr)

	return ctx, op, func(err error) {
		// The three outcomes a person meets are not counted as failures, for
		// the reason isTokenError gives: somebody following an expired link is
		// this flow working, and a dashboard that charted it as an error would
		// bury the driver failures that belong there under the most routine
		// event in the package.
		if err != nil && !isTokenError(err) {
			s.instruments.Failed(ctx, attr)
		}

		stop()
		op.End()
	}
}

// flowAttr labels an instrument with the operation it was recorded in.
func flowAttr(name string) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String("password_reset.operation", name))
}

// Request mints a reset link for whoever holds an address, mails it, and says
// nothing about whether anybody holds it.
//
// An address nobody has is answered with a nil error, after the same delay a
// known one takes — see [WithRequestFloor] for what that floor is and what it
// costs. The two halves matter together: a handler that returned 404 for an
// unknown address has built an account-enumeration oracle out of a feature
// meant to protect accounts, and one that returned 200 in four milliseconds
// while a known address took two hundred has built the same oracle with a
// stopwatch in front of it.
//
// What it does for an address somebody holds is three steps in the order the
// flow requires. The token is issued in a transaction of its own; the
// transaction commits; the mail goes out afterwards. A send from inside the
// callback would be a link delivered for a reset that then rolled back, and no
// retry can un-send it.
//
// The scope is the tenant the reset is for, and it is an argument rather than
// something derived from the address because an address is not unique across
// tenants — see the package documentation on tenancy. An application with one
// directory passes tenancy.Global.
//
// A Mailer's error is returned, and by then the row is committed. The caller's
// retry mints a second link rather than replacing the first, which is
// Store.Issue's stated behavior and the right one here: the user who eventually
// receives either message can spend it.
func (s *Service) Request(ctx context.Context, scope tenancy.Scope, emailAddress string) (err error) {
	ctx, op, done := s.begin(ctx, opRequest, observability.WithValue(scopeKey, scope.String()))
	defer func() { done(err) }()

	// Refused before the floor is armed, deliberately. An empty address is the
	// calling code being wrong rather than a guess about who exists, so there is
	// nothing here for a timing comparison to learn.
	if emailAddress == "" {
		return op.Error(ErrEmptyEmailAddress, "requesting a password reset")
	}

	defer s.padTo(ctx, op, s.clk.Now().Add(s.requestFloor))

	user, err := s.directory.GetUserByEmailAddress(ctx, s.client.Reader(), scope, emailAddress)
	if err != nil {
		// The one refusal this flow swallows, and the only one it may. Every
		// other error here is the deployment being unwell, which is not a fact
		// about whether an account exists.
		if stderrors.Is(err, identity.ErrUserNotFound) {
			return nil
		}

		return op.Error(err, "reading the user a password reset was asked for")
	}

	var issuance *Issuance

	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		var issueErr error
		issuance, issueErr = s.tokens.Issue(ctx, tx, scope, user.ID, s.lifetime)

		return issueErr
	}); err != nil {
		return op.Error(err, "issuing a password reset token")
	}

	observe(op, issuance.Token)

	if err = s.mailer.SendPasswordReset(ctx, &Mail{User: user.Redacted(), Issuance: issuance}); err != nil {
		return op.Error(err, "mailing a password reset link")
	}

	return nil
}

// Verify resolves a secret to its token without spending it: the page load that
// precedes the form.
//
// It answers ErrTokenNotFound, ErrTokenExpired or ErrTokenRedeemed for a link
// that cannot be spent, which are the three the package documentation argues a
// person holding a link is owed and the three errormappers gives a status to. A
// nil error means the token was live when it was read, which is weaker than
// Complete's answer: nothing here holds it live until the submit.
//
// The read runs on the write pool rather than a replica, and that is the whole
// reason this method exists rather than the consumer calling Store.Verify. A
// reset token is written by one request and read by the very next one the user
// makes — the one they made by following a link that arrived seconds later —
// and replica lag turns that into a link that is "not found" and then works when
// reloaded.
func (s *Service) Verify(ctx context.Context, scope tenancy.Scope, secret string) (token *Token, err error) {
	ctx, op, done := s.begin(ctx, opVerify, observability.WithValue(scopeKey, scope.String()))
	defer func() { done(err) }()

	if token, err = s.tokens.Verify(ctx, s.client.Writer(), scope, secret); err != nil {
		if isTokenError(err) {
			return nil, err
		}

		return nil, op.Error(err, "verifying a password reset token")
	}

	return token, nil
}

// Complete spends a link and writes the password it was issued for.
//
// The redemption, the password write and the revocation of every other link the
// user was holding are one transaction. That is the deliverable, and the order
// inside it is not the interesting part — what matters is that no two of the
// three can land without the third. A revocation that fails rolls the password
// change back with it, which is the direction the hand-written version gets
// wrong: it logs the failed revoke and reports success, leaving live reset links
// for an account whose password has just changed.
//
// Hashing happens outside the transaction, so a deliberately expensive
// Authenticator does not hold a row lock for a fraction of a second per
// redemption.
//
// Whether the new password is acceptable is the consumer's rule, applied before
// this call — this package holds no password policy, for the reason
// authentication/signin does not. The one rule it does apply is that the
// password is not empty, which is not a policy but a write that would lock the
// user out, and it is applied before the token is spent so a client that
// submitted an empty form still holds its link.
//
// What comes back is the token as it was spent, RedeemedAt set, which is what an
// audit entry or a log line names. The secret is not on it and never was.
func (s *Service) Complete(
	ctx context.Context,
	scope tenancy.Scope,
	secret, newPassword string,
) (token *Token, err error) {
	ctx, op, done := s.begin(ctx, opComplete, observability.WithValue(scopeKey, scope.String()))
	defer func() { done(err) }()

	if newPassword == "" {
		return nil, op.Error(ErrEmptyNewPassword, "completing a password reset")
	}

	hashed, err := s.authenticator.HashPassword(ctx, newPassword)
	if err != nil {
		return nil, op.Error(err, "hashing a reset password")
	}

	var spent *Token

	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		consumed, txErr := s.tokens.Consume(ctx, tx, scope, secret)
		if txErr != nil {
			return txErr
		}

		if txErr = s.directory.UpdateUserPassword(ctx, tx, scope, consumed.UserID, hashed); txErr != nil {
			return txErr
		}

		// Returned rather than logged, which is the whole point of the
		// transaction. A revoke that failed and was acknowledged leaves the
		// links that were outstanding a moment ago working against the password
		// that has just replaced the one they were issued under.
		if _, txErr = s.tokens.RevokeForUser(ctx, tx, scope, consumed.UserID); txErr != nil {
			return txErr
		}

		spent = consumed

		return nil
	}); err != nil {
		if isTokenError(err) {
			return nil, err
		}

		return nil, op.Error(err, "completing a password reset")
	}

	observe(op, spent)

	return spent, nil
}

// padTo holds the answer until deadline, so that what the flow did — or did not
// do — is not readable off how long it took.
//
// It sleeps on the Service's clock rather than on time.Sleep, so a test can
// assert the deadline both paths are held to without waiting for it.
//
// A context that ends first ends the wait, because there is nobody left to be
// told anything in constant time — and the span says so rather than the wait
// being silently short. A caller whose clients time out below the floor is a
// caller whose floor is not covering anything, which is a fact about their
// configuration and not about this request.
func (s *Service) padTo(ctx context.Context, op observability.Operation, deadline time.Time) {
	remaining := deadline.Sub(s.clk.Now())
	if remaining <= 0 {
		return
	}

	if err := s.clk.Sleep(ctx, remaining); err != nil {
		op.SpanOnly(padKey, false)
	}
}
