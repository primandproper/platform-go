/*
Package ddl renders a package's embedded schema against a dialect and a table
prefix, and vets the prefix against every identifier the schema would create.

It exists because six packages in this module ship the same three .sql files and
the same forty lines of Go to render them: pick the body for the dialect,
substitute the prefix, split on semicolons, and check the result is a legal
identifier. Six copies is six chances for one of them to drift — and one of them
already had, in the one place drift is invisible: whether the prefix carries its
own trailing separator or the template supplies it.

Like database/dialect, this is a leaf. A migrations subpackage cannot import its
parent without closing a cycle through the parent's tests, so the shared pieces
have to live below both.

# How a table name is built

A name has three parts, and only the first is configurable:

	ddb          _  audit_log   _  entries
	consumer        component      table
	namespace       segment

The component segment is owned by the schema and baked into its DDL, so a table
always says which package created it. A consumer reading their schema cold can
tell audit_log_entries from metering_events without consulting this module.

The namespace is the caller's, and is optional. Empty renders the component's
own names — audit_log_entries — which is what a consumer with one application per
database wants. Setting it to "ddb" renders ddb_audit_log_entries, which is what
a consumer sharing a database between applications wants:

	CREATE TABLE {{PREFIX}}audit_log_entries
	         ""  -> audit_log_entries
	      "ddb"  -> ddb_audit_log_entries

The separator belongs to the renderer, not the caller: a non-empty namespace has
'_' appended once, here. A caller who passes "ddb_" would otherwise render
ddb__audit_log_entries — legal SQL, and a table nobody meant to name — so a
namespace ending in '_' is rejected rather than silently accepted.
*/
package ddl

import (
	"regexp"
	"slices"
	"strings"

	"github.com/primandproper/platform-go/v13/charset"
	"github.com/primandproper/platform-go/v13/database/dialect"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

// Placeholder is the token every schema in this module uses for its table
// prefix.
const Placeholder = "{{PREFIX}}"

// MaxIdentifierLength is the longest identifier the supported engines all
// accept.
//
// Postgres allows 63 bytes and silently truncates anything longer; MySQL allows
// 64 and errors. Truncation is the dangerous half: two index names that differ
// only past byte 63 become one name, and the second CREATE INDEX fails against a
// schema that looks correct. Validating against the smaller limit turns both
// outcomes into the same error, raised before any DDL runs.
const MaxIdentifierLength = 63

// placeholderToken matches the placeholder together with the identifier tail
// that follows it, which is how a rendered name's full length is known without
// anyone maintaining a list of them.
var placeholderToken = regexp.MustCompile(regexp.QuoteMeta(Placeholder) + `[A-Za-z0-9_]*`)

// ErrPrefixTooLong indicates a prefix that renders an identifier longer than the
// supported engines accept. It is distinct from dialect.ErrInvalidIdentifier
// because the fix is different: the prefix is well-formed, just too long.
var ErrPrefixTooLong = platformerrors.New("table prefix renders an over-long SQL identifier")

// ErrPrefixTrailingSeparator indicates a namespace ending in '_'. The renderer
// supplies the separator, so a namespace carrying one too renders a doubled
// separator — legal SQL, and a table nobody meant to name.
var ErrPrefixTrailingSeparator = platformerrors.New("table prefix must not end in '_'")

// namespace is a table prefix on its own terms: a bare SQL identifier fragment,
// or nothing at all.
//
// It is not dialect.ValidIdentifier. That one admits a schema qualifier and
// refuses the empty string, and a namespace is the other way around on both
// counts — a dot in one would name a schema this module does not create, and
// empty is the ordinary case rather than a missing value.
//
// The alphabet is dialect's, not a second copy of it, so the two rules cannot
// come to disagree about which characters a name may hold.
var namespace = charset.New(
	dialect.IdentifierChars,
	charset.WithFirst(dialect.IdentifierLeadChars),
	charset.AllowEmpty(),
)

// ValidNamespace reports whether namespace is a legal table prefix on its own —
// a bare identifier fragment, or empty.
//
// It answers only the character question. Whether the prefix renders legal
// names, and names short enough, is Schema.ValidatePrefix, which needs a schema
// to know. This exists because four packages outside this one ask the character
// question first, each so it can report its own sentinel before the shared one:
// audit, audit/migrations, authorization/database and dataprivacy/auditerasure
// all carried an identical copy of the rule to do it.
func ValidNamespace(prefix string) bool {
	return namespace.Valid(prefix)
}

// Qualify renders the namespace portion of an identifier: empty for an empty
// namespace, and the namespace with a single trailing '_' otherwise.
//
// It is exported because packages build some names in Go rather than in SQL —
// a query builder naming the table it selects from — and those names have to
// agree with the DDL exactly.
func Qualify(namespace string) string {
	if namespace == "" {
		return ""
	}

	return namespace + "_"
}

// Schema is one package's DDL, in each dialect it supports.
type Schema struct {
	// Component names the owning package, and appears in every error this type
	// raises so a failure says which schema rejected the prefix.
	Component string

	Postgres string
	MySQL    string
	SQLite   string
}

// body returns the DDL for d.
func (s Schema) body(d dialect.Dialect) (string, error) {
	switch d {
	case dialect.Postgres:
		return s.Postgres, nil
	case dialect.MySQL:
		return s.MySQL, nil
	case dialect.SQLite:
		return s.SQLite, nil
	default:
		return "", platformerrors.Wrapf(dialect.ErrUnsupported, "%s migration dialect %q", s.Component, d)
	}
}

// Identifiers returns every identifier the schema would create under prefix,
// across all three dialects, sorted and deduplicated.
//
// It reads them out of the DDL rather than from a hand-maintained list, so an
// index added to the .sql files is covered by validation the moment it is added
// — which is what the previous per-package TableSuffixes lists could not do.
// They named the tables only, leaving the longest identifiers in every schema,
// the index names, unchecked.
func (s Schema) Identifiers(namespace string) []string {
	seen := map[string]struct{}{}
	qualifier := Qualify(namespace)

	for _, body := range []string{s.Postgres, s.MySQL, s.SQLite} {
		for _, token := range placeholderToken.FindAllString(body, -1) {
			seen[qualifier+strings.TrimPrefix(token, Placeholder)] = struct{}{}
		}
	}

	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}

	slices.Sort(out)

	return out
}

