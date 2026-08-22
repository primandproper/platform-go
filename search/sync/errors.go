package searchsync

import (
	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

var (
	// ErrEmptyName indicates a Syncer or Reindexer built without an index name.
	//
	// Refused rather than defaulted, because the name is the metric attribute
	// every instrument here carries. A service syncing three indexes under one
	// blank name has one lag histogram covering all three, which is the reading
	// most likely to be quoted and least likely to be true.
	ErrEmptyName = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "empty search index name")

	// ErrNilSource indicates a nil Fetcher or Scanner. It wraps
	// errors.ErrNilInputParameter, so a caller may check either.
	ErrNilSource = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil search sync source")

	// ErrNilTarget indicates a nil Target. It wraps
	// errors.ErrNilInputParameter, so a caller may check either.
	ErrNilTarget = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil search sync target")

	// ErrNilIndex indicates a nil index handed to TextTarget or VectorTarget.
	// It wraps errors.ErrNilInputParameter, so a caller may check either.
	ErrNilIndex = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil search index")

	// ErrInvalidEvent indicates an event that cannot be applied — no document
	// ID, or an op this package does not know. Handle wraps it with
	// retry.Unretryable, because a payload that is malformed now will be
	// malformed on every redelivery, and each of those attempts is latency the
	// healthy events behind it spend waiting.
	ErrInvalidEvent = platformerrors.New("invalid search sync event")

	// ErrEmptyDocumentID indicates a Source produced a document with no ID.
	// Indexing it would either overwrite whatever the backend keys an empty ID
	// to or fail inside the backend, and neither says what actually went wrong.
	ErrEmptyDocumentID = platformerrors.New("search sync document has no ID")

	// ErrUnsortedScan indicates a Scanner or Enumerator returned IDs that do
	// not ascend in byte order.
	//
	// It aborts the reindex rather than being tolerated, and that is the point
	// of checking. Pruning merges the source's ordered IDs against the index's,
	// and treats an index ID that the source stream has passed as a document
	// whose row is gone. If either stream is in a different order — a collation
	// that sorts case-insensitively is enough — that inference is wrong and the
	// reindex deletes documents that are perfectly alive.
	ErrUnsortedScan = platformerrors.New("search sync scan returned out-of-order IDs")
)
