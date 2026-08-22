package syncsource

import (
	"context"
	"database/sql"
	"errors"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	searchsync "github.com/primandproper/platform-go/v13/search/sync"
)

type (
	// FetchFunc reads one row by ID. It is the repository's existing
	// get-by-ID method.
	//
	// A row that is gone is reported as sql.ErrNoRows or as a nil entity with
	// no error, and either is an expected outcome here rather than a failure —
	// see Fetch. Any other error is a real one and fails the batch.
	FetchFunc[E any] func(ctx context.Context, id string) (*E, error)

	// ScanFunc returns up to limit IDs sorting strictly after `after`, in
	// ascending byte order. It is the repository's keyset walk over the table —
	// conventionally its ScanXIDsForReindex method — and a page shorter than
	// limit means the walk is over.
	//
	// Ascending *byte* order, as Go's < compares strings, not whatever
	// collation the database defaults to. Against Postgres that is ORDER BY id
	// COLLATE "C"; see the package documentation for what the default collation
	// costs a reindex.
	ScanFunc func(ctx context.Context, after string, limit int) ([]string, error)

	// ConvertFunc turns a row into the subset that is actually indexed. It is
	// called only for rows that exist, so it never receives nil, and returning
	// nil for a row that does exist is ErrNilDocumentBody.
	ConvertFunc[E, T any] func(*E) *T
)

// Source is a searchsync.Fetcher and a searchsync.Scanner over one entity,
// built from the three functions that are all the two seams actually differ in.
//
// E is the row the repository returns; T is the search subset the index holds.
type Source[E, T any] struct {
	fetch   FetchFunc[E]
	scan    ScanFunc
	convert ConvertFunc[E, T]
	name    string
}

var (
	_ searchsync.Fetcher[struct{}] = (*Source[struct{}, struct{}])(nil)
	_ searchsync.Scanner[struct{}] = (*Source[struct{}, struct{}])(nil)
)

// New builds a Source over one entity.
//
// name is the index this Source feeds. It appears in every error this package
// returns, so a failure says which index it came from rather than only that a
// fetch failed, and it is the name NewSyncer and NewReindexer carry into their
// spans and metric attributes.
//
// The three functions are refused rather than defaulted when nil. A Source with
// a nil function is one that panics on the first event it is handed, in a
// background consumer, some time after the wiring that built it returned
// successfully.
func New[E, T any](name string, fetch FetchFunc[E], scan ScanFunc, convert ConvertFunc[E, T]) (*Source[E, T], error) {
	if name == "" {
		return nil, searchsync.ErrEmptyName
	}
	if fetch == nil {
		return nil, ErrNilFetchFunc
	}
	if scan == nil {
		return nil, ErrNilScanFunc
	}
	if convert == nil {
		return nil, ErrNilConvertFunc
	}

	return &Source[E, T]{name: name, fetch: fetch, scan: scan, convert: convert}, nil
}

// Name is the index this Source feeds.
func (s *Source[E, T]) Name() string {
	return s.name
}

// Fetch returns the current document for each of ids, omitting any whose row no
// longer exists.
//
// The omission is the interesting half of the contract. A missing row is not an
// error and must not be reported as one: it is how the Syncer learns that a row
// was deleted between the event being written and the event being applied, and
// it responds by removing the document rather than leaving a tombstone in the
// index. Reporting it as an error instead would retry the event until it
// dead-lettered, and leave the deleted document in the index the whole time.
//
// Documents come back in the order the IDs were given, minus the omissions.
// searchsync.Fetcher promises no order and the change feed asks for one
// document at a time, so nothing outside this package should lean on it — but
// Scan does, and it is the reason Scan needs no sort of its own.
func (s *Source[E, T]) Fetch(ctx context.Context, ids ...string) ([]searchsync.Document[T], error) {
	documents := make([]searchsync.Document[T], 0, len(ids))

	for _, id := range ids {
		entity, err := s.fetch(ctx, id)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}

			return nil, platformerrors.Wrapf(err, "fetching %s document %q", s.name, id)
		}

		// The other spelling of the same outcome. Repositories disagree about
		// which one they use — sql.ErrNoRows straight from the driver, or a nil
		// entity from one that already translated it — and both mean the row is
		// gone.
		if entity == nil {
			continue
		}

		body := s.convert(entity)
		if body == nil {
			return nil, platformerrors.Wrapf(ErrNilDocumentBody, "converting %s document %q", s.name, id)
		}

		documents = append(documents, searchsync.Document[T]{ID: id, Body: body})
	}

	return documents, nil
}

