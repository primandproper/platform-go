package signin

import (
	"context"
	"time"

	"github.com/primandproper/platform-go/v14/identity"

	"github.com/primandproper/primitives-go/v2/authentication/totp"
	"github.com/primandproper/primitives-go/v2/clock"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	"github.com/primandproper/primitives-go/v2/observability/tracing"
	"github.com/primandproper/primitives-go/v2/random"
)

// DefaultTokenTTL is how long an ordinary sign-in's token lives.
//
// An hour, and it is a default rather than a policy this package holds: a
// consumer with an opinion says so with WithTokenTTL. What a sign-in service
// cannot do is decline to pick — a token with no expiry is a credential that
// outlives every revocation, which is the one answer that is wrong for
// everybody.
const DefaultTokenTTL = time.Hour

// DefaultAdminTokenTTL is how long an administrative sign-in's token lives.
//
// Fifteen minutes, and shorter than DefaultTokenTTL deliberately: the token
// that can ban a user is the one worth stealing, and the window it is worth
// stealing in is the part a default can shrink without asking anybody.
const DefaultAdminTokenTTL = 15 * time.Minute

// DefaultRefreshTokenTTL is how long an ordinary sign-in's refresh token lives,
// and therefore how long that sign-in lasts.
//
// Thirty days, and it is the deadline that actually bounds a session: the access
// token above expires every hour and is replaced without a password, so what
// ends a sign-in is this one lapsing. Every exchange mints a successor with a
// fresh window of the same length, so the thirty days is an idle timeout rather
// than an absolute one — a client that keeps refreshing keeps the login, and one
// that stops loses it thirty days later.
//
// A consumer wanting an absolute bound as well enforces it above this package,
// from RefreshToken.IssuedAt or from whatever their AfterIssueToken hook
// recorded when the family was first minted. This package deliberately does not:
// a maximum session age is policy that differs per deployment far more than a
// default could guess, and a column holding one would be a second deadline
// competing with the one in the row.
const DefaultRefreshTokenTTL = 30 * 24 * time.Hour

// DefaultAdminRefreshTokenTTL is how long an administrative sign-in lasts.
//
// Twelve hours, and shorter than DefaultRefreshTokenTTL for the reason
// DefaultAdminTokenTTL is shorter than DefaultTokenTTL: the sign-in that can ban
// a user is the one worth stealing, and an operator's working day is the window
// a default can shrink it to without asking anybody. An operator who is still
// working after twelve hours signs in again; an operator who went home is not
// carrying a live administrative session for a month.
const DefaultAdminRefreshTokenTTL = 12 * time.Hour

// The claims DefaultClaims puts on a token beside the registered ones the
// issuer owns. They are exported because a consumer's own interceptor reads
// them back off a parsed token, and a claim key spelled twice is a claim read
// as absent.
const (
	// ClaimAccountID is the account the token was issued for — the one the
	// principal resolved, which is the user's default when the request named
	// none.
	ClaimAccountID = "account_id"

	// ClaimScope is the directory the token was issued in, as
	// tenancy.Scope.String renders it. It is the empty string for
	// tenancy.Global, which is what a single-tenant deployment sees.
	ClaimScope = "scope"

	// ClaimFamilyID is which continuous login the token belongs to — SignIn's
	// FamilyID, and the value every successor a refresh exchange mints carries
	// too.
	//
	// Its wire spelling is "sid", which is the conventional key for this claim
	// and what a consumer's interceptor already looks for. The Go vocabulary is
	// "family" throughout this module because that is what the mechanism is, and
	// the two are not coupled: a claim key is a string on a wire, chosen for the
	// clients that read it, and a package's nouns are chosen for the people
	// reading its code. Naming the constant after the mechanism is what keeps
	// this one place the wire spelling appears.
	ClaimFamilyID = "sid"
)

// SecondFactorPolicy is what this service does about a user who holds no proven
// second factor.
//
// It is the one question about second factors that a library cannot answer: a
// consumer rolling TOTP out to an existing user base needs the first value, and
// a consumer who has finished rolling it out wants the second. What happens to a
// user who *does* hold one is not a policy and is not configurable — they are
// asked for a code.
type SecondFactorPolicy uint8

