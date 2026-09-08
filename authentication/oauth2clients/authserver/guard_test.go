package authserver_test

import (
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/oauth2clients"
	"github.com/primandproper/platform-go/v14/authentication/oauth2clients/authserver"
	"github.com/primandproper/platform-go/v14/authentication/oauth2server"
	platformerrors "github.com/primandproper/platform-go/v14/errors"
	"github.com/primandproper/platform-go/v14/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// These are the cases that must hold on *both* paths to an authorization code,
// asserted against both seams in one subtest each.
//
// The rest of this suite tests each seam on its own, which is right for what the
// two do differently — a refusal is a rendered page on one and a decline on the
// other, and those are separate assertions about separate contracts. What it
// cannot catch is the failure this file exists for: the two seams share the
// lookup that reaches oauth2clients.Client.Admits, and a change that regressed
// it would leave whichever path the reviewer was not looking at unguarded. On
// the resolver's path that is silent by construction — a decline is (nil, nil),
// which is indistinguishable from "not one of mine" — so nothing above it can
// tell "the guard declined" from "the guard is gone".
//
// So each case below runs one registry answer through both seams and asserts on
// both outcomes, and the assertion that matters in every one of them is that
// neither path issued a subject.

// pathOutcome is what one of the two seams did with a request.
type pathOutcome struct {
	subject *oauth2server.Subject
	err     error
}

// issued reports whether this path handed back a subject, which is the fact both
// paths must agree on: a subject is an authorization code.
func (o pathOutcome) issued() bool { return o.subject != nil }

// onBothPaths sends the same registry answer down both paths to a code, for the
// same person in the same registry.
//
// The authenticator signs that person in first, so the harness's own user is
// what the inner resolver reports on the other side — otherwise the two seams
// would be comparing different subjects and agreeing by coincidence.
func onBothPaths(
	t *testing.T,
	scope tenancy.Scope,
	registry oauth2clients.Store,
	clientID string,
) (authenticated, resolved pathOutcome) {
	t.Helper()

	h := newSignInHarness(t, scope)

	auth := newAuthenticator(t, h, registry, scope)

	subject, err := auth.AuthenticateSubject(t.Context(),
		loginRequest(t, clientID, h.user.Username, h.password))
	authenticated = pathOutcome{subject: subject, err: err}

	guarded, err := authserver.NewGuardedResolver(resolving(h.user.ID, scope), registry, h.db)
	must.NoError(t, err)

	subject, err = guarded.ResolveSubject(t.Context(), authorizeRequest(t, clientID))
	resolved = pathOutcome{subject: subject, err: err}

	return authenticated, resolved
}

func TestBothPaths(T *testing.T) {
	T.Parallel()

	T.Run("a broken registry fails both paths and declines neither", func(t *testing.T) {
		t.Parallel()

		broken := platformerrors.New("the registry is unreachable")

		authenticated, resolved := onBothPaths(t, tenantA, &fakeRegistry{err: broken}, "cid-1")

		// A registry error is not a decline. It is the one error that has to
		// survive on both paths: turning it into a decline on the resolver's
		// side would convert a database that is down into a silent bypass, and
		// turning it into a re-rendered form on the authenticator's would
		// produce somebody who types their password four times.
		test.ErrorIs(t, authenticated.err, broken)
		test.False(t, authenticated.issued())
		test.False(t, platformerrors.Is(authenticated.err, oauth2server.ErrLoginFailed))

		test.ErrorIs(t, resolved.err, broken)
		test.False(t, resolved.issued())
	})

	T.Run("a scope-mismatched registration is refused on both paths", func(t *testing.T) {
		t.Parallel()

		// The registration is administered — no owner — so it admits *any*
		// subject in tenant B, and the only thing standing between the person
		// and a cross-tenant authorization code is that they are in tenant A.
		authenticated, resolved := onBothPaths(t, tenantA, &fakeRegistry{client: registration(tenantB, "")}, "cid-1")

		// Neither path issued a subject, which is the guarantee. How each says
		// so is where they differ, and both halves are asserted so that a change
		// collapsing them into one answer fails here.
		test.False(t, authenticated.issued())
		test.False(t, resolved.issued())

		// The authenticator refuses with a page: the person is still here.
		test.ErrorIs(t, authenticated.err, oauth2clients.ErrClientScopeMismatch)
		test.ErrorIs(t, authenticated.err, oauth2server.ErrLoginFailed)

		// The resolver declines: (nil, nil), so the request falls through to the
		// form where the authenticator gives the written answer above.
		test.NoError(t, resolved.err)
	})

	T.Run("a registration another person owns is refused on both paths", func(t *testing.T) {
		t.Parallel()

		// The other of the two refusals oauth2clients.Client.Admits makes: the
		// right registry, somebody else's personal credential.
		authenticated, resolved := onBothPaths(t, tenantA, &fakeRegistry{client: registration(tenantA, userB)}, "cid-1")

		test.False(t, authenticated.issued())
		test.False(t, resolved.issued())

		test.ErrorIs(t, authenticated.err, oauth2clients.ErrClientOwnerMismatch)
		test.ErrorIs(t, authenticated.err, oauth2server.ErrLoginFailed)
		test.NoError(t, resolved.err)
	})

	T.Run("a client this registry never issued fails both paths", func(t *testing.T) {
		t.Parallel()

		// An unresolvable client_id and an absent one are both "the check has
		// nothing to compare", and they must not be the same branch. This one is
		// a failed request on both paths, because it is only reachable in a
		// deployment whose authorization server resolves its clients from some
		// other table — where treating the miss as nothing to check would skip
		// this comparison on every request without saying so.
		authenticated, resolved := onBothPaths(t,
			tenantA, &fakeRegistry{err: oauth2clients.ErrClientNotFound}, "cid-1")

		test.ErrorIs(t, authenticated.err, authserver.ErrClientNotRegistered)
		test.False(t, authenticated.issued())

		test.ErrorIs(t, resolved.err, authserver.ErrClientNotRegistered)
		test.False(t, resolved.issued())
	})

	T.Run("a request naming no client is left alone on both paths", func(t *testing.T) {
		t.Parallel()

		// The other half of the case above, and the reason the two are separate
		// branches. The authorization server has already refused a request that
		// names no client, before either seam is reached, so neither invents a
		// second refusal — and neither reads the registry, which is what the
		// panicking fake asserts.
		authenticated, resolved := onBothPaths(t, tenantA, &unreadRegistry{}, "")

		test.NoError(t, authenticated.err)
		test.True(t, authenticated.issued())

		test.NoError(t, resolved.err)
		test.True(t, resolved.issued())
	})

	T.Run("an admitting registration is issued a subject on both paths", func(t *testing.T) {
		t.Parallel()

		// The control. Without it every assertion above is satisfied by a guard
		// that refuses everything.
		authenticated, resolved := onBothPaths(t, tenantA, &fakeRegistry{client: registration(tenantA, "")}, "cid-1")

		test.NoError(t, authenticated.err)
		must.True(t, authenticated.issued())

		test.NoError(t, resolved.err)
		must.True(t, resolved.issued())

		test.EqOp(t, authenticated.subject.ID, resolved.subject.ID)
	})
}
