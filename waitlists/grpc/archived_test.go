package grpc_test

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v14/waitlists"
	waitlistsgrpc "github.com/primandproper/platform-go/v14/waitlists/grpc"
	"github.com/primandproper/platform-go/v14/waitlists/waitlistspb"

	"github.com/primandproper/primitives-go/v2/authorization"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// The suite's stand-in for the half of a consumer's wiring that says what the
// caller may do, and the four reads' answer to a client that asked for the rows
// somebody archived.
//
// The extractor is what a deployment hands both this surface and
// primitives-go's authorization/grpc enforcer. It is not the enforcer: nothing
// in these tests checks whether the method may be called at all, because that
// check is an interceptor's and runs before the handler. What is under test is
// the second question — which rows the answer may contain — and it is asked
// inside the handler, where the filter is.
//
// This service archives two nouns under two grants, so every case below names
// which one it is about.

// grantsKey is where this suite puts the caller's authority.
type grantsKey struct{}

// withGrants narrows a request context to exactly the permissions named, which
// is how a test describes a caller who may read and may not archive.
func withGrants(ctx context.Context, perms ...authorization.Permission) context.Context {
	return context.WithValue(ctx, grantsKey{}, authorization.NewGrants(authorization.NewPermissionSet(perms...)))
}

// extractGrants is the authorization.GrantsExtractor every harness here is built
// with.
//
// A context nothing narrowed carries every grant, because the rest of this suite
// is about what a handler does and not about what a policy allows — a default of
// "nothing" would make every existing test a test of this file. A test that cares
// says so with [withGrants].
func extractGrants(ctx context.Context) (authorization.Grants, bool) {
	grants, ok := ctx.Value(grantsKey{}).(authorization.Grants)
	if !ok {
		return authorization.AllowAll(), true
	}

	return grants, true
}

// archiveList retires a seeded list directly through the store, so the row a
// test is about was withdrawn without going through the surface the test is
// about.
func (h *harness) archiveList(tb testing.TB, scope tenancy.Scope, listID string) {
	tb.Helper()

	must.NoError(tb, h.db.WithTransaction(tb.Context(), func(tx database.Tx) error {
		_, err := h.store.ArchiveList(tb.Context(), tx, scope, listID)

		return err
	}))
}

// archiveSignup retires a seeded signup the same way.
func (h *harness) archiveSignup(tb testing.TB, scope tenancy.Scope, listID, signupID string) {
	tb.Helper()

	must.NoError(tb, h.db.WithTransaction(tb.Context(), func(tx database.Tx) error {
		_, err := h.store.ArchiveSignup(tb.Context(), tx, scope, listID, signupID)

		return err
	}))
}

// seedSubjectSignup joins somebody who has an account, which is the only kind of
// signup ListSignupsForSubject can page.
func (h *harness) seedSubjectSignup(
	tb testing.TB,
	scope tenancy.Scope,
	listID, contact, userID string,
) *waitlists.Signup {
	tb.Helper()

	var signup *waitlists.Signup

	must.NoError(tb, h.db.WithTransaction(tb.Context(), func(tx database.Tx) error {
		joined, err := h.store.Join(tb.Context(), tx, scope, listID, &waitlists.Signup{
			Contact: contact,
			Subject: waitlists.Subject{Type: waitlists.SubjectUser, ID: userID},
		})
		if err != nil {
			return err
		}

		signup = joined

		return nil
	}))

	return signup
}

// listIDs and signupIDs are what every assertion below compares, because the
// rows' identity is the whole of the question: which rows came back.
func listIDs(results []*waitlistspb.Waitlist) []string {
	out := make([]string, 0, len(results))
	for _, result := range results {
		out = append(out, result.GetId())
	}

	return out
}

func signupIDs(results []*waitlistspb.Signup) []string {
	out := make([]string, 0, len(results))
	for _, result := range results {
		out = append(out, result.GetId())
	}

	return out
}

