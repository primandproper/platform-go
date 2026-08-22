package syncsource

import (
	"context"
	"database/sql"
	"slices"
	"testing"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	searchsync "github.com/primandproper/platform-go/v13/search/sync"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// exampleRow is the row a repository returns; exampleDoc is the subset the
// index holds. Keeping them different types is the point of ConvertFunc.
type exampleRow struct {
	id   string
	name string
}

type exampleDoc struct {
	Name string
}

func convertExample(row *exampleRow) *exampleDoc {
	return &exampleDoc{Name: row.name}
}

// table stands in for a repository: a set of rows and the ID order a keyset
// walk over them produces.
type table struct {
	rows  map[string]*exampleRow
	order []string
}

func newTable(names ...string) *table {
	t := &table{rows: make(map[string]*exampleRow, len(names))}
	for _, name := range names {
		t.rows[name] = &exampleRow{id: name, name: name + " row"}
		t.order = append(t.order, name)
	}

	slices.Sort(t.order)

	return t
}

func (tb *table) fetch(_ context.Context, id string) (*exampleRow, error) {
	row, ok := tb.rows[id]
	if !ok {
		return nil, sql.ErrNoRows
	}

	return row, nil
}

func (tb *table) scan(_ context.Context, after string, limit int) ([]string, error) {
	var page []string
	for _, id := range tb.order {
		if id > after {
			page = append(page, id)
		}

		if len(page) == limit {
			break
		}
	}

	return page, nil
}

// sourceForTest builds a Source over an in-memory table, so the tests exercise
// this package's contract — omission, refill, ordering, error wrapping — rather
// than a repository's.
func sourceForTest(t *testing.T, tb *table) *Source[exampleRow, exampleDoc] {
	t.Helper()

	source, err := New("example", tb.fetch, tb.scan, convertExample)
	must.NoError(t, err)

	return source
}

// ids is what most of the assertions here are actually about.
func ids(docs []searchsync.Document[exampleDoc]) []string {
	out := make([]string, 0, len(docs))
	for _, doc := range docs {
		out = append(out, doc.ID)
	}

	return out
}

func TestNew(T *testing.T) {
	T.Parallel()

	tb := newTable("a")

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		source, err := New("example", tb.fetch, tb.scan, convertExample)
		must.NoError(t, err)
		test.EqOp(t, "example", source.Name())
	})

	T.Run("refuses an empty name", func(t *testing.T) {
		t.Parallel()

		source, err := New("", tb.fetch, tb.scan, convertExample)
		test.ErrorIs(t, err, searchsync.ErrEmptyName)
		test.Nil(t, source)
	})

	T.Run("refuses a nil fetch func", func(t *testing.T) {
		t.Parallel()

		source, err := New("example", nil, tb.scan, convertExample)
		test.ErrorIs(t, err, ErrNilFetchFunc)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
		test.Nil(t, source)
	})

	T.Run("refuses a nil scan func", func(t *testing.T) {
		t.Parallel()

		source, err := New("example", tb.fetch, nil, convertExample)
		test.ErrorIs(t, err, ErrNilScanFunc)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
		test.Nil(t, source)
	})

	T.Run("refuses a nil convert func", func(t *testing.T) {
		t.Parallel()

		source, err := New[exampleRow, exampleDoc]("example", tb.fetch, tb.scan, nil)
		test.ErrorIs(t, err, ErrNilConvertFunc)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
		test.Nil(t, source)
	})
}

