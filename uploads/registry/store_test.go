package registry

import (
	"testing"

	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/database/dialect"
	databasemock "github.com/primandproper/primitives-go/database/mock"
	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/filtering"
	"github.com/primandproper/primitives-go/identifiers"
	"github.com/primandproper/primitives-go/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNewSQLStore(T *testing.T) {
	T.Parallel()

	newClient := func(d dialect.Dialect) *databasemock.ClientMock {
		return &databasemock.ClientMock{DialectFunc: func() dialect.Dialect { return d }}
	}

	T.Run("builds against every dialect the querier was generated for", func(t *testing.T) {
		t.Parallel()

		for _, d := range []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite} {
			store, err := NewSQLStore(newClient(d))
			must.NoError(t, err, must.Sprintf("dialect %s", d))
			must.NotNil(t, store)
		}
	})

	T.Run("refuses a nil client", func(t *testing.T) {
		t.Parallel()

		store, err := NewSQLStore(nil)
		must.ErrorIs(t, err, ErrNilDatabaseClient)
		must.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
		test.Nil(t, store)
	})

	T.Run("refuses a dialect it has no statements for", func(t *testing.T) {
		t.Parallel()

		store, err := NewSQLStore(newClient(dialect.Dialect("oracle")))
		must.Error(t, err)
		test.Nil(t, store)
	})

	T.Run("refuses a prefix that cannot render", func(t *testing.T) {
		t.Parallel()

		// Vetted against the identifiers it actually produces, so a prefix that
		// is legal alone and yields an over-long index name fails at
		// construction rather than at the first query.
		store, err := NewSQLStore(newClient(dialect.Postgres), WithTablePrefix("has space"))
		must.Error(t, err)
		test.Nil(t, store)
	})

	T.Run("ignores a nil option", func(t *testing.T) {
		t.Parallel()

		store, err := NewSQLStore(newClient(dialect.Postgres), nil)
		must.NoError(t, err)
		must.NotNil(t, store)
	})
}

// TestSQLStore_SQLite runs the whole behavioral contract against SQLite, which
// executes the real generated SQL without a container. The same suite runs
// against Postgres and MySQL in containers_test.go.
func TestSQLStore_SQLite(T *testing.T) {
	T.Parallel()

	runStoreSuite(T, newSQLiteEnv(T))
}

