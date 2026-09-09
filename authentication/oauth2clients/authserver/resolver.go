package authserver

import (
	"context"
	"net/http"

	"github.com/primandproper/platform-go/v14/authentication/oauth2clients"

	"github.com/primandproper/primitives-go/authentication/oauth2server"
	"github.com/primandproper/primitives-go/database"
	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/observability"
	"github.com/primandproper/primitives-go/observability/metrics"
	"github.com/primandproper/primitives-go/tenancy"
)

// resolverName scopes the guarded resolver's spans, logger and instruments.
const resolverName = "oauth2clients_authserver_resolver"

// ScopedSubjectResolver is what [GuardedResolver] wraps: a consumer's own
// resolver, answering with the subject *and the registry that subject's
// credential was issued in*.
//
// # Why the scope comes from here and not from the request
//
// Because the two facts have to come from the same place, and only the resolver
// holds both.
//
// [Authenticator] gets this for free. It resolves a scope off the request and
// then hands it to signin.Service.LoginForToken, which checks the credentials
// *within* that scope — so a request naming a registry its user is not in fails
// to sign in, and the scope that reaches [oauth2clients.Client.Admits] is one
// the person just proved membership of.
//
// A resolver has nothing equivalent. Its subject comes out of a session the
// consumer minted, and if the scope came off the request — a host header, a path
// segment, a header a gateway set — then nothing would check that the two belong
// together. Somebody holding a valid session in one registry could name another
// registry in the request and be admitted to its administered clients, which
// admit any subject in them: a cross-tenant authorization code, through the seam
// whose whole purpose is preventing one.
//
// So the scope is returned rather than resolved. The consumer's session knows
// which registry it was issued in — it had to, to be a session — and reporting
// it here is what binds the subject to the registry [GuardedResolver] then
// checks the registration against.
//
// # What it owes back
//
// The same three answers oauth2server.SubjectResolver gives, with a scope
// alongside a resolved subject. (nil, _, nil) is "not one of mine" and falls
// through to the login form. An error ends the attempt. A subject with an
// undecided scope is [ErrScopelessSubject] — see there.
type ScopedSubjectResolver interface {
	ResolveScopedSubject(
		ctx context.Context,
		req *http.Request,
	) (*oauth2server.Subject, tenancy.Scope, error)
}

// ScopedSubjectResolverFunc adapts a function to [ScopedSubjectResolver].
type ScopedSubjectResolverFunc func(
	ctx context.Context,
	req *http.Request,
) (*oauth2server.Subject, tenancy.Scope, error)

// ResolveScopedSubject implements [ScopedSubjectResolver].
func (f ScopedSubjectResolverFunc) ResolveScopedSubject(
	ctx context.Context,
	req *http.Request,
) (*oauth2server.Subject, tenancy.Scope, error) {
	return f(ctx, req)
}

var _ oauth2server.SubjectResolver = (*GuardedResolver)(nil)

// GuardedResolver wraps a consumer's own [ScopedSubjectResolver] so that the
// subject it reports is one the named registration admits.
//
// # Why this exists, and why it is not optional
//
// A subject resolver is how a request that already carries proof of who somebody
// is — a session cookie, a bearer token a first-party client holds — reaches an
// authorization code without meeting a login form. oauth2server consults it
// *before* the form and short-circuits on a non-nil subject.
//
// That is exactly the shape of a bypass. A deployment that wires
// [NewAuthenticator] for the form and its own resolver for signed-in users has
// put oauth2clients.Client.Admits on one of the two paths to a code, and the
// other path is the one its own first-party application takes. Wrapping the
// resolver is what closes it, and it is why this type is in the same package as
// the authenticator rather than left to a consumer to remember.
//
// # Why a refusal is (nil, nil) and not an error
//
// This is forced by oauth2server, and getting it backwards produces a worse
// failure than not checking at all.
//
// Server.resolveSubject turns *any* resolver error into a server_error and a
// redirect, with no form rendered — deliberately, because a caller who
// presented a credential has nothing to type. So a resolver that reported the
// mismatch as an error would replace an actionable page with an opaque failure
// at the client's redirect URI.
//
// Answering (nil, nil) means "not one of mine", which is the contract's own
// wording for a credential this resolver does not recognize. The request falls
// through to the login form, the person signs in, and [Authenticator] makes the
// same check and gives them the written answer. The guarantee is identical and
// the person gets a page instead of a redirect.
//
// A refusal is not silent, though it is invisible on the wire: every decline is
// recorded with the client_id, the registry and the reason, because an operator
// asked "why did this application stop working for one team" has nothing else to
// read. A broken registry is still an error — that is not a refused credential,
// and there is no form that fixes it.
type GuardedResolver struct {
	// The registry lookup this seam shares with [Authenticator]. See [guard]:
	// the two paths to a code run the same procedure up to Admits, and a copy of
	// it that drifted would leave the other path unguarded — this one, silently,
	// because a decline here is (nil, nil).
	guard

	inner ScopedSubjectResolver
	o11y  observability.Observer

	instruments *metrics.OperationSet

	// What the options wrote, kept only until the observer is built from it.
	opts observabilityOptions
}

