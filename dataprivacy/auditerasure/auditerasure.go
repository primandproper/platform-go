/*
Package auditerasure supplies a dataprivacy.Eraser for the audit log.

# Why this is not simply "delete the subject's audit entries"

The audit log is a hash chain. Each entry's digest covers its own content and
its predecessor's digest, per scope, and audit.Reader.Verify walks that chain to
report whether anything has been altered or removed. Deleting an entry from the
middle of a scope is indistinguishable — to Verify, and to anyone reading its
output — from an attacker deleting it. So is anonymizing one in place: the
digest covers the actor ID, so overwriting it makes the entry read as tampered.

That is not a bug to work around. It is the property the audit package exists to
provide, and an eraser that quietly broke it would trade a real security control
for a checkbox.

So this eraser does the one thing that is both effective and sound:

  - Entries in scopes belonging to the subject are deleted outright, together
    with those scopes' chain rows. A whole chain disappearing leaves no gap in
    any surviving chain — there is nothing left to verify — so this is the one
    deletion the structure permits. Where an application scopes audit entries
    per user or per account, which is the common arrangement and the one the
    prior art used, this is the great majority of what is held.

  - Entries elsewhere in which the subject appears only as the actor or as the
    resource — their actions inside somebody else's tenant — are retained, and
    reported as retained with a legal basis. They cannot be removed without
    breaking that scope's chain, and they are in any case the entries most
    likely to be covered by the legitimate-interest and legal-obligation
    grounds under which audit logs are normally kept.

The retention is reported rather than silent. dataprivacy.ErasureOutcome carries
it into the request record, so "we kept some audit entries, on this basis" is
something the subject is told and a regulator can read, instead of something
discovered later.

# Turning it off

An operator whose jurisdiction or policy says the audit log must not be touched
at all simply does not register this eraser — or sets AuditErasure.Disabled in
the dataprivacy config subpackage, which skips the registration when it wires
the registry. Left alone, the eraser is registered: an erasure that silently
skipped a store of personal data would be the more surprising default.
*/
package auditerasure

