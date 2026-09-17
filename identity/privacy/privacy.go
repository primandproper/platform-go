/*
Package privacy is the identity tables' contribution to a subject access
request: a dataprivacy.Collector that returns who somebody is to the directory,
and a dataprivacy.Eraser that destroys them.

# Why this is a package rather than two methods on the store

identity would otherwise import dataprivacy, which imports operations, which
imports the queue and the scheduler — so a service with a login form and no
privacy pipeline would compile all of it. The seam goes here for the same reason
comments/privacy and dataprivacy/auditerasure exist, and it costs one
constructor argument.

# What the erasure destroys, and the one thing it does not

[identity.Store.EraseUser] takes the user row and everything the schema hangs off
it — the service roles, the memberships, and the roles those carry — through ON
DELETE CASCADE. It does not take the invitations, because
identity_invitations references neither user it names and so cascades from
nothing. That is what [identity.InvitationStore.EraseInvitationsForSubject] is
for, and this [Eraser] runs the two in the order they have to run in: the
invitation erasure first, because it reads the subject's address off the row the
other one destroys.

What the erasure cannot resolve is an account the subject owned. EraseUser is
not refusable — a store that could decline an erasure would make somebody's
rights conditional on an account they may not even administer — so an owned
account survives with an owner_user_id naming nobody. Resolve those before the
request runs: transfer the accounts with other members, archive the ones
without. Nothing here can make that decision, because archiving an account to
satisfy one member's erasure takes the other members offline.

Nothing is reported as retained. The invitations the subject sent stay in the
table, and they are reported as anonymized rather than retained because what is
left on them is the recipient's data and not the subject's — see
[identity.InvitationErasure].

# Both halves see a subject the directory has hidden

Every read in identity excludes archived rows, which is what makes a deactivated
user absent from GetUser and from every Principal built out of one. That is the
wrong answer for exactly these two callers: a person exercising a right has
almost always been deactivated first, so an export routed through GetUser would
export nothing and an erasure routed through it would destroy nothing, and both
would report success over somebody still in the table. So both halves read
through [identity.DirectoryReader.GetUserIncludingArchived].

The memberships are the one place that widening stops. [identity.Store] has no
read that reaches an archived membership — ListMembershipsForUser is the read
behind every authorization decision the package answers, and widening it would
be widening those — so a subject deactivated before they asked carries their
user row and their invitations into the export and no memberships, because
deactivating them ended every one. A deployment that has to export those reads
its own audit trail of the archival, which is the record of what was ended and
when.

# Every status, one read at a time

The invitations are exported in all four statuses rather than the pending ones a
roster shows, because an invitation somebody accepted or declined is as much a
fact about them as one they have not answered. That is four paged walks per
direction rather than one: the store's read binds a single status, deliberately,
since the index behind it is partial on pending rows and "every status" would be
a table scan wearing a filter's clothes. [identity.InvitationStatuses] is the
list walked, so a fifth status added later is exported without anybody
remembering this loop.

# Scopes

Every read and write in identity is scoped, and a subject access request may
arrive without a scope — the request's confinement names nobody for the plain
"give me my data". So both halves take a [ScopeResolver]: the mapping from a
subject to the scopes their directory rows may be in, which is a question about
the consumer's tenancy model rather than about these tables.

It is a constructor argument rather than an option with a default, because there
is no default that is right twice. A deployment with one directory wants
[FixedScopes] over tenancy.Global(); one whose requests always name their scope
wants [RequestScope]; one that resolves a person to several directories has to
ask its own. A resolver that silently answered "the global scope" for the third
of those would export nothing and erase nothing, and report success for both.

A scope the subject has no user row in is not an error in either half. It is the
ordinary answer for a resolver that names more directories than any one person
appears in, and the alternative — failing the request — would make a broad
resolver unusable for the subjects it is right about.

# Executors

Every read and write in identity runs on an executor its caller supplies, and
the two halves get theirs from different places. dataprivacy.Eraser.Erase is
handed the request's database.Tx, so [Eraser] passes that straight down and the
subject's directory rows commit with the rest of their footprint.
dataprivacy.Collector.Collect is handed nothing, because an export is a read and
there is no transaction for it to be part of — so [NewCollector] takes the
executor once, at construction, and every collection runs on it.

That is a database.SQLQueryExecutor rather than a database.Client: a collector
reads and does nothing else, and the narrower type is also the one that lets a
consumer hand it a Tx where an export genuinely has to see a transaction's own
writes.

# The two seams are narrow on purpose

[SubjectReader] and [SubjectWriter] are the reads a collection makes and the
writes an erasure makes, and neither is identity.Store. That is the advice
identity.Store's own documentation gives — depend on the smallest interface that
covers what you do, because the narrow one is a statement about reach that the
compiler checks. A collector that held a Store could ban a user; this one cannot
write anything at all, and the eraser cannot read a password hash. *identity.SQLStore
satisfies both, so a consumer passes the same store to both constructors.

# Observability

Neither half instruments anything of its own. Everything they do is a call into
the store, which spans and logs every read and write it makes, and a second span
around a loop of those would name the same work twice.
*/
package privacy

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/primandproper/platform-go/v14/dataprivacy"
	"github.com/primandproper/platform-go/v14/identity"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// DefaultKey is the registry key these are normally registered under. It names
