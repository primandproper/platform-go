package mediaregistry

import (
	"context"
	"slices"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// MaxObjectIDsPerRead is the largest set [Store.ListObjectsByIDs] will read in
// one statement. A larger one is [ErrTooManyObjectIDs].
//
// It is a judgment about what that read is for rather than the ceiling any
// engine imposes. The dialects disagree about the ceiling and two of them have
// one at all: SQLite and MySQL bind a placeholder per id, against compiled-in
// limits of 32766 and 65535, while Postgres binds the set as a single array and
// has none. A number chosen at either engine's edge would be a number that
// works everywhere and means nothing — the set that read exists for is a page of
// rows a consumer already holds, and a page is tens or hundreds.
//
// The bound is here rather than left to the caller because the thing a caller
// would need in order to choose one is the dialect ceiling, which is a fact
// about this module's storage and not about their code. Choosing it wrong is
// silent on Postgres.
const MaxObjectIDsPerRead = 1000

// ListObjectsByIDsInBatches reads a set of any size, in as many batched reads as
// it takes, and answers as though it were one.
//
// It is the convenience, not the contract, in [StoreAndRecord]'s sense: a free
// function over the Store rather than a second method on it, so an implementer
// owes one batched read and not two, and so this composes over whichever
// implementation a consumer has. Nothing in Store knows it exists.
//
// It exists because the loop it replaces can be wrong in a way the caller
// cannot see. The batch size is a dialect fact — the ceilings live in this
// module, and a caller who picks one runs fine on Postgres and on MySQL and
// fails past 32766 on SQLite. And a loop that concatenates its batches gets
// rows ordered within each batch and not across them, which quietly breaks the
// id ordering the single read promises, so the bug is in the result rather than
// in an error.
//
// The answer is the single read's for any size. The set is sorted and its
// duplicates dropped before it is cut into batches, so the batches are
// contiguous runs of one order and concatenating them is that order — and an id
// named twice is one row, exactly as a single statement's IN would have made it.
// A set no larger than [MaxObjectIDsPerRead] is handed straight to the store, so
// the common call is the one statement it always was.
//
// Everything else is the store's: the executor, the scope, and every refusal.
// An id that names nothing in the scope is absent rather than an error, so a set
// of any size is still a partial answer rather than an all-or-nothing one.
//
// The batches run on the executor they are given and are not one unit of work
// unless that executor is already a transaction. A caller who needs the whole
// set to be one consistent read passes a database.Tx, which is the same thing
// that makes the single read consistent with the caller's own writes.
func ListObjectsByIDsInBatches(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	store Store,
	objectIDs []string,
) ([]*Object, error) {
	if store == nil {
		return nil, ErrNilStore
	}

	// Delegated whole rather than run through the batching below, so a call that
	// fits is byte for byte the call it was before this function existed — one
	// statement, the store's own refusals, and no copy of the caller's set.
	if len(objectIDs) <= MaxObjectIDsPerRead {
		return store.ListObjectsByIDs(ctx, q, scope, objectIDs)
	}

	// Sorted into a copy, never in place: the set belongs to the caller, and a
	// read that reordered the slice it was handed would be the same kind of
	// surprise as a write that filled in its argument.
	unique := slices.Compact(slices.Sorted(slices.Values(objectIDs)))

	objects := make([]*Object, 0, len(unique))

	for batch := range slices.Chunk(unique, MaxObjectIDsPerRead) {
		read, err := store.ListObjectsByIDs(ctx, q, scope, batch)
		if err != nil {
			return nil, err
		}

		objects = append(objects, read...)
	}

	return objects, nil
}
