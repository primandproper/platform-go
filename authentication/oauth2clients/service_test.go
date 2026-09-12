package oauth2clients

import (
	"context"
	"testing"

	"github.com/primandproper/primitives-go/v2/authentication/oauth2server"
	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// recordingHooks records what ran inside each operation's transaction, which is
// the seam a consumer's audit entry and outbox row go through.
type recordingHooks struct {
	err      error
	created  []*Client
	updated  []*Client
	archived []*Client
}

var _ Hooks = (*recordingHooks)(nil)

func (h *recordingHooks) AfterCreateClient(
	_ context.Context, _ database.Tx, _ tenancy.Scope, client *Client,
) error {
	h.created = append(h.created, client)

	return h.err
}

func (h *recordingHooks) AfterUpdateClient(
	_ context.Context, _ database.Tx, _ tenancy.Scope, client *Client,
) error {
	h.updated = append(h.updated, client)

	return h.err
}

func (h *recordingHooks) AfterArchiveClient(
	_ context.Context, _ database.Tx, _ tenancy.Scope, client *Client,
) error {
	h.archived = append(h.archived, client)

	return h.err
}

// newService builds a service over a freshly migrated store.
func newService(tb testing.TB, env *storeEnv, opts ...ServiceOption) (*Service, *SQLStore) {
	tb.Helper()

	store := env.newStore(tb)

	svc, err := NewService(env.client, store, opts...)
	must.NoError(tb, err)

	return svc, store
}

func TestService_CreateClient(T *testing.T) {
	T.Parallel()

	env := newSQLiteEnv(T)

	T.Run("mints a credential, returns it once, and stores only its digest", func(t *testing.T) {
		t.Parallel()

		svc, store := newService(t, env)

		issued, err := svc.CreateClient(t.Context(), testScope, testOwner, &CreationInput{
			Name:         "a client",
			RedirectURIs: []string{testRedirect},
		})
		must.NoError(t, err)
		must.NotNil(t, issued)

		test.NotEq(t, "", issued.Secret)
		test.NotEq(t, "", issued.Client.ClientID)
		test.NotEq(t, issued.Client.ID, issued.Client.ClientID,
			test.Sprint("the row id and the protocol identifier are the same value"))

		// What the row holds is the digest, produced by the same function the
		// authorization server compares against. There is no read that recovers
		// the plaintext, which is why the two are separate types.
		read, err := store.GetClient(t.Context(), env.reader(), testScope, issued.Client.ID)
		must.NoError(t, err)
		test.EqOp(t, oauth2server.Hash(issued.Secret), read.SecretHash)
		test.NotEq(t, issued.Secret, read.SecretHash)
	})

	T.Run("an empty owner is the administered arrangement", func(t *testing.T) {
		t.Parallel()

		svc, _ := newService(t, env)

		issued, err := svc.CreateClient(t.Context(), tenancy.Global(), "", &CreationInput{
			Name:         "an operator client",
			RedirectURIs: []string{testRedirect},
		})
		must.NoError(t, err)

		test.True(t, issued.Client.Administered())

		// Which is what Admits reads as "any subject may authorize through it",
		// and is the reason creating one sits behind a permission.
		must.NoError(t, issued.Client.Admits(tenantScopeForTest(), "anybody"))
	})

	T.Run("runs the hook inside the transaction and rolls back when it fails", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{err: platformerrors.New("the audit entry was refused")}
		svc, store := newService(t, env, WithHooks(hooks))

		issued, err := svc.CreateClient(t.Context(), testScope, testOwner, &CreationInput{
			Name:         "a client",
			RedirectURIs: []string{testRedirect},
		})
		test.Error(t, err)
		test.Nil(t, issued)

		// The hook saw the registration, and the registration is not there: a
		// credential with no provenance is exactly what the shared transaction
		// exists to prevent.
		must.SliceLen(t, 1, hooks.created)

		_, readErr := store.GetClient(t.Context(), env.reader(), testScope, hooks.created[0].ID)
		test.ErrorIs(t, readErr, ErrClientNotFound)
	})

	T.Run("refuses a registration the authorization server could not use", func(t *testing.T) {
		t.Parallel()

		svc, _ := newService(t, env)

		_, err := svc.CreateClient(t.Context(), testScope, testOwner, &CreationInput{
			RedirectURIs: []string{testRedirect},
		})
		test.ErrorIs(t, err, ErrEmptyName)

		// At registration is the only place this failure is legible; later it is
		// a client that "does not work".
		_, err = svc.CreateClient(t.Context(), testScope, testOwner, &CreationInput{Name: "a client"})
		test.ErrorIs(t, err, ErrNoRedirectURIs)

		_, err = svc.CreateClient(t.Context(), testScope, testOwner, &CreationInput{
			Name:         "a client",
			RedirectURIs: []string{"not a uri"},
		})
		test.ErrorIs(t, err, ErrInvalidRedirectURI)

		_, err = svc.CreateClient(t.Context(), testScope, testOwner, nil)
		test.ErrorIs(t, err, ErrNilInput)
	})

	T.Run("a generator that cannot mint is not a partial registration", func(t *testing.T) {
		t.Parallel()

		broken := platformerrors.New("no entropy")

		svc, _ := newService(t, env, WithCredentialGenerator(
			func() (string, string, error) { return "", "", broken }))

		_, err := svc.CreateClient(t.Context(), testScope, testOwner, &CreationInput{
			Name:         "a client",
			RedirectURIs: []string{testRedirect},
		})
		test.ErrorIs(t, err, broken)
	})
}

