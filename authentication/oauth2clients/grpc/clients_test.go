package grpc_test

import (
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/oauth2clients"
	oauth2clientsgrpc "github.com/primandproper/platform-go/v14/authentication/oauth2clients/grpc"
	"github.com/primandproper/platform-go/v14/authentication/oauth2clients/oauth2clientspb"
	"github.com/primandproper/platform-go/v14/filtering"
	"github.com/primandproper/platform-go/v14/filtering/filteringpb"
	"github.com/primandproper/platform-go/v14/identifiers"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestAdministeredCreateMintsARegistrationBelongingToNobody is the sharpest
// property on this half of the surface, and it is the one the code spells out
// rather than reads off the request.
//
// An administered registration has an empty belongs_to_user, which is what
// oauth2clients.Client.Admits reads as "any subject in this registry may
// authorize through it". So the owner cannot come off the wire: a request that
// could name one is a request that mints a credential in somebody else's name,
// and this asserts that the caller's own identifier does not become the owner
// either.
func TestAdministeredCreateMintsARegistrationBelongingToNobody(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	res, err := h.server.CreateOAuth2Client(h.ctx(t, testOwner),
		&oauth2clientspb.CreateOAuth2ClientRequest{Input: creationInput()})
	must.NoError(t, err)
	must.NotNil(t, res)

	test.EqOp(t, "", res.GetIssued().GetClient().GetBelongsToUser(),
		test.Sprint("the administered create wrote the caller as the owner"))

	// The secret is on the wire exactly once, and this is the message. A
	// registration whose secret never reached its creator is one nobody can use.
	test.NotEq(t, "", res.GetIssued().GetClientSecret())

	// And it is not in the row. The plaintext is returned once and never stored,
	// so the read-back carries the registration without the credential.
	read, err := h.server.GetOAuth2Client(h.ctx(t, testOwner),
		&oauth2clientspb.GetOAuth2ClientRequest{Oauth2ClientId: res.GetIssued().GetClient().GetId()})
	must.NoError(t, err)
	test.EqOp(t, res.GetIssued().GetClient().GetId(), read.GetResult().GetId())
	test.EqOp(t, "", read.GetResult().GetBelongsToUser())
}

// TestAdministeredHalfReachesEveryRowInTheRegistry is the difference between
// this half and the self-service one, asserted rather than described.
//
// The five RPCs here are behind a permission precisely because they are not
// keyed on the caller: an administrator reads, revises and withdraws another
// person's registration and the deployment's own. The self-service suite asserts
// the opposite of the same three rows, which is what makes the pair meaningful.
func TestAdministeredHalfReachesEveryRowInTheRegistry(T *testing.T) {
	T.Parallel()

	for _, owner := range []struct {
		name  string
		owner string
	}{
		{name: "another person's registration", owner: otherOwner},
		{name: "an administered registration", owner: ""},
	} {
		T.Run(owner.name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			theirs := h.seed(t, owner.owner)
			ctx := h.ctx(t, testOwner)

			read, err := h.server.GetOAuth2Client(ctx,
				&oauth2clientspb.GetOAuth2ClientRequest{Oauth2ClientId: theirs.ID})
			must.NoError(t, err)
			test.EqOp(t, theirs.ID, read.GetResult().GetId())

			revised, err := h.server.UpdateOAuth2Client(ctx, &oauth2clientspb.UpdateOAuth2ClientRequest{
				Oauth2ClientId: theirs.ID,
				Input:          updateInput("renamed"),
			})
			must.NoError(t, err)
			test.EqOp(t, "renamed", revised.GetResult().GetName())

			_, err = h.server.ArchiveOAuth2Client(ctx,
				&oauth2clientspb.ArchiveOAuth2ClientRequest{Oauth2ClientId: theirs.ID})
			must.NoError(t, err)

			// Withdrawn means the read no longer answers with it. The row is
			// still there — archiving is a soft delete, and the authorization
			// server's lookup has to find it in order to refuse it by name — but
			// nothing on this surface reaches it again.
			_, err = h.server.GetOAuth2Client(ctx,
				&oauth2clientspb.GetOAuth2ClientRequest{Oauth2ClientId: theirs.ID})
			must.Error(t, err)
			test.ErrorIs(t, err, oauth2clients.ErrClientNotFound)
		})
	}
}

