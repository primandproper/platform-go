package oauth2clients

import (
	"testing"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// runStoreSuite is every behavioral case, written once and run against each
// dialect: SQLite here, Postgres and MySQL from containers_test.go.
//
// It is one function rather than a file per dialect because what these cases
// assert is what the *statements* do, and the statements are generated from one
// description. A case that passed on one engine and not another would be
// database/querygen's bug or the schema's, and both are exactly what running the
// same suite three times is for.
func runStoreSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("a create answers with the row it wrote, creation time and all", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		client := env.seed(t, store, testScope, testOwner)

		// created_at is the database's, so a caller that serialized the value it
		// handed over would report the zero time for a row written a moment ago.
		// The create reads it back on its own transaction.
		test.False(t, client.CreatedAt.IsZero(),
			test.Sprint("the create did not answer with the creation time the database assigned"))

		read, err := store.GetClient(t.Context(), env.reader(), testScope, client.ID)
		must.NoError(t, err)

		test.EqOp(t, client.ClientID, read.ClientID)
		test.EqOp(t, client.SecretHash, read.SecretHash)
		test.Eq(t, []string{testRedirect}, read.RedirectURIs)
		test.True(t, read.LastUpdatedAt == nil, test.Sprint("a fresh registration reports an update"))
		test.False(t, read.Archived())
	})

	t.Run("a create leaves the registration it was handed alone", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		// What the write settles is on what it returns. An argument edited under
		// the caller is a second place to read one fact from, and the scope it
		// adopts below is the one field this write assigns at all.
		handed := newClient(tenancy.Scope{}, testOwner)

		written, err := env.create(t, store, testScope, handed)
		must.NoError(t, err)

		test.EqOp(t, tenancy.Scope{}, handed.Scope,
			test.Sprint("the create wrote the scope it adopted onto the caller's value"))
		test.True(t, handed.CreatedAt.IsZero(),
			test.Sprint("the create stamped the caller's value"))

		test.EqOp(t, testScope, written.Scope)
		test.False(t, written.CreatedAt.IsZero())
	})

	t.Run("a registry cannot read another registry's registration", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		client := env.seed(t, store, testScope, testOwner)

		_, err := store.GetClient(t.Context(), env.reader(), otherScope, client.ID)
		test.ErrorIs(t, err, ErrClientNotFound)
	})

	t.Run("a registration whose scope disagrees with the call is refused", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		// Refused rather than corrected: a scope read off a struct the caller
		// assembled somewhere else is the derivation the column rule rules out.
		client := newClient(otherScope, testOwner)
		test.ErrorIs(t, env.createErr(t, store, testScope, client), ErrScopeMismatch)
	})

	t.Run("a registration naming no scope adopts the call's", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		written, err := env.create(t, store, testScope, newClient(tenancy.Scope{}, testOwner))
		must.NoError(t, err)

		test.EqOp(t, testScope, written.Scope)

		read, err := store.GetClient(t.Context(), env.reader(), testScope, written.ID)
		must.NoError(t, err)
		test.EqOp(t, testScope, read.Scope)
	})

	t.Run("a client identifier already in use is refused rather than overwriting", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		first := env.seed(t, store, testScope, testOwner)

		// A different row, in a different registry, reusing the identifier. The
		// index is global on purpose: the lookup that reads it has no scope, so
		// two rows sharing one would make that read ambiguous.
		second := newClient(otherScope, otherOwner)
		second.ClientID = first.ClientID

		test.ErrorIs(t, env.createErr(t, store, otherScope, second), ErrClientIDTaken)
	})

	t.Run("the authorization server's lookup crosses every registry", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		client := env.seed(t, store, otherScope, otherOwner)

		// No scope is passed, and none could be: /authorize names a client
		// before anybody has signed in. What comes back carries the registry it
		// resolved, which is the whole point of the read.
		resolved, err := store.ResolveClientID(t.Context(), env.reader(), client.ClientID)
		must.NoError(t, err)

		test.EqOp(t, otherScope, resolved.Scope)
		test.EqOp(t, client.ID, resolved.ID)
	})

	t.Run("the lookup still finds a withdrawn registration", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		client := env.seed(t, store, testScope, testOwner)

		must.NoError(t, env.archive(t, store, testScope, client.ID))

		// The scoped read hides it, because that read serves a console.
		_, err := store.GetClient(t.Context(), env.reader(), testScope, client.ID)
		test.ErrorIs(t, err, ErrClientNotFound)

		// The lookup does not, because refusing a withdrawn client by name is a
		// decision that belongs to the layer holding a subject, not to a
		// predicate. Hiding it here would make withdrawn and unknown the same
		// thing at the one layer that can tell them apart.
		resolved, err := store.ResolveClientID(t.Context(), env.reader(), client.ClientID)
		must.NoError(t, err)
		test.True(t, resolved.Archived(), test.Sprint("the lookup reported a withdrawn registration as live"))
	})

	t.Run("an unknown client identifier is not found", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		_, err := store.ResolveClientID(t.Context(), env.reader(), "cid_nobody")
		test.ErrorIs(t, err, ErrClientNotFound)
	})

	t.Run("the administered page carries the registry and nothing else", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		mine := env.seed(t, store, testScope, testOwner)
		theirs := env.seed(t, store, testScope, otherOwner)
		administered := env.seed(t, store, testScope, "")
		elsewhere := env.seed(t, store, otherScope, testOwner)

		page, err := store.ListClients(t.Context(), env.reader(), testScope, nil)
		must.NoError(t, err)

		ids := clientIDs(page.Data)
		test.SliceContains(t, ids, mine.ID)
		test.SliceContains(t, ids, theirs.ID)
		test.SliceContains(t, ids, administered.ID)
		test.SliceNotContains(t, ids, elsewhere.ID)
	})

	t.Run("the self-service page carries one person's own and nobody else's", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		mine := env.seed(t, store, testScope, testOwner)
		theirs := env.seed(t, store, testScope, otherOwner)

		// An administered registration belongs to nobody, so it is not the
		// caller's either. Withdrawing the credential an operator minted for the
		// whole deployment is not something the self-service door does.
		administered := env.seed(t, store, testScope, "")

		page, err := store.ListClientsForOwner(t.Context(), env.reader(), testScope, testOwner, nil)
		must.NoError(t, err)

		ids := clientIDs(page.Data)
		test.SliceContains(t, ids, mine.ID)
		test.SliceNotContains(t, ids, theirs.ID)
		test.SliceNotContains(t, ids, administered.ID)
	})

	t.Run("a self-service page naming nobody is refused rather than widened", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		env.seed(t, store, testScope, "")

		// Answering with the administered rows would hand a caller with no
		// session the deployment's own credentials.
		_, err := store.ListClientsForOwner(t.Context(), env.reader(), testScope, "", nil)
		test.ErrorIs(t, err, ErrEmptyUserID)
	})

	t.Run("an update revises the descriptive fields and answers with the row", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		client := env.seed(t, store, testScope, testOwner)

		revised, err := env.update(t, store, testScope, client.ID, &UpdateInput{
			Name:         "renamed",
			Description:  "revised",
			RedirectURIs: []string{"https://example.test/other"},
			Scopes:       []string{"recipes:read"},
		})
		must.NoError(t, err)

		// What the write answered with is the revision, including the stamp the
		// statement assigned — which is the value a caller holding an id and a
		// patch had no other way to learn.
		test.EqOp(t, "renamed", revised.Name)
		test.Eq(t, []string{"https://example.test/other"}, revised.RedirectURIs)
		test.Eq(t, []string{"recipes:read"}, revised.Scopes)
		test.True(t, revised.LastUpdatedAt != nil,
			test.Sprint("the update did not answer with the stamp it assigned"))

		// The three immutable columns are not in UpdateInput at all, so this is
		// what the schema and the generated SET list agree on rather than
		// something a caller could have asked for.
		test.EqOp(t, client.ClientID, revised.ClientID)
		test.EqOp(t, client.SecretHash, revised.SecretHash)
		test.EqOp(t, testOwner, revised.BelongsToUser)

		// And it is the row, not a description of it: a second read on a
		// connection that never saw the transaction agrees.
		read, err := store.GetClient(t.Context(), env.reader(), testScope, client.ID)
		must.NoError(t, err)
		test.EqOp(t, "renamed", read.Name)
	})

	t.Run("an archive answers with the row it withdrew", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		client := env.seed(t, store, testScope, testOwner)

		var withdrawn *Client

		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			var archiveErr error
			withdrawn, archiveErr = store.ArchiveClient(t.Context(), tx, testScope, client.ID)

			return archiveErr
		}))

		// The row a withdrawal entry is written from, and the one read that can
		// see it: every consumer read but the lookup filters it out, and the
		// lookup needs a client_id rather than the row id this call was given.
		must.NotNil(t, withdrawn)
		test.EqOp(t, client.ID, withdrawn.ID)
		test.EqOp(t, client.ClientID, withdrawn.ClientID)
		test.EqOp(t, "test client", withdrawn.Name)
		test.Eq(t, []string{testRedirect}, withdrawn.RedirectURIs)
		test.True(t, withdrawn.Archived(),
			test.Sprint("the archive answered with the row as it stood before the write"))
	})

	t.Run("an archive that moves nothing answers with no row", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		client := env.seed(t, store, testScope, testOwner)

		must.NoError(t, env.archive(t, store, testScope, client.ID))

		// The guard decides, not the read-back: a second withdrawal matches
		// nothing, so it is refused rather than answered with the row the first
		// one moved.
		var second *Client

		err := env.inTx(t, func(tx database.Tx) error {
			var archiveErr error
			second, archiveErr = store.ArchiveClient(t.Context(), tx, testScope, client.ID)

			return archiveErr
		})
		test.ErrorIs(t, err, ErrClientNotFound)
		test.Nil(t, second)
	})

	t.Run("a write against another registry's row touches nothing", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		client := env.seed(t, store, testScope, testOwner)

		test.ErrorIs(t, env.archive(t, store, otherScope, client.ID), ErrClientNotFound)

		read, err := store.GetClient(t.Context(), env.reader(), testScope, client.ID)
		must.NoError(t, err)
		test.False(t, read.Archived(), test.Sprint("an archive from another registry withdrew the row"))
	})

	t.Run("a read inside a transaction sees that transaction's own write", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		client := newClient(testScope, testOwner)

		// The read takes the wider executor type deliberately, so one method
		// serves a caller holding Reader() and a caller inside a transaction —
		// and the second sees rows the first cannot yet. It is also what the
		// create's own read-back runs on.
		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			written, err := store.CreateClient(t.Context(), tx, testScope, client)
			if err != nil {
				return err
			}

			read, err := store.GetClient(t.Context(), tx, testScope, written.ID)
			if err != nil {
				return err
			}

			test.EqOp(t, written.ClientID, read.ClientID)

			return nil
		}))
	})

	t.Run("every write refuses a nil transaction", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		// The Tx argument is the caller's proof they are already inside one, so
		// a nil is a caller who is not — and no write here opens its own. Each
		// refusal answers with a nil row: the row comes back only alongside a
		// nil error.
		created, err := store.CreateClient(t.Context(), nil, testScope, newClient(testScope, testOwner))
		test.ErrorIs(t, err, ErrNilTransaction)
		test.Nil(t, created)

		revised, err := store.UpdateClient(t.Context(), nil, testScope, "id", &UpdateInput{})
		test.ErrorIs(t, err, ErrNilTransaction)
		test.Nil(t, revised)

		withdrawn, err := store.ArchiveClient(t.Context(), nil, testScope, "id")
		test.ErrorIs(t, err, ErrNilTransaction)
		test.Nil(t, withdrawn)
	})

	t.Run("every consumer read refuses an unset scope", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		// The zero Scope names nobody and is not Global(). Binding it as the
		// type rather than as a string derived from it is what makes this a
		// refusal instead of a wider result set.
		var unset tenancy.Scope

		_, err := store.GetClient(t.Context(), env.reader(), unset, "id")
		test.ErrorIs(t, err, tenancy.ErrNoScope)

		_, err = store.ListClients(t.Context(), env.reader(), unset, nil)
		test.ErrorIs(t, err, tenancy.ErrNoScope)

		_, err = store.ListClientsForOwner(t.Context(), env.reader(), unset, testOwner, nil)
		test.ErrorIs(t, err, tenancy.ErrNoScope)
	})

	t.Run("every read refuses a nil executor", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		_, err := store.GetClient(t.Context(), nil, testScope, "id")
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.ResolveClientID(t.Context(), nil, "cid")
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.ListClients(t.Context(), nil, testScope, nil)
		test.ErrorIs(t, err, ErrNilExecutor)
	})
}

