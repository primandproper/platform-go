/*
Package mediaregistry records what was uploaded: one row per object in storage,
saying who put it there, into which tenant, what it is, how big it is, and what
it belongs to.

uploads moves bytes to object storage and hands back a key. Nothing about that
key says whose it is, so every consumer that accepts uploads has written this
table itself — and the hand-written one is where a tenant column gets forgotten.

# Why the row is the access control

"May this caller read this object" is answered from metadata — the owner and the
scope on the row — not from the bucket. A consumer without the registry either
serves objects public-by-key, which makes an unguessable key the only protection
a private document has, or reinvents the registry. The row is also what makes
orphan detection possible at all: a row with no object, an object with no row.
Sweeping for either is deliberately not here yet; the registry existing is what
makes it writable later.

# It references the key, it never wraps the byte path

Nothing in this package opens, reads, or removes an object. Storing and
registering are separately callable on purpose — bytes that arrived through a
signed URL were written by the client, and a consumer adopting this against an
existing bucket registers objects nothing here ever wrote. [StoreAndRecord] is
the convenience that does both, in the order that fails safe, and it is a free
function over the two seams rather than a method on either.

Archival follows from the same split. [Store.ArchiveObject] is metadata-only: the
row is hidden and the object stays in the bucket, because whether a receipt is
still needed for tax purposes is the consumer's retention policy rather than
this package's guess. See the retention package. It is also why that write hands
the archived row back: the row is the only record of the key those surviving
bytes are at, and once the transaction commits every read here is written not to
see it.

# The row commits with what references it

Both writes take the caller's database.Tx and every read takes the wider
database.SQLQueryExecutor, which is the module's store convention. There is no
form of either write that opens a transaction of its own, and that is what the
signatures are for: a consumer that records an upload is almost always writing a
row of their own that references it, and two transactions means either a
reference to an object the registry has no row for or a row for an object
nothing points at. The bytes are spent either way. A caller with nothing to join
opens one with database.Client.WithTransaction and passes the Tx it is handed.

# Tenancy

Every read is scoped and there is no unscoped variant of any of them. The scope
is a column — TEXT NOT NULL with deliberately no DEFAULT, since the empty string
is tenancy.Global() rather than "unset" — it is bound as a tenancy.Scope rather
than a string derived from one, and no read path omits it. It is an argument on
every method, [Store.RecordObject] included, rather than read off the Object the
write is handed; see the Store documentation for why, and for what happens when
the two disagree. An application with a single tenant passes tenancy.Global()
everywhere and behaves exactly as it would have without the column.

# Usage

	store, err := mediaregistry.NewSQLStore(client)
	// ...

	object := &mediaregistry.Object{
		Key:       "avatars/" + userID + "/original.png",
		OwnerID:   userID,
		BelongsTo: mediaregistry.Subject{Type: "user", ID: userID},
	}

	// The row and the profile it hangs off commit together, or neither does.
	// Both writes hand back the row they wrote, so the id the reference needs
	// and the stamps an audit entry records come from the write itself rather
	// than from a read beside it.
	err = client.WithTransaction(ctx, func(tx database.Tx) error {
		recorded, txErr := mediaregistry.StoreAndRecord(ctx, tx, scope, manager, store, object, upload)
		if txErr != nil {
			return txErr
		}

		return profiles.SetAvatar(ctx, tx, scope, userID, recorded.ID)
	})
	if err != nil {
		// ...
	}

	// Later, deciding whether this caller may have the bytes. This read is
	// outside any transaction, so it runs on the client's reader; a caller
	// inside one passes their Tx and sees their own uncommitted writes.
	object, err = store.GetObjectByKey(ctx, client.Reader(), scope, key)
	switch {
	case errors.Is(err, mediaregistry.ErrObjectNotFound):
		// no row: not this tenant's object, or not registered at all
	case err != nil:
		// ...
	case object.OwnerID != callerID:
		// a row, and it is not theirs
	}

# Where the SQL comes from

Every statement this package executes is generated. The corpus is rendered by
database/querygen from the column list in mediaregistry/internal/queries,
checked by sqlc against the DDL mediaregistry/migrations renders, and turned
into the executable querier by sqlc-gen-unison — one shared set of Go types over
three per-dialect statement sets. There is no hand-written SQL here and no
statement assembled at runtime.

The schema is the consumer's to migrate. mediaregistry/migrations renders the
DDL for a dialect and a table prefix; it ships no numbered migration file,
because migration numbers are global per consumer.
*/
package mediaregistry

// Regenerate the .sql corpus after changing mediaregistry/internal/queries or
// the DDL, then run `make unison` to regenerate the querier from it.

//go:generate go run ./internal/queriesgen
