/*
Package privacy is the settings values table's contribution to a subject access
request: a dataprivacy.Collector that returns what somebody chose, and a
dataprivacy.Eraser that destroys it.

# Why this is a package rather than two methods on the store

settings would otherwise import dataprivacy, which imports operations, which
imports the queue and the scheduler — so a service with a preferences page and no
privacy pipeline would compile all of it. The seam goes here for the same reason
dataprivacy/auditerasure exists, and it costs one constructor argument.

This is the package settings/doc.go's "# Erasure" section has always promised.
[settings.ValueStore.DeleteValuesForSubject] was written as the thing a
dataprivacy.Eraser would be built on, and until now nothing was built on it —
which made the method a hard delete no caller in the module reached, and made
settings a package an erasure run walked past.

# Over the value store, not the whole store

Both halves take a [settings.ValueStore] rather than a [settings.Store]. The
definition catalog is administrative — a definition is a setting an operator
declared, not an answer a person gave — so it holds nothing about a subject,
appears in no export and survives every erasure. Taking the composed interface
would make a deployment hand this package six methods it must not call in order
to reach the two it must.

# Why the erasure deletes

Clearing is an archive, and an archived value still says what the subject chose:
[settings.ValueStore.ClearValue] stamps the row and leaves the answer in it. That
is deliberate — it is what an audit of a preference change reads — and it is
exactly why it is not what an erasure reaches for.
[settings.ValueStore.DeleteValuesForSubject] is the one hard delete in that
package, cleared answers included, and it is the write used here.

There is no anonymization to fall back to. A value is keyed on the subject and
holds a raw string the subject chose; stripping the subject off it leaves a row
that belongs to nobody and answers nothing, which is a deletion with extra steps.
The rows go.

Nothing is retained, and so nothing is reported as retained. A deployment whose
jurisdiction requires a record of what somebody chose registers no eraser — the
values then survive an erasure, which is a decision somebody has made rather than
one this package made for them.

# Subjects are two fields, and both vocabularies spell them the same

settings keys a value on a [settings.Subject]: a type and an id, two columns
rather than one composite string, for the reason the tenancy doctrine gives about
the scope. dataprivacy.Subject is the same pair, and settings.SubjectType is
documented as the bare string with suggested constants that dataprivacy.SubjectType
also is — "user" and "account" on both sides. So the mapping is a conversion and
not a lookup, and a test pins that it stays one.

A subject whose type is neither is resolved by the same rule: whatever pair the
request carries is what a value would have been filed under, and one that matches
nothing collects and erases nothing.

# Scopes

Every read and write in settings is scoped, and a subject access request may
arrive without a scope — the request's confinement names nobody for the plain
"give me my data". So both halves take a [ScopeResolver]: the mapping from a
subject to the scopes their settings may be in, which is a question about the
consumer's tenancy model rather than about this table.

It is a constructor argument rather than an option with a default, because there
is no default that is right twice. A deployment with one tenant wants
[FixedScopes] over tenancy.Global(); one whose requests always name their scope
wants [RequestScope]; one that resolves a person to their accounts has to ask its
own directory. A resolver that silently answered "the global scope" for the third
of those would export nothing and erase nothing, and report success for both.

# Executors

Every read and write in settings runs on an executor its caller supplies, and the
two halves get theirs from different places. dataprivacy.Eraser.Erase is handed
the request's database.Tx, so [Eraser] passes that straight down and a subject's
settings commit with the rest of their footprint — which is the whole of what
[settings.ValueStore.DeleteValuesForSubject]'s signature exists to require.
dataprivacy.Collector.Collect is handed nothing, because an export is a read and
there is no transaction for it to be part of — so [NewCollector] takes the
executor once, at construction, and every collection runs on it.

That is a database.SQLQueryExecutor rather than a database.Client: a collector
reads and does nothing else, and the narrower type is also the one that lets a
consumer hand it a Tx where an export genuinely has to see a transaction's own
writes.

# Observability

Neither half instruments anything of its own. Everything they do is a call into
the store, which spans and logs every read and write it makes, and a second span
around a loop of those would name the same work twice.
*/
package privacy

