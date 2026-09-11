package signin

import (
	"context"
	"time"

	"github.com/primandproper/platform-go/v14/identity"

	"github.com/primandproper/primitives-go/v2/authentication"
	"github.com/primandproper/primitives-go/v2/authentication/totp"
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

// serviceName scopes this package's spans, logger and instruments.
const serviceName = "signin_service"

// The keys this package attaches to spans and log lines. They match the ones
// identity's layers use, so a trace that crosses from a sign-in into the
// directory does not carry the same fact under two names.
const (
	scopeKey     = "identity.scope"
	userIDKey    = "identity.user_id"
	accountIDKey = "identity.account_id"

	// reasonKey records why a sign-in was refused. It is on the span and the log
	// line and nowhere else: the caller is told one sentinel for four refusals,
	// and this is where the difference survives.
	reasonKey = "signin.reason"

	// adminKey records which door an attempt came through.
	adminKey = "signin.administrative"
)

// The names this service labels its instruments with, one per operation. They
// are constants rather than literals at the call sites because a misspelled one
// is a second time series nobody notices until a dashboard is missing half its
// traffic.
//
// Authenticating and logging in are separate series deliberately, even though
// they share every step but the last. A dashboard that could not separate the
// door that mints a token from the door that does not would hide exactly the
// traffic the second door was added for.
const (
	opLogin             = "login_for_token"
	opAdminLogin        = "admin_login_for_token"
	opAuthenticate      = "authenticate"
	opAdminAuthenticate = "admin_authenticate"
	opGetAuthStatus     = "get_auth_status"
	opGetSelf           = "get_self"
	opUpdatePassword    = "update_password"
	//nolint:gosec // G101: these are instrument labels naming two operations, not credentials.
	opRefreshTOTPSecret = "refresh_totp_secret"
	//nolint:gosec // G101: as above.
	opVerifyTOTPSecret = "verify_totp_secret"
)

// Directory is what a sign-in needs from identity, and nothing else.
//
// It is the two handle lookups a sign-in form submits, the principal read every
// authenticated request afterwards makes, the one read that returns a user's
// credentials, and the three credential writes. identity.Store satisfies it, so
// a consumer passes theirs; a consumer whose directory is not that schema
// implements these nine methods.
//
// It is narrow deliberately, and for the reason identity.SignInReader is: this
// is the interface the component holding everybody's passwords depends on, and
// one that could also archive a user or read the whole directory is one that
// could be made to.
type Directory interface {
	identity.SignInReader

	// GetUser reads one of the scope's live users, credentials included. It is
	// the unredacted read, which is what a password comparison and a
	// second-factor check need and what identity.SignInReader's principal read
	// deliberately does not return.
	GetUser(
		ctx context.Context,
		q database.SQLQueryExecutor,
		scope tenancy.Scope,
		userID string,
	) (*identity.User, error)

	// UpdateUserPassword replaces the stored hash, stamps
	// PasswordLastChangedAt, and clears RequiresPasswordChange.
	UpdateUserPassword(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		userID, hashedPassword string,
	) error

	// UpdateUserTwoFactorSecret stores a new TOTP secret and marks it
	// unverified.
	UpdateUserTwoFactorSecret(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		userID, secret string,
	) error

	// MarkUserTwoFactorSecretVerified records that the user proved possession of
	// the secret they hold, and answers with the user it moved.
	//
	// The stamp on that row is the one the write made, which is what this
	// service hands AfterVerifyTOTPSecret: a consumer recording who proved a
	// second factor and when reads it off the row the write returned rather than
	// off the copy read before it, which carried no stamp at all.
	MarkUserTwoFactorSecretVerified(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		userID string,
	) (*identity.User, error)
}

// Credentials is what a sign-in form submits.
//
// Exactly one of Username and EmailAddress names the user: both is
// ErrAmbiguousHandle and neither is ErrEmptyHandle, because a client sending
// both has a bug and picking one for them makes it a bug that signs somebody
// in.
type Credentials struct {
	_ struct{} `json:"-"`

	// Username is the handle the user signs in with.
	Username string `json:"username"`

	// EmailAddress is the address they signed up with, as an alternative handle.
	EmailAddress string `json:"emailAddress"`

	// Password is the plaintext password, which is compared against the stored
	// hash and never stored, logged, traced or handed to a hook.
	Password string `json:"-"`

	// TOTPCode is the second-factor code, when the user holds a proven second
	// factor. It is required from those users and ignored for everybody else.
	TOTPCode string `json:"-"`

	// ActiveAccountID is the account the token should be issued for. Empty means
	// the user's default account, and an account the user is not a live member
	// of is refused by the directory rather than honored.
	ActiveAccountID string `json:"activeAccountID"`
}

// SignIn is a completed sign-in: the token, what it is for, and who it is for.
type SignIn struct {
	_ struct{} `json:"-"`

	// ExpiresAt is when the token stops being accepted, as this service asked
	// for it. What the token itself claims is the issuer's, and the two agree
	// unless a consumer's issuer overrides the expiry it was handed.
	ExpiresAt time.Time `json:"expiresAt"`

	// Principal is who signed in — the user, redacted, their memberships, and
	// the account this token is against.
	//
	// It is here because this service resolved it to mint the token and
	// discarding it would make the caller read it again. What a transport puts
	// in a response is the transport's decision; this is the whole answer, so
	// that decision can be made.
	Principal *identity.Principal `json:"principal"`

	// Token is the credential itself. It is not redacted anywhere, because a
	// sign-in that hides it has accomplished nothing — which is the reason it
	// must not be logged, recorded by a hook, or put in an error message.
	Token string `json:"token"`

	// TokenID is the issuer's "jti" for this token: the handle a revocation list
	// names and the value a hook records.
	TokenID string `json:"tokenID"`

	// Administrative reports whether this token came through
	// AdminLoginForToken.
	Administrative bool `json:"administrative"`
}

// AuthStatus is where a signed-in caller stands: who they are, which account
// they are in, and what the client has to make them do before anything else.
//
// The three booleans are computed from columns a redacted user does not carry,
// which is why they are fields here rather than something a caller derives from
// User. Asking identity.User.TwoFactorEnabled of the redacted user in this
// struct answers false for everybody.
type AuthStatus struct {
	_ struct{} `json:"-"`

	// User is the caller, redacted.
	User *identity.User `json:"user"`

	// ActiveAccountID is the account this caller's requests are against.
	ActiveAccountID string `json:"activeAccountID"`

	// AccountIDs is every account they are a live member of, default first.
	AccountIDs []string `json:"accountIDs"`

	// HasPassword reports whether they hold a password credential at all. A
	// passwordless user — registered with a passkey, or federated — reports
	// false, and a client offering them a "change password" form is offering
	// them a form that cannot work.
	HasPassword bool `json:"hasPassword"`

	// TwoFactorEnrolled reports whether they hold a second factor they have
	// actually proven. A secret issued and never verified is not one.
	TwoFactorEnrolled bool `json:"twoFactorEnrolled"`

	// RequiresPasswordChange reports whether an operator has forced a password
	// change. This service still signs such a user in — the alternative is a
	// user who cannot reach the form — so it is the client's job to send them
	// to it, and this is how they are told.
	RequiresPasswordChange bool `json:"requiresPasswordChange"`

	// EmailAddressVerified reports whether their address has been proven
	// reachable.
	EmailAddressVerified bool `json:"emailAddressVerified"`
}

// PasswordUpdate is a password change by the person whose password it is.
type PasswordUpdate struct {
	_ struct{} `json:"-"`

	// CurrentPassword is the password being replaced. It is required and
	// checked: a session is not proof enough to change the credential the
	// session was obtained with.
	CurrentPassword string `json:"-"`

	// NewPassword is what replaces it. Whether it is long enough, unusual
	// enough, or unlike the last four is the consumer's rule, applied before
	// this call — this package holds no password policy and validating one here
	// would be a policy every consumer then had to work around.
	NewPassword string `json:"-"`

	// TOTPCode is the second-factor code, required from a user who holds a
	// proven second factor.
	TOTPCode string `json:"-"`
}

// SecretRefresh is a request for a new second-factor secret, by the person who
// will hold it.
type SecretRefresh struct {
	_ struct{} `json:"-"`

	// CurrentPassword is required. Issuing a new second-factor secret to
	// whoever is holding an unlocked laptop is how a second factor stops being
	// one.
	CurrentPassword string `json:"-"`

	// TOTPCode is the code from the secret being replaced, required from a user
	// who holds a proven one. A user enrolling for the first time has none and
	// sends none.
	TOTPCode string `json:"-"`
}

// Service is sign-in: the orchestration over identity's directory and this
// module's authentication engines that every application would otherwise write.
//
// It owns no table and no schema. Everything it persists it persists through
// identity's credential writes, and everything it computes it computes with
// argon2, totp and tokens. What it adds is the order the four are used in, and
// the refusals — see the package documentation for which of those are collapsed
// and why.
type Service struct {
	client        database.Client
	directory     Directory
	authenticator authentication.Authenticator
	issuer        TokenIssuer
	verifier      totp.Verifier
	generator     totp.Generator
	hooks         Hooks
	clk           clock.Clock
	o11y          observability.Observer

	// What the options wrote, kept only until the observer is built from it.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	claims ClaimsBuilder

	instruments *metrics.OperationSet

	totpIssuer string

	adminRoles []string

	tokenTTL      time.Duration
	adminTokenTTL time.Duration

	secondFactor SecondFactorPolicy
}

// TokenIssuer is the half of tokens.Issuer a sign-in uses.
//
// Only IssueToken: parsing a token back into a caller is the consumer's
// interceptor's job, and a sign-in service holding the parser is a sign-in
// service that could be asked to authenticate a request. The seam is narrowed
// here rather than in tokens, whose Issuer is one thing a consumer configures
// and passes to both halves.
type TokenIssuer interface {
	IssueToken(
		ctx context.Context,
		subject string,
		expiry time.Duration,
		extraClaims map[string]any,
	) (tokenStr, jti string, err error)
}

// NewService builds the sign-in service.
//
// The four positional dependencies are the ones it genuinely cannot build. The
// client, because a hook needs a transaction and the reads need a reader. The
// directory, because whose users these are is not this package's to decide. The
// authenticator, because which engine hashes a password is the one choice a
// sign-in service must never make on a consumer's behalf — a default here would
// be this package picking everybody's password hashing. And the issuer, because
// a token's format, signing key and audience are the consumer's.
//
// Everything else has a default, and each default is stated on the option that
// replaces it: NoopHooks, this module's own TOTP verifier and generator,
// DefaultClaims, DefaultTokenTTL, DefaultAdminTokenTTL, and
// SecondFactorWhenEnrolled. Observability is optional and defaults to nothing.
//
// Administrative sign-in is off until WithAdminServiceRoles names a role, which
// is what "whether this service has an administrative door" means.
func NewService(
	client database.Client,
	directory Directory,
	authenticator authentication.Authenticator,
	issuer TokenIssuer,
	opts ...ServiceOption,
) (*Service, error) {
	if client == nil {
		return nil, ErrNilDatabaseClient
	}

	if directory == nil {
		return nil, ErrNilDirectory
	}

	if authenticator == nil {
		return nil, ErrNilAuthenticator
	}

	if issuer == nil {
		return nil, ErrNilTokenIssuer
	}

	s := &Service{
		client:        client,
		directory:     directory,
		authenticator: authenticator,
		issuer:        issuer,
		verifier:      totp.NewVerifier(),
		generator:     totp.NewGenerator(),
		hooks:         NoopHooks{},
		clk:           clock.NewClock(),
		claims:        DefaultClaims,
		tokenTTL:      DefaultTokenTTL,
		adminTokenTTL: DefaultAdminTokenTTL,
		secondFactor:  SecondFactorWhenEnrolled,
	}

	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}

	s.o11y = observability.NewObserver(serviceName, s.logger, s.tracerProvider)

	instruments, err := metrics.NewOperationSet(s.metricsProvider, serviceName)
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating sign-in service instruments")
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

	attr := operationAttr(name)
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

// operationAttr labels an instrument with the operation it was recorded in.
func operationAttr(name string) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String("signin.operation", name))
}
