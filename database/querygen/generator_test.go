package querygen

import (
	"fmt"
	"testing"

	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/filtering"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// everyDialect is the set For accepts. A test that walks it rather than naming
// three cases is a test a fourth dialect cannot be added behind.
func everyDialect() []dialect.Dialect {
	return []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite}
}

func TestFor(T *testing.T) {
	T.Parallel()

	T.Run("returns a generator bound to the dialect it was given", func(t *testing.T) {
		t.Parallel()

		for _, d := range everyDialect() {
			test.EqOp(t, d, For(d).Dialect(), test.Sprintf("dialect %q", d))
		}
	})

	T.Run("refuses a dialect outside the supported set", func(t *testing.T) {
		t.Parallel()

		// Emitting a plausible default for an unrecognized dialect is the
		// failure this exists to prevent: the SQL would generate, compile, and
		// be the wrong dialect's.
		for _, d := range []dialect.Dialect{"oracle", "", "POSTGRES"} {
			err := recovered(func() { _ = For(d) })

			must.Error(t, err, must.Sprintf("dialect %q", d))
			test.ErrorIs(t, err, dialect.ErrUnsupported, test.Sprintf("dialect %q", d))
			test.StrContains(t, err.Error(), string(d), test.Sprintf("dialect %q", d))
		}
	})
}

// The five tests below are the whole of what this package assumes about a
// server. Each pins all three answers, because a branch that falls through to
// Postgres by accident is the mutation this package is most exposed to: the
// Postgres text is correct SQL, so nothing about it reads as wrong until MySQL
// parses it.

func TestGenerator_substringMatch(T *testing.T) {
	T.Parallel()

	T.Run("per dialect", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, `things.name ILIKE '%' || sqlc.arg(q)::text || '%'`,
			For(dialect.Postgres).substringMatch("things.name", "q"))

		test.EqOp(t, `LOWER(things.name) LIKE CONCAT('%', LOWER(sqlc.arg(q)), '%')`,
			For(dialect.MySQL).substringMatch("things.name", "q"))

		test.EqOp(t, `LOWER(things.name) LIKE '%' || LOWER(sqlc.arg(q)) || '%'`,
			For(dialect.SQLite).substringMatch("things.name", "q"))
	})

	T.Run("folds both sides where the operator does not fold either", func(t *testing.T) {
		t.Parallel()

		// MySQL's LIKE folds only because the column's collation does, and
		// SQLite's only while PRAGMA case_sensitive_like is off. Neither is a
		// property of the emitted SQL, so both sides are folded explicitly and
		// the match is case-insensitive whatever the schema says. Neither casts
		// the argument: on MySQL a cast gives it the connection's collation and
		// the comparison becomes error 1267 rather than a match.
		for _, d := range []dialect.Dialect{dialect.MySQL, dialect.SQLite} {
			got := For(d).substringMatch("things.name", "q")

			test.StrContains(t, got, "LOWER(things.name)", test.Sprintf("dialect %q", d))
			test.StrContains(t, got, "LOWER(sqlc.arg(q))", test.Sprintf("dialect %q", d))
		}
	})

	T.Run("the wildcards are the generator's, never the caller's", func(t *testing.T) {
		t.Parallel()

		// A caller assembling '%term%' is a caller who can forget to escape a
		// literal '%', which turns a search for "50%" into a search for
		// everything.
		for _, d := range everyDialect() {
			test.StrContains(t, For(d).substringMatch("things.name", "q"), "'%'", test.Sprintf("dialect %q", d))
		}
	})
}

func TestGenerator_byteOrdered(T *testing.T) {
	T.Parallel()

	T.Run("per dialect", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, `things.id COLLATE "C"`, For(dialect.Postgres).byteOrdered("things.id"))
		test.EqOp(t, `CAST(things.id AS BINARY)`, For(dialect.MySQL).byteOrdered("things.id"))
		test.EqOp(t, `things.id COLLATE BINARY`, For(dialect.SQLite).byteOrdered("things.id"))
	})

	T.Run("names the order on every dialect, including the one it is already the default on", func(t *testing.T) {
		t.Parallel()

		// SQLite's default for text is BINARY, so the clause is redundant there
		// today. Leaving it off would make the emitted SQL depend on a default
		// rather than state the property search/sync's pruner requires.
		for _, d := range everyDialect() {
			test.NotEqOp(t, "things.id", For(d).byteOrdered("things.id"), test.Sprintf("dialect %q", d))
		}
	})
}

