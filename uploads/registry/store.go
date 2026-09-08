package registry

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
// That includes [Store.RecordObject], which takes a whole [Object] that already
// carries a Scope. It reads the scope off the argument rather than off
// Object.Scope, and the alternative — letting an entity that carries a scope
// supply its own, so the explicit argument appears only where there is no
// entity — was considered and rejected. The module's rule is that a scope goes
// into the query bound as a tenancy.Scope rather than derived from some other
// value, and an entity field is exactly the derivation that rule exists to rule
// out: it makes "which tenant is this write for" answerable only by reading a
// struct the caller assembled somewhere else. An Object.Scope that disagrees
// with the argument is [ErrScopeMismatch] rather than either value quietly
// winning; an unset one adopts the argument.
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
	// transaction, so the row commits with whatever references it. It fills in
	// the object's ID when it has none and its CreatedAt from the row, and
	// writes the scope the call named onto it. A nil tx is an error wrapping
	// ErrNilExecutor.
	//
	// The key must be free within the scope, archived rows included: a key
	// already registered is ErrObjectKeyTaken. The collision check, the insert
	// and the creation-time read-back all run on tx, so they are one unit with
	// whatever else the caller is writing — and the creation time handed back is
	// the one this transaction just wrote, rather than a zero time waiting on a
	// commit.
	//
	// The check is what turns the ordinary collision into a sentinel rather
	// than what guarantees uniqueness — the unique index is that. Two
	// registrations racing for one key reach the index, and the loser gets the
	// driver's error rather than ErrObjectKeyTaken.
	RecordObject(ctx context.Context, tx database.Tx, scope tenancy.Scope, object *Object) error

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
	// together or not at all. A nil tx is an error wrapping ErrNilExecutor.
	//
	// Metadata-only, and deliberately: the object stays in the bucket. Whether
	// and when the bytes go is the consumer's retention policy, because the
	// registry is not the thing that knows whether a receipt is still needed
	// for tax purposes. Archiving a row that is already archived, or one in
	// another scope, is ErrObjectNotFound.
	ArchiveObject(ctx context.Context, tx database.Tx, scope tenancy.Scope, objectID string) error
}
