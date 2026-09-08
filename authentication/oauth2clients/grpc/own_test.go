package grpc_test

import (
	"errors"
	"strings"
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

// creationInput is the smallest registration the store accepts.
func creationInput() *oauth2clientspb.OAuth2ClientCreationInput {
	return &oauth2clientspb.OAuth2ClientCreationInput{
		Name:         "test client",
		RedirectUris: []string{testRedirect},
	}
}

// updateInput is a revision under the named name.
//
// It carries the redirect URIs because an update is a replacement rather than a
// patch: the input describes the registration as it will stand, so one naming
// only a name is a registration with no redirect URI and is refused. Anything
// else would be a partial write deciding by omission which fields survive.
func updateInput(name string) *oauth2clientspb.OAuth2ClientUpdateInput {
	return &oauth2clientspb.OAuth2ClientUpdateInput{
		Name:         name,
		RedirectUris: []string{testRedirect},
	}
}

// TestSelfServiceRefusesACallerNamingNobody pins the refusal every self-service
// RPC owes, and it is the one that keeps the half safe without a permission.
//
// An empty owner is not an absent one on this surface: it is the value the
// administered CreateOAuth2Client writes into belongs_to_user, so a caller whose
// principal names nobody is a caller whose "own" rows are every administered
// credential in the registry. Each of the five is asserted separately because
// each reaches the owner by a different route — two read it directly, three
// through Server.own — and a fix applied to one is not a fix applied to all.
func TestSelfServiceRefusesACallerNamingNobody(t *testing.T) {
	t.Parallel()

	// refused asserts the shape of the answer: the sentinel is still matchable
	// through the chain, and the status a client reads is Unauthenticated rather
	// than the InvalidArgument an empty-parameter sentinel would have mapped to.
	refused := func(t *testing.T, err error) {
		t.Helper()

		must.Error(t, err)
		test.ErrorIs(t, err, oauth2clientsgrpc.ErrNoPrincipalUser)
		test.EqOp(t, codes.Unauthenticated, status.Code(err))
	}

	t.Run("create mints nothing", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		ctx := h.ctx(t, "")

		res, err := h.server.CreateOwnOAuth2Client(ctx,
			&oauth2clientspb.CreateOwnOAuth2ClientRequest{Input: creationInput()})
		refused(t, err)
		test.Nil(t, res)

		// The refusal is only worth anything if it happened before the write. An
		// administered registration minted through the public door is a
		// credential Client.Admits lets authorize anybody in the registry, and
		// PermissionCreateClients is the grant that is supposed to stand in
		// front of it.
		page, listErr := h.store.ListClients(t.Context(), h.db.Reader(), testScope, filtering.DefaultQueryFilter())
		must.NoError(t, listErr)
		test.SliceEmpty(t, page.Data)
	})

	t.Run("list pages nothing", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		h.seed(t, "")

		res, err := h.server.ListOwnOAuth2Clients(h.ctx(t, ""),
			&oauth2clientspb.ListOwnOAuth2ClientsRequest{})
		refused(t, err)
		test.Nil(t, res)
	})

	t.Run("get reads nothing", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		administered := h.seed(t, "")

		res, err := h.server.GetOwnOAuth2Client(h.ctx(t, ""),
			&oauth2clientspb.GetOwnOAuth2ClientRequest{Oauth2ClientId: administered.ID})
		refused(t, err)
		test.Nil(t, res)
	})

	t.Run("update revises nothing", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		administered := h.seed(t, "")

		res, err := h.server.UpdateOwnOAuth2Client(h.ctx(t, ""), &oauth2clientspb.UpdateOwnOAuth2ClientRequest{
			Oauth2ClientId: administered.ID,
			Input:          &oauth2clientspb.OAuth2ClientUpdateInput{Name: "renamed"},
		})
		refused(t, err)
		test.Nil(t, res)

		after, readErr := h.store.GetClient(t.Context(), h.db.Reader(), testScope, administered.ID)
		must.NoError(t, readErr)
		test.EqOp(t, administered.Name, after.Name)
	})

	t.Run("archive withdraws nothing", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		administered := h.seed(t, "")

		res, err := h.server.ArchiveOwnOAuth2Client(h.ctx(t, ""),
			&oauth2clientspb.ArchiveOwnOAuth2ClientRequest{Oauth2ClientId: administered.ID})
		refused(t, err)
		test.Nil(t, res)

		after, readErr := h.store.GetClient(t.Context(), h.db.Reader(), testScope, administered.ID)
		must.NoError(t, readErr)
		test.False(t, after.Archived())
	})
}