// TestIncludeArchivedIsAGrantAndNotAField is the ruling, executed, on all four
// paged reads.
//
// Each case seeds one live row and one somebody archived, sends
// include_archived: true, and asks the same question twice: as somebody holding
// only the grant the RPC requires, and as somebody who also holds the grant that
// archives the noun being paged.
func TestIncludeArchivedIsAGrantAndNotAField(T *testing.T) {
	T.Parallel()

	T.Run("ListLists hides the retired lists from a caller who cannot archive", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		live := h.seedOpenList(t, testScope)
		retired := h.seedOpenList(t, testScope)
		h.archiveList(t, testScope, retired.ID)

		res, err := h.server.ListLists(
			withGrants(h.ctx(t), waitlistsgrpc.PermissionReadLists),
			&waitlistspb.ListListsRequest{Filter: includeArchived()})
		must.NoError(t, err)

		test.Eq(t, []string{live.ID}, listIDs(res.GetResults()))
	})

	T.Run("ListLists answers the retired lists to a caller who can archive", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		live := h.seedOpenList(t, testScope)
		retired := h.seedOpenList(t, testScope)
		h.archiveList(t, testScope, retired.ID)

		res, err := h.server.ListLists(
			withGrants(h.ctx(t), waitlistsgrpc.PermissionReadLists, waitlistsgrpc.PermissionArchiveLists),
			&waitlistspb.ListListsRequest{Filter: includeArchived()})
		must.NoError(t, err)

		got := listIDs(res.GetResults())
		test.SliceContains(t, got, live.ID)
		test.SliceContains(t, got, retired.ID)
	})

	// The public RPC is under the same rule, and the rule is the whole of the
	// answer there: a visitor on a signup page carries no grants at all.
	T.Run("ListOpenLists hides the retired lists from the visitor it exists for", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		live := h.seedOpenList(t, testScope)
		retired := h.seedOpenList(t, testScope)
		h.archiveList(t, testScope, retired.ID)

		res, err := h.server.ListOpenLists(
			withGrants(h.anonCtx(t)),
			&waitlistspb.ListOpenListsRequest{Filter: includeArchived()})
		must.NoError(t, err)

		test.Eq(t, []string{live.ID}, listIDs(res.GetResults()))
	})

	T.Run("ListSignups hides the archived signups from a caller who cannot archive", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		list := h.seedOpenList(t, testScope)
		live := h.seedSignup(t, testScope, list.ID, "ada@example.com")
		retired := h.seedSignup(t, testScope, list.ID, "grace@example.com")
		h.archiveSignup(t, testScope, list.ID, retired.ID)

		res, err := h.server.ListSignups(
			withGrants(h.ctx(t), waitlistsgrpc.PermissionReadSignups),
			&waitlistspb.ListSignupsRequest{ListId: list.ID, Filter: includeArchived()})
		must.NoError(t, err)

		test.Eq(t, []string{live.ID}, signupIDs(res.GetResults()))
	})

	T.Run("ListSignups answers the archived signups to a caller who can archive", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		list := h.seedOpenList(t, testScope)
		live := h.seedSignup(t, testScope, list.ID, "ada@example.com")
		retired := h.seedSignup(t, testScope, list.ID, "grace@example.com")
		h.archiveSignup(t, testScope, list.ID, retired.ID)

		res, err := h.server.ListSignups(
			withGrants(h.ctx(t), waitlistsgrpc.PermissionReadSignups, waitlistsgrpc.PermissionArchiveSignups),
			&waitlistspb.ListSignupsRequest{ListId: list.ID, Filter: includeArchived()})
		must.NoError(t, err)

		got := signupIDs(res.GetResults())
		test.SliceContains(t, got, live.ID)
		test.SliceContains(t, got, retired.ID)
	})

	T.Run("ListSignupsForSubject hides the archived signups from a caller who cannot archive", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		list := h.seedOpenList(t, testScope)
		live := h.seedSubjectSignup(t, testScope, list.ID, "ada@example.com", testUser)

		other := h.seedOpenList(t, testScope)
		retired := h.seedSubjectSignup(t, testScope, other.ID, "ada@example.com", testUser)
		h.archiveSignup(t, testScope, other.ID, retired.ID)

		res, err := h.server.ListSignupsForSubject(
			withGrants(h.ctx(t), waitlistsgrpc.PermissionReadSignups),
			&waitlistspb.ListSignupsForSubjectRequest{
				Subject: &waitlistspb.SignupSubject{Type: string(waitlists.SubjectUser), Id: testUser},
				Filter:  includeArchived(),
			})
		must.NoError(t, err)

		test.Eq(t, []string{live.ID}, signupIDs(res.GetResults()))
	})

	T.Run("ListSignupsForSubject answers the archived signups to a caller who can archive", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		list := h.seedOpenList(t, testScope)
		live := h.seedSubjectSignup(t, testScope, list.ID, "ada@example.com", testUser)

		other := h.seedOpenList(t, testScope)
		retired := h.seedSubjectSignup(t, testScope, other.ID, "ada@example.com", testUser)
		h.archiveSignup(t, testScope, other.ID, retired.ID)

		res, err := h.server.ListSignupsForSubject(
			withGrants(h.ctx(t), waitlistsgrpc.PermissionReadSignups, waitlistsgrpc.PermissionArchiveSignups),
			&waitlistspb.ListSignupsForSubjectRequest{
				Subject: &waitlistspb.SignupSubject{Type: string(waitlists.SubjectUser), Id: testUser},
				Filter:  includeArchived(),
			})
		must.NoError(t, err)

		got := signupIDs(res.GetResults())
		test.SliceContains(t, got, live.ID)
		test.SliceContains(t, got, retired.ID)
	})

	// The two grants are separate, which is what makes them two: holding the one
	// that retires a list buys nothing on a page of signups.
	T.Run("the list grant does not reach the archived signups", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		list := h.seedOpenList(t, testScope)
		live := h.seedSignup(t, testScope, list.ID, "ada@example.com")
		retired := h.seedSignup(t, testScope, list.ID, "grace@example.com")
		h.archiveSignup(t, testScope, list.ID, retired.ID)

		res, err := h.server.ListSignups(
			withGrants(h.ctx(t), waitlistsgrpc.PermissionReadSignups, waitlistsgrpc.PermissionArchiveLists),
			&waitlistspb.ListSignupsRequest{ListId: list.ID, Filter: includeArchived()})
		must.NoError(t, err)

		test.Eq(t, []string{live.ID}, signupIDs(res.GetResults()))
	})

	// The narrowing is not a refusal, which is the half a caller notices: the
	// read succeeds and answers with what they were entitled to ask for.
	T.Run("clearing the field is not an error", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		h.seedOpenList(t, testScope)

		_, err := h.server.ListLists(
			withGrants(h.ctx(t), waitlistsgrpc.PermissionReadLists),
			&waitlistspb.ListListsRequest{Filter: includeArchived()})

		must.NoError(t, err)
	})

	// The fail-closed default. A consumer who wired no extractor cannot be told
	// apart from one whose caller holds nothing, so the surface answers the same
	// way rather than guessing.
	T.Run("a server built with no grants extractor clears the field", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, waitlistsgrpc.WithGrantsExtractor(nil))
		live := h.seedOpenList(t, testScope)
		retired := h.seedOpenList(t, testScope)
		h.archiveList(t, testScope, retired.ID)

		res, err := h.server.ListLists(h.ctx(t), &waitlistspb.ListListsRequest{Filter: includeArchived()})
		must.NoError(t, err)

		test.Eq(t, []string{live.ID}, listIDs(res.GetResults()))
	})

	// An extractor that reports no authority is the interceptor's "no grants
	// could be determined", and it is a denial everywhere else in the stack.
	T.Run("an extractor reporting no authority clears the field", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, waitlistsgrpc.WithGrantsExtractor(
			func(context.Context) (authorization.Grants, bool) { return authorization.AllowAll(), false }))

		live := h.seedOpenList(t, testScope)
		retired := h.seedOpenList(t, testScope)
		h.archiveList(t, testScope, retired.ID)

		res, err := h.server.ListLists(h.ctx(t), &waitlistspb.ListListsRequest{Filter: includeArchived()})
		must.NoError(t, err)

		test.Eq(t, []string{live.ID}, listIDs(res.GetResults()))
	})

	// A filter that never asked is left alone, which is what stops the
	// confinement from being a rewrite of everybody's page.
	T.Run("a filter that did not ask is untouched", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		live := h.seedOpenList(t, testScope)
		retired := h.seedOpenList(t, testScope)
		h.archiveList(t, testScope, retired.ID)

		res, err := h.server.ListLists(
			withGrants(h.ctx(t), waitlistsgrpc.PermissionReadLists, waitlistsgrpc.PermissionArchiveLists),
			&waitlistspb.ListListsRequest{})
		must.NoError(t, err)

		test.Eq(t, []string{live.ID}, listIDs(res.GetResults()))
	})
}
