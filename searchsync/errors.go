package searchsync

import (
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
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

	// ErrNoRules indicates a side effect built from an empty table.
	//
	// Refused rather than treated as "derive nothing", because a side effect
	// that derives nothing is indistinguishable at runtime from one whose rules
	// never match — and the registration that installed it is a statement that
	// this writer owes index events.
	ErrNoRules = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "no search sync rules")

	// ErrInvalidRule indicates a Rule missing its event type, its topic or its
	// ID key, or naming an op this package does not know. It is reported from
	// NewSideEffect rather than from the enqueue that would have used it: the
	// wiring is where a mistyped rule can still be found by whoever wrote it.
	ErrInvalidRule = platformerrors.New("invalid search sync rule")

	// ErrDuplicateRule indicates two rules that agree on event type, topic and
	// ID key, which would derive the same index event twice from one change.
	ErrDuplicateRule = platformerrors.New("duplicate search sync rule")

	// ErrMissingDocumentID indicates a change matched a rule and carried no
	// identifier under that rule's ID key.
	//
	// It fails the enqueue, and through it the caller's transaction, which is
	// the severe answer chosen deliberately. The alternative is to pass the
	// change over, and a passed-over change is a row the index never hears
	// about again until the next rebuild — the silent divergence this whole
	// package exists to close. A rule whose key the payload does not carry is
	// a wiring mistake, and the first write of that entity is when it should be
	// discovered rather than the first search that misses.
	ErrMissingDocumentID = platformerrors.New("search sync change carries no document ID")

	// ErrNilRegistry indicates RegisterIndex was handed no Registry. It wraps
	// errors.ErrNilInputParameter, so a caller may check either.
	ErrNilRegistry = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil search sync registry")

	// ErrEmptyTopic indicates an IndexSpec with no topic.
	//
	// Refused rather than defaulted to the index name. The two are routinely
	// the same string and are not the same thing — one is a queue the writing
	// process publishes to, the other a label this package's instruments carry
	// — and a default would let a rename of one silently stop matching the
	// other, in a different process, with no error anywhere.
	ErrEmptyTopic = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "empty search sync topic")

	// ErrDuplicateIndex indicates two indexes registered under one name or one
	// topic. See Registry.add for what each of those costs.
	ErrDuplicateIndex = platformerrors.New("duplicate search sync index")

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
