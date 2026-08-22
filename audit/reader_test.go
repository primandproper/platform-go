package audit

import (
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/filtering"
	"github.com/primandproper/platform-go/v13/pointer"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// bogusDialectClient reports a dialect this package cannot emit SQL for.
//
// The unsupported-dialect branch is otherwise unreachable: the dialect comes
// from the client rather than the caller, and every client this module ships
// reports one of the three supported dialects. Only Dialect is consulted before
// the constructor gives up, so the embedded Client is never called.
type bogusDialectClient struct {
	database.Client
}

func (bogusDialectClient) Dialect() dialect.Dialect { return "oracle" }

func TestNewReader(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		r, err := NewReader(newTestClient(t))
		must.NoError(t, err)
		test.NotNil(t, r)
	})

	T.Run("rejects a nil client", func(t *testing.T) {
		t.Parallel()

		_, err := NewReader(nil)
		test.ErrorIs(t, err, ErrNilDatabaseClient)
	})

	T.Run("rejects an unsupported dialect", func(t *testing.T) {
		t.Parallel()

		_, err := NewReader(bogusDialectClient{newTestClient(t)})
		test.ErrorIs(t, err, dialect.ErrUnsupported)
	})

	T.Run("rejects an unsafe table prefix", func(t *testing.T) {
		t.Parallel()

		_, err := NewReader(newTestClient(t), WithReaderTablePrefix("a-b"))
		test.ErrorIs(t, err, ErrInvalidTablePrefix)
	})
}

func TestReader_Get(T *testing.T) {
	T.Parallel()

	T.Run("round-trips an entry", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		recorder := newTestRecorder(t, newStubClock())
		reader := newTestReader(t, client)

		written := entryFor("acct_1", "recipe_1")
		written.Metadata = map[string]string{"reason": "typo"}
		record(t, client, recorder, written)

		read, err := reader.Get(t.Context(), written.ID)
		must.NoError(t, err)

		test.EqOp(t, written.ID, read.ID)
		test.EqOp(t, written.Seq, read.Seq)
		test.EqOp(t, written.Hash, read.Hash)
		test.EqOp(t, written.RecordedAt, read.RecordedAt)
		test.EqOp(t, EventUpdated, read.EventType)
		test.EqOp(t, "recipe_1", read.ResourceID)
		test.EqOp(t, ActorUser, read.Actor.Type)
		test.EqOp(t, "203.0.113.7", read.Actor.IP)
		test.Eq(t, map[string]string{"reason": "typo"}, read.Metadata)
		test.EqOp(t, "old", read.Changes["name"].Old)
	})

	T.Run("reports a missing entry", func(t *testing.T) {
		t.Parallel()

		reader := newTestReader(t, newTestClient(t))

		_, err := reader.Get(t.Context(), "nope")
		test.ErrorIs(t, err, ErrEntryNotFound)
	})

	T.Run("refuses an empty ID", func(t *testing.T) {
		t.Parallel()

		reader := newTestReader(t, newTestClient(t))

		_, err := reader.Get(t.Context(), "")
		test.ErrorIs(t, err, platformerrors.ErrInvalidIDProvided)
	})
}

