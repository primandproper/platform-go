/*
Package privacy is the registry table's contribution to a subject access
request: a dataprivacy.Collector that returns what somebody uploaded, and a
dataprivacy.Eraser that withdraws it and says what is left.

# Why this is a package rather than two methods on the store

mediaregistry would otherwise import dataprivacy, which imports operations, which
imports the queue and the scheduler — so a service that accepts avatars and runs
no privacy pipeline would compile all of it. The seam goes here for the same
reason dataprivacy/auditerasure exists, and it costs one constructor argument.

# Why the erasure archives, and what it reports retained

This is the one eraser in the module that keeps its rows on purpose, and the
reason is the shape of the package beneath it rather than a softer reading of
the right to erasure.

Nothing in mediaregistry opens, reads or removes an object. A row says where the
bytes are and who put them there; the bytes themselves are in a bucket this
package deliberately never touches, and [mediaregistry.Store.ArchiveObject] hands
its row back precisely because "the row is the only record of the key those
surviving bytes are at". So a delete here would destroy that record and leave the
photograph exactly where it is — the deployment would have erased the map and
kept the territory, and would then have no way to find what it still has to
remove. That is worse than doing nothing, because it looks like success.

What this eraser does instead is hide every row the subject owns and report the
objects as retained, naming the count and where the bytes are. The count is the
honest number: [mediaregistry.Store.ArchiveObjectsForOwner] skips rows already
archived, so an erasure retried after a rollback elsewhere reports what it
actually hid.

Deleted is therefore zero, and that is not an omission. Nothing was destroyed.
Anonymized is zero too, for a stricter reason: an archived row still carries the
owner, the key and the filename the subject chose, so calling it anonymized would
be a claim about the row that reading it disproves.

# What a deployment does to finish the job

The archived rows are the work list. A deployment that wants the bytes gone walks
them for their keys — [mediaregistry.Store.ListObjectsByOwner] with
filtering.QueryFilter.IncludeArchived set is the read that reaches them — removes
each object through the uploads.UploadManager it stored them with, and then, if
it wants the metadata gone as well, deletes the rows with its own statement.

That sequence is deliberately not a method here, and not because it would be hard
to write. A bucket delete is not transactional: it cannot be rolled back with the
request's transaction, so a method that ran one inside dataprivacy.Eraser.Erase
would destroy somebody's bytes on behalf of an erasure that then failed to
commit. The two halves happen at different times, under different failure models,
and this package owns the half it can actually promise.

# The owner, not the attachment

Both halves key on [mediaregistry.Object.OwnerID], which is what this package
documents as the principal — a user, a service account, an API key.

They deliberately do not key on [mediaregistry.Subject]. That pair is whatever a
consumer hung an object off, in the consumer's own words — "recipe", "invoice" —
and a principal is not one of the things that vocabulary is for. An object
attached to a person's row is also owned by somebody, so the owner axis reaches
it; a deployment that has genuinely filed a person's uploads under a subject type
and no owner has a resolver's problem rather than this package's, and writes a
collector of its own.

# Scopes

Every read and write in mediaregistry is scoped, and a subject access request may
arrive without a scope — the request's confinement names nobody for the plain
"give me my data". So both halves take a [ScopeResolver]: the mapping from a
subject to the scopes their uploads may be in, which is a question about the
consumer's tenancy model rather than about this table.

It is a constructor argument rather than an option with a default, because there
is no default that is right twice. A deployment with one tenant wants
[FixedScopes] over tenancy.Global(); one whose requests always name their scope
wants [RequestScope]; one that resolves a person to their accounts has to ask its
own directory. A resolver that silently answered "the global scope" for the third
of those would export nothing and archive nothing, and report success for both.

# Executors

Every read and write in mediaregistry runs on an executor its caller supplies,
and the two halves get theirs from different places. dataprivacy.Eraser.Erase is
handed the request's database.Tx, so [Eraser] passes that straight down and the
rows commit with the rest of the subject's footprint.
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
	"fmt"

	"github.com/primandproper/platform-go/v14/dataprivacy"
	"github.com/primandproper/platform-go/v14/mediaregistry"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// DefaultKey is the registry key these are normally registered under. It names
// the section an export's artifact carries them in, and the prefix of any
// entries an outcome reports.
const DefaultKey = "media_registry"

// heldNoun names what this domain holds, for the message dataprivacy's fan-out
// helpers wrap a failed read in. The registry key names the section; this names
// the table, which is what somebody reading the log wants.
const heldNoun = "uploaded objects"

// RetainedObjects is the key an erasure outcome reports the surviving bytes
// under. It is named here so a consumer reading a request record back looks for
// a constant rather than for a string somebody typed twice.
const RetainedObjects = "objects"

// The sentinels this package returns.
var (
	// ErrNilStore indicates a nil mediaregistry.Store.
	ErrNilStore = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil media registry store")

	// ErrNilScopeResolver indicates a nil ScopeResolver. It is required, and
	// refusing it here is what stops an export that quietly covers no scope from
	// being discovered by the subject.
	ErrNilScopeResolver = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil media registry privacy scope resolver")

	// ErrNilExecutor indicates a nil executor. Both halves run on one somebody
	// else supplies — the collector's at construction, the eraser's per request —
	// because mediaregistry keeps no connection of its own to fall back to.
	ErrNilExecutor = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil media registry privacy query executor")

	// ErrUnscopedRequest indicates a request that names no scope, handed to
	// [RequestScope], which has nowhere else to get one.
	ErrUnscopedRequest = platformerrors.New("media registry request names no scope")
)

// ScopeResolver names the scopes a subject's uploads may be in.
//
// It is [dataprivacy.ScopeResolver] under this package's name, and the = is
// load-bearing rather than cosmetic: a defined type of its own would be
// assignable to the identical defined type in the sibling adapters only through
// a conversion, so a deployment with one resolver function would write one
// conversion per domain. See dataprivacy.ScopeResolver for what a resolver
// answers, what returning none means, and why it has no default.
type ScopeResolver = dataprivacy.ScopeResolver

// RequestScope resolves the scope the request itself names, for a deployment
// where a privacy request always arrives scoped.
//
// A request that names none is [ErrUnscopedRequest] rather than the global
// scope — see dataprivacy.RequestScopeOr, which this is built from, for why the
// difference is not recoverable later.
var RequestScope = dataprivacy.RequestScopeOr(ErrUnscopedRequest)

// FixedScopes resolves every subject to the same scopes, for a deployment whose
// tenancy is fixed — most often the single-tenant one, as
// FixedScopes(tenancy.Global()).
var FixedScopes = dataprivacy.FixedScopes

// Collector returns the objects a subject uploaded.
type Collector struct {
	store   mediaregistry.Store
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
	store mediaregistry.Store,
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
// It pages each scope's objects to the end through dataprivacy.CollectAll,
// because a collector that read one page and stopped would return a truncated
// subject access request — well-formed, present, and missing everything past the
// first page.
//
// It asks for archived rows as well as live ones, on a copy of the filter it is
// handed, and here that is load-bearing twice over. An archived row is a file
// the subject uploaded and whose bytes are still in the bucket, so an export
// that left it out would be an export omitting data the deployment still holds —
// and after this package's own eraser has run, every row it hid is archived.
func (c *Collector) Collect(
	ctx context.Context,
	requestScope tenancy.Scope,
	subject dataprivacy.Subject,
) (json.RawMessage, error) {
	uploaded, err := dataprivacy.CollectByScope(ctx, c.resolve, requestScope, subject, heldNoun,
		func(
			ctx context.Context,
			scope tenancy.Scope,
			filter *filtering.QueryFilter,
		) (*filtering.QueryFilteredResult[mediaregistry.Object], error) {
			everything := *filter
			everything.IncludeArchived = new(true)

			return c.store.ListObjectsByOwner(ctx, c.reader, scope, subject.ID, &everything)
		})
	if err != nil {
		return nil, err
	}

	return dataprivacy.Fragment(len(uploaded) > 0, uploaded)
}

// Eraser withdraws the objects a subject uploaded and reports the bytes that
// outlive the rows.
type Eraser struct {
	store   mediaregistry.Store
	resolve ScopeResolver
}

var _ dataprivacy.Eraser = (*Eraser)(nil)

// NewEraser builds the eraser over a store and a scope resolver. Both are
// required.
func NewEraser(store mediaregistry.Store, resolve ScopeResolver) (*Eraser, error) {
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
// rows and the rest of the subject's footprint commit or roll back together. A
// subject who uploaded nothing archives nothing and is not an error.
//
// It reports nothing deleted and nothing anonymized, and everything it touched
// retained. See the package documentation: the bytes are not this package's to
// remove, the archived row is the only record of where they are, and a count of
// rows destroyed would be a claim about a file that is still in the bucket.
func (e *Eraser) Erase(
	ctx context.Context,
	tx database.Tx,
	requestScope tenancy.Scope,
	subject dataprivacy.Subject,
) (dataprivacy.ErasureOutcome, error) {
	if tx == nil {
		return dataprivacy.ErasureOutcome{}, ErrNilExecutor
	}

	var archived int64

	// dataprivacy.ForEachOwner rather than EraseByScope, for two reasons and
	// both of them are this package's ruling. The verb is "archiving", not
	// "erasing". And the count goes into a local rather than into the outcome:
	// Deleted and Anonymized stay at zero deliberately, because a withdrawn row
	// whose bytes survive is neither, and a helper that summed it into one of
	// them would report a deletion this package did not perform.
	err := dataprivacy.ForEachOwner(ctx, e.resolve, requestScope, subject,
		func(ctx context.Context, scope tenancy.Scope) error {
			hidden, archiveErr := e.store.ArchiveObjectsForOwner(ctx, tx, scope, subject.ID)
			if archiveErr != nil {
				return platformerrors.Wrapf(archiveErr, "archiving uploaded objects in scope %q", scope)
			}

			archived += hidden

			return nil
		})
	if err != nil {
		return dataprivacy.ErasureOutcome{}, err
	}

	var outcome dataprivacy.ErasureOutcome

	// Reported only when something was withdrawn: a subject who uploaded
	// nothing has no bytes in the bucket, and a retention entry saying "0 of
	// them" would be a line in front of a regulator about nothing.
	if archived > 0 {
		outcome.Retained = map[string]string{RetainedObjects: retainedObjects(archived)}
	}

	return outcome, nil
}

// retainedObjects is the sentence the request record carries for what an erasure
// kept, and why. It names the number because the record is per request, and the
// basis because the record is what answers the question.
func retainedObjects(n int64) string {
	return fmt.Sprintf(
		"%d object(s): the registry rows were withdrawn, and the bytes remain in the deployment's bucket — "+
			"mediaregistry never opens the byte path, so removing them is the deployment's own step, "+
			"against the keys the withdrawn rows still carry",
		n)
}
