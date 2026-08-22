package audit

import (
	"fmt"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	platformerrors "github.com/primandproper/platform-go/v13/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNewRecorder(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		r, err := NewRecorder(dialect.SQLite)
		must.NoError(t, err)
		test.NotNil(t, r)
	})

	T.Run("rejects an unsupported dialect", func(t *testing.T) {
		t.Parallel()

		_, err := NewRecorder("cassandra")
		test.ErrorIs(t, err, dialect.ErrUnsupported)
	})

	T.Run("rejects an unsafe table prefix", func(t *testing.T) {
		t.Parallel()

		_, err := NewRecorder(dialect.SQLite, WithRecorderTablePrefix("audit; DROP TABLE"))
		test.ErrorIs(t, err, ErrInvalidTablePrefix)
	})

	T.Run("ignores nil options", func(t *testing.T) {
		t.Parallel()

		r, err := NewRecorder(dialect.SQLite, nil)
		must.NoError(t, err)
		test.NotNil(t, r)
	})
}

func TestRecorder_Record(T *testing.T) {
	T.Parallel()

	T.Run("assigns identity, time, and chain fields", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		c := newStubClock()
		r := newTestRecorder(t, c)

		entry := entryFor("acct_1", "recipe_1")
		record(t, client, r, entry)

		test.NotEq(t, "", entry.ID)
		test.EqOp(t, c.read(), entry.RecordedAt)
		test.EqOp(t, int64(0), entry.Seq)
		test.EqOp(t, "", entry.PrevHash)
		test.NotEq(t, "", entry.Hash)
	})

	T.Run("chains entries within a scope", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		r := newTestRecorder(t, newStubClock())

		first, second := entryFor("acct_1", "recipe_1"), entryFor("acct_1", "recipe_2")
		record(t, client, r, first, second)

		test.EqOp(t, int64(0), first.Seq)
		test.EqOp(t, int64(1), second.Seq)
		test.EqOp(t, first.Hash, second.PrevHash)
		test.NotEq(t, first.Hash, second.Hash)
	})

	T.Run("chains scopes independently", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		r := newTestRecorder(t, newStubClock())

		one, two := entryFor("acct_1", "recipe_1"), entryFor("acct_2", "recipe_2")
		record(t, client, r, one, two)

		// Both are the first entry in their own scope, so both start at zero
		// with no predecessor. A shared chain would have made the second link to
		// the first.
		test.EqOp(t, int64(0), one.Seq)
		test.EqOp(t, int64(0), two.Seq)
		test.EqOp(t, "", two.PrevHash)
	})

	T.Run("continues a chain across calls", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		r := newTestRecorder(t, newStubClock())

		first := entryFor("acct_1", "recipe_1")
		record(t, client, r, first)

		second := entryFor("acct_1", "recipe_2")
		record(t, client, r, second)

		test.EqOp(t, int64(1), second.Seq)
		test.EqOp(t, first.Hash, second.PrevHash)
	})

	T.Run("rolls back with the caller's transaction", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		r := newTestRecorder(t, newStubClock())

		boom := platformerrors.New("caller work failed")

		err := client.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			if recordErr := r.Record(t.Context(), q, entryFor("acct_1", "recipe_1")); recordErr != nil {
				return recordErr
			}

			return boom
		})
		test.ErrorIs(t, err, boom)

		test.EqOp(t, 0, countRows(t, client, "audit_log_entries", "1=1"))
		test.EqOp(t, 0, countRows(t, client, "audit_log_chains", "1=1"))
	})

	T.Run("refuses a nil executor", func(t *testing.T) {
		t.Parallel()

		r := newTestRecorder(t, newStubClock())

		test.ErrorIs(t, r.Record(t.Context(), nil, entryFor("acct_1", "recipe_1")), ErrNilExecutor)
	})

	T.Run("accepts no entries", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		r := newTestRecorder(t, newStubClock())

		must.NoError(t, client.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			return r.Record(t.Context(), q)
		}))
	})

	T.Run("rejects incomplete entries before writing any of them", func(t *testing.T) {
		t.Parallel()

		for _, tc := range []struct {
			wantErr error
			entry   *Entry
			name    string
		}{
			{name: "nil", entry: nil, wantErr: ErrNilEntry},
			{
				name:    "no resource type",
				entry:   &Entry{EventType: EventCreated, Actor: Actor{ID: "u"}},
				wantErr: ErrEmptyResourceType,
			},
			{
				name:    "no event type",
				entry:   &Entry{ResourceType: "recipe", Actor: Actor{ID: "u"}},
				wantErr: ErrEmptyEventType,
			},
			{
				name:    "no actor",
				entry:   &Entry{ResourceType: "recipe", EventType: EventCreated},
				wantErr: ErrEmptyActor,
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()

				client := newTestClient(t)
				r := newTestRecorder(t, newStubClock())

				good := entryFor("acct_1", "recipe_1")

				err := client.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
					return r.Record(t.Context(), q, good, tc.entry)
				})
				test.ErrorIs(t, err, tc.wantErr)

				// The valid entry that preceded the bad one is not written
				// either: validation runs over the whole batch first.
				test.EqOp(t, 0, countRows(t, client, "audit_log_entries", "1=1"))
			})
		}
	})

	T.Run("truncates the timestamp to microseconds", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		r := newTestRecorder(t, newStubClock())

		entry := entryFor("acct_1", "recipe_1")
		entry.RecordedAt = time.Date(2026, time.July, 31, 12, 0, 0, 123456789, time.UTC)

		record(t, client, r, entry)

		// Nanoseconds do not survive Postgres or MySQL, and a timestamp that
		// changed on the way back out would make every entry read as tampered.
		test.EqOp(t, 123456000, entry.RecordedAt.Nanosecond())
	})

	T.Run("preserves a caller-supplied ID", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		r := newTestRecorder(t, newStubClock())

		entry := entryFor("acct_1", "recipe_1")
		entry.ID = "entry_supplied"

		record(t, client, r, entry)

		test.EqOp(t, "entry_supplied", entry.ID)
	})

	T.Run("chains a batch larger than one INSERT can carry", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		recorder := newTestRecorder(t, newStubClock())
		reader := newTestReader(t, client)

		// Past maxBatchRows, so the insert is split — and the chain has to
		// survive the split, since the positions were assigned across the whole
		// batch rather than per statement.
		const count = maxBatchRows*2 + 3

		entries := make([]*Entry, 0, count)
		for i := range count {
			entries = append(entries, entryFor("acct_1", fmt.Sprintf("recipe_%d", i)))
		}

		record(t, client, recorder, entries...)

		test.EqOp(t, count, countRows(t, client, "audit_log_entries", "1=1"))
		test.EqOp(t, int64(count-1), entries[count-1].Seq)

		result, err := reader.Verify(t.Context(), "acct_1", time.Time{}, time.Time{})
		must.NoError(t, err)
		test.True(t, result.Intact())
		test.EqOp(t, count, result.Checked)
	})

	T.Run("refuses a duplicate position", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		r := newTestRecorder(t, newStubClock())

		first := entryFor("acct_1", "recipe_1")
		record(t, client, r, first)

		// Simulating what a forked chain would have to write: the unique index
		// on (scope, seq) is what makes that unrepresentable rather than merely
		// detectable.
		_, err := client.Writer().ExecContext(t.Context(),
			"INSERT INTO audit_log_entries "+
				"(id, seq, scope, recorded_at, event_type, resource_type, resource_id, "+
				"actor_id, actor_type, actor_ip, change_set, metadata, prev_hash, hash) "+
				"VALUES ('fork', 0, 'acct_1', ?, 'updated', 'recipe', 'r', 'u', 'user', '', NULL, NULL, '', 'deadbeef')",
			first.RecordedAt,
		)
		test.Error(t, err)
	})
}
