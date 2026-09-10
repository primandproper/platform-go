package mediaregistry

import (
	"context"

	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/filtering"
	"github.com/primandproper/primitives-go/tenancy"
)

// Store is the registry: the rows that say what the objects in storage are.
//
// # The transaction is the caller's
//
// Every write takes a database.Tx and every read takes the wider
// database.SQLQueryExecutor, which is the module's store convention rather than
// anything this package invented. No write here opens a transaction of its own,
// and that absence is the point. The registry row is the metadata half of an
// upload whose bytes are already in a bucket, and the consumer recording one is
// almost always writing a row of their own that references it — the avatar on
// the profile, the attachment on the ticket, the receipt on the invoice. Two
// transactions means one of two outcomes: a reference to an object the registry
// has no row for, or a row for an object nothing points at. The bytes are spent
// either way.
//
// The read takes the wider type so that one method serves both moments. A
// consumer listing a user's uploads for a page holds no transaction and passes
// Client.Reader(); a consumer that has just registered an object passes the Tx
// it wrote through, and sees it. A read narrowed to Tx would force the first
// caller into a transaction it has no use for, and one narrowed to
// Client.Reader() would read a database that does not yet hold the row its
// caller just wrote.
//
// A caller with genuinely nothing to join opens one with Client.WithTransaction
// and passes the Tx it is handed. A Store whose backing is not SQL still takes
// these types; an implementation with no transaction of its own ignores the
// executor, and the seam stays one signature rather than one per backing.
//
// # The scope is an argument, on every method
//
// Every read is scoped and there is no unscoped variant of any of them. That is
// the point rather than a convenience: the caller who reaches for an unscoped
// read is the caller who has not thought about tenancy, and the way to make
// that unreachable is not to ship one. An application with a single tenant
// passes tenancy.Global() everywhere and gets exactly the behavior it would
// have had without the column.
//
// That includes [Store.RecordObject]. It used to take a whole [Object], which
// carries a Scope of its own, and the two had to be reconciled: the argument won,
// a disagreement was refused, and there was a sentinel for the refusal. The
// module's rule is that a scope goes into the query bound as a tenancy.Scope
// rather than derived from some other value, and an entity field is exactly the
// derivation that rule exists to rule out — it makes "which tenant is this write
// for" answerable only by reading a struct the caller assembled somewhere else.
//
// [ObjectInput] settles it by not having the field. There is one scope in the
// call, it is the argument, and there is no disagreement left to have a sentinel
// for.
//
// # Both writes hand back the row they moved
//
// [Store.RecordObject] answers with what it registered and [Store.ArchiveObject]
// with what it hid, each read on the caller's transaction after the statement
// ran. Neither writes to anything the caller still holds: the create takes an
// [ObjectInput] by value, and the archive takes an id.
//
// It is not a convenience, and the reason is the transaction. A caller inside an
// uncommitted transaction has no other way to read the row back — the stamps on
// it are the server's clock, not anything the caller could assemble — and the
// audit entry describing the write is written beside the write, from the row.
// Without this the caller reads first and writes second, and its record then
// describes the row as it stood a statement earlier rather than as the statement
// left it.
//
// The archive is the case where reading first does not merely mislead, and it is
// worth saying why, because settings.Store draws the line in the other place and
// for a reason that does not reach here. A retired setting is a row nothing
// outside the database depends on; an archived object row is the only record of
// the key bytes are still sitting at, and archival here is metadata-only, so the
// bytes outlive it. Once the transaction commits, every read on this interface
// filters archived_at IS NULL and that key is unreachable — which makes the row
// the archive hands back the last place a consumer's retention sweep can read it
// from. The cost is one statement, on a write whose caller is by construction
// deciding what to do about the bytes.
//
// The rejected spelling was mutating the caller's argument in place, which
// delivers the same guarantee — comments.Store.CreateComment does exactly that.
// Returning is the one this module already has more of, and it is the one that
// also works for a write that takes an id rather than an entity, which is what
// made it the module's answer rather than this package's.
//
// Returning alone would not have been enough here, though, and that is why
// [ObjectInput] exists. While the argument and the row were one type, a caller
// that kept using the argument — handing it to a response, an audit entry, a
// cache — compiled cleanly and shipped a zero CreatedAt and a zero Size. The
// return value is where the answer is; the input type is what makes reading it
// from anywhere else fail to build.
//
// # Nothing here touches bytes
//
// The Store writes and reads rows; uploads.UploadManager writes and reads
// objects. [StoreAndRecord] is the convenience that does both, and it is a free
// function rather than a method precisely so that storing and registering stay
// separately callable — a consumer whose bytes arrived through a signed URL
// registers what somebody else stored.
type Store interface {
	// RecordObject writes the row for an object in storage through the caller's
	// transaction, so the row commits with whatever references it, and answers
	// with the row it wrote: the ID it assigned when the input carried none, the
	// CreatedAt the database stamped, and the scope the call named. A nil tx is
	// an error wrapping ErrNilExecutor, and a failed write answers with a nil
	// Object.
	//
	// It takes an ObjectInput rather than an Object, and the returned row is the
	// only place what this call settled can be read. That is a type distinction
	// rather than a naming one: an input has no CreatedAt and no Scope, so a
	// caller cannot pass its own argument where the row belongs and get a
	// zero-valued answer that compiles.
	//
	// The key must be free within the scope, archived rows included: a key
	// already registered is ErrObjectKeyTaken. The collision check, the insert
	// and the read-back all run on tx, so they are one unit with whatever else
	// the caller is writing — and the row handed back is the one this
	// transaction just wrote, rather than a struct whose creation time is a zero
	// time waiting on a commit.
	//
	// The check is what turns the ordinary collision into a sentinel rather
	// than what guarantees uniqueness — the unique index is that. Two
	// registrations racing for one key reach the index, and the loser gets the
	// driver's error rather than ErrObjectKeyTaken.
	RecordObject(ctx context.Context, tx database.Tx, scope tenancy.Scope, in ObjectInput) (*Object, error)

	// GetObject reads one of the scope's objects by row id, on the caller's
	// executor. An archived object reads as absent. A nil q is an error wrapping
	// ErrNilExecutor.
	GetObject(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, objectID string) (*Object, error)

	// GetObjectByKey reads one of the scope's objects by the key its bytes live
	// at, which is what a request holding a URL path rather than a row id runs.
	// An archived object reads as absent. A nil q is an error wrapping
	// ErrNilExecutor.
	GetObjectByKey(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, key string) (*Object, error)

	// ListObjects pages the scope's objects, in the direction the filter names.
	ListObjects(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[Object], error)

	// ListObjectsByOwner pages one owner's objects within the scope.
	ListObjectsByOwner(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, ownerID string, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[Object], error)

	// ListObjectsBySubject pages the objects attached to one thing within the
	// scope. The subject must name something — see ErrUnattachedSubject.
	ListObjectsBySubject(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, subject Subject, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[Object], error)

	// ArchiveObject soft-deletes the row through the caller's transaction, so
	// the row leaves and whatever the caller records about it — the audit entry
	// naming who removed the attachment, the reference it hung off — commit
	// together or not at all. It answers with the row it archived, carrying the
	// ArchivedAt the database stamped. A nil tx is an error wrapping
	// ErrNilExecutor, and a write that moved nothing answers with a nil Object.
	//
	// Metadata-only, and deliberately: the object stays in the bucket. Whether
	// and when the bytes go is the consumer's retention policy, because the
	// registry is not the thing that knows whether a receipt is still needed
	// for tax purposes. Archiving a row that is already archived, or one in
	// another scope, is ErrObjectNotFound.
	ArchiveObject(ctx context.Context, tx database.Tx, scope tenancy.Scope, objectID string) (*Object, error)
}