const (
	// SecondFactorWhenEnrolled asks for a code only from users who hold a proven
	// second factor. Everybody else signs in with a password alone.
	//
	// It is the zero value, so it is what a service that names no policy runs,
	// and it is the only one that behaves sensibly for a service where nobody
	// has enrolled yet.
	SecondFactorWhenEnrolled SecondFactorPolicy = iota

	// SecondFactorRequired refuses a sign-in by anybody who holds no proven
	// second factor, with ErrSecondFactorNotEnrolled.
	//
	// Switching to it locks out every user who has not enrolled, and they cannot
	// enroll without signing in. Enroll first, then switch.
	SecondFactorRequired
)

// Valid reports whether p is one of the two policies.
func (p SecondFactorPolicy) Valid() bool {
	switch p {
	case SecondFactorWhenEnrolled, SecondFactorRequired:
		return true
	default:
		return false
	}
}

// String renders the policy for a log line.
func (p SecondFactorPolicy) String() string {
	switch p {
	case SecondFactorWhenEnrolled:
		return "when_enrolled"
	case SecondFactorRequired:
		return "required"
	default:
		return "unknown"
	}
}

// ClaimsInput is what a ClaimsBuilder is given: everything this service knows
// about the token it is about to mint.
//
// It is a struct rather than a parameter list because the list was the problem.
// The builder took a principal alone, so a claim naming the login it belonged to
// was unreachable — and the login is exactly what an interceptor needs to check
// a token against a revocation. Growing a second parameter would have fixed that
// once; a struct fixes it for whatever the next claim needs, which is then
// additive rather than a third break.
type ClaimsInput struct {
	_ struct{} `json:"-"`

	// Principal is who the token is for — the user, redacted, their memberships,
	// and the account it is against.
	Principal *identity.Principal `json:"principal"`

	// FamilyID is which continuous login this token belongs to. It is the same
	// value on the token a sign-in mints and on every successor an exchange
	// mints after it, which is what makes a claim built from it name a session
	// rather than a request.
	//
	// It is set whether or not the service stores refresh tokens: a service that
	// mints one token per sign-in still has a login to name.
	FamilyID string `json:"familyID"`
}

// ClaimsBuilder produces the application-specific claims a token carries beside
// the registered ones its issuer owns.
//
// It is a seam because what a token says is the consumer's — a tenant, a plan, a
// device — and because the obvious thing to put in one is the thing that must
// not go in: the caller's roles. A token carrying roles is a permission set
// frozen at sign-in, so revoking a role has no effect until the token expires.
// Resolve permissions per request from the principal instead.
//
// Returning an error fails the sign-in, which is the right way round: a token
// that should have carried a claim and did not is worse than no token.
//
// The registered claim keys are the issuer's and are refused —
// tokens.ReservedClaimKeys names them.
type ClaimsBuilder func(ctx context.Context, input *ClaimsInput) (map[string]any, error)

// DefaultClaims is the ClaimsBuilder a service uses when none is named: the
// account the token is for, the directory it was issued in, and the login it
// belongs to.
//
// The first two are there because both are needed to make sense of the subject.
// A user ID alone does not say which account's data the request is against, and
// in a multi-directory deployment it does not even identify a person.
//
// The third is there because a consumer's interceptor cannot do its half of
// rotation without it. A token carries no reference to the sign-in it came from
// otherwise, so "this login was signed out" is a fact nothing on the wire can be
// checked against — and a detected refresh token reuse would revoke a family
// whose access tokens no interceptor could recognize.
func DefaultClaims(_ context.Context, input *ClaimsInput) (map[string]any, error) {
	if input == nil || input.Principal == nil {
		return nil, identity.ErrNilUser
	}

	return map[string]any{
		ClaimAccountID: input.Principal.ActiveAccountID,
		ClaimScope:     input.Principal.User.Scope.String(),
		ClaimFamilyID:  input.FamilyID,
	}, nil
}

// ServiceOption configures a Service.
//
// The observability dependencies are options rather than parameters because
// every one of them is genuinely optional: an absent logger logs nowhere, an
// absent tracer provider traces nowhere, and an absent metrics provider records
// nothing.
type ServiceOption func(*Service)

// WithLogger attaches a logger. An absent logger logs nowhere.
//
// Worth setting. A refused sign-in is the security-relevant event this package
// produces, and every one of them collapses to a single sentinel on the way out
// — which is the point, but it means the log line is the only place the reason
// survives.
func WithLogger(logger logging.Logger) ServiceOption {
	return func(s *Service) { s.logger = logger }
}

