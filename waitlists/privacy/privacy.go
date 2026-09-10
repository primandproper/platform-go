/*
Package privacy is the waitlist signups table's contribution to a subject access
request: a dataprivacy.Collector that returns the signups somebody holds, and a
dataprivacy.Eraser that withdraws them.

# Why this is a package rather than two methods on the store

waitlists would otherwise import dataprivacy, which imports operations, which
imports the queue and the scheduler — so a service with a signup form and no
privacy pipeline would compile all of it. The seam goes here for the same reason
comments/privacy and dataprivacy/auditerasure exist, and it costs one
constructor argument.

# Why the erasure withdraws rather than deletes

A signup is the one row in this module whose erasure has to remember the
person it erased. Deleting it frees the unique key on the contact, so somebody
erased at their own request could be re-subscribed by the next form submission
from their address — which is the opposite of what they asked for. So the
eraser runs [waitlists.SignupStore.WithdrawSignupsForSubject], which leaves
each row as [waitlists.SignupStore.Withdraw] leaves one: the contact, the notes
and the subject reference blank, the status withdrawn, and the contact digest
kept.

That digest is what is retained, and the outcome says so under [RetainedDigests]
rather than leaving it to be found. It is a one-way hash of the normalized
address, not reversible to the address, and it holds nothing but the fact that
whoever owned that address asked to be left alone — see the waitlists package
documentation for what it protects and what it does not. The rows are reported
as anonymized rather than deleted, because that is what they are.

# What the erasure reaches

Every signup naming the subject in each resolved scope, archived signups
included. An archived signup is hidden from the ordinary reads and still holds
the address it was made with, so an erasure that skipped it would be reporting
completion over a row still naming somebody. The collector reaches them for the
same reason: an export that omitted them would be missing data the table holds.

A signup that named no subject — a pre-launch list has an address and nothing
else — is not reachable by either half, because nothing ties it to the person
asking. That is a property of the data rather than of this package: a
deployment whose signups are anonymous has nothing to export or erase by
subject, and the person's own unsubscribe link is [waitlists.SignupStore.Withdraw].

# Subjects

A dataprivacy.Subject and a waitlists.Subject say the same two things — a kind
of principal and an id within it — and their vocabularies agree: both spell a
person "user" and an organization "account". The mapping is therefore a
conversion rather than a resolver, and the test suite pins the agreement, since
a deployment whose two vocabularies drifted apart would erase nothing and report
success.

# Scopes

Every read and write in waitlists is scoped, and a subject access request may
arrive without a scope — the request's confinement names nobody for the plain
"give me my data". So both halves take a [ScopeResolver]: the mapping from a subject
to the scopes their signups may be in, which is a question about the consumer's
tenancy model rather than about this table.

It is a constructor argument rather than an option with a default, because there
is no default that is right twice. A deployment with one catalog wants
[FixedScopes] over tenancy.Global(); one whose requests always name their scope
wants [RequestScope]; one that resolves a person to their accounts has to ask its
own directory. A resolver that silently answered "the global scope" for the third
of those would export nothing and erase nothing, and report success for both.

# Executors

Every read and write in waitlists runs on an executor its caller supplies, and
the two halves get theirs from different places. dataprivacy.Eraser.Erase is
handed the request's database.Tx, so [Eraser] passes that straight down and a
subject's signups are withdrawn with the rest of their footprint.
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
	"github.com/primandproper/platform-go/v14/waitlists"

	"github.com/primandproper/primitives-go/database"
	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/filtering"
	"github.com/primandproper/primitives-go/tenancy"
)

// DefaultKey is the registry key these are normally registered under. It names
// the section an export's artifact carries them in, and the prefix of any
// entries an outcome reports.
const DefaultKey = "waitlists"

// RetainedDigests is the key under which an erasure reports what it kept: one
// contact digest per withdrawn signup, so that the suppression outlives the
// erasure on every list the person was on.
const RetainedDigests = "contact_digests"

// The sentinels this package returns.
var (
	// ErrNilStore indicates a nil waitlists.SignupStore.
	ErrNilStore = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil waitlist signup store")

	// ErrNilScopeResolver indicates a nil ScopeResolver. It is required, and
	// refusing it here is what stops an export that quietly covers no scope from
	// being discovered by the subject.
	ErrNilScopeResolver = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil waitlist privacy scope resolver")

	// ErrNilExecutor indicates a nil executor. Both halves run on one somebody
	// else supplies — the collector's at construction, the eraser's per request —
	// because waitlists keeps no connection of its own to fall back to.
	ErrNilExecutor = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil waitlist privacy query executor")

	// ErrUnscopedRequest indicates a request that names no scope, handed to
	// [RequestScope], which has nowhere else to get one.
	ErrUnscopedRequest = platformerrors.New("waitlists request names no scope")
)

// ScopeResolver names the scopes a subject's signups may be in.
//
// Returning no scopes is legitimate and means the subject has nothing here: the
// collector reports the domain as holding nothing, and the eraser withdraws
// nothing. Returning too many is how one subject's erasure reaches another
// tenant's signups, so it is worth being exact.
// requestScope is the confinement the privacy request named, which the
// fulfiller hands over beside the subject. The zero Scope is the request that
// named none — a plain "give me my data" — and what a resolver makes of that is
// the whole of the decision this seam exists for.
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
// declined to name. The difference is not recoverable later: an
// export that quietly covered only the global scope would be well-formed, would
// have a section, and would be missing every signup the subject actually holds.
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

// subjectOf renders a privacy request's subject as the store keys signups on.
// The two vocabularies agree — see the package documentation — so this is a
// conversion and not a lookup.
func subjectOf(subject dataprivacy.Subject) waitlists.Subject {
	return waitlists.Subject{Type: waitlists.SubjectType(subject.Type), ID: subject.ID}
}

// Collector returns the signups a subject holds.
type Collector struct {
	store   waitlists.SignupStore
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
	store waitlists.SignupStore,
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
// It pages each scope's signups to the end through dataprivacy.CollectAll,
// because a collector that read one page and stopped would return a truncated
// subject access request — well-formed, present, and missing everything past the
// first page. It asks for archived signups too, since those still hold the
// address they were made with.
func (c *Collector) Collect(
	ctx context.Context,
	requestScope tenancy.Scope,
	subject dataprivacy.Subject,
) (json.RawMessage, error) {
	scopes, err := c.resolve(ctx, requestScope, subject)
	if err != nil {
		return nil, platformerrors.Wrap(err, "resolving waitlist scopes for subject")
	}

	who := subjectOf(subject)

	var held []waitlists.Signup

	for _, scope := range scopes {
		page, collectErr := dataprivacy.CollectAll(ctx,
			func(ctx context.Context, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[waitlists.Signup], error) {
				everything := *filter
				everything.IncludeArchived = new(true)

				return c.store.ListSignupsForSubject(ctx, c.reader, scope, who, &everything)
			})
		if collectErr != nil {
			return nil, platformerrors.Wrapf(collectErr, "collecting waitlist signups in scope %q", scope)
		}

		held = append(held, page...)
	}

	return dataprivacy.Fragment(len(held) > 0, held)
}

// Eraser withdraws the signups a subject holds.
type Eraser struct {
	store   waitlists.SignupStore
	resolve ScopeResolver
}

var _ dataprivacy.Eraser = (*Eraser)(nil)

// NewEraser builds the eraser over a store and a scope resolver. Both are
// required.
func NewEraser(store waitlists.SignupStore, resolve ScopeResolver) (*Eraser, error) {
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
// signups and the rest of the subject's footprint commit or roll back together.
// Each row is anonymized in place rather than deleted, and the digest each one
// keeps is reported under [RetainedDigests] — see the package documentation. A
// subject who joined nothing withdraws nothing and is not an error.
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
			platformerrors.Wrap(err, "resolving waitlist scopes for subject")
	}

	who := subjectOf(subject)

	var outcome dataprivacy.ErasureOutcome

	for _, scope := range scopes {
		withdrawn, withdrawErr := e.store.WithdrawSignupsForSubject(ctx, tx, scope, who)
		if withdrawErr != nil {
			return dataprivacy.ErasureOutcome{},
				platformerrors.Wrapf(withdrawErr, "withdrawing waitlist signups in scope %q", scope)
		}

		outcome.Anonymized += withdrawn
	}

	// Reported only when something was kept: a subject with no signups has no
	// digests, and a retention entry saying "0 of them" would be a line in
	// front of a regulator about nothing.
	if outcome.Anonymized > 0 {
		outcome.Retained = map[string]string{RetainedDigests: retainedDigests(outcome.Anonymized)}
	}

	return outcome, nil
}

// retainedDigests is the sentence the request record carries for what an
// erasure kept, and why. It names the number because the record is per
// request, and the basis because the record is what answers the question.
func retainedDigests(n int64) string {
	return fmt.Sprintf(
		"%d contact digest(s): a one-way hash of each withdrawn address, kept so that a later signup "+
			"from the same address is refused rather than re-subscribing somebody who asked to be left alone; "+
			"not reversible to the address",
		n)
}
