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
	"github.com/primandproper/primitives-go/v2/random"
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

	// padKey records whether a timing floor was held to in full. It is false only
	// where the caller's context ended first, which makes a short answer a fact
	// about that request rather than a silent hole in the enumeration defense.
	// See Service.padTo.
	padKey = "signin.padded"

	// familyKey records which continuous login an operation belongs to. It is
	// the one identifier a refresh token's row carries that is safe to trace: it
	// names a sign-in rather than a credential, which is exactly what following
	// a rotation across several requests needs.
	familyKey = "signin.family_id"

	// signOutNothingToEndKey records that a sign-out presented a token naming no
	// live login — unknown, already spent, already revoked or expired, which
	// Service.SignOut answers identically and deliberately.
	//
	// On the span only, and never as a metric. It is the ordinary shape of a
	// client signing out of a session that had already lapsed, so a counter of it
	// would alarm on users behaving normally; what it is useful for is explaining
	// a single sign-out that revoked nothing.
	signOutNothingToEndKey = "signin.sign_out_nothing_to_end"
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

	// The doors that mint for a principal another credential proved. They are a
	// series of their own rather than a kind of login, because a dashboard asking
	// how often somebody proves a password must not count a passkey as one.
	opIssueForPrincipal      = "issue_for_principal"
	opAdminIssueForPrincipal = "admin_issue_for_principal"

	// The four refresh doors. Exchanging is a series of its own rather than a
	// second kind of login, because the two answer different questions of a
	// dashboard: how often somebody proves a password, and how long their
	// sign-ins actually last.
	//
	// Signing out is a series of its own for the same reason, and is not folded
	// into revoke_refresh_token_family even though it ends in that call: what a
	// dashboard asks of a sign-out is how many people leave deliberately, and
	// counting it beside the revocations a detected reuse performs would mix a
	// person's decision with an alarm.
	opExchangeRefreshToken = "exchange_refresh_token"
	opSignOut              = "sign_out"
	opRevokeRefreshFamily  = "revoke_refresh_token_family"
	opRevokeRefreshSubject = "revoke_refresh_tokens_for_subject"
	opUpdatePassword       = "update_password"

	// Listing a person's logins and ending one of them are two series of their
	// own. Ending one is not folded into revoke_refresh_token_family for the
	// reason signing out is not: it is a person's decision, and that series is
	// where a detected reuse's alarm lands.
	opListSignIns = "list_sign_ins"
	opEndSignIn   = "end_sign_in"

	// The registration door and the three that finish one. Registering is a
	// series of its own rather than a kind of login: what a dashboard asks of it
	// is how many people arrived, which has nothing to do with how often they
	// come back.
	opRegister             = "register"
	opAttachPassword       = "attach_password"
	opVerifyEmailAddress   = "verify_email_address"
	opCompleteVerification = "complete_verification"
	// The passwordless door, both halves. It is a series of its own for the
	// reason registering is: what a dashboard asks of it is how many people
	// arrive without a password, which is a different question from how often
	// anybody signs in.
	opRequestMagicLink = "request_magic_link"
	opRedeemMagicLink  = "redeem_magic_link"
	// Withdrawing somebody's outstanding links is not part of that series: it is
	// what disabling an account and erasing a subject do, so it answers the
	// question the refresh token revocation beside it answers rather than the
	// one the two doors above do.
	opRevokeMagicLinksSubject = "revoke_magic_links_for_subject"

	//nolint:gosec // G101: these are instrument labels naming two operations, not credentials.
	opRefreshTOTPSecret = "refresh_totp_secret"
	//nolint:gosec // G101: as above.
	opVerifyTOTPSecret = "verify_totp_secret"

	// The two recovery code doors. Spending a code is not a series of its own:
	// it happens inside a sign-in or a re-enrollment, and is counted there.
	opReplaceRecoveryCodes   = "replace_recovery_codes"
	opRecoveryCodesRemaining = "recovery_codes_remaining"
)