func TestGenerator_timeHorizon(T *testing.T) {
	T.Parallel()

	T.Run("per dialect", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, `(SELECT CURRENT_TIMESTAMP - '999 years'::INTERVAL)`, For(dialect.Postgres).timeHorizon("-"))
		test.EqOp(t, `(SELECT CURRENT_TIMESTAMP + INTERVAL 999 YEAR)`, For(dialect.MySQL).timeHorizon("+"))
		test.EqOp(t, `(SELECT datetime(CURRENT_TIMESTAMP, '-999 years'))`, For(dialect.SQLite).timeHorizon("-"))
	})

	T.Run("the sign reaches the expression on every dialect", func(t *testing.T) {
		t.Parallel()

		// A horizon that ignored its sign would put both ends of every window in
		// the same direction, which excludes every row for one bound and no rows
		// for the other — and reads, in the emitted SQL, like a working filter.
		for _, d := range everyDialect() {
			test.NotEqOp(t, For(d).timeHorizon("-"), For(d).timeHorizon("+"), test.Sprintf("dialect %q", d))
		}
	})
}

func TestGenerator_includeArchivedFlag(T *testing.T) {
	T.Parallel()

	T.Run("per dialect", func(t *testing.T) {
		t.Parallel()

		// The cast is what makes sqlc generate a *bool on Postgres rather than
		// an interface{}. MySQL and SQLite have no boolean type to cast to, and
		// take the COALESCE's type from the false.
		test.EqOp(t, `COALESCE(sqlc.narg(include_archived), false)::boolean`,
			For(dialect.Postgres).includeArchivedFlag())

		for _, d := range []dialect.Dialect{dialect.MySQL, dialect.SQLite} {
			test.EqOp(t, `COALESCE(sqlc.narg(include_archived), false)`,
				For(d).includeArchivedFlag(), test.Sprintf("dialect %q", d))
		}
	})

	T.Run("an absent flag reads as false rather than as NULL on every dialect", func(t *testing.T) {
		t.Parallel()

		// A NULL here makes the OR return NULL for every unarchived row, and a
		// WHERE treats NULL as false — so an unset flag would hide every row
		// rather than only the archived ones.
		for _, d := range everyDialect() {
			test.StrContains(t, For(d).includeArchivedFlag(), ", false)", test.Sprintf("dialect %q", d))
		}
	})
}

func TestGenerator_limitClause(T *testing.T) {
	T.Parallel()

	T.Run("per dialect", func(t *testing.T) {
		t.Parallel()

		// The default is filtering's constant rather than a number spelled here,
		// so the SQL and QueryFilter.Normalize cannot come to disagree about the
		// size of a page nobody asked for.
		want := fmt.Sprintf("LIMIT COALESCE(sqlc.narg(result_limit), %d)", filtering.DefaultQueryFilterLimit)

		for _, d := range []dialect.Dialect{dialect.Postgres, dialect.SQLite} {
			test.EqOp(t, want, For(d).limitClause(), test.Sprintf("dialect %q", d))
		}

		// MySQL admits an integer literal or a placeholder after LIMIT and
		// nothing else, so it binds the size instead of defaulting it.
		test.EqOp(t, "LIMIT sqlc.arg(result_limit)", For(dialect.MySQL).limitClause())
	})

	T.Run("names the same argument on every dialect", func(t *testing.T) {
		t.Parallel()

		// Only the nullability of the generated parameter differs. A dialect
		// that also renamed the argument would make the difference a rewrite
		// rather than a type change.
		for _, d := range everyDialect() {
			test.StrContains(t, For(d).limitClause(), "("+LimitArg+")", test.Sprintf("dialect %q", d))
		}
	})
}

func TestGenerator_idSetPredicate(T *testing.T) {
	T.Parallel()

	T.Run("per dialect", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, `id = ANY(sqlc.arg(ids)::text[])`, For(dialect.Postgres).idSetPredicate())

		// Neither of the others has an array type, so sqlc expands the slice
		// into as many placeholders as there are ids. The Go signature is
		// []string either way.
		for _, d := range []dialect.Dialect{dialect.MySQL, dialect.SQLite} {
			test.EqOp(t, `id IN (sqlc.slice(ids))`,
				For(d).idSetPredicate(), test.Sprintf("dialect %q", d))
		}
	})

	T.Run("binds the set rather than one statement per id, on every dialect", func(t *testing.T) {
		t.Parallel()

		for _, d := range everyDialect() {
			test.StrContains(t, For(d).idSetPredicate(), IDsArg, test.Sprintf("dialect %q", d))
		}
	})
}
