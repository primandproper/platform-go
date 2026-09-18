package audit

import (
	"fmt"
	"testing"
	"time"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// ActorUnattributed is untyped deliberately, and these pin it: one name has to
// spell both halves of an unattributed actor, because the ID is what
// ErrEmptyActor requires and the type is what a reader of the log filters on. A
// typed ActorType constant would need a conversion at the first, and a plain
// string constant would need one at the second.
var (
	_ string    = ActorUnattributed
	_ ActorType = ActorUnattributed
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

		entry := entryFor(tenancy.Of("acct_1"), "recipe_1")
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

		first, second := entryFor(tenancy.Of("acct_1"), "recipe_1"), entryFor(tenancy.Of("acct_1"), "recipe_2")
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

		one, two := entryFor(tenancy.Of("acct_1"), "recipe_1"), entryFor(tenancy.Of("acct_2"), "recipe_2")
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

		first := entryFor(tenancy.Of("acct_1"), "recipe_1")
		record(t, client, r, first)

		second := entryFor(tenancy.Of("acct_1"), "recipe_2")
		record(t, client, r, second)

		test.EqOp(t, int64(1), second.Seq)
		test.EqOp(t, first.Hash, second.PrevHash)
	})

	T.Run("rolls back with the caller's transaction", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		r := newTestRecorder(t, newStubClock())

		boom := platformerrors.New("caller work failed")

		err := client.WithTransaction(t.Context(), func(q database.Tx) error {
			if recordErr := r.Record(t.Context(), q, tenancy.Of("acct_1"), entryFor(tenancy.Of("acct_1"), "recipe_1")); recordErr != nil {
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

		test.ErrorIs(t, r.Record(t.Context(), nil, tenancy.Of("acct_1"), entryFor(tenancy.Of("acct_1"), "recipe_1")), ErrNilExecutor)
	})

	T.Run("accepts no entries", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		r := newTestRecorder(t, newStubClock())

		must.NoError(t, client.WithTransaction(t.Context(), func(q database.Tx) error {
			return r.Record(t.Context(), q, tenancy.Of("acct_1"))
		}))
	})

	// The scope that names nobody, refused on the argument the write binds. It
	// is the call a caller makes from a lookup that came back empty, and
	// recording it would file the events in the chain platform-level events
	// belong to — events about somebody, in the log about nobody, findable by
	// no scoped read.
	T.Run("refuses a write that names no scope", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		r := newTestRecorder(t, newStubClock())

		err := client.WithTransaction(t.Context(), func(q database.Tx) error {
			return r.Record(t.Context(), q, tenancy.Scope{}, entryFor(tenancy.Of("acct_1"), "recipe_1"))
		})
		test.ErrorIs(t, err, tenancy.ErrNoScope)

		test.EqOp(t, 0, countRows(t, client, "audit_log_entries", "1=1"))
	})

	// Checked before the empty-batch shortcut, so the refusal does not depend
	// on whether the caller also had anything to write.
	T.Run("refuses a scopeless write with no entries", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		r := newTestRecorder(t, newStubClock())

		err := client.WithTransaction(t.Context(), func(q database.Tx) error {
			return r.Record(t.Context(), q, tenancy.Scope{})
		})
		test.ErrorIs(t, err, tenancy.ErrNoScope)
	})

	T.Run("an entry that names no scope adopts the write's", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		r := newTestRecorder(t, newStubClock())
		reader := newTestReader(t, client)

		scope := tenancy.Of("acct_1")

		entry := entryFor(scope, "recipe_1")
		entry.Scope = tenancy.Scope{}

		must.NoError(t, client.WithTransaction(t.Context(), func(q database.Tx) error {
			return r.Record(t.Context(), q, scope, entry)
		}))

		// Written back onto the caller's value, the way every other field
		// Record assigns is, so the entry names the chain it actually landed
		// in.
		test.EqOp(t, scope, entry.Scope)

		read, err := reader.Get(t.Context(), client.Reader(), &scope, entry.ID)
		must.NoError(t, err)
		test.EqOp(t, scope, read.Scope)
	})

	// The global chain is a scope like any other, and the zero Scope is not it:
	// an entry that names Global is agreeing with a write that names Global,
	// and disagreeing with any other.
	T.Run("tells the global scope apart from an unset one", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		r := newTestRecorder(t, newStubClock())

		must.NoError(t, client.WithTransaction(t.Context(), func(q database.Tx) error {
			return r.Record(t.Context(), q, tenancy.Global(), entryFor(tenancy.Global(), "recipe_1"))
		}))

		err := client.WithTransaction(t.Context(), func(q database.Tx) error {
			return r.Record(t.Context(), q, tenancy.Of("acct_1"), entryFor(tenancy.Global(), "recipe_2"))
		})
		test.ErrorIs(t, err, ErrScopeMismatch)
	})

	// The mismatch is refused rather than corrected, and neither value quietly
	// wins. Here that matters more than it does anywhere else in this module:
	// the scope is the chain's partition, so guessing would append to a chain
	// nobody named rather than mislabel a row.
	T.Run("refuses an entry naming another tenant, and writes nothing", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		r := newTestRecorder(t, newStubClock())

		scope := tenancy.Of("acct_1")
		good := entryFor(scope, "recipe_1")

		err := client.WithTransaction(t.Context(), func(q database.Tx) error {
			return r.Record(t.Context(), q, scope, good, entryFor(tenancy.Of("acct_2"), "recipe_2"))
		})
		test.ErrorIs(t, err, ErrScopeMismatch)

		// The valid entry ahead of the mismatched one is not written either:
		// the batch is settled before any of it is.
		test.EqOp(t, 0, countRows(t, client, "audit_log_entries", "1=1"))
		test.EqOp(t, "", good.Hash)
	})

	// Two tenants in one transaction is two calls, and each chain is its own.
	// It used to be one call whose slice named both, which is the shape the
	// scope argument removed.
	T.Run("chains two scopes written in one transaction separately", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		r := newTestRecorder(t, newStubClock())

		mine := entryFor(tenancy.Of("acct_1"), "recipe_1")
		theirs := entryFor(tenancy.Of("acct_2"), "recipe_2")

		must.NoError(t, client.WithTransaction(t.Context(), func(q database.Tx) error {
			if err := r.Record(t.Context(), q, tenancy.Of("acct_1"), mine); err != nil {
				return err
			}

			return r.Record(t.Context(), q, tenancy.Of("acct_2"), theirs)
		}))

		// Both are position zero, because a position is a position in a chain
		// and there are two chains.
		test.EqOp(t, int64(0), mine.Seq)
		test.EqOp(t, int64(0), theirs.Seq)
		test.EqOp(t, "", mine.PrevHash)
		test.EqOp(t, "", theirs.PrevHash)
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
			{
				// Naming the absence in the type alone is still an omission.
				// ActorUnattributed is the value the ID takes for a write with
				// no principal on its path, not permission to leave the ID out,
				// so an entry that carries the type and nothing else is refused
				// exactly like one that carries neither.
				name:    "unattributed type with no actor ID",
				entry:   &Entry{ResourceType: "recipe", EventType: EventCreated, Actor: Actor{Type: ActorUnattributed}},
				wantErr: ErrEmptyActor,
			},
			{
				// A scope that names a different tenant than the write does.
				// It is a caller holding one tenant's entry and recording it
				// into another, and here that would not mislabel the row — it
				// would append to a chain nobody named.
				name: "another tenant's scope",
				entry: &Entry{
					ResourceType: "recipe",
					EventType:    EventCreated,
					Actor:        Actor{ID: "u"},
					Scope:        tenancy.Of("acct_2"),
				},
				wantErr: ErrScopeMismatch,
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()

				client := newTestClient(t)
				r := newTestRecorder(t, newStubClock())

				good := entryFor(tenancy.Of("acct_1"), "recipe_1")

				err := client.WithTransaction(t.Context(), func(q database.Tx) error {
					return r.Record(t.Context(), q, tenancy.Of("acct_1"), good, tc.entry)
				})
				test.ErrorIs(t, err, tc.wantErr)

				// The valid entry that preceded the bad one is not written
				// either: validation runs over the whole batch first.
				test.EqOp(t, 0, countRows(t, client, "audit_log_entries", "1=1"))
			})
		}
	})

	T.Run("records an unattributed actor", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		r := newTestRecorder(t, newStubClock())
		reader := newTestReader(t, client)

		// One name for both halves, which is what the constant is untyped for:
		// a write that reached the recorder with no principal anywhere on its
		// path says so in the log, in the spelling every consumer shares,
		// rather than in a placeholder each one invents.
		entry := entryFor(tenancy.Of("acct_1"), "recipe_1")
		entry.Actor = Actor{ID: ActorUnattributed, Type: ActorUnattributed}

		record(t, client, r, entry)

		got, err := reader.Get(t.Context(), client.Reader(), nil, entry.ID)
		must.NoError(t, err)
		test.EqOp(t, ActorUnattributed, got.Actor.ID)
		test.EqOp(t, ActorUnattributed, got.Actor.Type)

		// And it is stored as its own claim rather than as the application
		// acting deliberately, so a query that counts unattributed writes does
		// not also count every sweep and migration.
		test.EqOp(t, 1, countRows(t, client, "audit_log_entries", "actor_type = 'unattributed'"))
		test.EqOp(t, 0, countRows(t, client, "audit_log_entries", "actor_type = 'system'"))
	})

	T.Run("truncates the timestamp to what the dialect stores", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		r := newTestRecorder(t, newStubClock())
		reader := newTestReader(t, client)

		entry := entryFor(tenancy.Of("acct_1"), "recipe_1")
		entry.RecordedAt = time.Date(2026, time.July, 31, 12, 0, 0, 123456789, time.UTC)

		record(t, client, r, entry)

		// SQLite stores a timestamp as text in the shape its own
		// CURRENT_TIMESTAMP writes, which is whole seconds; Postgres and MySQL
		// keep microseconds. Either way what the caller is handed back is what
		// was hashed, because the truncation happens before the digest.
		test.EqOp(t, 0, entry.RecordedAt.Nanosecond())

		// And the round trip is exact, which is the whole reason the truncation
		// is there: a value that changed on the way back out would make every
		// entry in the table read as tampered.
		got, err := reader.Get(t.Context(), client.Reader(), nil, entry.ID)
		must.NoError(t, err)
		test.EqOp(t, entry.RecordedAt, got.RecordedAt)

		result, err := reader.Verify(t.Context(), client.Reader(), tenancy.Of("acct_1"), time.Time{}, time.Time{}, ChainStart)
		must.NoError(t, err)
		test.True(t, result.Intact())
	})

	T.Run("preserves a caller-supplied ID", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		r := newTestRecorder(t, newStubClock())

		entry := entryFor(tenancy.Of("acct_1"), "recipe_1")
		entry.ID = "entry_supplied"

		record(t, client, r, entry)

		test.EqOp(t, "entry_supplied", entry.ID)
	})

	T.Run("chains a batch far larger than one round trip", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		recorder := newTestRecorder(t, newStubClock())
		reader := newTestReader(t, client)

		// Well past the seventy rows the multi-row INSERT this replaced was
		// capped at by SQLite's bind-parameter ceiling. The chain has to survive
		// a batch of any size, since the positions are assigned across the whole
		// batch and written one statement at a time.
		const count = 143

		entries := make([]*Entry, 0, count)
		for i := range count {
			entries = append(entries, entryFor(tenancy.Of("acct_1"), fmt.Sprintf("recipe_%d", i)))
		}

		record(t, client, recorder, entries...)

		test.EqOp(t, count, countRows(t, client, "audit_log_entries", "1=1"))
		test.EqOp(t, int64(count-1), entries[count-1].Seq)

		result, err := reader.Verify(t.Context(), client.Reader(), tenancy.Of("acct_1"), time.Time{}, time.Time{}, ChainStart)
		must.NoError(t, err)
		test.True(t, result.Intact())
		test.EqOp(t, count, result.Checked)
	})

	T.Run("records the global scope as a chain of its own", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		r := newTestRecorder(t, newStubClock())

		platform := entryFor(tenancy.Global(), "config_1")
		tenant := entryFor(tenancy.Of("acct_1"), "recipe_1")

		record(t, client, r, platform, tenant)

		// Two chains rather than one, each starting at position zero: the
		// global scope is a scope like any other and does not share the
		// tenant's positions.
		test.EqOp(t, int64(0), platform.Seq)
		test.EqOp(t, int64(0), tenant.Seq)
		test.EqOp(t, "", platform.PrevHash)
		test.EqOp(t, 2, countRows(t, client, "audit_log_chains", "1=1"))
		test.EqOp(t, 1, countRows(t, client, "audit_log_entries", "scope = ''"))
	})

	T.Run("refuses a duplicate position", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		r := newTestRecorder(t, newStubClock())

		first := entryFor(tenancy.Of("acct_1"), "recipe_1")
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
