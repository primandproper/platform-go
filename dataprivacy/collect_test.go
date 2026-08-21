package dataprivacy

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	platformerrors "github.com/primandproper/platform-go/v12/errors"
	"github.com/primandproper/platform-go/v12/filtering"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// collectRow is the smallest thing a paged read can return: something with an
// identifier, because the identifier is the cursor.
type collectRow struct {
	ID string `json:"id"`
}

func collectRowID(row *collectRow) string { return row.ID }

func collectRows(n int) []*collectRow {
	rows := make([]*collectRow, 0, n)
	for i := range n {
		rows = append(rows, &collectRow{ID: fmt.Sprintf("row-%02d", i)})
	}

	return rows
}

// pagedStore answers reads out of a fixed slice the way a keyset store does:
// resume after the row the cursor names, and answer at most pageSize rows
// however many were asked for. Reporting the size it actually applied is the
// part that matters — a store that clamps below the requested size is what
// distinguishes a walk that reads the cursor rules correctly from one that
// stops after the first page.
type pagedStore struct {
	rows     []*collectRow
	cursors  []string
	calls    int
	pageSize uint16
}

func newPagedStore(rows []*collectRow, pageSize uint16) *pagedStore {
	return &pagedStore{rows: rows, pageSize: pageSize}
}

func (s *pagedStore) fetch(_ context.Context, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[collectRow], error) {
	s.calls++

	cursor := ""
	if filter.Cursor != nil {
		cursor = *filter.Cursor
	}

	s.cursors = append(s.cursors, cursor)

	start := 0

	if cursor != "" {
		for i, row := range s.rows {
			if row.ID == cursor {
				start = i + 1

				break
			}
		}
	}

	size := s.pageSize
	if filter.MaxResponseSize != nil && *filter.MaxResponseSize < size {
		size = *filter.MaxResponseSize
	}

	end := min(start+int(size), len(s.rows))

	applied := *filter
	applied.MaxResponseSize = &size

	return filtering.NewQueryFilteredResult(s.rows[start:end], uint64(len(s.rows)), uint64(len(s.rows)), collectRowID, &applied), nil
}

func collectedIDs(rows []collectRow) []string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}

	return ids
}

func TestFragment(T *testing.T) {
	T.Parallel()

	T.Run("nothing held is nil rather than null", func(t *testing.T) {
		t.Parallel()

		// The whole point of the helper: a section the artifact omits, not one
		// written as null. An artifact padded with empty objects for every
		// domain in the application reads as a form rather than an answer.
		fragment, err := Fragment(false, []collectRow{})
		must.NoError(t, err)
		test.Nil(t, fragment)
	})

	T.Run("held data is encoded", func(t *testing.T) {
		t.Parallel()

		fragment, err := Fragment(true, []collectRow{{ID: "a"}})
		must.NoError(t, err)
		test.EqOp(t, `[{"id":"a"}]`, string(fragment))
	})

	T.Run("held is the domain's answer, not the value's", func(t *testing.T) {
		t.Parallel()

		// An empty slice can still be data held about the subject — a settings
		// row that exists with nothing in it — so the flag is passed rather
		// than derived.
		fragment, err := Fragment(true, []collectRow{})
		must.NoError(t, err)
		test.EqOp(t, `[]`, string(fragment))
	})

	T.Run("an unencodable value is an error", func(t *testing.T) {
		t.Parallel()

		fragment, err := Fragment(true, make(chan int))
		test.Error(t, err)
		test.Nil(t, fragment)
	})

	T.Run("an unencodable value nothing holds is still nothing", func(t *testing.T) {
		t.Parallel()

		fragment, err := Fragment(false, make(chan int))
		must.NoError(t, err)
		test.Nil(t, fragment)
	})
}