func TestService_UpdateClient(T *testing.T) {
	T.Parallel()

	env := newSQLiteEnv(T)

	T.Run("returns the row the write answered with, stamp and all", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		svc, store := newService(t, env, WithHooks(hooks))

		client := env.seed(t, store, testScope, testOwner)

		updated, err := svc.UpdateClient(t.Context(), testScope, client.ID, &UpdateInput{
			Name:         "renamed",
			RedirectURIs: []string{testRedirect},
		})
		must.NoError(t, err)

		test.EqOp(t, "renamed", updated.Name)
		test.True(t, updated.LastUpdatedAt != nil,
			test.Sprint("the write's read-back ran outside the transaction that stamped the row"))

		must.SliceLen(t, 1, hooks.updated)
		test.EqOp(t, "renamed", hooks.updated[0].Name)
	})

	T.Run("a registration that is not there is not found", func(t *testing.T) {
		t.Parallel()

		svc, _ := newService(t, env)

		_, err := svc.UpdateClient(t.Context(), testScope, "nobody", &UpdateInput{
			Name:         "renamed",
			RedirectURIs: []string{testRedirect},
		})
		test.ErrorIs(t, err, ErrClientNotFound)
	})
}

func TestService_ArchiveClient(T *testing.T) {
	T.Parallel()

	env := newSQLiteEnv(T)

	T.Run("hands the hook what was withdrawn, not just its id", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		svc, store := newService(t, env, WithHooks(hooks))

		client := env.seed(t, store, testScope, testOwner)

		must.NoError(t, svc.ArchiveClient(t.Context(), testScope, client.ID))

		// The store answers with the row the withdrawal moved, so this operation
		// is one statement rather than a read on either side of one: an audit
		// entry written from the id alone would record that something was
		// withdrawn, and what the credential was for is exactly what somebody
		// reading it later needs.
		must.SliceLen(t, 1, hooks.archived)
		test.EqOp(t, client.ClientID, hooks.archived[0].ClientID)
		test.EqOp(t, "test client", hooks.archived[0].Name)

		// And it is the row as the write left it, not as it stood a statement
		// earlier — which is what an entry recording when the credential stopped
		// working is written from.
		test.True(t, hooks.archived[0].Archived(),
			test.Sprint("the hook saw the registration as it stood before the withdrawal"))

		_, err := store.GetClient(t.Context(), env.reader(), testScope, client.ID)
		test.ErrorIs(t, err, ErrClientNotFound)
	})

	T.Run("a registration in another registry is not found", func(t *testing.T) {
		t.Parallel()

		svc, store := newService(t, env)
		client := env.seed(t, store, testScope, testOwner)

		test.ErrorIs(t, svc.ArchiveClient(t.Context(), otherScope, client.ID), ErrClientNotFound)
	})
}

func TestNewService(T *testing.T) {
	T.Parallel()

	env := newSQLiteEnv(T)

	T.Run("refuses what it cannot be built from", func(t *testing.T) {
		t.Parallel()

		_, err := NewService(nil, env.newStore(t))
		test.ErrorIs(t, err, ErrNilDatabaseClient)

		_, err = NewService(env.client, nil)
		test.ErrorIs(t, err, ErrNilStore)
	})
}

// tenantScopeForTest is a registry that is not the global one, for the
// assertion that a global registration admits a subject in any of them.
func tenantScopeForTest() tenancy.Scope { return tenancy.Of("some-other-tenant") }
