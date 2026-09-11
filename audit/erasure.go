package audit

import (
	"context"

	"github.com/primandproper/platform-go/v14/audit/internal/auditdb"
	"github.com/primandproper/platform-go/v14/audit/internal/queries"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/ddl"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// Erasure is the audit log expressed as an erasure target: the two writes and
// the count a right-to-be-forgotten request runs against these tables.
//
// It exists because dataprivacy/auditerasure owns no table. The statements it
// runs address this package's schema, so they belong in this package's corpus —
// a second corpus over somebody else's schema would be a second place a column
// rename here has to be noticed — and this type is how they reach a caller
// outside the directory the generated querier is internal to. What that caller
// keeps is everything an erasure is actually about: which scopes belong to the
// subject, the basis retained entries are kept under, and the registry the
// eraser is wired into.
//
// Like the Recorder, it holds no database handle. Each method takes the
// caller's executor, so both statements run inside the erasure transaction
// dataprivacy opened — an audit deletion that committed while the erasure it
// belongs to rolled back would be a record of something that did not happen.
//
// # Why the division is what it is
//
// Whole scopes belonging to the subject are deleted outright, chain row and
// all. A chain that disappears entirely leaves no gap in any surviving chain,
// so this is the one deletion the structure permits.
//
// Entries elsewhere, where the subject appears only as the actor or as the
// resource, cannot be removed. The digest covers both columns and links each
// entry to the one before it, so deleting or anonymizing one would make every
// later verification of that scope report tampering — which is the property the
// package exists to provide, not a bug to work around. Those are counted
// instead, and the count is what the subject is told.
type Erasure struct {
	// q is the generated querier, instantiated for the configured dialect at
	// the configured prefix.
	q auditdb.Querier

	prefix string
}

// ErasureOption configures an Erasure.
type ErasureOption func(*Erasure)

// WithErasureTablePrefix overrides DefaultTablePrefix. It must match the prefix
// the audit tables were rendered with.
func WithErasureTablePrefix(prefix string) ErasureOption {
	return func(e *Erasure) {
		if prefix != "" {
			e.prefix = prefix
		}
	}
}

// NewErasure builds an Erasure over the audit tables.
//
// The dialect must match the database the erasure transaction runs against, and
// the prefix must match the audit tables' own.
func NewErasure(d dialect.Dialect, opts ...ErasureOption) (*Erasure, error) {
	if !d.Valid() {
		return nil, platformerrors.Wrapf(dialect.ErrUnsupported, "audit dialect %q", d)
	}

	e := &Erasure{prefix: DefaultTablePrefix}
	for _, opt := range opts {
		if opt != nil {
			opt(e)
		}
	}

	if err := ValidateTablePrefix(e.prefix); err != nil {
		return nil, err
	}

	q, err := newQuerier(d, e.prefix)
	if err != nil {
		return nil, err
	}

	e.q = q

	return e, nil
}

// Describe names the entries table this Erasure deletes from, at the prefix it
// was built with.
func (e *Erasure) Describe() string {
	return ddl.Qualify(e.prefix) + queries.EntriesTable
}

// DeleteScopes removes whole audit scopes — their entries, then their chain
// rows — and reports how many entries went.
//
// Both, and in that order. Leaving the chain row behind would leave a scope
// whose recorded head position is ahead of any surviving entry, and a later
// entry written into that scope would be assigned a position the chain claims
// is already used.
//
// An empty set is not a query. There is no text to send for a zero-length set —
// `IN ()` is a syntax error on two of the three dialects — so the caller
// answers it, which is what "the subject owns no deletable scope" means: no
// scopes, no statement, nothing deleted.
//
// A scope that names nobody is refused rather than skipped. The set is bound as
// the identifiers its scopes name, and the identifier the zero Scope would
// render is the one tenancy.Global names — so a resolver that lost a scope
// somewhere in the middle of its list would delete the platform's own log
// instead of dropping a member from a set.
func (e *Erasure) DeleteScopes(ctx context.Context, q database.SQLQueryExecutor, scopes []tenancy.Scope) (int64, error) {
	if q == nil {
		return 0, ErrNilExecutor
	}

	if len(scopes) == 0 {
		return 0, nil
	}

	owners, err := scopeOwners(scopes)
	if err != nil {
		return 0, err
	}

	deleted, err := e.q.DeleteAuditLogEntriesInScopes(ctx, q,
		auditdb.DeleteAuditLogEntriesInScopesParams{Scopes: owners})
	if err != nil {
		return 0, platformerrors.Wrap(err, "deleting audit entries for subject scopes")
	}

	if err = e.q.DeleteAuditChainsInScopes(ctx, q,
		auditdb.DeleteAuditChainsInScopesParams{Scopes: owners}); err != nil {
		return 0, platformerrors.Wrap(err, "deleting audit chains for subject scopes")
	}

	return deleted, nil
}

// scopeOwners renders a set of scopes as the identifiers the column holds,
// refusing one that names nobody.
//
// The set is a bound set rather than a column value, which is why it is not a
// tenancy.Scope on the generated parameter: it is a membership test, and the
// three dialects expand it into as many placeholders as there are members. The
// check tenancy.Scope.Value would have made at the driver is made here instead,
// once per member and before any statement is sent.
func scopeOwners(scopes []tenancy.Scope) ([]string, error) {
	owners := make([]string, 0, len(scopes))

	for i, scope := range scopes {
		if err := scope.Validate(); err != nil {
			return nil, platformerrors.Wrapf(err, "audit scope %d of %d", i+1, len(scopes))
		}

		owners = append(owners, scope.Owner())
	}

	return owners, nil
}

// CountMentions counts the entries the chain will not let go of: the ones where
// the subject acted inside another tenant's scope, or was the thing acted on.
//
// It is counted rather than sampled, because the number is what goes in front
// of the subject and "some" is not an answer. Call it after DeleteScopes, so
// that entries just removed are not also reported as retained.
func (e *Erasure) CountMentions(ctx context.Context, q database.SQLQueryExecutor, subjectID string) (int64, error) {
	if q == nil {
		return 0, ErrNilExecutor
	}

	row, err := e.q.CountAuditLogEntriesForSubject(ctx, q,
		auditdb.CountAuditLogEntriesForSubjectParams{SubjectID: subjectID})
	if err != nil {
		return 0, platformerrors.Wrap(err, "counting retained audit entries")
	}

	return row.Count, nil
}