import (
	"context"
	"fmt"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/ddl"
	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/dataprivacy"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

// DefaultKey is the registry key this eraser is normally registered under. It
// becomes the prefix of its entries in ErasureOutcome.Retained.
const DefaultKey = "audit"

// DefaultRetentionBasis is the basis recorded for entries that cannot be
// removed.
//
// It is deliberately generic and deliberately overridable. The correct wording
// is a legal question with a different answer in each jurisdiction, and a
// library that shipped a confident-sounding citation would be putting words in
// a lawyer's mouth.
const DefaultRetentionBasis = "audit records retained under legitimate interest and legal obligation; " +
	"entries are cryptographically chained and cannot be removed without destroying the integrity guarantee"

// ErrInvalidTablePrefix indicates a prefix that is not a plain SQL identifier
// fragment.
var ErrInvalidTablePrefix = platformerrors.New("invalid audit table prefix")

// ScopeResolver names the audit scopes that belong to a subject and may
// therefore be deleted whole.
//
// The default treats the subject's own ID as a scope, which is right when audit
// entries are scoped per user or per account — the arrangement the audit
// package's Scope field is designed for. An application that scopes differently
// must supply this, and one that returns too many scopes here deletes another
// tenant's audit log, so it is worth being exact.
//
// Returning no scopes is legitimate: it means nothing is deletable and
// everything is reported as retained.
type ScopeResolver func(ctx context.Context, subject dataprivacy.Subject) ([]string, error)

// Eraser removes a subject's audit scopes and reports what it could not remove.
type Eraser struct {
	resolve ScopeResolver
	entries string
	chains  string
	basis   string
	dialect dialect.Dialect
}

var _ dataprivacy.Eraser = (*Eraser)(nil)

// Option configures an Eraser.
type Option func(*Eraser)

// WithTablePrefix overrides audit.DefaultTablePrefix. It must match the prefix
// the audit tables were rendered with.
func WithTablePrefix(prefix string) Option {
	return func(e *Eraser) {
		e.entries = ddl.Qualify(prefix) + "audit_log_entries"
		e.chains = ddl.Qualify(prefix) + "audit_log_chains"
	}
}

// WithScopeResolver replaces the mapping from subject to deletable audit
// scopes.
func WithScopeResolver(resolve ScopeResolver) Option {
	return func(e *Eraser) {
		if resolve != nil {
			e.resolve = resolve
		}
	}
}

// WithRetentionBasis replaces the wording recorded against retained entries.
func WithRetentionBasis(basis string) Option {
	return func(e *Eraser) {
		if basis != "" {
			e.basis = basis
		}
	}
}

// New builds an Eraser over the audit tables.
//
// The dialect must match the database the erasure transaction runs against, and
// the prefix must match the audit tables' own.
func New(d dialect.Dialect, prefix string, opts ...Option) (*Eraser, error) {
	if !d.Valid() {
		return nil, platformerrors.Wrapf(dialect.ErrUnsupported, "audit erasure dialect %q", d)
	}

	if !ddl.ValidNamespace(prefix) {
		return nil, platformerrors.Wrapf(ErrInvalidTablePrefix, "audit table prefix %q", prefix)
	}

	e := &Eraser{
		dialect: d,
		entries: ddl.Qualify(prefix) + "audit_log_entries",
		chains:  ddl.Qualify(prefix) + "audit_log_chains",
		basis:   DefaultRetentionBasis,
		resolve: func(_ context.Context, subject dataprivacy.Subject) ([]string, error) {
			return []string{subject.ID}, nil
		},
	}
	for _, opt := range opts {
		if opt != nil {
			opt(e)
		}
	}

	for _, name := range []string{e.entries, e.chains} {
		if !dialect.ValidIdentifier(name) {
			return nil, platformerrors.Wrapf(ErrInvalidTablePrefix, "audit table %q", name)
		}
	}

	return e, nil
}

// Erase deletes the subject's audit scopes and reports what remains.
//
// It runs in the dataprivacy erasure's transaction, so the audit entries and the
// domain rows they describe commit or roll back together — which matters here
// more than anywhere else, since an audit log that survived a rolled-back
// erasure would be a record of something that did not happen.
func (e *Eraser) Erase(
	ctx context.Context,
	q database.SQLQueryExecutor,
	subject dataprivacy.Subject,
) (dataprivacy.ErasureOutcome, error) {
	if q == nil {
		return dataprivacy.ErasureOutcome{}, platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil query executor")
	}

	scopes, err := e.resolve(ctx, subject)
	if err != nil {
		return dataprivacy.ErasureOutcome{}, platformerrors.Wrap(err, "resolving audit scopes for subject")
	}

	outcome := dataprivacy.ErasureOutcome{Retained: map[string]string{}}

	if len(scopes) > 0 {
		if outcome.Deleted, err = e.deleteScopes(ctx, q, scopes); err != nil {
			return dataprivacy.ErasureOutcome{}, err
		}
	}

	// Counted after the scope deletion, so entries that were just removed are
	// not also reported as retained.
	remaining, err := e.countRemaining(ctx, q, subject)
	if err != nil {
		return dataprivacy.ErasureOutcome{}, err
	}

	if remaining > 0 {
		outcome.Retained["entries"] = fmt.Sprintf("%d %s", remaining, e.basis)
	}

	if len(outcome.Retained) == 0 {
		outcome.Retained = nil
	}

	return outcome, nil
}

// deleteScopes removes whole audit scopes: their entries, then their chain
// rows.
//
// Both, and in that order. Leaving the chain row behind would leave a scope
// whose recorded head position is ahead of any surviving entry, and a later
// entry written into that scope would be assigned a position the chain claims
// is already used.
func (e *Eraser) deleteScopes(ctx context.Context, q database.SQLQueryExecutor, scopes []string) (int64, error) {
	args := make([]any, 0, len(scopes))
	for _, scope := range scopes {
		args = append(args, scope)
	}

	placeholders := e.dialect.Placeholders(1, len(scopes))

	result, err := q.ExecContext(ctx,
		fmt.Sprintf("DELETE FROM %s WHERE scope IN (%s)", e.entries, placeholders), args...)
	if err != nil {
		return 0, platformerrors.Wrap(err, "deleting audit entries for subject scopes")
	}

	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, platformerrors.Wrap(err, "counting deleted audit entries")
	}

	if _, err = q.ExecContext(ctx,
		fmt.Sprintf("DELETE FROM %s WHERE scope IN (%s)", e.chains, placeholders), args...); err != nil {
		return 0, platformerrors.Wrap(err, "deleting audit chains for subject scopes")
	}

	return deleted, nil
}

// countRemaining counts entries elsewhere that still name the subject.
//
// These are the ones the chain will not let go of: the subject acting inside
// another tenant's scope, or appearing as the resource of somebody else's
// action. They are counted rather than sampled, because the number is what goes
// in front of the subject and "some" is not an answer.
func (e *Eraser) countRemaining(ctx context.Context, q database.SQLQueryExecutor, subject dataprivacy.Subject) (int64, error) {
	query := fmt.Sprintf(
		"SELECT COUNT(*) FROM %s WHERE actor_id = %s OR resource_id = %s",
		e.entries, e.dialect.Placeholder(1), e.dialect.Placeholder(2),
	)

	var remaining int64
	if err := q.QueryRowContext(ctx, query, subject.ID, subject.ID).Scan(&remaining); err != nil {
		return 0, platformerrors.Wrap(err, "counting retained audit entries")
	}

	return remaining, nil
}
