package mediaregistry

import (
	platformerrors "github.com/primandproper/primitives-go/errors"
)

// The sentinels this package returns. They live together because a caller
// deciding what to do next is choosing between them, and a set spread across
// the files that happen to return each one cannot be read as the set it is.
var (
	// ErrNilDatabaseClient indicates a nil database.Client. It wraps
	// errors.ErrNilInputParameter, so a caller may check either.
	ErrNilDatabaseClient = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil upload registry database client")

	// ErrNilExecutor indicates a nil executor. Every method on the Store runs on
	// one the caller supplies — a database.Tx for a write, an executor for a
	// read — so there is no method that can fall back to a connection of the
	// store's own.
	ErrNilExecutor = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil upload registry query executor")

	// ErrNilUploadManager indicates a nil uploads.UploadManager handed to
	// StoreAndRecord.
	ErrNilUploadManager = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil upload manager")

	// ErrNilStore indicates a nil Store handed to StoreAndRecord.
	ErrNilStore = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil registry store")

	// ErrNilReader indicates a nil io.Reader handed to StoreAndRecord.
	ErrNilReader = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil reader")

	// ErrObjectNotFound indicates an object that does not exist in the scope
	// that asked. An object in another scope reads as absent, which is what it
	// is from here — and is the answer that does not turn the read into an
	// oracle for which keys exist in other tenants.
	ErrObjectNotFound = platformerrors.New("object not found")

	// ErrObjectKeyTaken indicates a key already registered in this scope.
	//
	// It is a distinct error rather than a raw constraint violation because the
	// difference between "this key is spoken for" and "the database is unwell"
	// decides whether the caller mints a new key or reports a failure. A
	// registration that hits it has almost always found a genuine collision:
	// the key names bytes that are already in the bucket, registered to
	// somebody.
	ErrObjectKeyTaken = platformerrors.New("object key is already registered")

	// ErrPartialSubject indicates a Subject with a type and no id, or an id and
	// no type. Either alone names nothing that can be looked up — see Subject.
	ErrPartialSubject = platformerrors.New("belongs-to subject has a type or an id but not both")

	// ErrUnattachedSubject indicates the zero Subject handed to a read that
	// lists by subject. Listing the objects attached to nothing is not the
	// question that read answers, and the statement would report every
	// standalone upload in the scope as though they were one thing's
	// attachments.
	ErrUnattachedSubject = platformerrors.New("belongs-to subject names nothing")
)