func TestCollectAll(T *testing.T) {
	T.Parallel()

	T.Run("a single short page is one read", func(t *testing.T) {
		t.Parallel()

		store := newPagedStore(collectRows(3), filtering.MaxQueryFilterLimit)

		rows, err := CollectAll(t.Context(), store.fetch)
		must.NoError(t, err)
		test.Eq(t, []string{"row-00", "row-01", "row-02"}, collectedIDs(rows))
		test.EqOp(t, 1, store.calls)
	})

	T.Run("an empty collection collects nothing", func(t *testing.T) {
		t.Parallel()

		store := newPagedStore(nil, filtering.MaxQueryFilterLimit)

		rows, err := CollectAll(t.Context(), store.fetch)
		must.NoError(t, err)
		// nil rather than an empty slice, so it composes with Fragment's held
		// flag without a length check at the call site.
		test.Nil(t, rows)
		test.EqOp(t, 1, store.calls)
	})

	T.Run("the walk continues past the first page", func(t *testing.T) {
		t.Parallel()

		// The defect this exists to prevent: a collector that reads one page
		// and stops returns a truncated subject access request that looks
		// exactly like a correct one.
		store := newPagedStore(collectRows(25), 10)

		rows, err := CollectAll(t.Context(), store.fetch)
		must.NoError(t, err)
		test.SliceLen(t, 25, rows)
		test.EqOp(t, 3, store.calls)

		// Each read resumes after the last row of the one before it.
		test.Eq(t, []string{"", "row-09", "row-19"}, store.cursors)
	})

	T.Run("a store clamping below the requested size still walks", func(t *testing.T) {
		t.Parallel()

		// The walk asks for MaxQueryFilterLimit; this store answers 5. Judging
		// shortness against the requested size would call the first page short
		// and stop with a fifth of the subject's data.
		store := newPagedStore(collectRows(20), 5)

		rows, err := CollectAll(t.Context(), store.fetch)
		must.NoError(t, err)
		test.SliceLen(t, 20, rows)
		test.EqOp(t, 5, store.calls)
	})

	T.Run("a collection that divides evenly ends on the empty page", func(t *testing.T) {
		t.Parallel()

		// Twenty rows in pages of ten: neither page is short, so the walk ends
		// on the empty third page, whose cursor is empty because it held no
		// rows.
		store := newPagedStore(collectRows(20), 10)

		rows, err := CollectAll(t.Context(), store.fetch)
		must.NoError(t, err)
		test.SliceLen(t, 20, rows)
		test.EqOp(t, 3, store.calls)
	})

	T.Run("rows come back in order across pages", func(t *testing.T) {
		t.Parallel()

		store := newPagedStore(collectRows(7), 3)

		rows, err := CollectAll(t.Context(), store.fetch)
		must.NoError(t, err)
		test.Eq(t, []string{"row-00", "row-01", "row-02", "row-03", "row-04", "row-05", "row-06"}, collectedIDs(rows))
	})

	T.Run("nil rows are skipped", func(t *testing.T) {
		t.Parallel()

		fetch := func(_ context.Context, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[collectRow], error) {
			return filtering.NewQueryFilteredResult([]*collectRow{{ID: "a"}, nil, {ID: "b"}}, 3, 3, func(row *collectRow) string {
				if row == nil {
					return ""
				}

				return row.ID
			}, filter), nil
		}

		rows, err := CollectAll(t.Context(), fetch)
		must.NoError(t, err)
		test.Eq(t, []string{"a", "b"}, collectedIDs(rows))
	})

	T.Run("a nil read is refused", func(t *testing.T) {
		t.Parallel()

		rows, err := CollectAll[collectRow](t.Context(), nil)
		test.ErrorIs(t, err, ErrNilFetch)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
		test.Nil(t, rows)
	})

	T.Run("a nil page is an error rather than an end", func(t *testing.T) {
		t.Parallel()

		// Treating it as the end would produce a short export that reads as a
		// complete one.
		fetch := func(context.Context, *filtering.QueryFilter) (*filtering.QueryFilteredResult[collectRow], error) {
			return nil, nil
		}

		rows, err := CollectAll(t.Context(), fetch)
		test.ErrorIs(t, err, ErrNilPage)
		test.Nil(t, rows)
	})

	T.Run("an error discards what was collected", func(t *testing.T) {
		t.Parallel()

		// A Collector must not return partially-collected data alongside an
		// error, so neither may the walk underneath it.
		boom := platformerrors.New("boom")
		calls := 0

		fetch := func(_ context.Context, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[collectRow], error) {
			calls++

			if calls > 1 {
				return nil, boom
			}

			page := filtering.NewQueryFilteredResult(collectRows(2), 2, 2, collectRowID, filter)
			page.MaxResponseSize = 2

			return page, nil
		}

		rows, err := CollectAll(t.Context(), fetch)
		test.ErrorIs(t, err, boom)
		test.Nil(t, rows)
		test.EqOp(t, 2, calls)
	})

	T.Run("a cursor that does not advance is reported", func(t *testing.T) {
		t.Parallel()

		// Stopping instead would silently drop every row past the stall.
		fetch := func(_ context.Context, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[collectRow], error) {
			page := filtering.NewQueryFilteredResult(collectRows(2), 2, 2, collectRowID, filter)
			page.MaxResponseSize = 2

			return page, nil
		}

		rows, err := CollectAll(t.Context(), fetch)
		test.ErrorIs(t, err, ErrCursorStalled)
		test.Nil(t, rows)
	})

	T.Run("cancellation stops the walk between pages", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(t.Context())
		calls := 0

		fetch := func(_ context.Context, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[collectRow], error) {
			calls++

			cancel()

			page := filtering.NewQueryFilteredResult(collectRows(2), 2, 2, collectRowID, filter)
			page.MaxResponseSize = 2

			return page, nil
		}

		rows, err := CollectAll(ctx, fetch)
		test.ErrorIs(t, err, context.Canceled)
		test.Nil(t, rows)
		test.EqOp(t, 1, calls)
	})
}