// Scan returns up to limit documents whose IDs sort strictly after `after`, in
// ascending byte order, for a reindex to walk.
//
// It pages IDs through the ScanFunc and turns them into documents through
// Fetch, which is what makes the two seams agree: there is one row-to-document
// transform, and the reindex reaches it the same way the change feed does.
//
// Two things it does that a straight scan-then-fetch does not:
//
// It refills a page that Fetch shortened. searchsync.Scanner reads a page
// shorter than limit as the end of the stream, and Fetch omits rows that have
// been deleted, so a full page containing one vanished row would otherwise end
// a reindex partway through and report success. This asks for more IDs until it
// has limit documents or the ScanFunc itself comes up short, which is the only
// thing that actually means the walk is over.
//
// It checks the IDs the ScanFunc returned rather than sorting them, and fails
// with searchsync.ErrUnsortedScan if they do not ascend strictly after the
// cursor. Sorting would repair the symptom and hide the disease: a ScanFunc in
// a locale collation returns a page that sorts into perfect order and still
// skips every row between this page's largest ID and the next page's first,
// because the query — not the page — is what resumes. A pruning reindex then
// deletes those live documents. The check is also what guarantees the cursor
// advances, so the refill above cannot spin.
//
// The two together are why nothing here sorts: the IDs ascend because they were
// checked to, and Fetch hands documents back in the order it was given them.
func (s *Source[E, T]) Scan(ctx context.Context, after string, limit int) ([]searchsync.Document[T], error) {
	if limit <= 0 {
		return nil, nil
	}

	documents := make([]searchsync.Document[T], 0, limit)
	cursor := after

	for len(documents) < limit {
		want := limit - len(documents)

		ids, err := s.scan(ctx, cursor, want)
		if err != nil {
			return nil, platformerrors.Wrapf(err, "scanning %s IDs for reindex after %q", s.name, cursor)
		}

		if len(ids) == 0 {
			break
		}

		if err = s.checkOrder(cursor, ids); err != nil {
			return nil, err
		}

		// A ScanFunc that overshoots its limit is held to it here rather than
		// passed on, because "up to limit" is what this promises the Reindexer
		// and the extra IDs are read on the next page anyway. Trimming after
		// the order check is what keeps the cursor pointing at the last ID
		// actually used.
		if len(ids) > want {
			ids = ids[:want]
		}

		page, err := s.Fetch(ctx, ids...)
		if err != nil {
			return nil, err
		}

		documents = append(documents, page...)
		cursor = ids[len(ids)-1]

		// A page shorter than what was asked for is the ScanFunc's own
		// statement that there is nothing left, and it is the only condition
		// that ends this loop other than a full page of documents. The order
		// check above guarantees the cursor advanced, so a ScanFunc that
		// ignores it cannot spin here.
		if len(ids) < want {
			break
		}
	}

	return documents, nil
}

// checkOrder holds the ScanFunc to the contract the reindex depends on: IDs
// that are non-empty and ascend strictly, starting strictly after the cursor.
func (s *Source[E, T]) checkOrder(cursor string, ids []string) error {
	previous := cursor

	for _, id := range ids {
		if id == "" {
			return platformerrors.Wrapf(searchsync.ErrEmptyDocumentID, "scanning %s IDs for reindex", s.name)
		}

		if id <= previous {
			return platformerrors.Wrapf(searchsync.ErrUnsortedScan,
				"scanning %s IDs for reindex: %q followed %q", s.name, id, previous)
		}

		previous = id
	}

	return nil
}
