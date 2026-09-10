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

	"github.com/primandproper/platform-go/v14/audit"
	"github.com/primandproper/platform-go/v14/dataprivacy"

	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/database/dialect"
	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/tenancy"
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
//
// It is audit.ErrInvalidTablePrefix rather than a sentinel of this package's
// own, and that is the port rather than a shortcut: this package renders no
// table name any more — the statements it runs are audit's, against audit's
// tables — so the rule about what a prefix may be has one home and one error.
// It stays exported here so that a caller checking this package's errors need
// not know which package the check moved to.
var ErrInvalidTablePrefix = audit.ErrInvalidTablePrefix

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
// everything is reported as retained. Returning a scope that names nobody is
// not, and audit.Erasure.DeleteScopes refuses the whole set rather than
// deleting the platform's own chain in its place — a resolver whose own lookup
// came back empty is a resolver that has not answered.
//
// requestScope is the confinement the privacy request named, handed over beside
// the subject. The default ignores it, because a subject's own chain is theirs
// whichever tenant asked; a deployment whose audit scopes are tenants reads it
// as the instruction to delete inside that one, and one whose requests never
// name a confinement will only ever see the zero Scope.
type ScopeResolver func(
	ctx context.Context,
	requestScope tenancy.Scope,
	subject dataprivacy.Subject,
) ([]tenancy.Scope, error)

// Eraser removes a subject's audit scopes and reports what it could not remove.
//
// It holds no SQL. The two deletes and the count are audit's own statements,
// in audit's checked corpus against the schema audit ships the migrations for,
// reached through the [audit.Erasure] this type is built around — see that
// type's documentation for why a package owning no table owns no corpus
// either. What lives here is the part that is genuinely this package's: which
// scopes belong to a subject, and the basis retained entries are kept under.
type Eraser struct {
	resolve ScopeResolver
	erasure *audit.Erasure
	basis   string
	prefix  string
}

var _ dataprivacy.Eraser = (*Eraser)(nil)

// Option configures an Eraser.
type Option func(*Eraser)

// WithTablePrefix overrides audit.DefaultTablePrefix. It must match the prefix
// the audit tables were rendered with.
//
// It names no table: the prefix is forwarded to audit, which vets it and hands
// it to the generated querier, and nothing here renders an identifier from it.
// It is the only way to say which tables are meant, which is the point — a
// constructor that also took the prefix positionally would be two places to
// name one thing, and a prefix accepted in one and overridden in the other.
func WithTablePrefix(prefix string) Option {
	return func(e *Eraser) {
		e.prefix = prefix
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
// The dialect must match the database the erasure transaction runs against. The
// tables are audit.DefaultTablePrefix's unless WithTablePrefix says otherwise,
// in which case the prefix must match the audit tables' own.
func New(d dialect.Dialect, opts ...Option) (*Eraser, error) {
	if !d.Valid() {
		return nil, platformerrors.Wrapf(dialect.ErrUnsupported, "audit erasure dialect %q", d)
	}

	e := &Eraser{
		prefix: audit.DefaultTablePrefix,
		basis:  DefaultRetentionBasis,
		resolve: func(_ context.Context, _ tenancy.Scope, subject dataprivacy.Subject) ([]tenancy.Scope, error) {
			return []tenancy.Scope{tenancy.Of(subject.ID)}, nil
		},
	}
	for _, opt := range opts {
		if opt != nil {
			opt(e)
		}
	}

	// The prefix is vetted where the tables are named, which is the audit
	// package: one rule, applied once, so a prefix this package accepted and
	// that one refused is not a thing that can happen.
	erasure, err := audit.NewErasure(d, audit.WithErasureTablePrefix(e.prefix))
	if err != nil {
		return nil, err
	}

	e.erasure = erasure

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
	tx database.Tx,
	requestScope tenancy.Scope,
	subject dataprivacy.Subject,
) (dataprivacy.ErasureOutcome, error) {
	if tx == nil {
		return dataprivacy.ErasureOutcome{}, platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil query executor")
	}

	scopes, err := e.resolve(ctx, requestScope, subject)
	if err != nil {
		return dataprivacy.ErasureOutcome{}, platformerrors.Wrap(err, "resolving audit scopes for subject")
	}

	outcome := dataprivacy.ErasureOutcome{Retained: map[string]string{}}

	if outcome.Deleted, err = e.erasure.DeleteScopes(ctx, tx, scopes); err != nil {
		return dataprivacy.ErasureOutcome{}, err
	}

	// Counted after the scope deletion, so entries that were just removed are
	// not also reported as retained.
	remaining, err := e.erasure.CountMentions(ctx, tx, subject.ID)
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

// Describe names the audit table this eraser deletes from, at the prefix it was
// built with.
func (e *Eraser) Describe() string {
	return e.erasure.Describe()
}