// WithTracerProvider attaches a tracer provider. An absent provider traces
// nowhere.
//
// It takes a provider rather than a ready-made tracer so that this package's
// spans carry this package's instrumentation scope.
func WithTracerProvider(tracerProvider tracing.Provider) ServiceOption {
	return func(s *Service) { s.tracerProvider = tracerProvider }
}

// WithMetricsProvider attaches a metrics provider. An absent provider records
// nothing.
func WithMetricsProvider(metricsProvider metrics.Provider) ServiceOption {
	return func(s *Service) { s.metricsProvider = metricsProvider }
}

// WithPillars supplies logger, tracer provider and metrics provider at once.
//
// Options apply in order, so WithPillars(p) followed by WithMetricsProvider(nil)
// leaves this one component unmetered.
func WithPillars(p *observability.Pillars) ServiceOption {
	return func(s *Service) { s.logger, s.tracerProvider, s.metricsProvider = p.Deps() }
}

// WithHooks attaches what a consumer commits alongside a sign-in. A nil Hooks is
// ignored, leaving NoopHooks.
func WithHooks(hooks Hooks) ServiceOption {
	return func(s *Service) {
		if hooks != nil {
			s.hooks = hooks
		}
	}
}

// WithSecondFactorPolicy sets what happens to a user who holds no proven second
// factor. An unrecognized policy is ignored, leaving SecondFactorWhenEnrolled.
func WithSecondFactorPolicy(policy SecondFactorPolicy) ServiceOption {
	return func(s *Service) {
		if policy.Valid() {
			s.secondFactor = policy
		}
	}
}

// WithTokenTTL sets how long an ordinary sign-in's token lives. A non-positive
// duration is ignored, leaving DefaultTokenTTL.
func WithTokenTTL(ttl time.Duration) ServiceOption {
	return func(s *Service) {
		if ttl > 0 {
			s.tokenTTL = ttl
		}
	}
}

// WithAdminTokenTTL sets how long an administrative sign-in's token lives. A
// non-positive duration is ignored, leaving DefaultAdminTokenTTL.
func WithAdminTokenTTL(ttl time.Duration) ServiceOption {
	return func(s *Service) {
		if ttl > 0 {
			s.adminTokenTTL = ttl
		}
	}
}

// WithRefreshTokenTTL sets how long an ordinary sign-in's refresh token lives,
// which is how long that sign-in lasts. A non-positive duration is ignored,
// leaving DefaultRefreshTokenTTL.
//
// It is a separate lifetime from WithTokenTTL rather than a multiple of it,
// because the two answer different questions. The access token's lifetime is how
// stale a permission check may be; this one is how long somebody stays signed
// in. A deployment tightening the first almost never means to tighten the
// second, and a single knob would make it do both.
//
// Shorter than WithTokenTTL is refused at construction — see
// ErrRefreshTokenTTLTooShort.
func WithRefreshTokenTTL(ttl time.Duration) ServiceOption {
	return func(s *Service) {
		if ttl > 0 {
			s.refreshTokenTTL = ttl
		}
	}
}

// WithAdminRefreshTokenTTL sets how long an administrative sign-in lasts. A
// non-positive duration is ignored, leaving DefaultAdminRefreshTokenTTL.
//
// Shorter than WithAdminTokenTTL is refused at construction — see
// ErrRefreshTokenTTLTooShort.
func WithAdminRefreshTokenTTL(ttl time.Duration) ServiceOption {
	return func(s *Service) {
		if ttl > 0 {
			s.adminRefreshTokenTTL = ttl
		}
	}
}

// WithRefreshTokenStore attaches where this service's refresh tokens live, which
// is what turns one token per sign-in into a rotating pair. A nil store is
// ignored, leaving none.
//
// Naming none — which is the default — is what "this service issues one token
// per sign-in" means: the two token doors mint no refresh token, SignIn.Token is
// the whole credential, and the three refresh doors refuse with
// ErrRefreshTokensNotConfigured. That shape is deliberate rather than
// vestigial. A consumer using Service.Authenticate as a credential check owes no
// table, and this package held no schema at all before rotation existed.
//
// What a service gains by naming one is the property the store's documentation
// is about: a sign-in that outlives its access token without a password, and a
// stolen refresh token that becomes a detected event rather than a shared
// session. This module's implementation is
// [github.com/primandproper/platform-go/v14/authentication/signin/refreshtokens].
//
// SignIn.FamilyID is set either way, because it names a login rather than a row.
func WithRefreshTokenStore(store RefreshTokenStore) ServiceOption {
	return func(s *Service) {
		if store != nil {
			s.refreshTokens = store
		}
	}
}