func TestReader_List(T *testing.T) {
	T.Parallel()

	T.Run("filters by scope, actor, resource, and event type", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		recorder := newTestRecorder(t, newStubClock())
		reader := newTestReader(t, client)

		mine := entryFor("acct_1", "recipe_1")

		theirs := entryFor("acct_2", "recipe_2")
		theirs.Actor.ID = "user_2"

		deletion := entryFor("acct_1", "recipe_3")
		deletion.EventType = EventDeleted

		record(t, client, recorder, mine, theirs, deletion)

		listed, err := reader.List(t.Context(), &Query{Scope: pointer.To("acct_1")}, nil)
		must.NoError(t, err)
		test.SliceLen(t, 2, listed.Data)
		test.EqOp(t, uint64(2), listed.TotalCount)

		listed, err = reader.List(t.Context(), &Query{ActorID: "user_2"}, nil)
		must.NoError(t, err)
		must.SliceLen(t, 1, listed.Data)
		test.EqOp(t, theirs.ID, listed.Data[0].ID)

		listed, err = reader.List(t.Context(), &Query{EventTypes: []EventType{EventDeleted}}, nil)
		must.NoError(t, err)
		must.SliceLen(t, 1, listed.Data)
		test.EqOp(t, deletion.ID, listed.Data[0].ID)

		listed, err = reader.List(t.Context(), &Query{ResourceTypes: []string{"recipe"}, ResourceID: "recipe_1"}, nil)
		must.NoError(t, err)
		must.SliceLen(t, 1, listed.Data)
		test.EqOp(t, mine.ID, listed.Data[0].ID)

		listed, err = reader.List(t.Context(), &Query{ActorType: ActorUser}, nil)
		must.NoError(t, err)
		test.SliceLen(t, 3, listed.Data)

		listed, err = reader.List(t.Context(), &Query{ActorType: ActorSystem}, nil)
		must.NoError(t, err)
		test.SliceEmpty(t, listed.Data)
	})

	T.Run("treats the empty scope as a scope of its own", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		recorder := newTestRecorder(t, newStubClock())
		reader := newTestReader(t, client)

		platform := entryFor("", "config_1")
		tenant := entryFor("acct_1", "recipe_1")
		record(t, client, recorder, platform, tenant)

		// A plain string field could not have expressed this: it would have
		// been indistinguishable from "do not filter", and would have returned
		// the tenant's entry too.
		listed, err := reader.List(t.Context(), &Query{Scope: pointer.To("")}, nil)
		must.NoError(t, err)
		must.SliceLen(t, 1, listed.Data)
		test.EqOp(t, platform.ID, listed.Data[0].ID)

		listed, err = reader.List(t.Context(), &Query{}, nil)
		must.NoError(t, err)
		test.SliceLen(t, 2, listed.Data)
	})

	T.Run("pages with a cursor", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		recorder := newTestRecorder(t, newStubClock())
		reader := newTestReader(t, client)

		for i := range 5 {
			record(t, client, recorder, entryFor("acct_1", string(rune('a'+i))))
		}

		filter := filtering.DefaultQueryFilter()
		filter.MaxResponseSize = pointer.To(uint16(2))

		first, err := reader.List(t.Context(), nil, filter)
		must.NoError(t, err)
		must.SliceLen(t, 2, first.Data)
		test.EqOp(t, uint64(5), first.TotalCount)

		filter.Cursor = &first.Cursor

		second, err := reader.List(t.Context(), nil, filter)
		must.NoError(t, err)
		must.SliceLen(t, 2, second.Data)
		test.NotEq(t, first.Data[0].ID, second.Data[0].ID)
	})

	T.Run("sorts newest first on request", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		recorder := newTestRecorder(t, newStubClock())
		reader := newTestReader(t, client)

		first := entryFor("acct_1", "recipe_1")
		record(t, client, recorder, first)

		last := entryFor("acct_1", "recipe_2")
		record(t, client, recorder, last)

		filter := filtering.DefaultQueryFilter()
		filter.SortBy = filtering.SortDescending

		listed, err := reader.List(t.Context(), nil, filter)
		must.NoError(t, err)
		must.SliceLen(t, 2, listed.Data)
		test.EqOp(t, last.ID, listed.Data[0].ID)
	})

	T.Run("honors the filter's time window", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		c := newStubClock()
		recorder := newTestRecorder(t, c)
		reader := newTestReader(t, client)

		old := entryFor("acct_1", "recipe_1")
		record(t, client, recorder, old)

		c.advance(48 * time.Hour)

		recent := entryFor("acct_1", "recipe_2")
		record(t, client, recorder, recent)

		filter := filtering.DefaultQueryFilter()
		filter.CreatedAfter = pointer.To(old.RecordedAt.Add(time.Hour))

		listed, err := reader.List(t.Context(), nil, filter)
		must.NoError(t, err)
		must.SliceLen(t, 1, listed.Data)
		test.EqOp(t, recent.ID, listed.Data[0].ID)
	})
}

