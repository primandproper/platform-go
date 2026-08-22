package querygen

import (
	"fmt"

	"github.com/primandproper/platform-go/v13/database/dialect"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/filtering"
)

// Generator emits sqlc input for one SQL dialect.
//
// The dialect is bound to the value rather than passed to each call, because a
// fragment and the statement it lands in have to agree about which server will
// parse them. A Postgres COLLATE "C" inside MySQL is a syntax error, which is
// the good case; a Postgres ILIKE has no SQLite spelling at all and the
// substitute differs in what it folds, which is the bad one. Binding the
// dialect to the value is what makes a mixed pair unrepresentable rather than
// merely discouraged.
//
// Every method that emits SQL hangs off this type, including the ones whose
// output is currently identical on all three dialects. A caller should not have
// to know which fragments happen to be portable this week, and a divergence
// found later — the archived-row toggle was portable until sqlc's type
// inference wanted a cast — should be a change to one method body rather than a
// change to the package's surface.
type Generator struct {
	dialect dialect.Dialect
}

// For returns a Generator emitting d's SQL.
//
// It panics on a dialect outside the supported set, in the manner of the rest
// of this package: the argument is a constant in a generator binary, so an
// unsupported dialect is a typo a build should stop for rather than a condition
// a caller could do anything with. The panic value is an error wrapping
// dialect.ErrUnsupported. A caller holding a dialect that came from
// configuration rather than a literal can ask dialect.Dialect.Valid first, and
// report the rejection in whatever terms its own users understand.
func For(d dialect.Dialect) *Generator {
	if !d.Valid() {
		panic(platformerrors.Wrapf(dialect.ErrUnsupported, "querygen: dialect %q", d))
	}

	return &Generator{dialect: d}
}

// Dialect returns the dialect g emits for.
func (g *Generator) Dialect() dialect.Dialect {
	return g.dialect
}

// The six expressions below are every place the three dialects genuinely
// disagree. They live together, in one file, so that adding a fourth dialect is
// a matter of reading one screen rather than grepping for casts — and so that a
// reader asking "what does this package assume about Postgres" gets a complete
// answer instead of a representative sample.
//
// Each carries the divergence and nothing else. The statements that use them are
// written once, in fragments.go and standard.go, and are the same text on every
// dialect apart from what these return.

// substringMatch renders a case-insensitive substring match of column against a
// bound argument.
//
// The wildcards are concatenated around the bound value rather than the caller
// passing '%term%', because a caller assembling the pattern is a caller who can
// forget to escape a literal '%' in a user's search term, which turns a search
// for "50%" into a search for everything.
//
// Postgres has an operator for this and it is ILIKE, which folds case by the
// database's collation rules — Unicode included. Neither of the others has one.
// MySQL's LIKE is case-insensitive only because its default collation is, so a
// column declared with a _bin or _cs collation would match case-sensitively
// while the emitted SQL still said LIKE; SQLite's LIKE folds ASCII only, and
// only while PRAGMA case_sensitive_like is off. Both are made unconditional by
// folding both sides explicitly, which costs the index either way — as ILIKE
// does on Postgres without a trigram index.
//
// The residual difference is worth stating plainly rather than papering over:
// on Postgres this matches "STRASSE" against "straße" and on the other two it
// does not, because LOWER outside Postgres folds ASCII and stops.
//
// Neither non-Postgres arm casts its argument, and on MySQL that is load-bearing
// rather than an omission. A bound parameter is the weakest thing in MySQL's
// coercibility order, so it takes the collation of whatever it is compared
// against; CAST(... AS CHAR) turns it into a string carrying the connection's
// own collation instead, and comparing that against a column whose collation
// differs is not a fallback but error 1267, "illegal mix of collations". The
// connection's collation is the driver's to choose and the column's is the
// schema's, so the two disagreeing is the ordinary case rather than the exotic
// one — a go-sql-driver connection against a MySQL 8 table disagrees out of the
// box. Leaving the parameter uncast is what lets it adopt the column's.
func (g *Generator) substringMatch(column, argument string) string {
	switch g.dialect {
	case dialect.MySQL:
		return fmt.Sprintf("LOWER(%s) LIKE CONCAT('%%', LOWER(sqlc.arg(%s)), '%%')", column, argument)
	case dialect.SQLite:
		return fmt.Sprintf("LOWER(%s) LIKE '%%' || LOWER(sqlc.arg(%s)) || '%%'", column, argument)
	// Postgres, which For has already narrowed the alternatives to.
	default:
		return fmt.Sprintf("%s ILIKE '%%' || sqlc.arg(%s)::text || '%%'", column, argument)
	}
}

// byteOrdered wraps an expression so that comparing and ordering it is a
// comparison of bytes rather than of collated text.
//
// search/sync requires ascending byte order from the reindex scan, because the
// pruning half of a reindex merges that stream against the index's own stream of
// ids. Two ordered streams merged under disagreeing orders do not fail; they
// conclude that live documents are absent from the source and delete them. Every
// dialect here defaults to something other than byte order for text — Postgres
// to the database's collation, which under en_US.UTF-8 sorts case-insensitively
// and ignores punctuation; MySQL to utf8mb4_0900_ai_ci, which does the same — so
// the order is named rather than assumed on all three, SQLite included, where it
// happens to already be the default.
//
// MySQL is a cast rather than a COLLATE clause on purpose. utf8mb4_bin would be
// the direct translation and it is wrong for a column that is not utf8mb4: a
// latin1 id column would take the collation clause and fail at parse time, for a
// reason that reads as a charset problem. Casting to BINARY compares the stored
// bytes whatever they are, which is the property being asked for.
func (g *Generator) byteOrdered(expression string) string {
	switch g.dialect {
	case dialect.MySQL:
		return fmt.Sprintf("CAST(%s AS BINARY)", expression)
	case dialect.SQLite:
		return expression + " COLLATE BINARY"
	// Postgres, which For has already narrowed the alternatives to.
	default:
		return expression + ` COLLATE "C"`
	}
}

