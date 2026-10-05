package audit

import (
	"bytes"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v15/callers"

	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// impersonatedEntry is an operator's act, filed under the customer they were
// acting as.
func impersonatedEntry(scope tenancy.Scope, resourceID string) *Entry {
	entry := entryFor(scope, resourceID)
	entry.Actor = Actor{ID: "customer", Type: ActorUser, IP: "198.51.100.4", Impersonator: "operator"}

	return entry
}

func TestRecorder_RecordsTheImpersonator(T *testing.T) {
	T.Parallel()

	T.Run("files the entry under the subject and keeps the operator", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		reader := newTestReader(t, client)
		scope := tenancy.Of("acct_1")

		entry := impersonatedEntry(scope, "article_1")
		record(t, client, newTestRecorder(t, newStubClock()), entry)

		got, err := reader.Get(t.Context(), client.Reader(), scope, entry.ID)
		must.NoError(t, err)
		test.EqOp(t, "customer", got.Actor.ID)
		test.EqOp(t, "operator", got.Actor.Impersonator)
		test.EqOp(t, entry.Hash, got.Hash)
	})

	T.Run("verifies a chain mixing impersonated and ordinary entries", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		reader := newTestReader(t, client)
		scope := tenancy.Of("acct_1")

		record(t, client, newTestRecorder(t, newStubClock()),
			entryFor(scope, "article_1"),
			impersonatedEntry(scope, "article_2"),
			entryFor(scope, "article_3"),
		)

		result, err := reader.Verify(t.Context(), client.Reader(), scope, time.Time{}, time.Time{}, ChainStart)
		must.NoError(t, err)
		test.True(t, result.Intact())
		test.EqOp(t, 3, result.Checked)
	})

	// The reason it is inside the hash: an operator who could clear the column
	// afterwards could make their impersonation read as the customer's own act.
	T.Run("detects an impersonator removed from the row", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		reader := newTestReader(t, client)
		scope := tenancy.Of("acct_1")

		entry := impersonatedEntry(scope, "article_1")
		record(t, client, newTestRecorder(t, newStubClock()), entry)

		exec(t, client, "UPDATE audit_log_entries SET actor_impersonator = '' WHERE id = ?", entry.ID)

		result, err := reader.Verify(t.Context(), client.Reader(), scope, time.Time{}, time.Time{}, ChainStart)
		must.NoError(t, err)
		must.NotNil(t, result.FirstBreak)
		test.EqOp(t, BreakContentAltered, result.FirstBreak.Reason)
		test.EqOp(t, entry.ID, result.FirstBreak.EntryID)
	})

	T.Run("detects an impersonator added to an ordinary row", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		reader := newTestReader(t, client)
		scope := tenancy.Of("acct_1")

		entry := entryFor(scope, "article_1")
		record(t, client, newTestRecorder(t, newStubClock()), entry)

		exec(t, client, "UPDATE audit_log_entries SET actor_impersonator = 'somebody' WHERE id = ?", entry.ID)

		result, err := reader.Verify(t.Context(), client.Reader(), scope, time.Time{}, time.Time{}, ChainStart)
		must.NoError(t, err)
		must.NotNil(t, result.FirstBreak)
		test.EqOp(t, BreakContentAltered, result.FirstBreak.Reason)
	})
}

func TestCanonicalImage_Impersonator(T *testing.T) {
	T.Parallel()

	T.Run("an ordinary entry keeps the first framing", func(t *testing.T) {
		t.Parallel()

		// Every entry recorded before the column existed has a hash taken over
		// this framing, and still has to verify.
		image := canonicalImage(entryFor(tenancy.Of("acct_1"), "article_1"), nil, nil)
		test.True(t, bytes.Contains(image, []byte(imageVersion)))
		test.False(t, bytes.Contains(image, []byte(impersonatedImageVersion)))
	})

	T.Run("an impersonated entry is framed apart and carries the operator", func(t *testing.T) {
		t.Parallel()

		image := canonicalImage(impersonatedEntry(tenancy.Of("acct_1"), "article_1"), nil, nil)
		test.True(t, bytes.Contains(image, []byte(impersonatedImageVersion)))
		test.True(t, bytes.HasSuffix(image, []byte("operator")))
	})
}

func TestReader_ListByImpersonator(T *testing.T) {
	T.Parallel()

	client := newTestClient(T)
	reader := newTestReader(T, client)
	scope := tenancy.Of("acct_1")

	impersonated := impersonatedEntry(scope, "article_1")

	own := entryFor(scope, "article_2")
	own.Actor.ID = "operator"

	record(T, client, newTestRecorder(T, newStubClock()), impersonated, own, entryFor(scope, "article_3"))

	list := func(t *testing.T, query *Query) []string {
		t.Helper()

		page, err := reader.List(t.Context(), client.Reader(), scope, query, filtering.DefaultQueryFilter())
		must.NoError(t, err)

		ids := make([]string, 0, len(page.Data))
		for _, entry := range page.Data {
			ids = append(ids, entry.ID)
		}

		return ids
	}

	T.Run("finds what an operator did as somebody else", func(t *testing.T) {
		t.Parallel()

		test.Eq(t, []string{impersonated.ID}, list(t, &Query{ImpersonatorID: "operator"}))
	})

	T.Run("files an impersonated act under the subject", func(t *testing.T) {
		t.Parallel()

		test.Eq(t, []string{impersonated.ID}, list(t, &Query{ActorID: "customer"}))
	})

	T.Run("keeps the operator's own acts under their own id", func(t *testing.T) {
		t.Parallel()

		test.Eq(t, []string{own.ID}, list(t, &Query{ActorID: "operator"}))
	})
}

func TestErasure_CountMentionsCountsTheImpersonator(T *testing.T) {
	T.Parallel()

	client := newTestClient(T)
	record(T, client, newTestRecorder(T, newStubClock()), impersonatedEntry(tenancy.Of("acct_9"), "article_1"))

	for _, subject := range []string{"operator", "customer"} {
		count, err := newTestErasure(T).CountMentions(T.Context(), client.Reader(), subject)
		must.NoError(T, err)
		test.EqOp(T, int64(1), count, test.Sprintf("subject %s", subject))
	}
}

// delegatedPrincipal is a session type that carries the second slot.
type delegatedPrincipal struct {
	userID, actorID string
}

func (p *delegatedPrincipal) UserID() string          { return p.userID }
func (p *delegatedPrincipal) Scope() tenancy.Scope    { return tenancy.Global() }
func (p *delegatedPrincipal) ActiveAccountID() string { return "" }
func (p *delegatedPrincipal) ActorID() string         { return p.actorID }

func TestPrincipalActor(T *testing.T) {
	T.Parallel()

	T.Run("files a delegated principal under its user and names the actor", func(t *testing.T) {
		t.Parallel()

		got := PrincipalActor(&delegatedPrincipal{userID: "customer", actorID: "operator"})
		test.Eq(t, Actor{ID: "customer", Type: ActorUser, Impersonator: "operator"}, got)
	})

	T.Run("names no impersonator for a principal acting for itself", func(t *testing.T) {
		t.Parallel()

		got := PrincipalActor(&delegatedPrincipal{userID: "customer"})
		test.Eq(t, Actor{ID: "customer", Type: ActorUser}, got)
	})

	T.Run("is the zero actor for nobody", func(t *testing.T) {
		t.Parallel()

		var nobody callers.Principal

		test.Eq(t, Actor{}, PrincipalActor(nobody))
	})
}
