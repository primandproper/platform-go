/*
Package privacy is the audit log's contribution to a subject access request: a
dataprivacy.Collector that returns the entries in which the subject acted or was
acted on.

# Access only, and why

The right of access is not the right of erasure, and this package answers the
first one alone. An audit entry naming a person as its actor or its resource is
personal data about them, and a subject access request answered without those
entries is incomplete — so this collector exists. What it deliberately does not
ship is an Eraser, and the omission is a ruling rather than an unfinished job.

The audit log is a hash chain. Each entry's digest covers its own content —
the actor and resource columns included — and its predecessor's digest, per
scope, and audit.Reader.Verify reports a deleted or rewritten entry as
tampering. An Eraser here would have two possible implementations: one that
deletes or anonymizes the subject's entries in place, which breaks every chain
they sit in and turns the log's one guarantee into a false alarm, or one that
erases nothing, which would invite a deployment to register it and believe the
entries were gone.

The erasure that the chain does permit already ships, and it is not here.
dataprivacy/auditerasure deletes the scopes belonging to the subject whole —
entries and chain rows together, which leaves no gap in any surviving chain —
and reports every other entry naming the subject as retained, under
auditerasure.DefaultRetentionBasis: legitimate interest and legal obligation,
with the chain's integrity as the reason those entries cannot be removed. It is
registered under the same key this collector is, so a subject's "audit" section
and the "audit" line of their erasure outcome describe the same log.

Retained is not kept forever. The audit log's retention policy
(audit.PruneTarget, scheduled through auditcfg.NewRetentionPolicy) removes
entries once they are older than the configured window — audit.DefaultRetention,
seven years, unless a deployment sets its own — and states the basis it does so
under as audit.DefaultRetentionBasis. The retained entries go at the end of that
window, together with every other entry of the same age, and until then this
collector is how the subject sees them.

# Which entries are the subject's

An entry is exported when its actor is the subject or its resource id is the
subject's id. That is the predicate audit.Erasure.CountMentions counts, and it is
chosen for that reason: the number an erasure puts in front of the subject as
retained is a count of the entries this collector would have shown them, and an
export and an outcome that disagreed about which entries are "about" a person
would leave the subject unable to reconcile the two.

The resource match does not also narrow by resource type, though audit.Query
advises pairing a resource id with its type. Narrowing would need a type name
this package cannot know — a deployment's "user" resources may go by several —
and an export narrowed by a guessed one would be short with nothing to say so.
It would also stop agreeing with the count above, which matches the id alone. A
deployment whose resource ids can collide across types has the same question to
answer of its erasure outcome, and answers it once for both.

Two reads per scope — one by actor, one by resource — because audit.Query's
selectors are conjuncts and there is no "or". An entry in which the subject acted
on themselves matches both and is exported once. Each scope's entries come back
in chain order, by Seq, which is the only order the log itself vouches for.

# Somebody else's address

An entry whose resource is the subject and whose actor is somebody else names
that somebody, and the subject is entitled to see who acted on their data. What
the entry also carries is the address the actor arrived from, which is personal
data about the actor and tells the subject nothing about themselves. So Actor.IP
is cleared on every exported entry whose actor is not the subject, and kept on
the entries whose actor is.

That makes an exported entry one that no longer hashes to its own Hash. An
export is a copy handed to a person, not evidence to be re-verified — Verify
reads the table, never an artifact — and the chain fields are exported as
recorded so that an entry the subject asks about can be found again by them.

# Subjects are people, and entries are in scopes

Every read the audit Reader makes of a tenant's entries is confined to a scope,
and a subject access request may name none. So the collector takes a
[ScopeResolver] — the mapping from a subject to the scopes their entries may be
in — as every sibling adapter does, and for the reason dataprivacy.ScopeResolver
gives there is no default. A deployment that scopes audit entries per user
returns the user's own scope beside the tenants they belong to; a single-tenant
one returns tenancy.Global().

# Where the executor comes from

audit.Reader runs every read on an executor its caller supplies, and
dataprivacy.Collector.Collect is handed nothing — so [NewCollector] takes the
executor once, at construction, the shape comments/privacy took first. A Tx is
what a consumer passes when the export must see entries that transaction has
recorded and not yet committed.

# Why this is a package rather than methods on audit

audit would otherwise import dataprivacy, which imports operations, which imports
the queue and the scheduler — so a service that records audit entries and runs
no privacy pipeline would compile all of it. The seam goes here for the reason
dataprivacy/auditerasure exists outside audit.

# Observability

The collector instruments nothing of its own. Every read it makes is a List on
the audit Reader, which spans and logs each one, and a second span around a loop
of those would name the same work twice.
*/
package privacy

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"

	"github.com/primandproper/platform-go/v14/audit"
	"github.com/primandproper/platform-go/v14/dataprivacy"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// DefaultKey is the registry key this collector is normally registered under. It
// names the section an export's artifact carries it in.
//
// It is dataprivacy/auditerasure's key as well, deliberately: the collector and
// the eraser are the two halves of one domain shipped from two packages, and a
// registry keys its collectors and erasers separately, so the one name holds
// both.
const DefaultKey = "audit"