func TestSource_Fetch(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		source := sourceForTest(t, newTable("a", "b"))

		docs, err := source.Fetch(t.Context(), "a", "b")
		must.NoError(t, err)

		must.SliceLen(t, 2, docs)
		test.EqOp(t, "a", docs[0].ID)
		test.EqOp(t, "a row", docs[0].Body.Name)
		test.EqOp(t, "b", docs[1].ID)
		test.EqOp(t, "b row", docs[1].Body.Name)
	})

	T.Run("omits rows that no longer exist", func(t *testing.T) {
		t.Parallel()

		// The Syncer relies on this: an omission is how it learns a row was
		// deleted between the event being written and the event being applied,
		// and it removes the document rather than leaving a tombstone.
		// Reporting the miss as an error instead would retry the event until it
		// dead-lettered, with the stale document still in the index.
		source := sourceForTest(t, newTable("a"))

		docs, err := source.Fetch(t.Context(), "a", "vanished")
		must.NoError(t, err)

		test.Eq(t, []string{"a"}, ids(docs))
	})

	T.Run("omits a row a repository reports as a nil entity", func(t *testing.T) {
		t.Parallel()

		// The other spelling of the same outcome, for a repository that
		// translated sql.ErrNoRows before this package ever saw it.
		source, err := New("example",
			func(context.Context, string) (*exampleRow, error) { return nil, nil },
			newTable("a").scan,
			convertExample)
		must.NoError(t, err)

		docs, err := source.Fetch(t.Context(), "a")
		must.NoError(t, err)
		test.SliceEmpty(t, docs)
	})

	T.Run("surfaces a real fetch error, naming the index", func(t *testing.T) {
		t.Parallel()

		expected := platformerrors.New("database on fire")

		source, err := New("example",
			func(context.Context, string) (*exampleRow, error) { return nil, expected },
			newTable("a").scan,
			convertExample)
		must.NoError(t, err)

		docs, err := source.Fetch(t.Context(), "a")
		must.ErrorIs(t, err, expected)
		test.StrContains(t, err.Error(), "example")
		test.StrContains(t, err.Error(), `"a"`)
		test.Nil(t, docs)
	})

	T.Run("refuses a nil body for a row that exists", func(t *testing.T) {
		t.Parallel()

		// Not the same event as a missing row: the row is there and the
		// transform said nothing about it. Indexing it would store a null body
		// under a live ID.
		source, err := New("example",
			newTable("a").fetch,
			newTable("a").scan,
			func(*exampleRow) *exampleDoc { return nil })
		must.NoError(t, err)

		docs, err := source.Fetch(t.Context(), "a")
		must.ErrorIs(t, err, ErrNilDocumentBody)
		test.StrContains(t, err.Error(), "example")
		test.Nil(t, docs)
	})

	T.Run("preserves the order of the IDs it was given", func(t *testing.T) {
		t.Parallel()

		// Scan leans on this rather than sorting: the IDs it walks are checked
		// to ascend, so the documents do too. If this ever stops holding, Scan
		// needs a sort back.
		source := sourceForTest(t, newTable("a", "b", "c"))

		docs, err := source.Fetch(t.Context(), "c", "a", "b")
		must.NoError(t, err)
		test.Eq(t, []string{"c", "a", "b"}, ids(docs))
	})

	T.Run("returns an empty batch for no IDs", func(t *testing.T) {
		t.Parallel()

		source := sourceForTest(t, newTable("a"))

		docs, err := source.Fetch(t.Context())
		must.NoError(t, err)
		test.SliceEmpty(t, docs)
	})
}

func TestSource_Scan(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		source := sourceForTest(t, newTable("a", "b", "c"))

		docs, err := source.Scan(t.Context(), "", 10)
		must.NoError(t, err)
		test.Eq(t, []string{"a", "b", "c"}, ids(docs))
	})

	T.Run("resumes strictly after the cursor", func(t *testing.T) {
		t.Parallel()

		source := sourceForTest(t, newTable("a", "b", "c"))

		docs, err := source.Scan(t.Context(), "a", 10)
		must.NoError(t, err)
		test.Eq(t, []string{"b", "c"}, ids(docs))
	})

	T.Run("honors the limit", func(t *testing.T) {
		t.Parallel()

		source := sourceForTest(t, newTable("a", "b", "c"))

		docs, err := source.Scan(t.Context(), "", 2)
		must.NoError(t, err)
		test.Eq(t, []string{"a", "b"}, ids(docs))
	})

	T.Run("returns nothing for a non-positive limit", func(t *testing.T) {
		t.Parallel()

		tb := newTable("a", "b")
		scanned := 0

		source, err := New("example", tb.fetch,
			func(ctx context.Context, after string, limit int) ([]string, error) {
				scanned++

				return tb.scan(ctx, after, limit)
			}, convertExample)
		must.NoError(t, err)

		docs, err := source.Scan(t.Context(), "", 0)
		must.NoError(t, err)
		test.SliceEmpty(t, docs)
		test.EqOp(t, 0, scanned)
	})

	T.Run("ends the walk on an empty page", func(t *testing.T) {
		t.Parallel()

		source := sourceForTest(t, newTable())

		docs, err := source.Scan(t.Context(), "", 10)
		must.NoError(t, err)
		test.SliceEmpty(t, docs)
	})

	T.Run("surfaces a scan error, naming the index", func(t *testing.T) {
		t.Parallel()

		expected := platformerrors.New("database on fire")

		source, err := New("example", newTable("a").fetch,
			func(context.Context, string, int) ([]string, error) { return nil, expected },
			convertExample)
		must.NoError(t, err)

		docs, err := source.Scan(t.Context(), "", 10)
		must.ErrorIs(t, err, expected)
		test.StrContains(t, err.Error(), "example")
		test.Nil(t, docs)
	})

	T.Run("surfaces a fetch error from the page it is building", func(t *testing.T) {
		t.Parallel()

		expected := platformerrors.New("database on fire")

		source, err := New("example",
			func(context.Context, string) (*exampleRow, error) { return nil, expected },
			newTable("a").scan, convertExample)
		must.NoError(t, err)

		docs, err := source.Scan(t.Context(), "", 10)
		must.ErrorIs(t, err, expected)
		test.Nil(t, docs)
	})
}

