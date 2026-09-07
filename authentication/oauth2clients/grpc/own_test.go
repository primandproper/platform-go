package grpc_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/oauth2clients"
	oauth2clientsgrpc "github.com/primandproper/platform-go/v14/authentication/oauth2clients/grpc"
	"github.com/primandproper/platform-go/v14/authentication/oauth2clients/oauth2clientspb"
	"github.com/primandproper/platform-go/v14/filtering"
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