// NewGuardedResolver wraps a resolver with the registration check.
//
// The inner resolver is the consumer's: this package has no opinion about what
// a session looks like, which is why there is no token parser here and no
// interface for one. What it adds is the one comparison the authorization
// server cannot make for itself, against the registry the consumer's own
// resolver says the subject's credential belongs to.
//
// Observability is optional and defaults to nothing: an unconfigured resolver
// logs to a noop logger, traces to a noop provider and counts through a noop
// metrics provider. This is the seam where leaving it unconfigured costs the
// most. A refusal here is a decline rather than an error — see [GuardedResolver]
// — so the person is sent to a login form and the request succeeds; nothing on
// the wire, in a status code or in an error rate says the check ran and said no,
// and the report that reaches an operator is "it keeps asking me to sign in".
//
// Every [GuardedResolver.ResolveSubject] is one attempt. A subject the
// registration would not admit is deliberately *not* counted as a failure: the
// request carries on to the form, so counting it would put a refused
// registration in the same number as a broken one. It is recorded on the span
// and the log line instead, beside the client_id and the registry, which is the
// only place it appears at all.
func NewGuardedResolver(
	inner ScopedSubjectResolver,
	registry oauth2clients.Store,
	client database.Client,
	opts ...ResolverOption,
) (*GuardedResolver, error) {
	if inner == nil {
		return nil, platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil subject resolver")
	}

	if registry == nil {
		return nil, oauth2clients.ErrNilStore
	}

	if client == nil {
		return nil, oauth2clients.ErrNilDatabaseClient
	}

	r := &GuardedResolver{registry: registry, client: client, inner: inner}

	for _, opt := range opts {
		if opt != nil {
			opt(r)
		}
	}

	r.o11y = observability.NewObserver(resolverName, r.opts.logger, r.opts.tracerProvider)

	instruments, err := metrics.NewOperationSet(r.opts.metricsProvider, resolverName)
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating oauth2clients authserver resolver instruments")
	}

	r.instruments = instruments

	return r, nil
}

// ResolveSubject implements oauth2server.SubjectResolver.
func (r *GuardedResolver) ResolveSubject(
	ctx context.Context,
	req *http.Request,
) (*oauth2server.Subject, error) {
	ctx, op := r.o11y.Begin(ctx)
	defer op.End()

	r.instruments.Attempt(ctx)

	subject, scope, err := r.inner.ResolveScopedSubject(ctx, req)
	if err != nil || subject == nil {
		// The inner resolver's answer, unchanged. It declined, or it broke, and
		// this wrapper has nothing to add to either.
		if err != nil {
			r.instruments.Failed(ctx)
		}

		return subject, err
	}

	op.Set(subjectKey, subject.ID).Set(scopeKey, scope.String())

	// An undecided scope is not the global one. See [ErrScopelessSubject].
	if scopeErr := scope.Validate(); scopeErr != nil {
		r.instruments.Failed(ctx)

		return nil, op.Error(platformerrors.Wrap(ErrScopelessSubject, scopeErr.Error()),
			"resolving the registry of an authorization request")
	}

	// The lookup both paths to a code share, up to but not including Admits. A
	// broken registry and a client_id this registry never issued are errors on
	// both paths, and neither is a decline: see [guard.registrationFor].
	registered, err := r.registrationFor(ctx, op, req)
	if err != nil {
		r.instruments.Failed(ctx)

		return nil, err
	}

	if registered == nil {
		// No client named, so there is nothing to check the subject against and
		// the inner resolver's answer stands.
		return subject, nil
	}

	if err = registered.Admits(scope, subject.ID); err != nil {
		// Decline rather than refuse. See the type documentation: an error here
		// becomes a server_error redirect, and declining sends the person to the
		// form where the authenticator makes the same check and can say why.
		//
		// Acknowledged rather than returned: the request carries on, so this is
		// the only record that the check ran and said no.
		op.Acknowledge(err, "declining a subject the registration does not admit")

		return nil, nil //nolint:nilnil // Declining is the contract's answer to a refused registration; see the type documentation.
	}

	return subject, nil
}