// ValidatePrefix reports whether prefix renders a legal identifier for every
// table and index the schema creates.
//
// The prefix is interpolated into query text rather than bound, so it is
// restricted rather than escaped. Vetting the prefix alone would not be enough:
// every rendered name has to be legal too, and a prefix that is fine on its own
// can still push an index name past the length limit.
func (s Schema) ValidatePrefix(namespace string) error {
	// An empty namespace is the ordinary case, not a missing value: it renders
	// the component's own names, which is what a consumer with one application
	// per database wants.
	if strings.HasSuffix(namespace, "_") {
		return platformerrors.Wrapf(ErrPrefixTrailingSeparator, "%s table prefix %q", s.Component, namespace)
	}

	for _, name := range s.Identifiers(namespace) {
		if !dialect.ValidIdentifier(name) {
			return platformerrors.Wrapf(dialect.ErrInvalidIdentifier, "%s identifier %q", s.Component, name)
		}

		if len(name) > MaxIdentifierLength {
			return platformerrors.Wrapf(ErrPrefixTooLong,
				"%s identifier %q is %d bytes, over the %d-byte limit",
				s.Component, name, len(name), MaxIdentifierLength)
		}
	}

	return nil
}

// Statements renders the DDL for the dialect against prefix and splits it into
// individually executable statements, each table before its indexes.
func (s Schema) Statements(d dialect.Dialect, prefix string) ([]string, error) {
	body, err := s.body(d)
	if err != nil {
		return nil, err
	}

	if err = s.ValidatePrefix(prefix); err != nil {
		return nil, err
	}

	return dialect.SplitStatements(strings.ReplaceAll(body, Placeholder, Qualify(prefix))), nil
}

// SQL renders the same DDL as Statements, joined back into one migration body.
// It is what a caller hands to database/migrate's WithGeneratedMigration.
//
// The comments are already stripped, which matters: goose splits a migration
// into statements on semicolons, and a '--' comment containing one would be torn
// in half.
func (s Schema) SQL(d dialect.Dialect, prefix string) (string, error) {
	stmts, err := s.Statements(d, prefix)
	if err != nil {
		return "", err
	}

	return strings.Join(stmts, ";\n\n") + ";\n", nil
}