// TestSelfServiceReachesOnlyTheCallersRows is the other half of the same
// guarantee, for a caller who does name somebody: a registration owned by
// another person and one owned by nobody are the same answer, and it is a
// refusal.
func TestSelfServiceReachesOnlyTheCallersRows(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		owner string
	}{
		{name: "another person's", owner: otherOwner},
		{name: "an administered", owner: ""},
	} {
		t.Run(tc.name+" registration is not the caller's", func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			theirs := h.seed(t, tc.owner)

			res, err := h.server.GetOwnOAuth2Client(h.ctx(t, testOwner),
				&oauth2clientspb.GetOwnOAuth2ClientRequest{Oauth2ClientId: theirs.ID})
			must.Error(t, err)

			// The sentinel is in the chain, which is what this process's own
			// logs read. What the caller gets is NotFound — see below.
			test.ErrorIs(t, err, oauth2clients.ErrOwnerMismatch)
			test.EqOp(t, codes.NotFound, status.Code(err))
			test.Nil(t, res)
		})
	}

	t.Run("the caller's own registration is theirs", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		ctx := h.ctx(t, testOwner)

		created, err := h.server.CreateOwnOAuth2Client(ctx,
			&oauth2clientspb.CreateOwnOAuth2ClientRequest{Input: creationInput()})
		must.NoError(t, err)
		must.NotNil(t, created)
		test.EqOp(t, testOwner, created.GetIssued().GetClient().GetBelongsToUser())

		read, err := h.server.GetOwnOAuth2Client(ctx,
			&oauth2clientspb.GetOwnOAuth2ClientRequest{Oauth2ClientId: created.GetIssued().GetClient().GetId()})
		must.NoError(t, err)
		test.EqOp(t, created.GetIssued().GetClient().GetId(), read.GetResult().GetId())
	})
}

// TestSelfServiceDoesNotDiscloseWhichRefusalItMade is the anti-enumeration
// property, asserted on the wire rather than in the wording.
//
// A caller who can tell "somebody else's" from "does not exist" can walk the
// registry's identifiers and learn which ones exist — and an identifier here is
// an xid, which is a timestamp, a machine, a pid and a counter, so walking them
// is arithmetic. Matching the two messages does not close that; the status code
// discloses it on its own. So both arms are asserted to carry the same code and
// the same words, differing only in the identifier the caller themself sent —
// which is the strongest form the guarantee can take, and the form a matching
// message alone does not deliver.
func TestSelfServiceDoesNotDiscloseWhichRefusalItMade(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	theirs := h.seed(t, otherOwner)

	notMine, err := h.server.GetOwnOAuth2Client(h.ctx(t, testOwner),
		&oauth2clientspb.GetOwnOAuth2ClientRequest{Oauth2ClientId: theirs.ID})
	must.Error(t, err)
	test.Nil(t, notMine)

	mismatch := status.Convert(err)

	// A row identifier that is well-formed and belongs to nobody, which is what
	// the caller walking identifiers is holding.
	missing := identifiers.New()

	notThere, err := h.server.GetOwnOAuth2Client(h.ctx(t, testOwner),
		&oauth2clientspb.GetOwnOAuth2ClientRequest{Oauth2ClientId: missing})
	must.Error(t, err)
	test.Nil(t, notThere)

	absent := status.Convert(err)

	test.EqOp(t, absent.Code(), mismatch.Code())

	// Identical up to the identifier the caller themself sent, which is the
	// strongest form the property can take: the answer is a function of what was
	// asked, and carries nothing about what was found.
	test.EqOp(t, strings.ReplaceAll(absent.Message(), missing, theirs.ID), mismatch.Message())
}

// TestRefusalDoesNotMatchAnUnrelatedSentinel keeps the suite honest about the
// chain it asserts on: a status error that had flattened its cause would make
// every ErrorIs above pass vacuously against a different sentinel.
func TestRefusalDoesNotMatchAnUnrelatedSentinel(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	_, err := h.server.GetOwnOAuth2Client(h.ctx(t, ""),
		&oauth2clientspb.GetOwnOAuth2ClientRequest{Oauth2ClientId: "whatever"})
	must.Error(t, err)
	test.False(t, errors.Is(err, oauth2clients.ErrClientNotFound))
}