// WithAdminServiceRoles names the identity service roles that admit an
// administrative sign-in. Holding any one of them is enough.
//
// Naming none — which is the default — is what "this service has no
// administrative door" means: Service.AdminLoginForToken then refuses every
// request with ErrAdminLoginDisabled. That is the shape the decision takes,
// because whether administrative sign-in exists at all is the consumer's and a
// library cannot guess which of their role names is the operator one.
//
// Empty strings are dropped, so a consumer assembling the list from
// configuration cannot accidentally admit every user by leaving a blank line in
// it.
func WithAdminServiceRoles(roles ...string) ServiceOption {
	return func(s *Service) {
		kept := make([]string, 0, len(roles))

		for _, role := range roles {
			if role != "" {
				kept = append(kept, role)
			}
		}

		s.adminRoles = kept
	}
}

// WithClaimsBuilder sets what a token carries beyond its subject. A nil builder
// is ignored, leaving DefaultClaims.
func WithClaimsBuilder(build ClaimsBuilder) ServiceOption {
	return func(s *Service) {
		if build != nil {
			s.claims = build
		}
	}
}

// WithTOTPIssuer sets the application name an authenticator app labels an
// enrollment with — the consumer's own name, as a person would recognize it.
//
// There is no default and there cannot be one: a label this package invented
// would appear on somebody's phone. Service.RefreshTOTPSecret refuses with
// ErrTOTPIssuerNotConfigured until this is set, and a consumer who never enrolls
// a second factor never needs it.
func WithTOTPIssuer(issuer string) ServiceOption {
	return func(s *Service) {
		if issuer != "" {
			s.totpIssuer = issuer
		}
	}
}

// WithTOTPVerifier replaces what checks a second-factor code. A nil verifier is
// ignored, leaving this module's own.
//
// Unlike the authenticator, this has a default: there is one TOTP
// implementation here, RFC 6238 leaves nothing to choose, and a service that
// had to be handed one would make every consumer wire a component with no
// decision in it. The seam is for a consumer whose codes come from somewhere
// else, and for a test.
func WithTOTPVerifier(verifier totp.Verifier) ServiceOption {
	return func(s *Service) {
		if verifier != nil {
			s.verifier = verifier
		}
	}
}

// WithTOTPGenerator replaces what mints a second-factor secret. A nil generator
// is ignored, leaving this module's own. See WithTOTPVerifier for why this one
// defaults.
func WithTOTPGenerator(generator totp.Generator) ServiceOption {
	return func(s *Service) {
		if generator != nil {
			s.generator = generator
		}
	}
}

// WithClock replaces the clock a token's expiry is computed from, for tests that
// need an issued token and its expiry to be a known distance apart. A nil clock
// is ignored.
//
// It is this package's clock and not the issuer's: what the token itself says
// about its own expiry is the issuer's business, and what this service reports
// having asked for is ours.
func WithClock(c clock.Clock) ServiceOption {
	return func(s *Service) {
		if c != nil {
			s.clk = c
		}
	}
}

// WithRegistrar attaches what this service registers people through, which is
// what turns it from a door into a way in.
//
// A nil registrar is ignored, leaving none, and naming none is what "this
// service registers nobody" means: [Service.Register] refuses with
// ErrRegistrationNotConfigured, which is the right shape for a consumer using
// this service as a credential check over a directory something else fills.
//
// identity's Service satisfies it. That is the layer rather than the store
// because both registrations there write three rows, assign an owner, mint a
// default membership and call a consumer's hook, all on one transaction — see
// Registrar.
func WithRegistrar(registrar Registrar) ServiceOption {
	return func(s *Service) {
		if registrar != nil {
			s.registrar = registrar
		}
	}
}

// WithVerifications attaches the reads and writes the three doors that finish a
// registration need — Service.AttachPassword, Service.VerifyEmailAddress and
// Service.CompleteVerification. A nil value is ignored, leaving none, and each
// of the three then refuses with ErrVerificationsNotConfigured.
//
// identity's Store satisfies it. It is a second option rather than three more
// methods on Directory because Directory is the interface the component holding
// everybody's passwords depends on, and because a consumer implementing that
// one themselves should not have to grow it for a flow they do not run.
func WithVerifications(verifications Verifications) ServiceOption {
	return func(s *Service) {
		if verifications != nil {
			s.verifications = verifications
		}
	}
}