func TestSource_Scan_refill(T *testing.T) {
	T.Parallel()

	T.Run("refills a page a vanished row shortened", func(t *testing.T) {
		t.Parallel()

		// searchsync.Scanner reads a page shorter than limit as the end of the
		// stream. Without the refill, one deleted row inside a full page ends a
		// reindex partway through and reports success.
		tb := newTable("a", "b", "c", "d", "e")
		delete(tb.rows, "b")

		source := sourceForTest(t, tb)

		docs, err := source.Scan(t.Context(), "", 2)
		must.NoError(t, err)
		test.Eq(t, []string{"a", "c"}, ids(docs))
	})

	T.Run("stops refilling when the ID stream is exhausted", func(t *testing.T) {
		t.Parallel()

		// A short page is only the end of the walk when the ScanFunc is the one
		// that came up short.
		tb := newTable("a", "b", "c")
		delete(tb.rows, "b")

		source := sourceForTest(t, tb)

		docs, err := source.Scan(t.Context(), "", 10)
		must.NoError(t, err)
		test.Eq(t, []string{"a", "c"}, ids(docs))
	})

	T.Run("asks only for what the page is still missing", func(t *testing.T) {
		t.Parallel()

		tb := newTable("a", "b", "c", "d")
		delete(tb.rows, "a")

		var asked []int

		source, err := New("example", tb.fetch,
			func(ctx context.Context, after string, limit int) ([]string, error) {
				asked = append(asked, limit)

				return tb.scan(ctx, after, limit)
			}, convertExample)
		must.NoError(t, err)

		docs, err := source.Scan(t.Context(), "", 3)
		must.NoError(t, err)
		test.Eq(t, []string{"b", "c", "d"}, ids(docs))
		test.Eq(t, []int{3, 1}, asked)
	})

	T.Run("holds a ScanFunc that overshoots to the limit", func(t *testing.T) {
		t.Parallel()

		tb := newTable("a", "b", "c")

		source, err := New("example", tb.fetch,
			func(ctx context.Context, after string, _ int) ([]string, error) {
				return tb.scan(ctx, after, 3)
			}, convertExample)
		must.NoError(t, err)

		docs, err := source.Scan(t.Context(), "", 2)
		must.NoError(t, err)

		// The third document is not dropped, only deferred: the next page
		// resumes from the last ID this one actually used.
		test.Eq(t, []string{"a", "b"}, ids(docs))

		next, err := source.Scan(t.Context(), docs[len(docs)-1].ID, 2)
		must.NoError(t, err)
		test.Eq(t, []string{"c"}, ids(next))
	})

	T.Run("returns an empty page when every row in the stream is gone", func(t *testing.T) {
		t.Parallel()

		tb := newTable("a", "b", "c")
		tb.rows = map[string]*exampleRow{}

		source := sourceForTest(t, tb)

		docs, err := source.Scan(t.Context(), "", 2)
		must.NoError(t, err)
		test.SliceEmpty(t, docs)
	})
}

