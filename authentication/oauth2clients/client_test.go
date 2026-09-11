package oauth2clients_test

import (
	"context"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/oauth2clients"

	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// TestClientCloneIsDeep is what stops a hook from editing the row the store is
// still holding.
//
// A shallow copy shares the two slices and the two time pointers, so a consumer
// writing an audit entry that normalized a redirect URI would be normalizing the
// value the caller is about to be handed back — at a distance, with nothing in
// either file suggesting the two are the same memory.
func TestClientCloneIsDeep(T *testing.T) {
	T.Parallel()

	updated := time.Date(2026, time.September, 7, 12, 0, 0, 0, time.UTC)
	archived := updated.Add(time.Hour)

	original := &oauth2clients.Client{
		Scope:         tenancy.Of("acct_1"),
		BelongsToUser: "user_1",
		ID:            "row_1",
		ClientID:      "cid_1",
		SecretHash:    "digest",
		Name:          "test client",
		RedirectURIs:  []string{"https://example.test/callback"},
		Scopes:        []string{"read"},
		LastUpdatedAt: &updated,
		ArchivedAt:    &archived,
	}

	clone := original.Clone()
	must.NotNil(T, clone)
	test.Eq(T, original, clone)

	// Every reference the struct carries is its own.
	clone.RedirectURIs[0] = "https://elsewhere.test/callback"
	clone.Scopes[0] = "write"
	*clone.LastUpdatedAt = updated.Add(-time.Hour)
	*clone.ArchivedAt = archived.Add(-time.Hour)
	clone.Name = "renamed"

	test.EqOp(T, "https://example.test/callback", original.RedirectURIs[0])
	test.EqOp(T, "read", original.Scopes[0])
	test.EqOp(T, updated, *original.LastUpdatedAt)
	test.EqOp(T, archived, *original.ArchivedAt)
	test.EqOp(T, "test client", original.Name)
}

// TestClientCloneToleratesTheAbsences keeps the copy from being the thing that
// panics on a row nobody has revised.
func TestClientCloneToleratesTheAbsences(T *testing.T) {
	T.Parallel()

	test.Nil(T, (*oauth2clients.Client)(nil).Clone())

	bare := (&oauth2clients.Client{ID: "row_1"}).Clone()
	must.NotNil(T, bare)

	test.Nil(T, bare.RedirectURIs)
	test.Nil(T, bare.Scopes)
	test.Nil(T, bare.LastUpdatedAt)
	test.Nil(T, bare.ArchivedAt)
}

// TestAdministeredIsAValueAndNotAnAbsence is the reading the whole package rests
// on, stated where a reader looking for it would look.
//
// An empty BelongsToUser is not a missing owner: it names the arrangement with
// the *wider* reach, the one Client.Admits lets any subject in the registry
// authorize through. The method exists so callers ask the question rather than
// writing a comparison that reads as a missing-value check.
func TestAdministeredIsAValueAndNotAnAbsence(T *testing.T) {
	T.Parallel()

	test.True(T, (&oauth2clients.Client{}).Administered())
	test.False(T, (&oauth2clients.Client{BelongsToUser: "user_1"}).Administered())
}

// TestNoopHooksDoNothingToEveryOperation is what a Service built without
// WithHooks runs.
//
// All three, because they are three methods and a consumer embedding NoopHooks
// to implement one of them relies on the other two staying silent — a noop that
// grew an error would fail the transaction of an operation the consumer never
// asked to hook.
func TestNoopHooksDoNothingToEveryOperation(T *testing.T) {
	T.Parallel()

	var hooks oauth2clients.Hooks = oauth2clients.NoopHooks{}

	scope := tenancy.Of("acct_1")
	client := &oauth2clients.Client{ID: "row_1"}

	// A nil transaction, because a noop must not reach for one.
	test.NoError(T, hooks.AfterCreateClient(T.Context(), nil, scope, client))
	test.NoError(T, hooks.AfterUpdateClient(T.Context(), nil, scope, client))
	test.NoError(T, hooks.AfterArchiveClient(T.Context(), nil, scope, client))
}

// auditOnlyHooks is the shape the NoopHooks documentation tells a consumer to
// write: one method that matters, and the rest inherited.
//
// It never spells AfterUpdateClient or AfterArchiveClient, which is the whole
// claim — those two arrive from the embedded type, and a fourth added to Hooks
// later would arrive the same way rather than as a compile failure here.
type auditOnlyHooks struct {
	oauth2clients.NoopHooks

	created []string
}

var _ oauth2clients.Hooks = (*auditOnlyHooks)(nil)

func (h *auditOnlyHooks) AfterCreateClient(
	_ context.Context, _ database.Tx, _ tenancy.Scope, client *oauth2clients.Client,
) error {
	h.created = append(h.created, client.ID)

	return nil
}

// TestEmbeddingNoopHooksOverridesOneMethodAndInheritsTheRest pins what the
// documentation on Hooks and NoopHooks promises a consumer.
//
// The two the embedder did not write must stay silent, because an embedder
// relies on that: a consumer recording registrations has opted into one of the
// three operations, and an inherited method that returned an error would abort
// the two they never asked about.
func TestEmbeddingNoopHooksOverridesOneMethodAndInheritsTheRest(T *testing.T) {
	T.Parallel()

	var hooks oauth2clients.Hooks = &auditOnlyHooks{}

	scope := tenancy.Of("acct_1")
	client := &oauth2clients.Client{ID: "row_1"}

	// A nil transaction, because the inherited methods must not reach for one
	// and the override here does not write.
	must.NoError(T, hooks.AfterCreateClient(T.Context(), nil, scope, client))
	test.NoError(T, hooks.AfterUpdateClient(T.Context(), nil, scope, client))
	test.NoError(T, hooks.AfterArchiveClient(T.Context(), nil, scope, client))

	recorded, ok := hooks.(*auditOnlyHooks)
	must.True(T, ok)
	test.Eq(T, []string{"row_1"}, recorded.created)
}