// The sentinels this package returns.
var (
	// ErrNilReader indicates a nil audit.Reader.
	ErrNilReader = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil audit privacy reader")

	// ErrNilScopeResolver indicates a nil ScopeResolver. It is required, and
	// refusing it here is what stops an export that quietly covers no scope from
	// being discovered by the subject.
	ErrNilScopeResolver = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil audit privacy scope resolver")

	// ErrNilExecutor indicates a nil executor. The collector's is supplied at
	// construction, because audit keeps no connection of its own to fall back
	// to and Collect has nowhere to take one.
	ErrNilExecutor = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil audit privacy query executor")

	// ErrUnscopedRequest indicates a request that names no scope, handed to
	// [RequestScope], which has nowhere else to get one.
	ErrUnscopedRequest = platformerrors.New("audit request names no scope")
)

// ScopeResolver names the scopes a subject's audit entries may be in.
//
// It is [dataprivacy.ScopeResolver] under this package's name, and the = is
// load-bearing rather than cosmetic: a defined type of its own would be
// assignable to the identical defined type in the sibling adapters only through
// a conversion. See dataprivacy.ScopeResolver for what a resolver answers, what
// returning none means, and why it has no default.
type ScopeResolver = dataprivacy.ScopeResolver

// RequestScope resolves the scope the request itself names, for a deployment
// where a privacy request always arrives scoped.
//
// A request that names none is [ErrUnscopedRequest] rather than the global
// scope — see dataprivacy.RequestScopeOr, which this is built from.
var RequestScope = dataprivacy.RequestScopeOr(ErrUnscopedRequest)

// FixedScopes resolves every subject to the same scopes, for a deployment whose
// tenancy is fixed — most often the single-tenant one, as
// FixedScopes(tenancy.Global()).
var FixedScopes = dataprivacy.FixedScopes

// Collector returns the audit entries in which a subject acted or was acted on.
//
// It is exported, and returned by NewCollector, so a caller can depend on what it
// built rather than on the dataprivacy.Collector seam.
type Collector struct {
	log     audit.Reader
	reader  database.SQLQueryExecutor
	resolve ScopeResolver
}

var _ dataprivacy.Collector = (*Collector)(nil)

// NewCollector builds the collector over an audit reader, the executor its reads
// run on, and a scope resolver. All three are required.
//
// The reader must be built at the prefix the audit tables were rendered with,
// which is audit.NewReader's own option rather than a second one here.
func NewCollector(
	log audit.Reader,
	reader database.SQLQueryExecutor,
	resolve ScopeResolver,
) (*Collector, error) {
	if log == nil {
		return nil, ErrNilReader
	}

	if reader == nil {
		return nil, ErrNilExecutor
	}

	if resolve == nil {
		return nil, ErrNilScopeResolver
	}

	return &Collector{log: log, reader: reader, resolve: resolve}, nil
}

// Collect implements dataprivacy.Collector.
//
// It pages each read to its end through dataprivacy.CollectAll, because an
// export that stopped after a page would be well-formed and missing everything
// past it. A subject who appears in no entry in any resolved scope reports the
// domain as holding nothing.
func (c *Collector) Collect(
	ctx context.Context,
	requestScope tenancy.Scope,
	subject dataprivacy.Subject,
) (json.RawMessage, error) {
	var collected []audit.Entry

	err := dataprivacy.ForEachOwner(ctx, c.resolve, requestScope, subject,
		func(ctx context.Context, scope tenancy.Scope) error {
			entries, collectErr := c.collectScope(ctx, scope, subject.ID)
			if collectErr != nil {
				return platformerrors.Wrapf(collectErr, "collecting audit entries in scope %q", scope)
			}

			collected = append(collected, entries...)

			return nil
		})
	if err != nil {
		return nil, err
	}

	return dataprivacy.Fragment(len(collected) > 0, collected)
}

// collectScope reads one scope's entries naming the subject, as actor and as
// resource, and returns each entry once, in chain order.
func (c *Collector) collectScope(ctx context.Context, scope tenancy.Scope, subjectID string) ([]audit.Entry, error) {
	acted, err := c.drain(ctx, &audit.Query{Scope: &scope, ActorID: subjectID})
	if err != nil {
		return nil, platformerrors.Wrap(err, "reading the entries the subject acted in")
	}

	actedOn, err := c.drain(ctx, &audit.Query{Scope: &scope, ResourceID: subjectID})
	if err != nil {
		return nil, platformerrors.Wrap(err, "reading the entries the subject was acted on in")
	}

	// Seq is unique within a scope, so after the sort an entry both reads
	// returned sits beside itself and compacts to one.
	entries := slices.Concat(acted, actedOn)

	slices.SortFunc(entries, func(a, b audit.Entry) int {
		return cmp.Compare(a.Seq, b.Seq)
	})

	entries = slices.CompactFunc(entries, func(a, b audit.Entry) bool {
		return a.ID == b.ID
	})

	for i := range entries {
		if entries[i].Actor.ID != subjectID {
			entries[i].Actor.IP = ""
		}
	}

	return entries, nil
}

// drain pages one query to its end.
func (c *Collector) drain(ctx context.Context, query *audit.Query) ([]audit.Entry, error) {
	return dataprivacy.CollectAll(ctx,
		func(ctx context.Context, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[audit.Entry], error) {
			return c.log.List(ctx, c.reader, query, filter)
		})
}
