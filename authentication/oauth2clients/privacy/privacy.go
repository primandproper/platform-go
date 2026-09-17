/*
Package privacy is the client registry's contribution to a subject access
request: a dataprivacy.Collector that returns the applications somebody
registered, and a dataprivacy.Eraser that destroys them.

# Why this is a package rather than two methods on the store

oauth2clients would otherwise import dataprivacy, which imports operations, which
imports the queue and the scheduler — so a service with an API credentials page
and no privacy pipeline would compile all of it. The seam goes here for the same
reason dataprivacy/auditerasure exists, and it costs one constructor argument.

# Why the erasure deletes where the store archives

[oauth2clients.Store.ArchiveClient] withdraws a registration and keeps the row,
because a client_id names tokens that may still be live and the row is what the
authorization server reads to refuse them. That is right for a withdrawal and
wrong for an erasure: the row it keeps carries belongs_to_user, the name somebody
chose and the description they wrote, which between them are the whole of what
this table says about a person. An erasure built on the archive would be an
erasure that erased nothing — notifications/privacy's reading of its own soft
delete, reached again here.

So the store grew [oauth2clients.Store.DeleteClientsForOwner] rather than this
package reaching for the write that was already there. It also reaches
registrations somebody withdrew years ago, which a delete written like every
other statement over that table would have skipped — and those are exactly the
rows nobody is looking at.

Deleting does not weaken the refusal it takes away. A token naming a client_id no
row resolves is refused by the absence, which is stricter than the withdrawn
row's refusal rather than looser. What is lost is the ability to tell a withdrawn
client from one that never existed, and for a subject who asked to be forgotten
"never existed" is the answer they asked for.

Nothing is retained, and so nothing is reported as retained. A deployment whose
jurisdiction requires a record of what was registered and by whom registers no
eraser — the rows then survive an erasure, which is a decision somebody has made
rather than one this package made for them.

# The export is redacted, and one field is why

The fragment carries [oauth2clients.Client] as the store holds it, through
[oauth2clients.Client.Redacted], which clears SecretHash. A subject access
request is a right to what the table holds about the person asking; it is not a
credential dump, and the digest is the one column here that would make it one.

Everything else goes: the name and description the subject typed, the redirect
addresses their application receives codes at, the scopes it may ask for, and the
client_id itself. That last one looks like a thing to withhold and is not — it is
a public identifier its owner already has, sent on every authorization request
they make, and an export that withheld it would be answering "what may we show
you" rather than "what do we have".

# Administered registrations belong to nobody

belongs_to_user is the empty string on a registration the deployment or the
tenant keeps on its own behalf. Neither half here reaches one: the store refuses
an empty owner on both the read and the write, so a subject whose id is somehow
empty gets a refusal rather than the deployment's own credentials exported to
them or destroyed on their account.

# Scopes

Every consumer read and write in oauth2clients is scoped, and a subject access
request may arrive without a scope — the request's confinement names nobody for
the plain "give me my data". So both halves take a [ScopeResolver]: the mapping
from a subject to the registries their clients may be in, which is a question
about the consumer's tenancy model rather than about this table.

It is a constructor argument rather than an option with a default, because there
is no default that is right twice. A deployment with one tenant wants
[FixedScopes] over tenancy.Global(); one whose requests always name their scope
wants [RequestScope]; one that resolves a person to their accounts has to ask its
own directory. A resolver that silently answered "the global scope" for the third
of those would export nothing and erase nothing, and report success for both.

# Executors

Every read and write in oauth2clients runs on an executor its caller supplies,
and the two halves get theirs from different places. dataprivacy.Eraser.Erase is
handed the request's database.Tx, so [Eraser] passes that straight down and the
credentials stop resolving at the same moment the rest of the subject's footprint
goes. dataprivacy.Collector.Collect is handed nothing, because an export is a
read and there is no transaction for it to be part of — so [NewCollector] takes
the executor once, at construction, and every collection runs on it.

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

	"github.com/primandproper/platform-go/v14/authentication/oauth2clients"
	"github.com/primandproper/platform-go/v14/dataprivacy"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// DefaultKey is the registry key these are normally registered under. It names
// the section an export's artifact carries them in, and the prefix of any
// entries an outcome reports.
const DefaultKey = "oauth2_clients"

// The sentinels this package returns.
var (
	// ErrNilStore indicates a nil oauth2clients.Store.
	ErrNilStore = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil oauth2 client registry store")

	// ErrNilScopeResolver indicates a nil ScopeResolver. It is required, and
	// refusing it here is what stops an export that quietly covers no registry
	// from being discovered by the subject.
	ErrNilScopeResolver = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil oauth2 clients privacy scope resolver")

	// ErrNilExecutor indicates a nil executor. Both halves run on one somebody
	// else supplies — the collector's at construction, the eraser's per request —
	// because oauth2clients keeps no connection of its own to fall back to.
	ErrNilExecutor = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil oauth2 clients privacy query executor")

	// ErrUnscopedRequest indicates a request that names no scope, handed to
	// [RequestScope], which has nowhere else to get one.
	ErrUnscopedRequest = platformerrors.New("oauth2 clients request names no scope")
)

// ScopeResolver names the registries a subject's clients may be in.
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

// Collector returns the applications a subject registered.
type Collector struct {
	store   oauth2clients.Store
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
	store oauth2clients.Store,
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
// It pages each registry's registrations to the end through
// dataprivacy.CollectAll, because a collector that read one page and stopped
// would return a truncated subject access request — well-formed, present, and
// missing everything past the first page.
//
// It asks for withdrawn registrations as well as live ones, on a copy of the
// filter it is handed. A withdrawn client is still something the subject
// registered, and the row is still there — see [oauth2clients.Store.ArchiveClient]
// on why it stays — so an export that showed only what had not been withdrawn
// would answer a different question than the one the right of access asks.
//
// Every row goes through [oauth2clients.Client.Redacted], so the secret digest
// is not in the artifact.
func (c *Collector) Collect(
	ctx context.Context,
	requestScope tenancy.Scope,
	subject dataprivacy.Subject,
) (json.RawMessage, error) {
	var registered []oauth2clients.Client

	// dataprivacy.ForEachOwner rather than CollectByScope, for the noun. A
	// client is registered in a registry, and this package says so in the one
	// message an operator reads; the helper that concatenates pages would have
	// called it a scope. The redaction is the other half of why: what reaches
	// the artifact is never the row the store handed back.
	err := dataprivacy.ForEachOwner(ctx, c.resolve, requestScope, subject,
		func(ctx context.Context, scope tenancy.Scope) error {
			page, collectErr := dataprivacy.CollectAll(ctx,
				func(
					ctx context.Context,
					filter *filtering.QueryFilter,
				) (*filtering.QueryFilteredResult[oauth2clients.Client], error) {
					everything := *filter
					everything.IncludeArchived = new(true)

					return c.store.ListClientsForOwner(ctx, c.reader, scope, subject.ID, &everything)
				})
			if collectErr != nil {
				return platformerrors.Wrapf(collectErr, "collecting oauth2 clients in registry %q", scope)
			}

			for i := range page {
				registered = append(registered, *page[i].Redacted())
			}

			return nil
		})
	if err != nil {
		return nil, err
	}

	return dataprivacy.Fragment(len(registered) > 0, registered)
}

// Eraser destroys the applications a subject registered.
type Eraser struct {
	store   oauth2clients.Store
	resolve ScopeResolver
}

var _ dataprivacy.Eraser = (*Eraser)(nil)

// NewEraser builds the eraser over a store and a scope resolver. Both are
// required.
func NewEraser(store oauth2clients.Store, resolve ScopeResolver) (*Eraser, error) {
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
// registrations and the rest of the subject's footprint commit or roll back
// together. A subject who registered nothing erases nothing and is not an error.
//
// What it does not do is revoke the tokens those clients hold. Nothing has to:
// a token names a client_id, and the row that resolved it is gone, so the
// authorization server refuses it the next time it is presented. Deleting the
// row is what stops the token.
func (e *Eraser) Erase(
	ctx context.Context,
	tx database.Tx,
	requestScope tenancy.Scope,
	subject dataprivacy.Subject,
) (dataprivacy.ErasureOutcome, error) {
	if tx == nil {
		return dataprivacy.ErasureOutcome{}, ErrNilExecutor
	}

	var outcome dataprivacy.ErasureOutcome

	// dataprivacy.ForEachOwner rather than EraseByScope, for the noun the
	// collector gives above: these rows live in a registry, and the helper that
	// sums outcomes would have called it a scope.
	err := dataprivacy.ForEachOwner(ctx, e.resolve, requestScope, subject,
		func(ctx context.Context, scope tenancy.Scope) error {
			deleted, deleteErr := e.store.DeleteClientsForOwner(ctx, tx, scope, subject.ID)
			if deleteErr != nil {
				return platformerrors.Wrapf(deleteErr, "erasing oauth2 clients in registry %q", scope)
			}

			outcome.Deleted += deleted

			return nil
		})
	if err != nil {
		return dataprivacy.ErasureOutcome{}, err
	}

	return outcome, nil
}
