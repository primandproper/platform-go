package oauth2clients_test

import (
	"errors"
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/oauth2clients"

	"github.com/primandproper/primitives-go/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// tenantA and tenantB are two registries, named once so that a test asserting a
// refusal cannot accidentally compare a scope with itself.
var (
	tenantA = tenancy.Of("tenant-a")
	tenantB = tenancy.Of("tenant-b")
)

func TestClientAdmits(T *testing.T) {
	T.Parallel()

	T.Run("a global registration with no owner admits anybody", func(t *testing.T) {
		t.Parallel()

		// This is the administered arrangement: an operator minted the client to
		// speak for the service, and whoever signs in is who it speaks for.
		client := &oauth2clients.Client{ClientID: "abc", Scope: tenancy.Global()}

		for _, scope := range []tenancy.Scope{tenancy.Global(), tenantA, tenantB} {
			must.NoError(t, client.Admits(scope, "user-1"))
			must.NoError(t, client.Admits(scope, "user-2"))
		}
	})

	T.Run("a registration in one registry refuses a subject in another", func(t *testing.T) {
		t.Parallel()

		// The cross-tenant guarantee, and the reason the store's lookup can
		// afford to take no scope.
		client := &oauth2clients.Client{ClientID: "abc", Scope: tenantA}

		must.NoError(t, client.Admits(tenantA, "user-1"))

		test.ErrorIs(t, client.Admits(tenantB, "user-1"), oauth2clients.ErrClientScopeMismatch)
		test.ErrorIs(t, client.Admits(tenancy.Global(), "user-1"), oauth2clients.ErrClientScopeMismatch)
	})

	T.Run("a registration with an owner refuses everybody else", func(t *testing.T) {
		t.Parallel()

		// A personal credential that could sign somebody else in is an account
		// takeover with a client_id in front of it.
		client := &oauth2clients.Client{ClientID: "abc", Scope: tenantA, BelongsToUser: "user-1"}

		must.NoError(t, client.Admits(tenantA, "user-1"))
		test.ErrorIs(t, client.Admits(tenantA, "user-2"), oauth2clients.ErrClientOwnerMismatch)
	})

	T.Run("an owned registration in the global registry still refuses everybody else", func(t *testing.T) {
		t.Parallel()

		// The two facts are independent, and this is the pair that proves it:
		// the global scope waives the registry check and not the owner check.
		// A single-tenant deployment passes Global() everywhere, and its
		// people's personal credentials still have to be theirs.
		client := &oauth2clients.Client{ClientID: "abc", Scope: tenancy.Global(), BelongsToUser: "user-1"}

		must.NoError(t, client.Admits(tenancy.Global(), "user-1"))
		test.ErrorIs(t, client.Admits(tenancy.Global(), "user-2"), oauth2clients.ErrClientOwnerMismatch)
	})

	T.Run("the registry is answered before the owner", func(t *testing.T) {
		t.Parallel()

		// Both are wrong here, and the scope is the one reported: it is the
		// coarser fact, and its message is the one a person in the wrong
		// organization can act on. Reporting the owner instead would tell a
		// stranger that this client belongs to somebody.
		client := &oauth2clients.Client{ClientID: "abc", Scope: tenantA, BelongsToUser: "user-1"}

		err := client.Admits(tenantB, "user-2")
		test.ErrorIs(t, err, oauth2clients.ErrClientScopeMismatch)
		test.False(t, errors.Is(err, oauth2clients.ErrClientOwnerMismatch))
	})

	T.Run("a nil registration admits nobody", func(t *testing.T) {
		t.Parallel()

		var client *oauth2clients.Client

		test.ErrorIs(t, client.Admits(tenantA, "user-1"), oauth2clients.ErrNilClient)
	})
}
