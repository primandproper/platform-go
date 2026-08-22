/*
Package dialect names the SQL dialects the module's SQL-emitting packages
support, and carries the small helpers every one of them otherwise reimplements:
bind-marker rendering, identifier vetting, and DDL statement splitting.

It exists to be a leaf. database/migrate, outbox, and authorization/database all
speak the same three dialects, and their migrations subpackages cannot import
their parents without closing a cycle through the parents' tests — so before
this package, each of the five declared its own Dialect type and tests converted
between them. One shared type makes those conversions unrepresentable.
*/
package dialect

import (
	"slices"
	"strconv"
	"strings"

	"github.com/primandproper/platform-go/v13/charset"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

// Dialect selects the SQL a package emits. It must match the database provider
// the emitted SQL runs against.
type Dialect string

const (
	// Postgres targets PostgreSQL, which numbers its placeholders and supports
	// SKIP LOCKED.
	Postgres Dialect = "postgres"
	// MySQL targets MySQL 8.0+ — the first version with WITH RECURSIVE — which
	// supports SKIP LOCKED.
	MySQL Dialect = "mysql"
	// SQLite targets SQLite, which is single-writer by nature and has no
	// SKIP LOCKED.
	SQLite Dialect = "sqlite"
)

// ErrUnsupported indicates a dialect outside the supported set. Packages wrap
// it with their own context, so errors.Is works across all of them.
var ErrUnsupported = platformerrors.New("unsupported SQL dialect")

// Valid reports whether d is a dialect this module can emit SQL for.
func (d Dialect) Valid() bool {
	switch d {
	case Postgres, MySQL, SQLite:
		return true
	default:
		return false
	}
}

// SupportsSkipLocked reports whether the dialect can claim rows with
// FOR UPDATE SKIP LOCKED, which is what allows more than one competing worker
// to claim from the same table at once.
func (d Dialect) SupportsSkipLocked() bool {
	return d == Postgres || d == MySQL
}

// SupportsNotify reports whether the dialect can signal a listening session
// with LISTEN/NOTIFY, which is what lets a poller be woken instead of waiting
// out its interval.
func (d Dialect) SupportsNotify() bool {
	return d == Postgres
}

// PostgresNotifyStatement emits a payload-free notification on the channel
// bound to it, waking anything listening on that channel — see
// database/postgres/pgnotify for the other end.
//
// The payload is empty on purpose. Postgres collapses duplicate
// (channel, payload) pairs within a transaction, so a transaction that notifies
// fifty times sends one notification, and there is nothing in it for a consumer
// to come to depend on. The channel is bound rather than interpolated; the
// listening side has to render it into a LISTEN, which takes no parameters, so
// it is vetted with ValidIdentifier there.
//
// It lives here, in the leaf every SQL-emitting package already imports, rather
// than beside the listener: outbox serves three dialects and workqueue serves
// one, and neither should take a pgx dependency to reach a constant.
const PostgresNotifyStatement = `SELECT pg_notify($1, '')`

// RequireDialect returns a wrapped ErrUnsupported naming component and d unless
// d is one of want.
//
// It is for the packages whose SQL is written against particular dialects rather
// than reduced to a portable subset, so that all of them refuse the same way and
// at the same moment — construction — instead of emitting syntax the server
// rejects on the first query. component names the caller in the message ("work
// queue", "workqueue migration"), since a process wiring several of these needs
// to know which one objected.
//
// want is variadic because the constraint is not always a single dialect: this
// module holds packages that support Postgres and MySQL but not SQLite, which
// has no SKIP LOCKED. Prefer Valid over listing all three.
//
// Calling it with no accepted dialects is a programming error rather than a
// vacuous pass, and says so.
func RequireDialect(component string, d Dialect, want ...Dialect) error {
	if len(want) == 0 {
		return platformerrors.Wrapf(ErrUnsupported,
			"%s dialect %q: RequireDialect called with no accepted dialects", component, d)
	}

	if slices.Contains(want, d) {
		return nil
	}

	return platformerrors.Wrapf(ErrUnsupported, "%s dialect %q: requires %s", component, d, requirement(want))
}

// requirement renders want as the tail of the error message: one dialect reads
// as itself, several as a list.
func requirement(want []Dialect) string {
	names := make([]string, len(want))
	for i, w := range want {
		names[i] = string(w)
	}

	if len(names) == 1 {
		return names[0]
	}

	return "one of " + strings.Join(names, ", ")
}

// RequirePostgres is RequireDialect for the Postgres-only packages, which are
// the common case — the work queue and its migrations both reach for it.
func RequirePostgres(component string, d Dialect) error {
	return RequireDialect(component, d, Postgres)
}

// Placeholder renders the n-th bind marker (1-indexed). Postgres numbers its
// placeholders; MySQL and SQLite do not.
func (d Dialect) Placeholder(n int) string {
	if d == Postgres {
		return "$" + strconv.Itoa(n)
	}

	return "?"
}

// Placeholders renders count bind markers starting at start, joined for use
// inside an IN clause or a VALUES tuple.
func (d Dialect) Placeholders(start, count int) string {
	parts := make([]string, 0, count)
	for i := range count {
		parts = append(parts, d.Placeholder(start+i))
	}

	return strings.Join(parts, ", ")
}

// IdentifierChars and IdentifierLeadChars are the alphabet a SQL identifier is
// drawn from, and the narrower one its first character comes from. A leading
// digit is excluded so a bare name is never mistakable for a number.
//
// They are exported so that the rules built on top of an identifier — a table
// prefix, which is an identifier fragment or nothing — can be assembled from
// the same alphabet rather than from a second copy of it that could drift.
//
// ASCII only. Admitting the full Unicode letter category would let two names
// that render identically — homoglyphs, or the same string in NFC and NFD —
// claim to be two different tables.
var (
	IdentifierChars     = charset.ASCIIAlphanumeric.Union(charset.Bytes('_'))
	IdentifierLeadChars = charset.ASCIILetters.Union(charset.Bytes('_'))
)

// identifier is a name safe to interpolate into query text: a bare identifier,
// optionally qualified by exactly one schema.
//
// The whole string has to satisfy the rule, with nothing before or after it.
// That was the load-bearing property of the anchored regexp this replaces —
// which needed a note that Go's regexp is RE2, where $ means end of text rather
// than "before an optional trailing newline" as in Perl, so a trailing newline
// could not slip past. charset scans the string it is given and has no anchors
// to get wrong.
var identifier = charset.New(
	IdentifierChars,
	charset.WithFirst(IdentifierLeadChars),
	charset.WithSeparator('.', 2),
)

// ErrInvalidIdentifier indicates a name that ValidIdentifier rejects. Packages
// wrap it with their own context, so errors.Is works across all of them —
// including across a package that builds a table's DDL and one that queries it,
// which is the pair most likely to be checked against each other.
var ErrInvalidIdentifier = platformerrors.New("invalid SQL identifier")

// ValidIdentifier reports whether s is safe to interpolate into query text as
// a table name. Table names are interpolated rather than bound, so they are
// restricted rather than escaped.
func ValidIdentifier(s string) bool {
	return identifier.Valid(s)
}

// QuoteIdentifier renders an identifier as a quoted one for d, doubling any
// embedded quote character so it cannot end the quoting early.
//
// It is the escaping counterpart to ValidIdentifier's restricting, and both
// exist because a table or column name is interpolated into statement text
// rather than bound. Prefer ValidIdentifier where the name comes from
// configuration and a rejection is actionable; this is for the names that are
// legal-but-awkward — a mixed-case column, a reserved word — where refusing
// would be refusing a database somebody already has.
//
// Postgres and SQLite quote with double-quotes per the SQL standard; MySQL
// quotes with backticks. An unrecognized dialect gets the standard form, which
// is the one every dialect here but MySQL uses.
//
// This is not sanitization for arbitrary input. A NUL byte, or a name from a
// hostile source, still belongs in ValidIdentifier's hands: doubling the quote
// character makes a legal identifier safe to quote, not an arbitrary string
// safe to interpolate.
func (d Dialect) QuoteIdentifier(id string) string {
	quote := `"`
	if d == MySQL {
		quote = "`"
	}

	return quote + strings.ReplaceAll(id, quote, quote+quote) + quote
}

// SplitStatements strips '--' comments from ddl and splits it into individually
// executable statements on ';', preserving statement order.
//
// Comments come out before the split, not after. A '--' comment may contain a
// semicolon — prose routinely does — and splitting first tears such a comment
// in half, leaving its tail masquerading as SQL at the head of the next
// statement.
//
// Comment stripping handles whole-line '--' comments and blank lines only, not
// a '--' appearing after SQL on the same line, nor semicolons inside string
// literals; the DDL shipped by this module contains neither, and the round-trip
// tests against real servers are what keep that true.
func SplitStatements(ddl string) []string {
	var stmts []string
	for raw := range strings.SplitSeq(stripComments(ddl), ";") {
		if stmt := strings.TrimSpace(raw); stmt != "" {
			stmts = append(stmts, stmt)
		}
	}

	return stmts
}

// stripComments drops whole-line '--' comments and blank lines.
func stripComments(ddl string) string {
	var kept []string

	for line := range strings.SplitSeq(ddl, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" && !strings.HasPrefix(trimmed, "--") {
			kept = append(kept, line)
		}
	}

	return strings.Join(kept, "\n")
}
