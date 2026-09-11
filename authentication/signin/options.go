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
type ClaimsBuilder func(ctx context.Context, principal *identity.Principal) (map[string]any, error)

// DefaultClaims is the ClaimsBuilder a service uses when none is named: the
// account the token is for and the directory it was issued in.
//
// Both are there because both are needed to make sense of the subject. A user
// ID alone does not say which account's data the request is against, and in a
// multi-directory deployment it does not even identify a person.
func DefaultClaims(_ context.Context, principal *identity.Principal) (map[string]any, error) {
	if principal == nil {
		return nil, identity.ErrNilUser
	}

	return map[string]any{
		ClaimAccountID: principal.ActiveAccountID,
		ClaimScope:     principal.User.Scope.String(),
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
