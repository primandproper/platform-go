package audit

import (
	"time"

	"github.com/primandproper/platform-go/v14/audit/internal/auditdb"

	"github.com/primandproper/primitives-go/v2/database/ddl"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
)

// newQuerier builds the generated querier for a dialect at a table prefix.
//
// It is the one place in this package that turns configuration into statements.
// The prefix is substituted into each statement once, here, and the dialect
// chooses which of the three renderings a call executes — so nothing below this
// line assembles SQL, and the four components that run it (the recorder, the
// reader, the prune target and the erasure) reach the same corpus by the same
// route.
func newQuerier(d dialect.Dialect, prefix string) (auditdb.Querier, error) {
	qd, err := auditdbDialect(d)
	if err != nil {
		return nil, err
	}

	q, err := auditdb.New(qd, ddl.Qualify(prefix))
	if err != nil {
		return nil, platformerrors.Wrap(err, "building the audit querier")
	}

	return q, nil
}

// auditdbDialect maps this module's dialect names onto the generated package's.
//
// The set is closed on both sides — every constructor here has already rejected
// anything d.Valid() declines — so the default arm is reachable only when this
// module learns a dialect the generated package was not generated for. That is
// a construction failure like any other, and it names the dialect rather than
// leaning on auditdb.New refusing the empty string.
func auditdbDialect(d dialect.Dialect) (auditdb.Dialect, error) {
	switch d {
	case dialect.Postgres:
		return auditdb.DialectPostgreSQL, nil
	case dialect.MySQL:
		return auditdb.DialectMySQL, nil
	case dialect.SQLite:
		return auditdb.DialectSQLite, nil
	default:
		return "", platformerrors.Wrapf(dialect.ErrUnsupported, "no generated audit queries for dialect %q", d)
	}
}

// storedPrecision is the resolution a bound time survives a round trip at on d,
// and it is the hash chain's problem rather than a cosmetic one.
//
// Every entry's digest covers its recorded_at, and Verify recomputes that
// digest over the value the database hands back. A timestamp that changes on
// the way through — by so much as a nanosecond — makes every entry in the table
// read as tampered, which is the loudest possible failure for the quietest
// possible cause. So the value is truncated at the write site to whatever the
// storage will return unchanged, and the digest is taken over the truncated
// value.
//
// Postgres and MySQL store microseconds; the recorder truncates to one, which
// is what it has always done. SQLite stores a timestamp as text, and the
// generated bindings render a bound time in the shape SQLite's own
// CURRENT_TIMESTAMP writes — whole seconds — because that column compares
// lexicographically and two shapes that merely sort alike is the bug that costs
// a whole timezone offset. On that dialect the entry's own resolution is
// therefore a second.
//
// What that does not cost is ordering. This package refuses to order the log by
// recorded_at at all — a chain is defined by position, and two entries in one
// transaction already share a timestamp — so a coarser stamp makes entries
// harder to tell apart in a report and impossible to misorder.
func storedPrecision(d dialect.Dialect) time.Duration {
	if d == dialect.SQLite {
		return time.Second
	}

	return time.Microsecond
}