// WithSecretGenerator replaces the source a verification link's token is drawn
// from. A nil generator is ignored, leaving random.NewGenerator, which reads
// crypto/rand.
//
// The seam is for a test that needs a token it can predict. Nothing else should
// name one: the secret this mints is the whole authority of the mail it travels
// in.
func WithSecretGenerator(generator random.Generator) ServiceOption {
	return func(s *Service) {
		if generator != nil {
			s.secrets = generator
		}
	}
}

// DefaultMagicLinkTTL is how long a sign-in link stays redeemable when
// WithMagicLinkTTL names nothing.
//
// Fifteen minutes. It is the shortest lifetime in this package by a wide margin,
// and deliberately: a sign-in link is a bearer credential sitting in an inbox,
// which is a place a great many people other than its owner can reach — a
// forwarded thread, a shared mailbox, a synced device, a mail server's logs. It
// is long enough to survive a slow delivery and somebody switching to their
// phone, and short enough that a link found later is almost always already dead.
//
// It is shorter than passwordreset's window on purpose, though the two
// mechanisms look alike. A reset link is answered by somebody who then types a
// new password, so the flow tolerates and expects a gap; this one is answered by
// somebody who wanted to be signed in when they asked.
const DefaultMagicLinkTTL = 15 * time.Minute

// DefaultVerificationLinkTTL is how long the verification link minted at
// registration stays answerable.
//
// Seventy-two hours. It is the longest deadline this package hands out, and
// deliberately: the link goes to somebody who has not yet used the product,
// whose mail may sit unread over a weekend, and whose only remedy for a dead one
// is a flow the consumer has to have built. It is also the strongest link — it
// proves the address, it promotes the registrant out of StatusUnverified, and
// Service.AttachPassword answers it with the first password on an account that
// holds none — so it is bounded rather than left open for the reason
// identity.Invitation.ExpiresAt is required at all.
//
// A deployment that mails a "confirm your address" link and expects it answered
// the same hour shortens it with WithVerificationLinkTTL. One that cannot say
// what its own window should be has the wrong question: the answer is how long
// an unanswered registration should stay claimable out of a mailbox.
const DefaultVerificationLinkTTL = 72 * time.Hour

// DefaultMagicLinkRequestFloor is how long Service.RequestMagicLink takes at the
// least, whatever it found.
//
// Five hundred milliseconds. The floor is the whole of the timing half of the
// enumeration defense: the door answers identically for an address nobody holds
// and one somebody does, and without a floor the difference between them is a
// directory read, a token mint, a commit and an SMTP conversation — which is not
// a subtle signal, it is most of a second.
//
// The value matters less than its being greater than the slowest path it covers.
// It is measured from the moment the call begins rather than from the read, so a
// deployment whose mail send is slower than this should raise it — see
// WithMagicLinkRequestFloor, which is also where the reason this cannot be
// defended in Go alone is written down.
const DefaultMagicLinkRequestFloor = 500 * time.Millisecond

// WithMagicLinkStore attaches where this service's sign-in links live, which is
// what turns the passwordless door on.
//
// Absent, Service.RequestMagicLink and Service.RedeemMagicLink both refuse with
// ErrMagicLinksNotConfigured, and that is the right default: a service built as
// a credential check over somebody else's directory mails nothing, and a door
// that quietly did nothing would be worse than one that says it is not
// configured.
//
// github.com/primandproper/platform-go/v14/authentication/signin/magiclinks is
// the SQL implementation this module ships, with the DDL it needs.
func WithMagicLinkStore(store MagicLinkStore) ServiceOption {
	return func(s *Service) {
		if store != nil {
			s.magicLinks = store
		}
	}
}

// WithMagicLinkMailer attaches what delivers a sign-in link.
//
// It is required by Service.RequestMagicLink and not by Service.RedeemMagicLink,
// which is the split a consumer who mints links somewhere else — a queue, a
// separate mailer service — depends on: they can configure a store and redeem
// what somebody else sent.
//
// The service mails rather than handing the secret back, and that is not a
// preference. An answer that carried a secret for a known address and nothing
// for an unknown one would be an enumeration oracle in the shape of a response
// body, and no amount of timing padding fixes a response shape. Registration
// takes the opposite reading — Register hands its verification token back on
// Registered — because a registration has already told the caller the account
// exists.
func WithMagicLinkMailer(mailer MagicLinkMailer) ServiceOption {
	return func(s *Service) {
		if mailer != nil {
			s.magicLinkMailer = mailer
		}
	}
}