// TestSQLStore runs the suite against SQLite, which needs no container.
func TestSQLStore(T *testing.T) {
	T.Parallel()

	runStoreSuite(T, newSQLiteEnv(T))
}

// TestNewSQLStore covers what construction refuses.
func TestNewSQLStore(T *testing.T) {
	T.Parallel()

	T.Run("refuses a nil database client", func(t *testing.T) {
		t.Parallel()

		store, err := NewSQLStore(nil)
		test.ErrorIs(t, err, ErrNilDatabaseClient)
		test.Nil(t, store)
	})

	T.Run("refuses a prefix that would not be an identifier", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)

		// The prefix is interpolated into statement text rather than bound, so
		// it is restricted rather than escaped.
		store, err := NewSQLStore(env.client, WithTablePrefix("not a prefix; DROP TABLE"))
		test.Error(t, err)
		test.Nil(t, store)
	})

	T.Run("defaults to the unprefixed table", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)

		store, err := NewSQLStore(env.client)
		must.NoError(t, err)
		test.EqOp(t, DefaultTablePrefix, store.TablePrefix())
	})
}

// clientIDs reads the row ids off a page, so the listing assertions read as the
// membership claims they are.
func clientIDs(clients []*Client) []string {
	ids := make([]string, 0, len(clients))
	for _, c := range clients {
		ids = append(ids, c.ID)
	}

	return ids
}