// TestSelfServicePagesOnlyTheCallersOwnRows is the read the whole half exists
// for, and the one whose failure would be silent.
//
// The three refusals above are visible: a caller gets an error. A page that
// answered with too much answers with a 200, so the assertion has to be on what
// is *absent* from it — another person's registration, and the administered one
// that belongs to nobody.
func TestSelfServicePagesOnlyTheCallersOwnRows(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	mine := h.seed(t, testOwner)
	theirs := h.seed(t, otherOwner)
	administered := h.seed(t, "")

	res, err := h.server.ListOwnOAuth2Clients(h.ctx(t, testOwner),
		&oauth2clientspb.ListOwnOAuth2ClientsRequest{})
	must.NoError(t, err)
	must.NotNil(t, res)

	ids := make([]string, 0, len(res.GetResults()))
	for _, c := range res.GetResults() {
		ids = append(ids, c.GetId())
	}

	test.SliceContains(t, ids, mine.ID)
	test.SliceNotContains(t, ids, theirs.ID,
		test.Sprint("another person's registration is on the caller's own page"))
	test.SliceNotContains(t, ids, administered.ID,
		test.Sprint("an administered registration is on the caller's own page"))

	test.NotNil(t, res.GetPagination())
}

// TestSelfServiceRevisesAndWithdrawsTheCallersOwnRows is the write half of the
// same reach, and the reason it is asserted at all is that the refusals above
// would pass just as well against a surface that refused everybody.
func TestSelfServiceRevisesAndWithdrawsTheCallersOwnRows(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	ctx := h.ctx(t, testOwner)

	created, err := h.server.CreateOwnOAuth2Client(ctx,
		&oauth2clientspb.CreateOwnOAuth2ClientRequest{Input: creationInput()})
	must.NoError(t, err)

	id := created.GetIssued().GetClient().GetId()

	revised, err := h.server.UpdateOwnOAuth2Client(ctx, &oauth2clientspb.UpdateOwnOAuth2ClientRequest{
		Oauth2ClientId: id,
		Input:          updateInput("renamed"),
	})
	must.NoError(t, err)
	test.EqOp(t, "renamed", revised.GetResult().GetName())

	// The revision does not reassign the owner, which is what keeps the check
	// above from being a formality.
	test.EqOp(t, testOwner, revised.GetResult().GetBelongsToUser())

	// A revised row reports when it was revised; a freshly minted one reports
	// nothing rather than 1970.
	test.Nil(t, created.GetIssued().GetClient().GetLastUpdatedAt())
	test.NotNil(t, revised.GetResult().GetLastUpdatedAt())

	_, err = h.server.ArchiveOwnOAuth2Client(ctx,
		&oauth2clientspb.ArchiveOwnOAuth2ClientRequest{Oauth2ClientId: id})
	must.NoError(t, err)

	_, err = h.server.GetOwnOAuth2Client(ctx, &oauth2clientspb.GetOwnOAuth2ClientRequest{Oauth2ClientId: id})
	must.Error(t, err)
	test.EqOp(t, codes.NotFound, status.Code(err))
}

// TestSelfServiceListRefusesAFilterItCannotRead is the InvalidArgument on this
// half, and it is a different failure from a broken database: the caller can fix
// a sort direction nobody recognizes and has nothing to do about a store that
// failed.
func TestSelfServiceListRefusesAFilterItCannotRead(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	sideways := "sideways"

	res, err := h.server.ListOwnOAuth2Clients(h.ctx(t, testOwner),
		&oauth2clientspb.ListOwnOAuth2ClientsRequest{
			Filter: &filteringpb.QueryFilter{SortBy: &sideways},
		})
	must.Error(t, err)
	test.Nil(t, res)
	test.EqOp(t, codes.InvalidArgument, status.Code(err))
}

// TestSelfServiceCreateRefusesARequestWithNoInput pins the same distinction the
// administered half makes: a nil input message is a malformed request rather
// than a registration with no name.
func TestSelfServiceCreateRefusesARequestWithNoInput(T *testing.T) {
	T.Parallel()

	T.Run("create", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		res, err := h.server.CreateOwnOAuth2Client(h.ctx(t, testOwner),
			&oauth2clientspb.CreateOwnOAuth2ClientRequest{})
		must.Error(t, err)
		test.Nil(t, res)
		test.ErrorIs(t, err, oauth2clients.ErrNilInput)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})

	T.Run("update", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		mine := h.seed(t, testOwner)

		// Refused before the ownership read, so a caller who named somebody
		// else's row and no input is told about the input rather than about the
		// row — which is the answer that discloses less.
		res, err := h.server.UpdateOwnOAuth2Client(h.ctx(t, testOwner),
			&oauth2clientspb.UpdateOwnOAuth2ClientRequest{Oauth2ClientId: mine.ID})
		must.Error(t, err)
		test.Nil(t, res)
		test.ErrorIs(t, err, oauth2clients.ErrNilInput)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})
}