// Directory is what a sign-in needs from identity, and nothing else.
//
// It is the two handle lookups a sign-in form submits, the principal read every
// authenticated request afterwards makes, the one read that returns a user's
// credentials, and the three credential writes. identity.Store satisfies it, so
// a consumer passes theirs; a consumer whose directory is not that schema
// implements these seven methods.
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
	//
	// On a service built with WithRecoveryCodeStore it may be one of the user's
	// recovery codes instead, which is tried when it does not verify as a TOTP
	// code and spent by the sign-in it proves. It is one field rather than two
	// because a person who has lost their authenticator types a code into the
	// box that asks for one, and because a field of its own would be a request
	// shape that says which kind of code somebody is trying.
	TOTPCode string `json:"-"`

	// ActiveAccountID is the account the token should be issued for. Empty means
	// the user's default account — or no account at all, for a user who holds no
	// memberships, who signs in and gets a token against nothing rather than
	// being refused. An account the user is not a live member of is refused by
	// the directory rather than honored.
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

	// RefreshTokenExpiresAt is when the refresh token stops being exchangeable,
	// and the zero time when there is none. It is the deadline that actually
	// bounds this sign-in: an idle client that lets it pass has to prove a
	// password again.
	RefreshTokenExpiresAt time.Time `json:"refreshTokenExpiresAt,omitzero"`

	// Token is the credential itself. It is not redacted anywhere, because a
	// sign-in that hides it has accomplished nothing — which is the reason it
	// must not be logged, recorded by a hook, or put in an error message.
	Token string `json:"token"`

	// RefreshToken is the credential that mints the next Token without a
	// password, and it is empty for a service built without
	// [WithRefreshTokenStore].
	//
	// It is single-use: exchanging it spends it and mints its successor, and
	// presenting a spent one ends the whole family. It is a credential like
	// Token and is redacted nowhere for the same reason — and it is the longer
	// lived of the two, so it is the one worth stealing and the one worth
	// storing most carefully.
	RefreshToken string `json:"refreshToken,omitempty"`

	// FamilyID is which continuous login this is: minted here, inherited by
	// every successor [Service.ExchangeRefreshToken] issues, and ended as a unit
	// when a spent refresh token is presented again.
	//
	// It is set whether or not a refresh token was minted, because it identifies
	// a sign-in rather than a row — which is what makes it the value
	// [DefaultClaims] emits as "sid" and the value a hook records. See
	// [RefreshToken.FamilyID] for why it is not called a session.
	FamilyID string `json:"familyID"`

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

	// ActiveAccountID is the account this caller's requests are against, and is
	// empty for a caller who belongs to none.
	ActiveAccountID string `json:"activeAccountID"`

	// AccountIDs is every account they are a live member of, default first. It
	// is empty for a caller who belongs to none, which is the state a client
	// acts on by sending them somewhere to be put in one.
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
	// enough, or unlike the last four is the consumer's rule — this package
	// holds no password policy, and validating one here would be a policy every
	// consumer then had to work around. The consumer's own is applied here if
	// the service was built with WithPasswordPolicy.
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
	//
	// On a service built with WithRecoveryCodeStore it may be one of the user's
	// recovery codes instead, which is the lost-phone case: the secret being
	// replaced is on the device that is gone, and the code on paper is what
	// proves the person asking is the one who enrolled it.
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

	// registrar is nil until WithRegistrar names one, and nil is what "this
	// service registers nobody" means: Register refuses with
	// ErrRegistrationNotConfigured, which is the right shape for a consumer
	// using this service as a credential check over a directory somebody else
	// fills.
	registrar Registrar

	// verifications is nil until WithVerifications names one, and nil means the
	// three doors that finish a registration refuse with
	// ErrVerificationsNotConfigured.
	verifications Verifications

	// secrets is what mints a verification token. It is never nil — the
	// constructor defaults it — and WithSecretGenerator replaces it.
	secrets random.Generator

	// refreshTokens is nil until WithRefreshTokenStore names one, and nil is
	// what "this service issues one token per sign-in" means: the two token
	// doors mint no refresh token, and the three refresh doors refuse with
	// ErrRefreshTokensNotConfigured.
	refreshTokens RefreshTokenStore

	// magicLinks is nil until WithMagicLinkStore names one, and nil is what
	// "this service has no passwordless door" means: both magic link doors
	// refuse with ErrMagicLinksNotConfigured.
	magicLinks MagicLinkStore

	// recoveryCodes is nil until WithRecoveryCodeStore names one, and nil is
	// what "this service accepts no recovery code" means: a second factor is a
	// TOTP code and nothing else, and the two recovery code doors refuse with
	// ErrRecoveryCodesNotConfigured.
	recoveryCodes RecoveryCodeStore

	// magicLinkMailer is nil until WithMagicLinkMailer names one. The request
	// door needs both it and the store, because a link that is minted and not
	// sent is a sign-in nobody can complete; the redemption door needs only the
	// store.
	magicLinkMailer MagicLinkMailer

	// passwordPolicy is nil until WithPasswordPolicy names one, and nil admits
	// any password that is not empty.
	passwordPolicy PasswordPolicy

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

	refreshTokenTTL      time.Duration
	adminRefreshTokenTTL time.Duration

	magicLinkTTL time.Duration

	// recoveryCodeCount is how many codes ReplaceRecoveryCodes mints — see
	// DefaultRecoveryCodeCount.
	recoveryCodeCount int

	// verificationLinkTTL is how long the link minted at registration stays
	// answerable. It becomes a deadline before it reaches identity's store,
	// which refuses a zero one — see DefaultVerificationLinkTTL.
	verificationLinkTTL time.Duration
	magicLinkFloor      time.Duration

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
		secrets:       random.NewGenerator(),
		generator:     totp.NewGenerator(),
		hooks:         NoopHooks{},
		clk:           clock.NewClock(),
		claims:        DefaultClaims,
		tokenTTL:      DefaultTokenTTL,
		adminTokenTTL: DefaultAdminTokenTTL,
		secondFactor:  SecondFactorWhenEnrolled,

		refreshTokenTTL:      DefaultRefreshTokenTTL,
		adminRefreshTokenTTL: DefaultAdminRefreshTokenTTL,

		magicLinkTTL: DefaultMagicLinkTTL,

		recoveryCodeCount: DefaultRecoveryCodeCount,

		verificationLinkTTL: DefaultVerificationLinkTTL,
		magicLinkFloor:      DefaultMagicLinkRequestFloor,
	}

	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}

	// The one relationship between the four lifetimes that is a mistake rather
	// than a preference. A refresh token that dies before the access token it
	// replaces is a sign-in that ends at a moment nothing chose: the client
	// still holds a working access token, so nothing prompts it to refresh, and
	// by the time it does the family is gone. Checked here rather than in the
	// options, because it is a relationship between two of them and an option
	// sees one.
	if err := s.validateLifetimes(); err != nil {
		return nil, err
	}

	s.o11y = observability.NewObserver(serviceName, s.logger, s.tracerProvider)

	instruments, err := metrics.NewOperationSet(s.metricsProvider, serviceName)
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating sign-in service instruments")
	}

	s.instruments = instruments

	return s, nil
}

// validateLifetimes rejects a service whose refresh tokens are shorter lived
// than the access tokens they mint.
//
// It is checked for both doors rather than only the ordinary one, because the
// administrative pair is where the mistake is easy to make: shortening the
// administrative access token is the obvious hardening, and shortening it past
// the administrative refresh token is the version of that hardening which locks
// operators out at an interval nobody configured.
//
// Equality is allowed. A refresh token exactly as long lived as its access token
// still gives a client the whole of that window to use it, which is a
// deployment's choice to make rather than an error.
func (s *Service) validateLifetimes() error {
	if s.refreshTokenTTL < s.tokenTTL {
		return platformerrors.Wrapf(ErrRefreshTokenTTLTooShort,
			"refresh token TTL %s is shorter than token TTL %s", s.refreshTokenTTL, s.tokenTTL)
	}

	if s.adminRefreshTokenTTL < s.adminTokenTTL {
		return platformerrors.Wrapf(ErrRefreshTokenTTLTooShort,
			"administrative refresh token TTL %s is shorter than administrative token TTL %s",
			s.adminRefreshTokenTTL, s.adminTokenTTL)
	}

	return nil
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