func TestReader_Verify(T *testing.T) {
	T.Parallel()

	T.Run("reports an intact chain", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		recorder := newTestRecorder(t, newStubClock())
		reader := newTestReader(t, client)

		record(t, client, recorder,
			entryFor("acct_1", "recipe_1"),
			entryFor("acct_1", "recipe_2"),
			entryFor("acct_1", "recipe_3"),
		)

		result, err := reader.Verify(t.Context(), "acct_1", time.Time{}, time.Time{})
		must.NoError(t, err)
		test.True(t, result.Intact())
		test.EqOp(t, 3, result.Checked)
		test.Nil(t, result.FirstBreak)
	})

	T.Run("reports an empty scope as intact", func(t *testing.T) {
		t.Parallel()

		reader := newTestReader(t, newTestClient(t))

		result, err := reader.Verify(t.Context(), "acct_nobody", time.Time{}, time.Time{})
		must.NoError(t, err)
		test.True(t, result.Intact())
		test.EqOp(t, 0, result.Checked)
	})

	T.Run("detects an edited entry", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		recorder := newTestRecorder(t, newStubClock())
		reader := newTestReader(t, client)

		first, second := entryFor("acct_1", "recipe_1"), entryFor("acct_1", "recipe_2")
		record(t, client, recorder, first, second)

		// Rewriting a column without touching the hashes: exactly what somebody
		// covering their tracks would do if the chain were not there.
		exec(t, client,
			"UPDATE audit_log_entries SET actor_id = 'somebody_else' WHERE id = ?", second.ID)

		result, err := reader.Verify(t.Context(), "acct_1", time.Time{}, time.Time{})
		must.NoError(t, err)
		must.False(t, result.Intact())
		test.EqOp(t, BreakContentAltered, result.FirstBreak.Reason)
		test.EqOp(t, second.ID, result.FirstBreak.EntryID)
		test.EqOp(t, int64(1), result.FirstBreak.Seq)
	})

	T.Run("detects a rewritten link", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		recorder := newTestRecorder(t, newStubClock())
		reader := newTestReader(t, client)

		first, second := entryFor("acct_1", "recipe_1"), entryFor("acct_1", "recipe_2")
		record(t, client, recorder, first, second)

		exec(t, client,
			"UPDATE audit_log_entries SET prev_hash = '00' WHERE id = ?", second.ID)

		result, err := reader.Verify(t.Context(), "acct_1", time.Time{}, time.Time{})
		must.NoError(t, err)
		must.False(t, result.Intact())
		test.EqOp(t, BreakLinkMismatch, result.FirstBreak.Reason)
		test.EqOp(t, first.Hash, result.FirstBreak.Expected)
		test.EqOp(t, "00", result.FirstBreak.Actual)
	})

	T.Run("detects a deleted entry in the middle", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		recorder := newTestRecorder(t, newStubClock())
		reader := newTestReader(t, client)

		first, second, third := entryFor("acct_1", "r1"), entryFor("acct_1", "r2"), entryFor("acct_1", "r3")
		record(t, client, recorder, first, second, third)

		exec(t, client, "DELETE FROM audit_log_entries WHERE id = ?", second.ID)

		result, err := reader.Verify(t.Context(), "acct_1", time.Time{}, time.Time{})
		must.NoError(t, err)
		must.False(t, result.Intact())
		test.EqOp(t, BreakMissingEntry, result.FirstBreak.Reason)
		test.EqOp(t, int64(1), result.FirstBreak.Seq)
	})

	T.Run("detects a deleted entry at the head of the chain", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		recorder := newTestRecorder(t, newStubClock())
		reader := newTestReader(t, client)

		first, second := entryFor("acct_1", "r1"), entryFor("acct_1", "r2")
		record(t, client, recorder, first, second)

		// Nothing left in range to link against, and no prune watermark to
		// explain the gap — which is what distinguishes this from retention.
		exec(t, client, "DELETE FROM audit_log_entries WHERE id = ?", first.ID)

		result, err := reader.Verify(t.Context(), "acct_1", time.Time{}, time.Time{})
		must.NoError(t, err)
		must.False(t, result.Intact())
		test.EqOp(t, BreakMissingEntry, result.FirstBreak.Reason)
		test.EqOp(t, int64(0), result.FirstBreak.Seq)
	})

	T.Run("anchors a range that begins mid-chain", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		c := newStubClock()
		recorder := newTestRecorder(t, c)
		reader := newTestReader(t, client)

		record(t, client, recorder, entryFor("acct_1", "r1"))

		c.advance(24 * time.Hour)
		middle := entryFor("acct_1", "r2")
		record(t, client, recorder, middle)

		c.advance(24 * time.Hour)
		record(t, client, recorder, entryFor("acct_1", "r3"))

		// The window excludes the first entry, so the walk has to fetch it to
		// anchor the second — and still verifies cleanly.
		result, err := reader.Verify(t.Context(), "acct_1", middle.RecordedAt, time.Time{})
		must.NoError(t, err)
		test.True(t, result.Intact())
		test.EqOp(t, 2, result.Checked)
	})

	T.Run("does not read a missing chain row as tampering", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		recorder := newTestRecorder(t, newStubClock())
		reader := newTestReader(t, client)

		record(t, client, recorder, entryFor("acct_1", "r1"), entryFor("acct_1", "r2"))

		// A scope with no chain row has never been pruned, so a chain that
		// starts at position zero is exactly what it should be. Reporting a
		// break here would cry wolf on every log restored without its chain
		// table.
		exec(t, client, "DELETE FROM audit_log_chains WHERE scope = 'acct_1'")

		result, err := reader.Verify(t.Context(), "acct_1", time.Time{}, time.Time{})
		must.NoError(t, err)
		test.True(t, result.Intact())
	})

	T.Run("verifies each scope independently", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		recorder := newTestRecorder(t, newStubClock())
		reader := newTestReader(t, client)

		mine, theirs := entryFor("acct_1", "r1"), entryFor("acct_2", "r1")
		record(t, client, recorder, mine, theirs)

		exec(t, client,
			"UPDATE audit_log_entries SET resource_id = 'tampered' WHERE id = ?", theirs.ID)

		mineResult, err := reader.Verify(t.Context(), "acct_1", time.Time{}, time.Time{})
		must.NoError(t, err)
		test.True(t, mineResult.Intact())

		theirsResult, err := reader.Verify(t.Context(), "acct_2", time.Time{}, time.Time{})
		must.NoError(t, err)
		test.False(t, theirsResult.Intact())
	})
}

func TestVerificationResult_Intact(T *testing.T) {
	T.Parallel()

	T.Run("a nil result is not intact", func(t *testing.T) {
		t.Parallel()

		var result *VerificationResult
		test.False(t, result.Intact())
	})
}