// WithMagicLinkTTL sets how long a sign-in link stays redeemable. A non-positive
// duration is ignored, leaving DefaultMagicLinkTTL.
//
// It is the service's rather than the store's, for the reason WithTokenTTL and
// WithRefreshTokenTTL are: how long a credential works is policy, and a store
// holding a default for it would be the value nobody passed competing with the
// value somebody chose. What the store does hold is the retention window past
// that deadline, which is storage's own business.
func WithMagicLinkTTL(ttl time.Duration) ServiceOption {
	return func(s *Service) {
		if ttl > 0 {
			s.magicLinkTTL = ttl
		}
	}
}

// WithVerificationLinkTTL sets how long the verification link minted at
// registration stays answerable. A non-positive duration is ignored, leaving
// DefaultVerificationLinkTTL.
//
// It is the service's rather than the store's, for the reason WithMagicLinkTTL
// is: how long a credential works is policy. identity's store takes the deadline
// this computes rather than a lifetime of its own, so there is one clock on the
// link and it is this one.
func WithVerificationLinkTTL(ttl time.Duration) ServiceOption {
	return func(s *Service) {
		if ttl > 0 {
			s.verificationLinkTTL = ttl
		}
	}
}

// WithMagicLinkRequestFloor sets how long Service.RequestMagicLink takes at the
// least. A non-positive duration is ignored, leaving
// DefaultMagicLinkRequestFloor.
//
// Raise it above the slowest thing that call does in your deployment, which is
// almost always the mail send. A floor shorter than the work it covers is a
// floor that only pads the fast path, and the fast path is the one that found
// nobody — so it would make the difference easier to measure rather than harder.
//
// What it cannot cover is what happens after this call returns. A consumer whose
// handler answers a known address with a 200 and an unknown one with a 404 has
// rebuilt the oracle in their own transport, and no value here reaches it. The
// answer is the same either way, and Service.RequestMagicLink says so.
func WithMagicLinkRequestFloor(floor time.Duration) ServiceOption {
	return func(s *Service) {
		if floor > 0 {
			s.magicLinkFloor = floor
		}
	}
}

// DefaultRecoveryCodeCount is how many recovery codes a set holds when
// WithRecoveryCodeCount names nothing.
//
// Eight. It is enough that a person who spends one each time they lose a phone
// is not back at the settings page for years, and few enough that a sheet of
// them is something a person will actually print and keep rather than paste into
// the password manager on the device they are about to lose.
const DefaultRecoveryCodeCount = 8

// WithRecoveryCodeStore attaches where this service's recovery codes live, which
// is what gives a person who has lost their authenticator a way back in that is
// not a support ticket. A nil store is ignored, leaving none.
//
// Naming none — which is the default — is what "this service accepts no recovery
// code" means: every second-factor check is a TOTP code and nothing else,
// exactly as it was before recovery codes existed, and Service.ReplaceRecoveryCodes
// and Service.RecoveryCodesRemaining refuse with ErrRecoveryCodesNotConfigured.
//
// Naming one changes what a second-factor code may be at every door that asks
// for one but Service.UpdatePassword — the four password sign-ins, the
// passwordless redemption, Service.RefreshTOTPSecret and
// Service.ReplaceRecoveryCodes — and nothing else about them: a code that is not the TOTP code is tried as a recovery code, spent
// by the operation it proves, and refused as ErrInvalidCredentials when it is
// neither. Service.UpdatePassword does not take one. Its second factor guards a
// password rather than the second factor itself, and a person who has lost their
// authenticator re-enrolls one before anything else.
//
// github.com/primandproper/platform-go/v14/authentication/signin/recoverycodes
// is the SQL implementation this module ships, with the DDL it needs.
func WithRecoveryCodeStore(store RecoveryCodeStore) ServiceOption {
	return func(s *Service) {
		if store != nil {
			s.recoveryCodes = store
		}
	}
}

// WithRecoveryCodeCount sets how many recovery codes Service.ReplaceRecoveryCodes
// mints. A non-positive count is ignored, leaving DefaultRecoveryCodeCount.
//
// It is the service's rather than the store's, for the reason WithMagicLinkTTL
// is: how many standing substitutes a second factor has is policy, and the store
// is handed the number on every replacement.
func WithRecoveryCodeCount(count int) ServiceOption {
	return func(s *Service) {
		if count > 0 {
			s.recoveryCodeCount = count
		}
	}
}
