/*
Package privacy is the audit log's contribution to a subject access request: a
dataprivacy.Collector that returns the entries in which the subject acted, was
acted on, or acted as somebody else.

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

An entry is exported when its actor is the subject, its resource id is the
subject's id, or its impersonator is the subject. That is the predicate
audit.Erasure.CountMentions counts, and it is
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

Two reads per scope — by actor and by resource — and one by impersonator
across every scope, because audit.Query's selectors are conjuncts and there is
no "or". An entry two reads return is exported once. Each scope's entries come
back in chain order, by Seq, which is the only order the log itself vouches for;
impersonated entries in a scope the resolver did not name follow the resolved
scopes', grouped by scope and in chain order within each.

# Impersonated entries

An entry an operator recorded while acting as somebody else is filed under the
somebody — Actor.ID is the subject whose identity the request carried, and
Actor.Impersonator is the operator. Both people are owed it, and it is exported
to each as what it was rather than as either one's own act.

The subject gets it through the actor read, with the impersonator intact: an
export that showed the act and left out that somebody else performed it under
their name would be the one falsehood the second slot exists to stop telling.
The operator gets it through the impersonator read. Without that read their
export would be missing everything they did while acting as a customer, which
is exactly the part of their record they might have reason to ask about.

That read is the one this collector makes without a scope, and deliberately.
An impersonated entry is filed in the customer's scope, and the operator's
[ScopeResolver] names the operator's scopes — for a deployment whose staff live
in a directory of their own, none of the customers'. A read confined to the
resolved scopes would find nothing, so this one spans every scope and is
confined instead by the impersonator column matching the subject's own ID. It
is subject-access machinery reading one person's acts wherever they were filed,
not a consumer read that omits the scope. It is also the predicate
audit.Erasure.CountMentions already counts without one.

The address follows the keyboard. Actor.IP on an impersonated entry is the
operator's, because the request arrived from the operator, so it is kept in
the operator's export and cleared in the subject's — see below.

# Somebody else's address, and somebody else's values

An entry whose resource is the subject and whose actor is somebody else names
that somebody, and the subject is entitled to see who acted on their data. What
the entry also carries is the address the actor arrived from, which is personal
data about the actor and tells the subject nothing about themselves. So Actor.IP
is cleared on every exported entry whose request the subject did not make — one
whose actor is somebody else, or whose actor is the subject and whose
impersonator is not — and kept on the entries they made themselves, including
the ones they made as somebody else.

The same reasoning runs the other way for what the subject did. An entry whose
actor is the subject and whose resource is not records the subject's act, and
the act is theirs to see — but its Changes are the resource's before and after,
and when the subject is an administrator who changed a colleague's email those
values are the colleague's data. The collector cannot tell a resource that is a
person from one that is not, so Changes on every exported entry whose resource
is not the subject keeps its field names and loses its values: the export says
the subject changed "email", and does not say from what to what. An entry whose
resource is the subject keeps its values whoever the actor was, because those
are the subject's own data. Metadata is exported as recorded; it is the
context the actor gave for the event, not a record of the resource.

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

A deployment whose recorder files by subject — recordingcfg.FileBySubject —
has the resolver already: [MembershipScopeResolver] reads the chains that rule
put the subject's entries on, which are their own, their accounts', and every
chain holding an entry they acted in. It is reached through
recordingcfg.Config.ScopeResolvers, which hands it out beside the eraser's for
the same rule, so the two cannot be chosen apart. Under
recordingcfg.FileByWrite the entries are on whatever scopes the writes ran in,
which this module cannot enumerate, so that rule has no shipped resolver.

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

	"github.com/primandproper/platform-go/v15/audit"
	"github.com/primandproper/platform-go/v15/dataprivacy"

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

// Collector returns the audit entries in which a subject acted, was acted on, or
// acted as somebody else.
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
// past it. A subject who appears in no entry in any resolved scope, and
// impersonated nobody anywhere, reports the domain as holding nothing, and a
// resolver that names no scope for them is read as saying so: nothing is read
// at all, the impersonator read included.
func (c *Collector) Collect(
	ctx context.Context,
	requestScope tenancy.Scope,
	subject dataprivacy.Subject,
) (json.RawMessage, error) {
	var groups []scopeEntries

	err := dataprivacy.ForEachOwner(ctx, c.resolve, requestScope, subject,
		func(ctx context.Context, scope tenancy.Scope) error {
			entries, collectErr := c.collectScope(ctx, scope, subject.ID)
			if collectErr != nil {
				return platformerrors.Wrapf(collectErr, "collecting audit entries in scope %q", scope)
			}

			groups = append(groups, scopeEntries{scope: scope, entries: entries})

			return nil
		})
	if err != nil {
		return nil, err
	}

	if len(groups) == 0 {
		return dataprivacy.Fragment(false, []audit.Entry(nil))
	}

	actedAs, err := c.actedAs(ctx, subject.ID)
	if err != nil {
		return nil, err
	}

	// Each impersonated entry joins its own scope's chain where the resolver
	// named that scope. What is left — a customer's scope, for an operator whose
	// own is a staff directory — follows, a scope at a time and in chain order
	// within each.
	var (
		collected []audit.Entry
		elsewhere []audit.Entry
	)

	joined := make(map[string]bool, len(groups))
	for i := range groups {
		joined[groups[i].scope.Owner()] = true
	}

	for i := range actedAs {
		if !joined[actedAs[i].Scope.Owner()] {
			elsewhere = append(elsewhere, actedAs[i])
		}
	}

	for g := range groups {
		entries := groups[g].entries

		for i := range actedAs {
			if actedAs[i].Scope.Owner() == groups[g].scope.Owner() {
				entries = append(entries, actedAs[i])
			}
		}

		collected = append(collected, redact(inChainOrder(entries), subject.ID)...)
	}

	slices.SortFunc(elsewhere, func(a, b audit.Entry) int {
		return cmp.Or(cmp.Compare(a.Scope.Owner(), b.Scope.Owner()), cmp.Compare(a.Seq, b.Seq))
	})

	collected = append(collected, redact(elsewhere, subject.ID)...)

	return dataprivacy.Fragment(len(collected) > 0, collected)
}

// scopeEntries is one resolved scope and the entries read in it by actor and
// by resource.
type scopeEntries struct {
	scope   tenancy.Scope
	entries []audit.Entry
}

// actedAs reads every entry the subject recorded while acting as somebody
// else, in every scope.
//
// It is the one read here that names no scope, and it is not a consumer read
// that forgot to: it is audit.Reader.ListAcrossScopes, spelled apart. It is
// subject-access machinery confined by the subject's own ID: an impersonated entry is filed in the scope of the person impersonated,
// which is by construction a scope the operator's resolver has no reason to
// name — an operator who is staff in a directory of their own belongs to none
// of their customers' — and a read confined to the resolved scopes would hand
// the operator an export missing exactly the acts they did under somebody
// else's name. What it can return is bounded by the impersonator column
// matching the subject's ID, which is the predicate audit.Erasure.CountMentions
// already counts across every scope.
func (c *Collector) actedAs(ctx context.Context, subjectID string) ([]audit.Entry, error) {
	entries, err := c.drain(ctx, func(ctx context.Context, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[audit.Entry], error) {
		return c.log.ListAcrossScopes(ctx, c.reader, &audit.Query{ImpersonatorID: subjectID}, filter)
	})
	if err != nil {
		return nil, platformerrors.Wrap(err, "reading the entries the subject acted in as somebody else")
	}

	return entries, nil
}

// collectScope reads one scope's entries naming the subject, as actor and as
// resource.
func (c *Collector) collectScope(ctx context.Context, scope tenancy.Scope, subjectID string) ([]audit.Entry, error) {
	acted, err := c.drain(ctx, c.inScope(scope, &audit.Query{ActorID: subjectID}))
	if err != nil {
		return nil, platformerrors.Wrap(err, "reading the entries the subject acted in")
	}

	actedOn, err := c.drain(ctx, c.inScope(scope, &audit.Query{ResourceID: subjectID}))
	if err != nil {
		return nil, platformerrors.Wrap(err, "reading the entries the subject was acted on in")
	}

	return slices.Concat(acted, actedOn), nil
}

// inChainOrder sorts one scope's entries by Seq and drops the duplicates two
// reads returned. Seq is unique within a scope, so after the sort an entry two
// reads returned sits beside itself and compacts to one.
func inChainOrder(entries []audit.Entry) []audit.Entry {
	slices.SortFunc(entries, func(a, b audit.Entry) int {
		return cmp.Compare(a.Seq, b.Seq)
	})

	return slices.CompactFunc(entries, func(a, b audit.Entry) bool {
		return a.ID == b.ID
	})
}

// redact clears what an exported entry carries about somebody other than the
// subject, in place: the address of a request the subject did not make, and
// the values of a change to a resource that is not theirs.
func redact(entries []audit.Entry, subjectID string) []audit.Entry {
	for i := range entries {
		if keyboard(entries[i].Actor) != subjectID {
			entries[i].Actor.IP = ""
		}

		if entries[i].ResourceID != subjectID {
			entries[i].Changes = fieldNamesOnly(entries[i].Changes)
		}
	}

	return entries
}

// keyboard is who the request an entry records arrived from: the impersonator
// when there was one, and the actor otherwise. It is whose address Actor.IP is.
func keyboard(actor audit.Actor) string {
	if actor.Impersonator != "" {
		return actor.Impersonator
	}

	return actor.ID
}

// fieldNamesOnly keeps which fields an event changed and drops what they
// changed from and to. It builds a new map rather than clearing the one it was
// handed, which belongs to whatever the reader returned.
func fieldNamesOnly(changes map[string]audit.Change) map[string]audit.Change {
	if changes == nil {
		return nil
	}

	names := make(map[string]audit.Change, len(changes))
	for field := range changes {
		names[field] = audit.Change{}
	}

	return names
}

// page is one paged read of the log, reduced to the filter it is asked with.
type page func(ctx context.Context, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[audit.Entry], error)

// inScope is the paged read of one scope's entries matching query.
func (c *Collector) inScope(scope tenancy.Scope, query *audit.Query) page {
	return func(ctx context.Context, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[audit.Entry], error) {
		return c.log.List(ctx, c.reader, scope, query, filter)
	}
}

// drain pages one read to its end.
func (c *Collector) drain(ctx context.Context, read page) ([]audit.Entry, error) {
	return dataprivacy.CollectAll(ctx, read)
}