import (
	"context"
	"encoding/json"

	"github.com/primandproper/platform-go/v14/dataprivacy"
	"github.com/primandproper/platform-go/v14/settings"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// DefaultKey is the registry key these are normally registered under. It names
// the section an export's artifact carries them in, and the prefix of any
// entries an outcome reports.
const DefaultKey = "settings"

// The sentinels this package returns.
var (
	// ErrNilStore indicates a nil settings.ValueStore.
	ErrNilStore = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil setting value store")

	// ErrNilScopeResolver indicates a nil ScopeResolver. It is required, and
	// refusing it here is what stops an export that quietly covers no scope from
	// being discovered by the subject.
	ErrNilScopeResolver = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil settings privacy scope resolver")

	// ErrNilExecutor indicates a nil executor. Both halves run on one somebody
	// else supplies — the collector's at construction, the eraser's per request —
	// because settings keeps no connection of its own to fall back to.
	ErrNilExecutor = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil settings privacy query executor")

	// ErrUnscopedRequest indicates a request that names no scope, handed to
	// [RequestScope], which has nowhere else to get one.
	ErrUnscopedRequest = platformerrors.New("settings request names no scope")
)

// ScopeResolver names the scopes a subject's settings may be in.
//
// Returning no scopes is legitimate and means the subject has nothing here: the
// collector reports the domain as holding nothing, and the eraser deletes
// nothing. Returning too many is how one subject's erasure reaches another
// tenant's preferences, so it is worth being exact.
//
// requestScope is the confinement the privacy request named, which the fulfiller
// hands over beside the subject. The zero Scope is the request that named none —
// a plain "give me my data" — and what a resolver makes of that is the whole of
// the decision this seam exists for.
type ScopeResolver func(
	ctx context.Context,
	requestScope tenancy.Scope,
	subject dataprivacy.Subject,
) ([]tenancy.Scope, error)

// RequestScope resolves the scope the request itself names, for a deployment
// where a privacy request always arrives scoped.
//
// A request that names none is ErrUnscopedRequest rather than the global scope.
// The confinement arrives as a tenancy.Scope, so "confined to nobody" and "the
// global scope" are already distinct values here and nothing has to reconstruct
// the difference; what a resolver still cannot do is invent the scope a request
// declined to name. The difference is not recoverable later: an export that
// quietly covered only the global scope would be well-formed, would have a
// section, and would be missing every preference the subject ever set.
func RequestScope(
	_ context.Context,
	requestScope tenancy.Scope,
	subject dataprivacy.Subject,
) ([]tenancy.Scope, error) {
	if requestScope.Validate() != nil {
		return nil, platformerrors.Wrapf(ErrUnscopedRequest, "subject %q", subject.ID)
	}

	return []tenancy.Scope{requestScope}, nil
}

// FixedScopes resolves every subject to the same scopes, for a deployment whose
// tenancy is fixed — most often the single-tenant one, as
// FixedScopes(tenancy.Global()).
func FixedScopes(scopes ...tenancy.Scope) ScopeResolver {
	fixed := make([]tenancy.Scope, len(scopes))
	copy(fixed, scopes)

	return func(context.Context, tenancy.Scope, dataprivacy.Subject) ([]tenancy.Scope, error) {
		return fixed, nil
	}
}

// subjectOf renders a privacy request's subject as the store keys values on. The
// two vocabularies agree — see the package documentation — so this is a
// conversion and not a lookup.
func subjectOf(subject dataprivacy.Subject) settings.Subject {
	return settings.Subject{Type: settings.SubjectType(subject.Type), ID: subject.ID}
}

// Collector returns the settings a subject chose.
type Collector struct {
	store   settings.ValueStore
	reader  database.SQLQueryExecutor
	resolve ScopeResolver
}

var _ dataprivacy.Collector = (*Collector)(nil)

// NewCollector builds the collector over a value store, the executor its reads
// run on, and a scope resolver. All three are required.
//
// The executor is a constructor argument because dataprivacy.Collector.Collect
// has nowhere to put one: an export is a read, and the seam that asks for it
// hands over a subject and nothing else. Client.Reader() is what a consumer
// ordinarily passes; a Tx is what it passes when the export has to see writes
// that transaction has not committed.
func NewCollector(
	store settings.ValueStore,
	reader database.SQLQueryExecutor,
	resolve ScopeResolver,
) (*Collector, error) {
	if store == nil {
		return nil, ErrNilStore
	}

	if reader == nil {
		return nil, ErrNilExecutor
	}

	if resolve == nil {
		return nil, ErrNilScopeResolver
	}

	return &Collector{store: store, reader: reader, resolve: resolve}, nil
}

// Collect implements dataprivacy.Collector.
//
// It pages each scope's values to the end through dataprivacy.CollectAll,
// because a collector that read one page and stopped would return a truncated
// subject access request — well-formed, present, and missing everything past the
// first page.
//
// It asks for cleared values as well as live ones, on a copy of the filter it is
// handed. A cleared value is archived rather than deleted and still says what the
// subject chose, which is the settings package's own reading of the row; an
// export showing only what somebody had not yet reset would answer a different
// question than the one the right of access asks.
func (c *Collector) Collect(
	ctx context.Context,
	requestScope tenancy.Scope,
	subject dataprivacy.Subject,
) (json.RawMessage, error) {
	scopes, err := c.resolve(ctx, requestScope, subject)
	if err != nil {
		return nil, platformerrors.Wrap(err, "resolving setting scopes for subject")
	}

	held := subjectOf(subject)

	var chosen []settings.Value

	for _, scope := range scopes {
		page, collectErr := dataprivacy.CollectAll(ctx,
			func(ctx context.Context, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[settings.Value], error) {
				everything := *filter
				everything.IncludeArchived = new(true)

				return c.store.ListValuesForSubject(ctx, c.reader, scope, held, &everything)
			})
		if collectErr != nil {
			return nil, platformerrors.Wrapf(collectErr, "collecting setting values in scope %q", scope)
		}

		chosen = append(chosen, page...)
	}

	return dataprivacy.Fragment(len(chosen) > 0, chosen)
}

// Eraser destroys the settings a subject chose.
type Eraser struct {
	store   settings.ValueStore
	resolve ScopeResolver
}

var _ dataprivacy.Eraser = (*Eraser)(nil)

// NewEraser builds the eraser over a value store and a scope resolver. Both are
// required.
func NewEraser(store settings.ValueStore, resolve ScopeResolver) (*Eraser, error) {
	if store == nil {
		return nil, ErrNilStore
	}

	if resolve == nil {
		return nil, ErrNilScopeResolver
	}

	return &Eraser{store: store, resolve: resolve}, nil
}

// Erase implements dataprivacy.Eraser.
//
// It runs in the request's transaction and uses the executor it is given, so the
// settings and the rest of the subject's footprint commit or roll back together.
// A subject who never answered anything erases nothing and is not an error.
func (e *Eraser) Erase(
	ctx context.Context,
	tx database.Tx,
	requestScope tenancy.Scope,
	subject dataprivacy.Subject,
) (dataprivacy.ErasureOutcome, error) {
	if tx == nil {
		return dataprivacy.ErasureOutcome{}, ErrNilExecutor
	}

	scopes, err := e.resolve(ctx, requestScope, subject)
	if err != nil {
		return dataprivacy.ErasureOutcome{},
			platformerrors.Wrap(err, "resolving setting scopes for subject")
	}

	held := subjectOf(subject)

	var outcome dataprivacy.ErasureOutcome

	for _, scope := range scopes {
		deleted, deleteErr := e.store.DeleteValuesForSubject(ctx, tx, scope, held)
		if deleteErr != nil {
			return dataprivacy.ErasureOutcome{},
				platformerrors.Wrapf(deleteErr, "erasing setting values in scope %q", scope)
		}

		outcome.Deleted += deleted
	}

	return outcome, nil
}
