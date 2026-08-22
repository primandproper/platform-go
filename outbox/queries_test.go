package outbox

import (
	"strings"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/database/dialect"

	"github.com/shoenig/test"
)

func TestBuildInsert(T *testing.T) {
	T.Parallel()

	T.Run("binds every column and repeats created_at as next_attempt", func(t *testing.T) {
		t.Parallel()

		now := time.Date(2026, time.July, 27, 12, 0, 0, 0, time.UTC)
		rows := []enqueueRow{
			{id: "1", topic: "orders", key: "k", payload: []byte(`{}`), createdAt: now},
			{id: "2", topic: "shipments", payload: []byte(`{}`), createdAt: now},
		}

		query, args := buildInsert(dialect.Postgres, DefaultTablePrefix, rows)

		test.True(t, strings.Contains(query, "($1, $2, $3, $4, $5, $6)"))
		test.True(t, strings.Contains(query, "($7, $8, $9, $10, $11, $12)"))
		test.SliceLen(t, 12, args)

		// created_at is bound twice per row so a new message is eligible now.
		test.Eq(t, any(now), args[4])
		test.Eq(t, any(now), args[5])

		mysqlQuery, _ := buildInsert(dialect.MySQL, DefaultTablePrefix, rows[:1])
		test.True(t, strings.Contains(mysqlQuery, "(?, ?, ?, ?, ?, ?)"))
	})
}

func TestBuildSelectClaimable(T *testing.T) {
	T.Parallel()

	T.Run("appends SKIP LOCKED only where it is supported and requested", func(t *testing.T) {
		t.Parallel()

		now := time.Now().UTC()

		pg, args := buildSelectClaimable(dialect.Postgres, DefaultTablePrefix, now, 10, true)
		test.True(t, strings.HasSuffix(pg, "FOR UPDATE SKIP LOCKED"))
		test.SliceLen(t, 3, args)

		lease, _ := buildSelectClaimable(dialect.Postgres, DefaultTablePrefix, now, 10, false)
		test.False(t, strings.Contains(lease, "SKIP LOCKED"))

		// SQLite has no SKIP LOCKED; asking for it must not produce invalid SQL.
		lite, _ := buildSelectClaimable(dialect.SQLite, DefaultTablePrefix, now, 10, true)
		test.False(t, strings.Contains(lite, "SKIP LOCKED"))
	})

	T.Run("carries the per-key ordering predicate", func(t *testing.T) {
		t.Parallel()

		query, _ := buildSelectClaimable(dialect.Postgres, DefaultTablePrefix, time.Now().UTC(), 10, false)

		// This subquery is the whole ordering guarantee: a keyed row is
		// claimable only when nothing older with that key is still pending.
		test.True(t, strings.Contains(query, "NOT EXISTS"))
		test.True(t, strings.Contains(query, "prior.created_at < m.created_at"))
		test.True(t, strings.Contains(query, "m.partition_key = ''"))

		// "Older" has to be the (created_at, id) tuple, not created_at alone.
		// One Enqueue stamps every row with the same timestamp, so a bare `<`
		// leaves same-call rows mutually unblocked. The tiebreak column must
		// also match the ORDER BY, or a batch can contain a pair it reorders.
		test.True(t, strings.Contains(query, "prior.created_at = m.created_at AND prior.id < m.id"))
		test.True(t, strings.Contains(query, "ORDER BY m.created_at, m.id"))
	})
}

func TestBuildReap(T *testing.T) {
	T.Parallel()

	T.Run("wraps the subquery for MySQL only", func(t *testing.T) {
		t.Parallel()

		before := time.Now().UTC()

		mysqlQuery, args := buildReap(dialect.MySQL, DefaultTablePrefix, before, 100)
		// MySQL rejects reading the table being deleted from unless the
		// subquery is materialized.
		test.True(t, strings.Contains(mysqlQuery, "AS doomed"))
		test.SliceLen(t, 2, args)

		pgQuery, _ := buildReap(dialect.Postgres, DefaultTablePrefix, before, 100)
		test.False(t, strings.Contains(pgQuery, "AS doomed"))
	})
}

func TestBuildRecordFailure(T *testing.T) {
	T.Parallel()

	T.Run("binds the schedule, the reason, and the quarantine flag", func(t *testing.T) {
		t.Parallel()

		next := time.Now().UTC()

		query, args := buildRecordFailure(dialect.Postgres, DefaultTablePrefix, "id-1", next, "boom", true)

		test.True(t, strings.Contains(query, "claimed_until = NULL"))
		test.Eq(t, []any{next, "boom", true, "id-1"}, args)
	})
}

func TestBuildBacklog(T *testing.T) {
	T.Parallel()

	T.Run("excludes published and quarantined rows", func(t *testing.T) {
		t.Parallel()

		query := buildBacklog(DefaultTablePrefix)

		test.True(t, strings.Contains(query, "COUNT(*)"))
		test.True(t, strings.Contains(query, "MIN(created_at)"))
		test.True(t, strings.Contains(query, "published_at IS NULL"))
		test.True(t, strings.Contains(query, "quarantined = FALSE"))
	})
}

func TestTruncateError(T *testing.T) {
	T.Parallel()

	T.Run("bounds what is stored", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "", truncateError(nil))

		long := errorOfLength(maxStoredErrorLength * 2)
		test.EqOp(t, maxStoredErrorLength, len(truncateError(long)))
	})
}

type fixedError string

func (e fixedError) Error() string { return string(e) }

func errorOfLength(n int) error {
	return fixedError(strings.Repeat("x", n))
}