func TestSource_Scan_ordering(T *testing.T) {
	T.Parallel()

	T.Run("refuses IDs that do not ascend", func(t *testing.T) {
		t.Parallel()

		// This is the check the sort would otherwise hide. A ScanFunc walking
		// in a locale collation arrives downstream looking perfectly ordered
		// while skipping rows between pages, and a pruning reindex deletes live
		// documents on the strength of that ordering.
		source, err := New("example", newTable("a", "b", "c").fetch,
			func(context.Context, string, int) ([]string, error) {
				return []string{"c", "a", "b"}, nil
			}, convertExample)
		must.NoError(t, err)

		docs, err := source.Scan(t.Context(), "", 10)
		must.ErrorIs(t, err, searchsync.ErrUnsortedScan)
		test.StrContains(t, err.Error(), "example")
		test.Nil(t, docs)
	})

	T.Run("refuses a repeated ID", func(t *testing.T) {
		t.Parallel()

		source, err := New("example", newTable("a").fetch,
			func(context.Context, string, int) ([]string, error) {
				return []string{"a", "a"}, nil
			}, convertExample)
		must.NoError(t, err)

		_, err = source.Scan(t.Context(), "", 10)
		must.ErrorIs(t, err, searchsync.ErrUnsortedScan)
	})

	T.Run("refuses an ID at or before the cursor", func(t *testing.T) {
		t.Parallel()

		// A ScanFunc that ignores `after` would otherwise page over the same
		// rows forever, refilling a page it can never fill.
		source, err := New("example", newTable("a", "b").fetch,
			func(context.Context, string, int) ([]string, error) {
				return []string{"a"}, nil
			}, convertExample)
		must.NoError(t, err)

		_, err = source.Scan(t.Context(), "a", 10)
		must.ErrorIs(t, err, searchsync.ErrUnsortedScan)
	})

	T.Run("refuses an empty ID", func(t *testing.T) {
		t.Parallel()

		source, err := New("example", newTable("a").fetch,
			func(context.Context, string, int) ([]string, error) {
				return []string{""}, nil
			}, convertExample)
		must.NoError(t, err)

		_, err = source.Scan(t.Context(), "", 10)
		must.ErrorIs(t, err, searchsync.ErrEmptyDocumentID)
		test.StrContains(t, err.Error(), "example")
	})

	T.Run("checks each refilled page against the advanced cursor", func(t *testing.T) {
		t.Parallel()

		// The second page has to ascend from where the first one ended, not
		// from where the caller's cursor did.
		tb := newTable("a", "b", "c")
		delete(tb.rows, "a")

		pages := 0

		source, err := New("example", tb.fetch,
			func(context.Context, string, int) ([]string, error) {
				pages++
				if pages == 1 {
					return []string{"a", "b"}, nil
				}

				return []string{"b"}, nil
			}, convertExample)
		must.NoError(t, err)

		_, err = source.Scan(t.Context(), "", 2)
		must.ErrorIs(t, err, searchsync.ErrUnsortedScan)
		test.StrContains(t, err.Error(), `"b" followed "b"`)
	})
}

// TestSource_agreement is the reason this type exists: the two seams cannot
// produce different documents for the same row, because there is only one
// transform and Scan reaches it through Fetch.
func TestSource_agreement(T *testing.T) {
	T.Parallel()

	T.Run("scan and fetch produce the same document", func(t *testing.T) {
		t.Parallel()

		tb := newTable("a", "b", "c")
		source := sourceForTest(t, tb)
		ctx := t.Context()

		scanned, err := source.Scan(ctx, "", 10)
		must.NoError(t, err)
		must.SliceLen(t, 3, scanned)

		for _, doc := range scanned {
			fetched, fetchErr := source.Fetch(ctx, doc.ID)
			must.NoError(t, fetchErr)
			must.SliceLen(t, 1, fetched)

			test.EqOp(t, doc.ID, fetched[0].ID)
			test.EqOp(t, *doc.Body, *fetched[0].Body)
		}
	})

	T.Run("scan reads every row a fetch of the same IDs would", func(t *testing.T) {
		t.Parallel()

		tb := newTable("a", "b", "c", "d", "e", "f", "g")
		source := sourceForTest(t, tb)
		ctx := t.Context()

		// Walk in pages of two, the way a Reindexer with a batch size of two
		// would, and check the whole table came back exactly once.
		var walked []string

		after := ""
		for {
			page, err := source.Scan(ctx, after, 2)
			must.NoError(t, err)

			if len(page) == 0 {
				break
			}

			walked = append(walked, ids(page)...)
			after = page[len(page)-1].ID

			if len(page) < 2 {
				break
			}
		}

		test.Eq(t, tb.order, walked)
	})
}