// TestAdministeredListPagesTheWholeRegistry pins the same reach for the page,
// which is the one RPC here whose answer is a set rather than a row.
func TestAdministeredListPagesTheWholeRegistry(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	mine := h.seed(t, testOwner)
	theirs := h.seed(t, otherOwner)
	administered := h.seed(t, "")

	res, err := h.server.ListOAuth2Clients(h.ctx(t, testOwner), &oauth2clientspb.ListOAuth2ClientsRequest{})
	must.NoError(t, err)
	must.NotNil(t, res)

	ids := make([]string, 0, len(res.GetResults()))
	for _, c := range res.GetResults() {
		ids = append(ids, c.GetId())
	}

	test.SliceContains(t, ids, mine.ID)
	test.SliceContains(t, ids, theirs.ID)
	test.SliceContains(t, ids, administered.ID)

	// The pagination describes the page that was served, which is what a client
	// walks the cursor with.
	test.NotNil(t, res.GetPagination())
}

// TestAdministeredHalfIsScopedToTheCallersRegistry is the tenancy obligation on
// this surface: the scope comes off the principal, so a registration in another
// registry is not reachable by naming its identifier.
func TestAdministeredHalfIsScopedToTheCallersRegistry(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	theirs := h.seed(t, testOwner)

	// The same identifier, asked for by somebody whose principal puts them in a
	// different registry.
	elsewhere := withPrincipal(t.Context(), &testPrincipal{userID: testOwner, scope: otherScope})

	res, err := h.server.GetOAuth2Client(elsewhere,
		&oauth2clientspb.GetOAuth2ClientRequest{Oauth2ClientId: theirs.ID})
	must.Error(t, err)
	test.Nil(t, res)
	test.ErrorIs(t, err, oauth2clients.ErrClientNotFound)
	test.EqOp(t, codes.NotFound, status.Code(err))
}

// TestAdministeredHalfRefusesAnAnonymousCaller is the refusal every RPC on this
// surface owes before it does anything, made in Server.caller.
//
// It is asserted per method because each one reaches it separately, and a method
// added later that resolved its own principal would pass a suite that only
// checked one of them.
func TestAdministeredHalfRefusesAnAnonymousCaller(T *testing.T) {
	T.Parallel()

	// No principal on the context at all, which is what an unauthenticated
	// request looks like once a consumer's interceptor has run and found nobody.
	anonymous := func(t *testing.T) *harness { t.Helper(); return newHarness(t) }

	refused := func(t *testing.T, err error) {
		t.Helper()

		must.Error(t, err)
		test.ErrorIs(t, err, oauth2clientsgrpc.ErrNoPrincipal)
		test.EqOp(t, codes.Unauthenticated, status.Code(err))
	}

	T.Run("create", func(t *testing.T) {
		t.Parallel()

		h := anonymous(t)

		res, err := h.server.CreateOAuth2Client(t.Context(),
			&oauth2clientspb.CreateOAuth2ClientRequest{Input: creationInput()})
		refused(t, err)
		test.Nil(t, res)

		// Before the write, which is the only thing that makes the refusal worth
		// anything: an administered registration minted anonymously is a
		// credential that authorizes anybody in the registry.
		page, listErr := h.store.ListClients(t.Context(), h.db.Reader(), testScope, filtering.DefaultQueryFilter())
		must.NoError(t, listErr)
		test.SliceEmpty(t, page.Data)
	})

	T.Run("get", func(t *testing.T) {
		t.Parallel()

		h := anonymous(t)
		seeded := h.seed(t, "")

		res, err := h.server.GetOAuth2Client(t.Context(),
			&oauth2clientspb.GetOAuth2ClientRequest{Oauth2ClientId: seeded.ID})
		refused(t, err)
		test.Nil(t, res)
	})

	T.Run("list", func(t *testing.T) {
		t.Parallel()

		h := anonymous(t)

		res, err := h.server.ListOAuth2Clients(t.Context(), &oauth2clientspb.ListOAuth2ClientsRequest{})
		refused(t, err)
		test.Nil(t, res)
	})

	T.Run("update", func(t *testing.T) {
		t.Parallel()

		h := anonymous(t)
		seeded := h.seed(t, "")

		res, err := h.server.UpdateOAuth2Client(t.Context(), &oauth2clientspb.UpdateOAuth2ClientRequest{
			Oauth2ClientId: seeded.ID,
			Input:          updateInput("renamed"),
		})
		refused(t, err)
		test.Nil(t, res)

		after, readErr := h.store.GetClient(t.Context(), h.db.Reader(), testScope, seeded.ID)
		must.NoError(t, readErr)
		test.EqOp(t, seeded.Name, after.Name)
	})

	T.Run("archive", func(t *testing.T) {
		t.Parallel()

		h := anonymous(t)
		seeded := h.seed(t, "")

		res, err := h.server.ArchiveOAuth2Client(t.Context(),
			&oauth2clientspb.ArchiveOAuth2ClientRequest{Oauth2ClientId: seeded.ID})
		refused(t, err)
		test.Nil(t, res)

		after, readErr := h.store.GetClient(t.Context(), h.db.Reader(), testScope, seeded.ID)
		must.NoError(t, readErr)
		test.False(t, after.Archived())
	})
}

