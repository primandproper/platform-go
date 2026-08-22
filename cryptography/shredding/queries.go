package shredding

import (
	"fmt"
	"time"

	"github.com/primandproper/platform-go/v13/database/ddl"
	"github.com/primandproper/platform-go/v13/database/dialect"
)

// Every time this package binds is a UTC time.Time, for the reason spelled out
// at length in dataprivacy/queries.go: modernc's SQLite driver stores a bound
// time.Time as Go's own String() rendering, so comparisons there are lexical.
// That is only chronological while everything is UTC. Do not remove the .UTC()
// calls at the binding sites.

// tables holds the rendered table names, derived from one prefix so a second
// table added later cannot be named inconsistently with the first.
type tables struct {
	base string
	keys string
}

func newTables(prefix string) *tables {
	return &tables{
		base: prefix,
		keys: ddl.Qualify(prefix) + "shredding_subject_keys",
	}
}

// prefix returns the prefix the names were derived from, for the validation
// that runs against every rendered name rather than any one of them.
func (t *tables) prefix() string {
	return t.base
}

// recordColumns is the projection every read scans. Declared once so the SELECT
// and the Scan cannot drift apart.
const recordColumns = "subject_type, subject_id, wrapped_key, created_at, shredded_at"

// buildSelectRecord renders the single-subject read.
func (t *tables) buildSelectRecord(d dialect.Dialect, subject Subject) (query string, args []any) {
	return fmt.Sprintf(
			"SELECT %s FROM %s WHERE subject_type = %s AND subject_id = %s",
			recordColumns, t.keys, d.Placeholder(1), d.Placeholder(2),
		),
		[]any{subject.Type, subject.ID}
}

// buildInsertRecord renders the mint, ignoring a row that is already there.
//
// The conflict clause is what turns a race between two replicas into a
// zero-rows-affected result the caller can react to, rather than a
// dialect-specific constraint violation it would have to parse. Reacting
// matters here more than it usually does: the loser of that race has generated
// a key that must be thrown away, because a second live key for one subject is
// a shred that leaves half the ciphertext readable.
func (t *tables) buildInsertRecord(d dialect.Dialect, record *Record) (query string, args []any) {
	const columns = 5

	query = fmt.Sprintf(
		"INSERT %sINTO %s (subject_type, subject_id, wrapped_key, created_at, shredded_at) VALUES (%s)",
		ignorePrefix(d), t.keys, d.Placeholders(1, columns),
	)

	if d == dialect.Postgres {
		query += " ON CONFLICT (subject_type, subject_id) DO NOTHING"
	}

	return query, []any{
		record.Subject.Type, record.Subject.ID, record.Wrapped, record.CreatedAt.UTC(), nil,
	}
}

// buildInsertTombstone renders the shred of a subject that has no row.
//
// Shredding somebody nothing was ever encrypted for still writes a row, because
// the tombstone is what stops a key being minted for them afterwards. Erasure
// that only works for subjects who happened to have data already is erasure
// that fails in exactly the case nobody tests.
func (t *tables) buildInsertTombstone(d dialect.Dialect, subject Subject, at time.Time) (query string, args []any) {
	const columns = 5

	query = fmt.Sprintf(
		"INSERT %sINTO %s (subject_type, subject_id, wrapped_key, created_at, shredded_at) VALUES (%s)",
		ignorePrefix(d), t.keys, d.Placeholders(1, columns),
	)

	if d == dialect.Postgres {
		query += " ON CONFLICT (subject_type, subject_id) DO NOTHING"
	}

	return query, []any{subject.Type, subject.ID, nil, at.UTC(), at.UTC()}
}

// buildShred renders the destruction.
//
// The row survives and the key material does not. Guarding on shredded_at IS
// NULL is what makes the operation idempotent without a read first: a second
// call matches nothing, and zero rows affected is how the caller learns the
// destruction was somebody else's.
func (t *tables) buildShred(d dialect.Dialect, subject Subject, at time.Time) (query string, args []any) {
	return fmt.Sprintf(
			"UPDATE %s SET wrapped_key = NULL, shredded_at = %s "+
				"WHERE subject_type = %s AND subject_id = %s AND shredded_at IS NULL",
			t.keys, d.Placeholder(1), d.Placeholder(2), d.Placeholder(3),
		),
		[]any{at.UTC(), subject.Type, subject.ID}
}

// ignorePrefix renders the dialect's "skip a duplicate row" clause prefix.
// Postgres has none and takes an ON CONFLICT clause instead, which the insert
// builders append.
func ignorePrefix(d dialect.Dialect) string {
	switch d {
	case dialect.MySQL:
		return "IGNORE "
	case dialect.SQLite:
		return "OR IGNORE "
	case dialect.Postgres:
		return ""
	default:
		return ""
	}
}
