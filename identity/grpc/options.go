package grpc

import (
	"context"
	"time"

	"github.com/primandproper/primitives-go/observability"
	"github.com/primandproper/primitives-go/observability/logging"
	"github.com/primandproper/primitives-go/observability/metrics"
	"github.com/primandproper/primitives-go/observability/tracing"
	"github.com/primandproper/primitives-go/random"
)

// DefaultInvitationTTL is how long an invitation lives when a client sends no
// expiry of its own.
//
// A week, and it is a default rather than a policy this package holds: a
// consumer with an opinion says so with WithInvitationTTL, and a client with
// one puts an expires_at on the request. What a transport cannot do is decline
// to pick — an invitation with no expiry is a link that works forever, which is
// the one answer that is wrong for everybody.
const DefaultInvitationTTL = 7 * 24 * time.Hour

// DefaultMaxInvitationTTL is the furthest ahead an invitation may be set to
// expire, whoever names the expiry.
//
// The default above only answers a request that named nothing. A request that
// names an expiry of its own would otherwise be the way around it: a client
// sending a timestamp in the year 9999 gets the forever-link the default exists
// to prevent, and a client sending one in the past gets a link the consumer's
// hook mails out already dead. So a named expiry has to fall between now and
// this ceiling, and the ceiling is a consumer's to lower or raise with
// WithMaxInvitationTTL — thirty days is a default, not a policy.
const DefaultMaxInvitationTTL = 30 * 24 * time.Hour

// defaultInvitationTokenBytes is the entropy behind an invitation link. Thirty-two
// bytes is what the rest of this module mints single-use tokens with.
const defaultInvitationTokenBytes = 32

// TokenMinter produces the value an invitation link carries.
//
// It is a seam rather than a fixed implementation because the token is what
// reaches a recipient, and a consumer whose links are signed, prefixed, or
// minted by a service of their own needs to say so. The default is this
// module's CSPRNG, which is not a policy choice — a guessable invitation token
// is an account takeover, and there is no second reasonable answer — where the
// lifetime above genuinely is one.
type TokenMinter func(ctx context.Context) (string, error)

// Option configures a Server.
type Option func(*Server)

// WithLogger sets the logger. Absent means no logging.
func WithLogger(logger logging.Logger) Option {
	return func(s *Server) { s.logger = logger }
}

// WithTracerProvider sets the tracer provider. Absent means no tracing.
//
// It is a provider rather than a ready-made tracer so that the instrumentation
// scope of this package's spans is decided here rather than by the caller.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(s *Server) { s.tracerProvider = tracerProvider }
}

// WithMetricsProvider sets the metrics provider. Absent means no metrics.
func WithMetricsProvider(metricsProvider metrics.Provider) Option {
	return func(s *Server) { s.metricsProvider = metricsProvider }
}

// WithPillars supplies logger, tracer provider and metrics provider at once.
//
// Options apply in order, so WithPillars(p) followed by WithMetricsProvider(nil)
// leaves this one component unmetered.
func WithPillars(p *observability.Pillars) Option {
	return func(s *Server) {
		s.logger, s.tracerProvider, s.metricsProvider = p.Deps()
	}
}

// WithInvitationTTL sets how long an invitation lives when the request names no
// expiry. A non-positive duration is ignored, leaving DefaultInvitationTTL.
//
// It must not exceed the ceiling WithMaxInvitationTTL sets; NewServer refuses
// the pair with ErrInvitationTTLExceedsMaximum rather than issuing invitations
// the server would itself have rejected a client for asking for.
func WithInvitationTTL(ttl time.Duration) Option {
	return func(s *Server) {
		if ttl > 0 {
			s.invitationTTL = ttl
		}
	}
}

// WithMaxInvitationTTL sets the furthest ahead any invitation may expire, the
// default included. A non-positive duration is ignored, leaving
// DefaultMaxInvitationTTL.
func WithMaxInvitationTTL(ttl time.Duration) Option {
	return func(s *Server) {
		if ttl > 0 {
			s.maxInvitationTTL = ttl
		}
	}
}

// WithTokenMinter sets what mints an invitation's token. A nil minter is
// ignored, leaving the default CSPRNG.
func WithTokenMinter(mint TokenMinter) Option {
	return func(s *Server) {
		if mint != nil {
			s.mintToken = mint
		}
	}
}

// defaultTokenMinter is the CSPRNG the module already uses for single-use
// tokens.
func defaultTokenMinter(ctx context.Context) (string, error) {
	return random.GenerateBase64EncodedString(ctx, defaultInvitationTokenBytes)
}

// WithTargetAuthorizer replaces the rule that decides whether the caller may act
// on the row a request named. A nil authorizer is ignored, leaving the default
// [MembershipAuthorizer].
//
// It is an option rather than a positional argument because the default is a
// real answer rather than a placeholder: a consumer who says nothing gets a
// directory whose request-named RPCs are closed to accounts the caller is not a
// member of, which is what the per-method permission fragment on its own could
// not give them. The consumers who need something else — an operator console, a
// support role that reads every account, a policy engine of their own — are the
// ones who name it.
//
// See [TargetAuthorizer] for what an implementation owes and
// [MembershipAuthorizer] for the three rules the default applies.
func WithTargetAuthorizer(authorizer TargetAuthorizer) Option {
	return func(s *Server) {
		if authorizer != nil {
			s.targets = authorizer
		}
	}
}