// the section an export's artifact carries them in, and the prefix of any
// entries an outcome reports.
const DefaultKey = "identity"

// The sentinels this package returns.
var (
	// ErrNilStore indicates a nil store.
	ErrNilStore = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil identity privacy store")

	// ErrNilScopeResolver indicates a nil ScopeResolver. It is required, and
	// refusing it here is what stops an export that quietly covers no directory
	// from being discovered by the subject.
	ErrNilScopeResolver = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil identity privacy scope resolver")

	// ErrNilExecutor indicates a nil executor. Both halves run on one somebody
	// else supplies — the collector's at construction, the eraser's per request
	// — because identity keeps no connection of its own to fall back to.
	ErrNilExecutor = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil identity privacy query executor")

	// ErrUnscopedRequest indicates a request that names no scope, handed to
	// [RequestScope], which has nowhere else to get one.
	ErrUnscopedRequest = platformerrors.New("identity request names no scope")
)

// SubjectReader is the directory reads a collection makes.
//
// It is four methods of identity.Store rather than all fifty, for the reason
// that interface's own documentation gives: the narrow one is a statement about
// reach, and it is checked by the compiler. A collector that held a Store could
// ban a user.
type SubjectReader interface {
	// GetUserIncludingArchived reads the subject whether or not the directory
	// has hidden them — see the package documentation for why that matters here
	// and nowhere else.
	GetUserIncludingArchived(
		ctx context.Context,
		q database.SQLQueryExecutor,
		scope tenancy.Scope,
		userID string,
	) (*identity.User, error)

	// ListMembershipsForUser returns every live membership the subject holds.
	ListMembershipsForUser(
		ctx context.Context,
		q database.SQLQueryExecutor,
		scope tenancy.Scope,
		userID string,
	) ([]*identity.Membership, error)

	// ListInvitationsFromUser pages what the subject sent, in one status.
	ListInvitationsFromUser(
		ctx context.Context,
		q database.SQLQueryExecutor,
		scope tenancy.Scope,
		userID string,
		status identity.InvitationStatus,
		filter *filtering.QueryFilter,
	) (*filtering.QueryFilteredResult[identity.Invitation], error)

	// ListInvitationsForEmailAddress pages what the subject was sent, in one
	// status.
	ListInvitationsForEmailAddress(
		ctx context.Context,
		q database.SQLQueryExecutor,
		scope tenancy.Scope,
		emailAddress string,
		status identity.InvitationStatus,
		filter *filtering.QueryFilter,
	) (*filtering.QueryFilteredResult[identity.Invitation], error)
}

// SubjectWriter is the two writes an erasure makes, in the order [Eraser] makes
// them.
type SubjectWriter interface {
	// EraseInvitationsForSubject destroys the invitations addressed to the
	// subject and takes them off the ones they sent. It runs first, because it
	// reads the subject's address off the row EraseUser destroys.
	EraseInvitationsForSubject(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		userID string,
	) (identity.InvitationErasure, error)

	// EraseUser destroys the user row and everything the schema cascades from
	// it.
	EraseUser(ctx context.Context, tx database.Tx, scope tenancy.Scope, userID string) (int64, error)
}