// TestAdministeredWritesRefuseARequestWithNoInput pins the distinction the
// converters make: a nil input message is a malformed request rather than a
// registration with no name.
//
// The difference is what the caller is told. Refusing it here is
// InvalidArgument naming the input; letting it through would reach the store and
// come back as a message about the name field, for a request that carried no
// fields at all.
func TestAdministeredWritesRefuseARequestWithNoInput(T *testing.T) {
	T.Parallel()

	T.Run("create", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		res, err := h.server.CreateOAuth2Client(h.ctx(t, testOwner),
			&oauth2clientspb.CreateOAuth2ClientRequest{})
		must.Error(t, err)
		test.Nil(t, res)
		test.ErrorIs(t, err, oauth2clients.ErrNilInput)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})

	T.Run("update", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		seeded := h.seed(t, "")

		res, err := h.server.UpdateOAuth2Client(h.ctx(t, testOwner),
			&oauth2clientspb.UpdateOAuth2ClientRequest{Oauth2ClientId: seeded.ID})
		must.Error(t, err)
		test.Nil(t, res)
		test.ErrorIs(t, err, oauth2clients.ErrNilInput)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})
}

// TestAdministeredListRefusesAFilterItCannotRead is the other InvalidArgument on
// this half, and it is a different failure from a broken database.
//
// The page's default is codes.Internal, which is right for a store that failed
// and wrong for a sort direction nobody recognizes — the caller can fix the
// second and has nothing to do about the first.
func TestAdministeredListRefusesAFilterItCannotRead(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	sideways := "sideways"

	res, err := h.server.ListOAuth2Clients(h.ctx(t, testOwner), &oauth2clientspb.ListOAuth2ClientsRequest{
		Filter: &filteringpb.QueryFilter{SortBy: &sideways},
	})
	must.Error(t, err)
	test.Nil(t, res)
	test.EqOp(t, codes.InvalidArgument, status.Code(err))
}

// TestAdministeredReadsAreNotAnEnumerationOracleAcrossRegistries is the same
// property the self-service suite asserts for owners, one level up.
//
// A registration in another registry and one that was never minted are the same
// answer, so a caller holding a permission in their own registry cannot use it
// to learn which identifiers exist in anybody else's.
func TestAdministeredReadsAreNotAnEnumerationOracleAcrossRegistries(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	elsewhere := h.seed(t, testOwner)

	other := withPrincipal(t.Context(), &testPrincipal{userID: testOwner, scope: otherScope})

	_, crossRegistry := h.server.GetOAuth2Client(other,
		&oauth2clientspb.GetOAuth2ClientRequest{Oauth2ClientId: elsewhere.ID})
	must.Error(t, crossRegistry)

	_, neverMinted := h.server.GetOAuth2Client(other,
		&oauth2clientspb.GetOAuth2ClientRequest{Oauth2ClientId: identifiers.New()})
	must.Error(t, neverMinted)

	test.EqOp(t, status.Code(neverMinted), status.Code(crossRegistry))
}