// runStoreSuite is the whole behavioral contract, run once per dialect.
//
// Each subtest migrates its own prefixed table, because a scope's object list is
// global within the table: without that, one subtest's rows are another's page.
func runStoreSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("records an object and reads it back", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		object := newObject("avatars/grace/original.png", "user_1")
		must.NoError(t, env.record(t, store, testScope, object))

		// The id is minted by the write, the creation time comes back from the
		// row rather than being left at the zero time, and the scope the write
		// named is written onto the value the caller still holds.
		test.NotEq(t, "", object.ID)
		test.False(t, object.CreatedAt.IsZero())
		test.Nil(t, object.LastUpdatedAt)
		test.Nil(t, object.ArchivedAt)
		test.EqOp(t, testScope, object.Scope)

		read, err := store.GetObject(t.Context(), env.reader(), testScope, object.ID)
		must.NoError(t, err)
		test.EqOp(t, object.ID, read.ID)
		test.EqOp(t, object.Key, read.Key)
		test.EqOp(t, "image/png", read.ContentType)
		test.EqOp(t, int64(1024), read.Size)
		test.EqOp(t, "user_1", read.OwnerID)
		test.EqOp(t, testScope, read.Scope)
		test.EqOp(t, object.CreatedAt.UTC(), read.CreatedAt)
	})

	t.Run("keeps a caller-supplied id", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		// The reference to the upload is often minted before the upload, so the
		// row a consumer is writing in the same request can name it.
		id := identifiers.New()

		object := newObject("receipts/"+id+".pdf", "user_1")
		object.ID = id

		must.NoError(t, env.record(t, store, testScope, object))
		test.EqOp(t, id, object.ID)

		read, err := store.GetObject(t.Context(), env.reader(), testScope, id)
		must.NoError(t, err)
		test.EqOp(t, id, read.ID)
	})

	t.Run("records what an object belongs to", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		object := newObject("receipts/january.pdf", "user_1")
		object.BelongsTo = Subject{Type: "invoice", ID: "invoice_1"}

		must.NoError(t, env.record(t, store, testScope, object))

		read, err := store.GetObject(t.Context(), env.reader(), testScope, object.ID)
		must.NoError(t, err)
		test.EqOp(t, "invoice", read.BelongsTo.Type)
		test.EqOp(t, "invoice_1", read.BelongsTo.ID)
		test.True(t, read.BelongsTo.Attached())
	})

	t.Run("refuses a key already registered in the scope", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		key := "avatars/grace/original.png"
		must.NoError(t, env.record(t, store, testScope, newObject(key, "user_1")))

		err := env.record(t, store, testScope, newObject(key, "user_2"))
		must.ErrorIs(t, err, ErrObjectKeyTaken)

		// The same key in another tenant is another object, and registering it
		// is not a collision.
		must.NoError(t, env.record(t, store, otherScope, newObject(key, "user_3")))
	})

	t.Run("keeps a key taken after the row is archived", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		key := "receipts/january.pdf"

		object := newObject(key, "user_1")
		must.NoError(t, env.record(t, store, testScope, object))
		must.NoError(t, env.archive(t, store, testScope, object.ID))

		// Archival is metadata-only and the bytes are still in the bucket, so a
		// second row for the same key would be two rows describing one object.
		must.ErrorIs(t, env.record(t, store, testScope, newObject(key, "user_1")), ErrObjectKeyTaken)
	})

	t.Run("refuses a row that could not answer who may read it", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		must.ErrorIs(t, env.record(t, store, testScope, nil), ErrNilObject)

		noKey := newObject("", "user_1")
		must.Error(t, env.record(t, store, testScope, noKey))

		noOwner := newObject("avatars/nobody.png", "")
		must.Error(t, env.record(t, store, testScope, noOwner))

		halfSubject := newObject("avatars/half.png", "user_1")
		halfSubject.BelongsTo = Subject{Type: "invoice"}
		must.ErrorIs(t, env.record(t, store, testScope, halfSubject), ErrPartialSubject)
	})

	t.Run("refuses an object that names a different tenant than the write", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		// The write binds the argument, so an object carrying another tenant's
		// scope is a caller holding a stale value or a mix-up. Neither is a
		// thing to guess at, and the row that would be written is one tenant's
		// object filed under another's.
		elsewhere := newObject("avatars/grace/original.png", "user_1")
		elsewhere.Scope = otherScope

		must.ErrorIs(t, env.record(t, store, testScope, elsewhere), ErrScopeMismatch)

		// Nothing was written under either scope: the check runs before any
		// statement the write would send.
		_, err := store.GetObjectByKey(t.Context(), env.reader(), testScope, elsewhere.Key)
		must.ErrorIs(t, err, ErrObjectNotFound)

		_, err = store.GetObjectByKey(t.Context(), env.reader(), otherScope, elsewhere.Key)
		must.ErrorIs(t, err, ErrObjectNotFound)

		// An object naming the same scope as the write is not a mismatch, and
		// one naming none adopts it.
		agreeing := newObject("avatars/ada/original.png", "user_1")
		agreeing.Scope = testScope
		must.NoError(t, env.record(t, store, testScope, agreeing))

		silent := newObject("avatars/hopper/original.png", "user_1")
		must.NoError(t, env.record(t, store, testScope, silent))
		test.EqOp(t, testScope, silent.Scope)
	})

	t.Run("reads by the key the bytes live at", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		key := "avatars/grace/original.png"
		object := newObject(key, "user_1")
		must.NoError(t, env.record(t, store, testScope, object))

		read, err := store.GetObjectByKey(t.Context(), env.reader(), testScope, key)
		must.NoError(t, err)
		test.EqOp(t, object.ID, read.ID)
		test.EqOp(t, "user_1", read.OwnerID)

		// The read a request runs is scoped, so it is not an oracle for which
		// keys exist in another tenant.
		_, err = store.GetObjectByKey(t.Context(), env.reader(), otherScope, key)
		must.ErrorIs(t, err, ErrObjectNotFound)

		_, err = store.GetObjectByKey(t.Context(), env.reader(), testScope, "avatars/nobody.png")
		must.ErrorIs(t, err, ErrObjectNotFound)
	})

	t.Run("answers a read from another scope as absent", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		object := newObject("avatars/grace/original.png", "user_1")
		must.NoError(t, env.record(t, store, testScope, object))

		_, err := store.GetObject(t.Context(), env.reader(), otherScope, object.ID)
		must.ErrorIs(t, err, ErrObjectNotFound)

		_, err = store.GetObject(t.Context(), env.reader(), testScope, identifiers.New())
		must.ErrorIs(t, err, ErrObjectNotFound)
	})

	t.Run("archives metadata only, once", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		object := newObject("receipts/january.pdf", "user_1")
		must.NoError(t, env.record(t, store, testScope, object))

		// Another tenant cannot archive it, and the refusal reads as absence
		// rather than as a permission error, which is the answer that does not
		// confirm the row exists.
		must.ErrorIs(t, env.archive(t, store, otherScope, object.ID), ErrObjectNotFound)

		must.NoError(t, env.archive(t, store, testScope, object.ID))

		// The statement carries the archived predicate, so a second archive
		// touches nothing rather than moving the timestamp forward.
		must.ErrorIs(t, env.archive(t, store, testScope, object.ID), ErrObjectNotFound)

		_, err := store.GetObject(t.Context(), env.reader(), testScope, object.ID)
		must.ErrorIs(t, err, ErrObjectNotFound)
	})

	t.Run("pages the scope's objects in the direction the filter names", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		first := newObject("a.png", "user_1")
		first.ID = "obj_1"
		must.NoError(t, env.record(t, store, testScope, first))

		second := newObject("b.png", "user_1")
		second.ID = "obj_2"
		must.NoError(t, env.record(t, store, testScope, second))

		// A neighbor's row must never appear in this tenant's page.
		must.NoError(t, env.record(t, store, otherScope, newObject("c.png", "user_9")))

		page, err := store.ListObjects(t.Context(), env.reader(), testScope, nil)
		must.NoError(t, err)
		must.SliceLen(t, 2, page.Data)
		test.EqOp(t, "obj_1", page.Data[0].ID)
		test.EqOp(t, "obj_2", page.Data[1].ID)

		filtered, total, known := page.Counts()
		must.True(t, known)
		test.EqOp(t, uint64(2), filtered)
		test.EqOp(t, uint64(2), total)

		newestFirst, err := store.ListObjects(t.Context(), env.reader(), testScope,
			&filtering.QueryFilter{SortBy: filtering.SortDescending})
		must.NoError(t, err)
		must.SliceLen(t, 2, newestFirst.Data)
		test.EqOp(t, "obj_2", newestFirst.Data[0].ID)
		test.EqOp(t, "obj_1", newestFirst.Data[1].ID)
	})

	t.Run("walks a page at a time", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		for _, id := range []string{"obj_1", "obj_2", "obj_3"} {
			object := newObject(id+".png", "user_1")
			object.ID = id
			must.NoError(t, env.record(t, store, testScope, object))
		}

		size := uint16(2)

		page, err := store.ListObjects(t.Context(), env.reader(), testScope,
			&filtering.QueryFilter{MaxResponseSize: &size})
		must.NoError(t, err)
		must.SliceLen(t, 2, page.Data)
		must.EqOp(t, "obj_2", page.Cursor)

		next, err := store.ListObjects(t.Context(), env.reader(), testScope,
			&filtering.QueryFilter{MaxResponseSize: &size, Cursor: &page.Cursor})
		must.NoError(t, err)
		must.SliceLen(t, 1, next.Data)
		test.EqOp(t, "obj_3", next.Data[0].ID)

		// The counts describe the collection rather than the page, so they do
		// not shrink as the caller walks it.
		_, total, known := next.Counts()
		must.True(t, known)
		test.EqOp(t, uint64(3), total)
	})

	t.Run("leaves archived rows out unless asked", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		object := newObject("a.png", "user_1")
		must.NoError(t, env.record(t, store, testScope, object))
		must.NoError(t, env.record(t, store, testScope, newObject("b.png", "user_1")))
		must.NoError(t, env.archive(t, store, testScope, object.ID))

		live, err := store.ListObjects(t.Context(), env.reader(), testScope, nil)
		must.NoError(t, err)
		must.SliceLen(t, 1, live.Data)

		includeArchived := true

		all, err := store.ListObjects(t.Context(), env.reader(), testScope,
			&filtering.QueryFilter{IncludeArchived: &includeArchived})
		must.NoError(t, err)
		must.SliceLen(t, 2, all.Data)

		for _, o := range all.Data {
			if o.ID == object.ID {
				test.NotNil(t, o.ArchivedAt)
			}
		}
	})

	t.Run("pages one owner's objects", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		mine := newObject("a.png", "user_1")
		must.NoError(t, env.record(t, store, testScope, mine))
		must.NoError(t, env.record(t, store, testScope, newObject("b.png", "user_2")))
		must.NoError(t, env.record(t, store, otherScope, newObject("c.png", "user_1")))

		page, err := store.ListObjectsByOwner(t.Context(), env.reader(), testScope, "user_1", nil)
		must.NoError(t, err)
		must.SliceLen(t, 1, page.Data)
		test.EqOp(t, mine.ID, page.Data[0].ID)

		// The neighbor's object belongs to a user with the same id, and the
		// scope is what keeps it out.
		neighbors, err := store.ListObjectsByOwner(t.Context(), env.reader(), otherScope, "user_1", nil)
		must.NoError(t, err)
		must.SliceLen(t, 1, neighbors.Data)
		test.EqOp(t, "c.png", neighbors.Data[0].Key)

		empty, err := store.ListObjectsByOwner(t.Context(), env.reader(), testScope, "user_9", nil)
		must.NoError(t, err)
		test.SliceEmpty(t, empty.Data)
	})

	t.Run("pages what is attached to one thing", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		invoice := newObject("receipts/january.pdf", "user_1")
		invoice.BelongsTo = Subject{Type: "invoice", ID: "invoice_1"}
		must.NoError(t, env.record(t, store, testScope, invoice))

		// Same id, different type: an id without its type names something else
		// in another one of the consumer's tables.
		ticket := newObject("tickets/screenshot.png", "user_1")
		ticket.BelongsTo = Subject{Type: "ticket", ID: "invoice_1"}
		must.NoError(t, env.record(t, store, testScope, ticket))

		// And a standalone upload, attached to nothing.
		must.NoError(t, env.record(t, store, testScope, newObject("loose.png", "user_1")))

		page, err := store.ListObjectsBySubject(t.Context(), env.reader(), testScope,
			Subject{Type: "invoice", ID: "invoice_1"}, nil)
		must.NoError(t, err)
		must.SliceLen(t, 1, page.Data)
		test.EqOp(t, invoice.ID, page.Data[0].ID)

		// The zero subject is refused rather than bound: every standalone
		// upload carries the empty pair, so the statement would report them as
		// one thing's attachments.
		_, err = store.ListObjectsBySubject(t.Context(), env.reader(), testScope, Subject{}, nil)
		must.ErrorIs(t, err, ErrUnattachedSubject)
	})

	t.Run("refuses an unset scope on every method", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		var unset tenancy.Scope

		_, err := store.GetObject(t.Context(), env.reader(), unset, "obj_1")
		must.Error(t, err)

		_, err = store.GetObjectByKey(t.Context(), env.reader(), unset, "a.png")
		must.Error(t, err)

		_, err = store.ListObjects(t.Context(), env.reader(), unset, nil)
		must.Error(t, err)

		_, err = store.ListObjectsByOwner(t.Context(), env.reader(), unset, "user_1", nil)
		must.Error(t, err)

		_, err = store.ListObjectsBySubject(t.Context(), env.reader(), unset,
			Subject{Type: "invoice", ID: "invoice_1"}, nil)
		must.Error(t, err)

		must.Error(t, env.archive(t, store, unset, "obj_1"))

		// The write is refused for the same reason and by the same call, now
		// that the scope is its argument rather than a field on the row.
		must.Error(t, env.record(t, store, unset, newObject("a.png", "user_1")))
	})

	t.Run("stores the global scope as itself", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		// A single-tenant application passes tenancy.Global() everywhere and
		// gets exactly what it would have had without the column — including
		// the fact that the global scope matches only itself.
		object := newObject("a.png", "user_1")
		must.NoError(t, env.record(t, store, tenancy.Global(), object))

		read, err := store.GetObject(t.Context(), env.reader(), tenancy.Global(), object.ID)
		must.NoError(t, err)
		test.True(t, read.Scope.IsGlobal())

		_, err = store.GetObject(t.Context(), env.reader(), testScope, object.ID)
		must.ErrorIs(t, err, ErrObjectNotFound)
	})

	t.Run("transactions", func(t *testing.T) {
		t.Parallel()

		runTransactionSuite(t, env)
	})
}

