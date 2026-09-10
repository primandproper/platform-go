/*
Package privacy is the issue reports table's contribution to a subject access
request: a dataprivacy.Collector that returns what somebody filed, and a
dataprivacy.Eraser that destroys it.

# Why this is a package rather than two methods on the store

issuereports would otherwise import dataprivacy, which imports operations, which
imports the queue and the scheduler — so a service with an issue report form and
no privacy pipeline would compile all of it. The seam goes here for the same
reason dataprivacy/auditerasure exists, and it costs one constructor argument.

# Why the erasure deletes

The details are a sentence somebody typed, and nothing can promise a sentence
somebody typed names nobody. There is no anonymization to fall back to: stripping
the reporter off a report leaves the free text, which is the part that identifies
people, so a "kept but anonymized" report would be a report that still says who
it is about. The row goes.

Nothing is retained, and so nothing is reported as retained. An operator whose
jurisdiction says otherwise registers no eraser — the reports then survive an
erasure, which is a decision somebody has made rather than one this package made
for them.

# Scopes

Every read and write in issuereports is scoped, and a subject access request may
arrive without a scope — dataprivacy.Subject.Scope names nobody for the plain
"give me my data". So both halves take a [ScopeResolver]: the mapping from a subject to
the scopes their reports may be in, which is a question about the consumer's
tenancy model rather than about this table.

It is a constructor argument rather than an option with a default, because there
is no default that is right twice. A deployment with one tenant wants
[FixedScopes] over tenancy.Global(); one whose requests always name their scope
wants [RequestScope]; one that resolves a person to their accounts has to ask its
own directory. A resolver that silently answered "the global scope" for the third
of those would export nothing and erase nothing, and report success for both.

# Executors

Every read and write in issuereports runs on an executor its caller supplies, and
the two halves get theirs from different places. dataprivacy.Eraser.Erase is
handed the request's database.Tx, so [Eraser] passes that straight down and a
subject's reports commit with the rest of their footprint.
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
	"github.com/primandproper/platform-go/v14/issuereports"

	"github.com/primandproper/primitives-go/database"
	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/filtering"
	"github.com/primandproper/primitives-go/tenancy"
)

// DefaultKey is the registry key these are normally registered under. It names
// the section an export's artifact carries them in, and the prefix of any
// entries an outcome reports.
const DefaultKey = "issue_reports"

// The sentinels this package returns.
var (
	// ErrNilStore indicates a nil issuereports.Store.
	ErrNilStore = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil issue report store")

	// ErrNilScopeResolver indicates a nil ScopeResolver. It is required, and
	// refusing it here is what stops an export that quietly covers no scope from
	// being discovered by the subject.
	ErrNilScopeResolver = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil issue reports privacy scope resolver")

	// ErrNilExecutor indicates a nil executor. Both halves run on one somebody
	// else supplies — the collector's at construction, the eraser's per request —
	// because issuereports keeps no connection of its own to fall back to.
	ErrNilExecutor = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil issue reports privacy query executor")

	// ErrUnscopedRequest indicates a request that names no scope, handed to
	// [RequestScope], which has nowhere else to get one.
	ErrUnscopedRequest = platformerrors.New("issue reports request names no scope")
)

// ScopeResolver names the scopes a subject's issue reports may be in.
//
// Returning no scopes is legitimate and means the subject has nothing here: the
// collector reports the domain as holding nothing, and the eraser deletes
// nothing. Returning too many is how one subject's erasure reaches another
// tenant's reports, so it is worth being exact.
type ScopeResolver func(ctx context.Context, subject dataprivacy.Subject) ([]tenancy.Scope, error)

// RequestScope resolves the scope the request itself names, for a deployment
// where a privacy request always arrives scoped.
//
// A request that names none is ErrUnscopedRequest rather than the global scope.
// The subject's scope is a tenancy.Scope, so the two are already distinct
// values here and nothing has to reconstruct the difference; what a resolver
// still cannot do is invent the scope a request declined to name. The
// difference is not recoverable later: an
// export that quietly covered only the global scope would be well-formed, would
// have a section, and would be missing every report the subject actually filed.
func RequestScope(_ context.Context, subject dataprivacy.Subject) ([]tenancy.Scope, error) {
	if subject.Scope.Validate() != nil {
		return nil, platformerrors.Wrapf(ErrUnscopedRequest, "subject %q", subject.ID)
	}

	return []tenancy.Scope{subject.Scope}, nil
}

// FixedScopes resolves every subject to the same scopes, for a deployment whose
// tenancy is fixed — most often the single-tenant one, as
// FixedScopes(tenancy.Global()).
func FixedScopes(scopes ...tenancy.Scope) ScopeResolver {
	fixed := make([]tenancy.Scope, len(scopes))
	copy(fixed, scopes)

	return func(context.Context, dataprivacy.Subject) ([]tenancy.Scope, error) {
		return fixed, nil
	}
}

// Collector returns the issue reports a subject filed.
type Collector struct {
	store   issuereports.Store
	reader  database.SQLQueryExecutor
	resolve ScopeResolver
}

var _ dataprivacy.Collector = (*Collector)(nil)

// NewCollector builds the collector over a store, the executor its reads run on,
// and a scope resolver. All three are required.
//
// The executor is a constructor argument because dataprivacy.Collector.Collect
// has nowhere to put one: an export is a read, and the seam that asks for it
// hands over a subject and nothing else. Client.Reader() is what a consumer
// ordinarily passes; a Tx is what it passes when the export has to see writes
// that transaction has not committed.
func NewCollector(
	store issuereports.Store,
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
// It pages each scope's reports to the end through dataprivacy.CollectAll,
// because a collector that read one page and stopped would return a truncated
// subject access request — well-formed, present, and missing everything past the
// first page.
func (c *Collector) Collect(ctx context.Context, subject dataprivacy.Subject) (json.RawMessage, error) {
	scopes, err := c.resolve(ctx, subject)
	if err != nil {
		return nil, platformerrors.Wrap(err, "resolving issue report scopes for subject")
	}

	var reports []issuereports.Report

	for _, scope := range scopes {
		page, collectErr := dataprivacy.CollectAll(ctx,
			func(ctx context.Context, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[issuereports.Report], error) {
				return c.store.ListReportsByReporter(ctx, c.reader, scope, subject.ID, filter)
			})
		if collectErr != nil {
			return nil, platformerrors.Wrapf(collectErr, "collecting issue reports in scope %q", scope)
		}

		reports = append(reports, page...)
	}

	return dataprivacy.Fragment(len(reports) > 0, reports)
}

// Eraser destroys the issue reports a subject filed.
type Eraser struct {
	store   issuereports.Store
	resolve ScopeResolver
}

var _ dataprivacy.Eraser = (*Eraser)(nil)

// NewEraser builds the eraser over a store and a scope resolver. Both are
// required.
func NewEraser(store issuereports.Store, resolve ScopeResolver) (*Eraser, error) {
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
// reports and the rest of the subject's footprint commit or roll back together.
// A subject who filed nothing erases nothing and is not an error.
func (e *Eraser) Erase(
	ctx context.Context,
	q database.Tx,
	subject dataprivacy.Subject,
) (dataprivacy.ErasureOutcome, error) {
	if q == nil {
		return dataprivacy.ErasureOutcome{}, ErrNilExecutor
	}

	scopes, err := e.resolve(ctx, subject)
	if err != nil {
		return dataprivacy.ErasureOutcome{},
			platformerrors.Wrap(err, "resolving issue report scopes for subject")
	}

	var outcome dataprivacy.ErasureOutcome

	for _, scope := range scopes {
		deleted, deleteErr := e.store.DeleteReportsByReporter(ctx, q, scope, subject.ID)
		if deleteErr != nil {
			return dataprivacy.ErasureOutcome{},
				platformerrors.Wrapf(deleteErr, "erasing issue reports in scope %q", scope)
		}

		outcome.Deleted += deleted
	}

	return outcome, nil
}