// timeHorizon renders the timestamp an unset filter bound coalesces to: 999
// years either side of now, per sign, which is "-" for a lower bound and "+"
// for an upper one.
//
// A bound that is absent is rendered as a bound that cannot exclude anything,
// rather than as an omitted predicate, so that all four bounds are the same
// statement whichever subset of them a caller sent — see boundPredicate.
//
// 999 years is inside every dialect's representable range and is not close to
// any of their edges: MySQL's DATETIME stops at 9999-12-31 and starts at
// 1000-01-01, which the offset clears in both directions for any date this code
// will run on. The scalar subquery is not required by any of them; it is kept
// because it is what Postgres's sqlc reads the COALESCE's type through, and one
// shape across three dialects is one shape to get wrong.
//
// SQLite has no interval type and no arithmetic on timestamps, so the offset is
// a modifier string handed to datetime(). Its result is text in the same
// YYYY-MM-DD HH:MM:SS shape CURRENT_TIMESTAMP produces, which is what makes the
// comparison against it lexicographic and correct — and what makes a SQLite
// table storing timestamps in any other shape a table this package cannot
// filter. See the package comment.
func (g *Generator) timeHorizon(sign string) string {
	switch g.dialect {
	case dialect.MySQL:
		return fmt.Sprintf("(SELECT %s %s INTERVAL 999 YEAR)", NowExpression, sign)
	case dialect.SQLite:
		return fmt.Sprintf("(SELECT datetime(%s, '%s999 years'))", NowExpression, sign)
	// Postgres, which For has already narrowed the alternatives to.
	default:
		return fmt.Sprintf("(SELECT %s %s '999 years'::INTERVAL)", NowExpression, sign)
	}
}

// includeArchivedFlag renders the archived toggle's argument: a nullable
// boolean coalesced to false.
//
// The Postgres cast is load-bearing and has no counterpart elsewhere. sqlc.narg
// types the argument from its use, and COALESCE over an untyped NULL leaves
// Postgres to guess; ::boolean is what makes the generated Go field a *bool
// rather than an interface{} the caller has to convince. MySQL and SQLite have
// no boolean type to cast to — both spell it as an integer — and their COALESCE
// takes its type from the false, so there is nothing to add and adding the
// nearest thing (CAST(... AS UNSIGNED)) would only turn the generated field
// into a number.
func (g *Generator) includeArchivedFlag() string {
	coalesced := fmt.Sprintf("COALESCE(sqlc.narg(%s), false)", IncludeArchivedArg)
	if g.dialect == dialect.Postgres {
		return coalesced + "::boolean"
	}

	return coalesced
}

// limitClause renders the page-size clause a keyset walk ends on.
//
// Postgres and SQLite take an expression, so an absent page size coalesces to
// filtering.DefaultQueryFilterLimit and the generated Go parameter is a pointer
// the caller may leave nil. MySQL takes an integer literal or a placeholder
// after LIMIT and nothing else — COALESCE there is a parse error, not a slower
// plan — so its clause binds the size and the generated parameter is a value.
//
// This is the one place a dialect changes a generated signature rather than only
// the SQL behind it, and leveling the other two down to match would be the
// wrong trade: it would take a working default away from the dialects that can
// express one in order to make a limitation uniform. Nothing drifts by leaving
// them different, because the default is filtering's constant rather than a
// number written here — the SQL and filtering.QueryFilter.Normalize read the
// same one.
//
// What a MySQL consumer owes its queries, then, is Normalize: it turns an absent
// or zero page size into that same constant and clamps an oversized one, which
// is the treatment the URL parameter already gets. A MySQL query handed a zero
// returns no rows, which is loud, rather than a page of some other size.
func (g *Generator) limitClause() string {
	if g.dialect == dialect.MySQL {
		return fmt.Sprintf("LIMIT sqlc.arg(%s)", LimitArg)
	}

	return fmt.Sprintf("LIMIT COALESCE(sqlc.narg(%s), %d)", LimitArg, filtering.DefaultQueryFilterLimit)
}

// idSetPredicate matches the id column against a bound set of ids.
//
// Postgres has arrays and takes the whole set as one argument, which is what
// keeps a flush of a hundred ids a statement with one parameter rather than a
// hundred. The other two have no array type, so the set is expanded by sqlc into
// as many placeholders as there are ids — sqlc.slice, which sqlc documents for
// exactly these two engines. The generated Go signature is []string either way,
// so this is a difference in what reaches the server rather than in what a
// caller writes.
func (g *Generator) idSetPredicate() string {
	if g.dialect == dialect.Postgres {
		return fmt.Sprintf("%s = ANY(sqlc.arg(%s)::text[])", IDColumn, IDsArg)
	}

	return fmt.Sprintf("%s IN (sqlc.slice(%s))", IDColumn, IDsArg)
}