// errCompanionWrite stands in for the consumer's own write — the profile row
// naming the avatar, the audit entry naming who uploaded it — failing after the
// registry row went in.
var errCompanionWrite = platformerrors.New("the caller's own write failed")

// runTransactionSuite is the commit boundary, which is the whole of what this
// store's signatures buy its caller.
//
// What is under test here is not that the statements work — the suite above
// covers that — but which side of a commit each of them lands on, and what a
// read handed the transaction can see. Those are the questions a store that
// opened its own transaction answered for its caller, and answered wrong.
func runTransactionSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("a write and a read inside one transaction observe each other", func(t *testing.T) {
		t.Parallel()

		// The property the reads were widened for, and the one no
		// auto-committing write could express: inside the transaction the row
		// is there, and from outside it is not there yet. A read narrowed to
		// the client's reader would be reading a database that does not hold
		// the row its own caller just wrote.
		store := env.newStore(t)

		object := newObject("avatars/grace/original.png", "user_1")

		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			if err := store.RecordObject(t.Context(), tx, testScope, object); err != nil {
				return err
			}

			read, err := store.GetObject(t.Context(), tx, testScope, object.ID)
			if err != nil {
				return err
			}

			test.EqOp(t, object.Key, read.Key)

			byKey, err := store.GetObjectByKey(t.Context(), tx, testScope, object.Key)
			if err != nil {
				return err
			}

			test.EqOp(t, object.ID, byKey.ID)

			page, err := store.ListObjects(t.Context(), tx, testScope, nil)
			if err != nil {
				return err
			}

			must.SliceLen(t, 1, page.Data)

			// And the same read, on the client, cannot see it: the transaction
			// has not committed, so this is the other half of the same fact
			// rather than a second one.
			outside, err := store.ListObjects(t.Context(), env.reader(), testScope, nil)
			if err != nil {
				return err
			}

			test.SliceEmpty(t, outside.Data)

			return nil
		}))

		// After the commit both executors agree, which is what makes the
		// reading above about visibility rather than about two different rows.
		read, err := store.GetObject(t.Context(), env.reader(), testScope, object.ID)
		must.NoError(t, err)
		test.EqOp(t, object.ID, read.ID)
	})

	t.Run("both writes commit with the caller's transaction", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		recorded := newObject("avatars/grace/original.png", "user_1")
		doomed := newObject("receipts/january.pdf", "user_1")
		must.NoError(t, env.record(t, store, testScope, doomed))

		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			if err := store.RecordObject(t.Context(), tx, testScope, recorded); err != nil {
				return err
			}

			return store.ArchiveObject(t.Context(), tx, testScope, doomed.ID)
		}))

		// The create reads its creation time back through the caller's
		// executor, so the value the caller is handed is the row this
		// transaction wrote rather than a zero time waiting on a commit.
		test.NotEqOp(t, "", recorded.ID)
		test.False(t, recorded.CreatedAt.IsZero())

		read, err := store.GetObject(t.Context(), env.reader(), testScope, recorded.ID)
		must.NoError(t, err)
		test.EqOp(t, recorded.Key, read.Key)

		_, err = store.GetObject(t.Context(), env.reader(), testScope, doomed.ID)
		must.ErrorIs(t, err, ErrObjectNotFound)
	})

	t.Run("a rolled back transaction takes both writes with it", func(t *testing.T) {
		t.Parallel()

		// This is the whole point of the signature, seen from the side that
		// matters: the consumer's own write fails, and the registry row goes
		// back with it rather than surviving in a transaction it was never part
		// of. What is left is an object in the bucket with no row, which is the
		// direction that does not lie to a reader.
		store := env.newStore(t)

		recorded := newObject("avatars/grace/original.png", "user_1")
		doomed := newObject("receipts/january.pdf", "user_1")
		must.NoError(t, env.record(t, store, testScope, doomed))

		err := env.inTx(t, func(tx database.Tx) error {
			if txErr := store.RecordObject(t.Context(), tx, testScope, recorded); txErr != nil {
				return txErr
			}

			if txErr := store.ArchiveObject(t.Context(), tx, testScope, doomed.ID); txErr != nil {
				return txErr
			}

			return errCompanionWrite
		})
		must.ErrorIs(t, err, errCompanionWrite)

		// The id was minted onto the caller's value on the way through. Nothing
		// undoes that, and nothing should: what rolled back is the row.
		test.NotEqOp(t, "", recorded.ID)

		_, err = store.GetObject(t.Context(), env.reader(), testScope, recorded.ID)
		must.ErrorIs(t, err, ErrObjectNotFound)

		read, err := store.GetObject(t.Context(), env.reader(), testScope, doomed.ID)
		must.NoError(t, err)
		test.Nil(t, read.ArchivedAt)

		// And the key the rolled-back registration took is free again, which it
		// would not be had the write committed on its own.
		must.NoError(t, env.record(t, store, testScope, newObject(recorded.Key, "user_2")))
	})

	t.Run("the collision check sees a key registered in the same transaction", func(t *testing.T) {
		t.Parallel()

		// The check runs on the caller's executor rather than on a connection
		// of the store's own, so two registrations in one transaction are
		// checked against each other. A check on a separate connection would
		// clear the second one and leave the unique index to refuse it, at the
		// caller's commit, as a driver error rather than as ErrObjectKeyTaken.
		store := env.newStore(t)

		key := "avatars/grace/original.png"

		err := env.inTx(t, func(tx database.Tx) error {
			if txErr := store.RecordObject(t.Context(), tx, testScope, newObject(key, "user_1")); txErr != nil {
				return txErr
			}

			return store.RecordObject(t.Context(), tx, testScope, newObject(key, "user_2"))
		})
		must.ErrorIs(t, err, ErrObjectKeyTaken)
	})

	t.Run("every method refuses a nil executor", func(t *testing.T) {
		t.Parallel()

		// Every one of the seven, not a representative one. There is no
		// connection of the store's own to fall back to, so a method that did
		// anything but refuse would be reaching for something that is not there.
		store := env.newStore(t)

		must.ErrorIs(t,
			store.RecordObject(t.Context(), nil, testScope, newObject("a.png", "user_1")),
			ErrNilExecutor)
		must.ErrorIs(t,
			store.ArchiveObject(t.Context(), nil, testScope, "obj_1"),
			ErrNilExecutor)

		_, err := store.GetObject(t.Context(), nil, testScope, "obj_1")
		must.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.GetObjectByKey(t.Context(), nil, testScope, "a.png")
		must.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.ListObjects(t.Context(), nil, testScope, nil)
		must.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.ListObjectsByOwner(t.Context(), nil, testScope, "user_1", nil)
		must.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.ListObjectsBySubject(t.Context(), nil, testScope,
			Subject{Type: "invoice", ID: "invoice_1"}, nil)
		must.ErrorIs(t, err, ErrNilExecutor)
	})

	t.Run("a refused write inside a transaction leaves the transaction usable", func(t *testing.T) {
		t.Parallel()

		// Every check the writes make runs before any statement they would
		// send, so a refusal is the store declining rather than the database
		// aborting. A caller that inspects one and carries on has a transaction
		// to carry on in, which is what lets the good write below commit.
		store := env.newStore(t)

		elsewhere := newObject("avatars/ada/original.png", "user_1")
		elsewhere.Scope = otherScope

		survivor := newObject("avatars/grace/original.png", "user_1")

		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			mismatch := store.RecordObject(t.Context(), tx, testScope, elsewhere)
			must.ErrorIs(t, mismatch, ErrScopeMismatch)

			missing := store.ArchiveObject(t.Context(), tx, testScope, identifiers.New())
			must.ErrorIs(t, missing, ErrObjectNotFound)

			return store.RecordObject(t.Context(), tx, testScope, survivor)
		}))

		read, err := store.GetObject(t.Context(), env.reader(), testScope, survivor.ID)
		must.NoError(t, err)
		test.EqOp(t, survivor.Key, read.Key)
	})
}