// ScopeResolver names the directories a subject's rows may be in.
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

// Export is what one directory holds about a subject.
//
// It is the store's own types rather than a flattened rendering of them, so that
// what a subject receives and what the application reads are the same shape —
// and so that a column added to a table appears in the export without anybody
// remembering to add it here. The user is redacted: a subject access request is
// not a credential dump, and the password hash, the two-factor secret and the
// pending verification token are the three fields that would make it one.
type Export struct {
	// User is the subject's directory row, redacted.
	User *identity.User `json:"user"`

	// Scope is the directory it is in. It renders as the identifier the column
	// holds — the empty string for the global scope — which is what
	// tenancy.Scope's own JSON is, so an export says the same thing the rows
	// behind it do.
	Scope tenancy.Scope `json:"scope"`

	// Memberships is every live membership the subject holds here. See the
	// package documentation on what a deactivated subject's says.
	Memberships []identity.Membership `json:"memberships"`

	// InvitationsSent is every invitation the subject sent, in every status.
	InvitationsSent []identity.Invitation `json:"invitationsSent"`

	// InvitationsReceived is every invitation addressed to the subject's
	// address, in every status.
	InvitationsReceived []identity.Invitation `json:"invitationsReceived"`
}

// Collector exports who a subject is to the directory.
type Collector struct {
	store   SubjectReader
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
	store SubjectReader,
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
// A scope the subject has no user row in contributes nothing and is not an
// error — see the package documentation. A subject in none of the resolved
// scopes collects nothing at all, which dataprivacy.Fragment renders as the
// domain holding nothing rather than as an empty section.
func (c *Collector) Collect(
	ctx context.Context,
	requestScope tenancy.Scope,
	subject dataprivacy.Subject,
) (json.RawMessage, error) {
	var held []Export

	// dataprivacy.ForEachOwner rather than CollectByScope, because what this
	// collector reads per scope is not one paged list: collectScope makes four
	// reads and answers whether the subject is in that directory at all. A
	// scope they are not in contributes nothing and is not an error, which is
	// the skip the helper that concatenates pages has no way to express.
	err := dataprivacy.ForEachOwner(ctx, c.resolve, requestScope, subject,
		func(ctx context.Context, scope tenancy.Scope) error {
			export, present, collectErr := c.collectScope(ctx, scope, subject)
			if collectErr != nil {
				return collectErr
			}

			if !present {
				return nil
			}

			held = append(held, export)

			return nil
		})
	if err != nil {
		return nil, err
	}

	return dataprivacy.Fragment(len(held) > 0, held)
}

// collectScope reads one directory, and reports whether the subject is in it at
// all.
//
// The presence flag is separate from the error because a directory the subject
// has no row in is an answer rather than a failure, and separate from the Export
// because an export that happened to be empty is not the same fact.
func (c *Collector) collectScope(
	ctx context.Context,
	scope tenancy.Scope,
	subject dataprivacy.Subject,
) (Export, bool, error) {
	user, err := c.store.GetUserIncludingArchived(ctx, c.reader, scope, subject.ID)
	if err != nil {
		if errors.Is(err, identity.ErrUserNotFound) {
			return Export{}, false, nil
		}

		return Export{}, false, platformerrors.Wrapf(err, "reading the subject in scope %q", scope)
	}

	memberships, err := c.store.ListMembershipsForUser(ctx, c.reader, scope, subject.ID)
	if err != nil {
		return Export{}, false, platformerrors.Wrapf(err, "collecting identity memberships in scope %q", scope)
	}

	sent, err := c.everyStatus(ctx, func(
		ctx context.Context,
		status identity.InvitationStatus,
		filter *filtering.QueryFilter,
	) (*filtering.QueryFilteredResult[identity.Invitation], error) {
		return c.store.ListInvitationsFromUser(ctx, c.reader, scope, subject.ID, status, filter)
	})
	if err != nil {
		return Export{}, false, platformerrors.Wrapf(err, "collecting identity invitations sent in scope %q", scope)
	}

	received, err := c.everyStatus(ctx, func(
		ctx context.Context,
		status identity.InvitationStatus,
		filter *filtering.QueryFilter,
	) (*filtering.QueryFilteredResult[identity.Invitation], error) {
		return c.store.ListInvitationsForEmailAddress(ctx, c.reader, scope, user.EmailAddress, status, filter)
	})
	if err != nil {
		return Export{}, false, platformerrors.Wrapf(err, "collecting identity invitations received in scope %q", scope)
	}

	return Export{
		User:                user.Redacted(),
		Scope:               scope,
		Memberships:         values(memberships),
		InvitationsSent:     sent,
		InvitationsReceived: received,
	}, true, nil
}

// everyStatus walks one keyed invitation read once per status and returns every
// row the four walks found.
//
// Four walks rather than one because the store's read binds a single status —
// see the package documentation — and each of them is paged to the end through
// dataprivacy.CollectAll, because a collector that read one page and stopped
// would return a truncated subject access request: well-formed, present, and
// missing everything past the first page.
func (c *Collector) everyStatus(
	ctx context.Context,
	page func(
		ctx context.Context,
		status identity.InvitationStatus,
		filter *filtering.QueryFilter,
	) (*filtering.QueryFilteredResult[identity.Invitation], error),
) ([]identity.Invitation, error) {
	var collected []identity.Invitation

	for _, status := range identity.InvitationStatuses {
		found, err := dataprivacy.CollectAll(ctx,
			func(ctx context.Context, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[identity.Invitation], error) {
				return page(ctx, status, filter)
			})
		if err != nil {
			return nil, platformerrors.Wrapf(err, "status %q", status)
		}

		collected = append(collected, found...)
	}

	return collected, nil
}

// values dereferences a slice of rows for the export, skipping any nil the
// store handed back — dataprivacy.CollectAll's reason, applied to the one read
// here that is not paged.
func values[T any](rows []*T) []T {
	out := make([]T, 0, len(rows))

	for _, row := range rows {
		if row == nil {
			continue
		}

		out = append(out, *row)
	}

	return out
}

// Eraser destroys a subject's directory rows.
type Eraser struct {
	store   SubjectWriter
	resolve ScopeResolver
}

var _ dataprivacy.Eraser = (*Eraser)(nil)

// NewEraser builds the eraser over a store and a scope resolver. Both are
// required.
func NewEraser(store SubjectWriter, resolve ScopeResolver) (*Eraser, error) {
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
// directory rows and the rest of the subject's footprint commit or roll back
// together. Per scope it runs the invitation erasure and then the user erasure,
// in that order, because the first reads the subject's address off the row the
// second destroys.
//
// A scope the subject has no user row in is skipped rather than failed. That is
// what the invitation erasure's ErrUserNotFound means when this method is the
// caller: this one controls the order, so the only way to reach that sentinel is
// a directory the subject is not in — which is the ordinary answer for a
// resolver covering more directories than any one person appears in, and what
// makes a replayed erasure a no-op rather than an error.
//
// Accounts the subject owned are not touched, and survive naming an owner who no
// longer exists. See the package documentation: resolving them is a decision
// nothing here can make.
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

	// dataprivacy.ForEachOwner rather than EraseByScope, because a scope is two
	// ordered writes here rather than one, they are worded separately, and a
	// directory the subject has no row in is skipped rather than failed. The
	// helper that sums one write's outcome has nowhere to put any of the three.
	err := dataprivacy.ForEachOwner(ctx, e.resolve, requestScope, subject,
		func(ctx context.Context, scope tenancy.Scope) error {
			invitations, eraseErr := e.store.EraseInvitationsForSubject(ctx, tx, scope, subject.ID)
			if eraseErr != nil {
				if errors.Is(eraseErr, identity.ErrUserNotFound) {
					return nil
				}

				return platformerrors.Wrapf(eraseErr, "erasing identity invitations in scope %q", scope)
			}

			erased, eraseErr := e.store.EraseUser(ctx, tx, scope, subject.ID)
			if eraseErr != nil {
				return platformerrors.Wrapf(eraseErr, "erasing the identity user in scope %q", scope)
			}

			outcome.Deleted += invitations.Deleted + erased
			outcome.Anonymized += invitations.Anonymized

			return nil
		})
	if err != nil {
		return dataprivacy.ErasureOutcome{}, err
	}

	return outcome, nil
}