func TestCollectorFor(T *testing.T) {
	T.Parallel()

	T.Run("collects every page into one fragment", func(t *testing.T) {
		t.Parallel()

		store := newPagedStore(collectRows(12), 5)

		collector := CollectorFor(func(ctx context.Context, _ Subject, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[collectRow], error) {
			return store.fetch(ctx, filter)
		})
		must.NotNil(t, collector)

		fragment, err := collector.Collect(t.Context(), Subject{ID: "subject", Type: SubjectUser})
		must.NoError(t, err)

		var decoded []collectRow
		must.NoError(t, json.Unmarshal(fragment, &decoded))
		test.SliceLen(t, 12, decoded)
		test.EqOp(t, "row-11", decoded[11].ID)
	})

	T.Run("a domain holding nothing omits its section", func(t *testing.T) {
		t.Parallel()

		store := newPagedStore(nil, filtering.MaxQueryFilterLimit)

		collector := CollectorFor(func(ctx context.Context, _ Subject, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[collectRow], error) {
			return store.fetch(ctx, filter)
		})

		fragment, err := collector.Collect(t.Context(), Subject{ID: "subject"})
		must.NoError(t, err)
		test.Nil(t, fragment)
	})

	T.Run("the subject reaches the read", func(t *testing.T) {
		t.Parallel()

		subject := Subject{ID: "subject", Scope: "account", Type: SubjectUser}

		var seen []Subject

		collector := CollectorFor(func(_ context.Context, s Subject, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[collectRow], error) {
			seen = append(seen, s)

			return filtering.NewQueryFilteredResult(collectRows(1), 1, 1, collectRowID, filter), nil
		})

		_, err := collector.Collect(t.Context(), subject)
		must.NoError(t, err)
		test.Eq(t, []Subject{subject}, seen)
	})

	T.Run("a failed read is not a fragment", func(t *testing.T) {
		t.Parallel()

		boom := platformerrors.New("boom")

		collector := CollectorFor(func(context.Context, Subject, *filtering.QueryFilter) (*filtering.QueryFilteredResult[collectRow], error) {
			return nil, boom
		})

		fragment, err := collector.Collect(t.Context(), Subject{ID: "subject"})
		test.ErrorIs(t, err, boom)
		test.Nil(t, fragment)
	})

	T.Run("a nil read fails at wiring time", func(t *testing.T) {
		t.Parallel()

		// A nil Collector is what Registry.RegisterCollector refuses, so the
		// mistake surfaces at startup rather than as a domain missing from the
		// first export.
		collector := CollectorFor[collectRow](nil)
		test.Nil(t, collector)

		test.ErrorIs(t, NewRegistry().RegisterCollector("domain", collector), platformerrors.ErrNilInputParameter)
	})

	T.Run("a registered collector composes into an artifact", func(t *testing.T) {
		t.Parallel()

		registry := NewRegistry()
		store := newPagedStore(collectRows(4), 2)

		must.NoError(t, registry.RegisterCollector("domain", CollectorFor(
			func(ctx context.Context, _ Subject, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[collectRow], error) {
				return store.fetch(ctx, filter)
			},
		)))

		test.Eq(t, []string{"domain"}, registry.CollectorKeys())
	})
}
