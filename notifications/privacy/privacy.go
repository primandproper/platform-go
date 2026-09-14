/*
Package privacy is notifications' contribution to a subject access request: what
somebody was told, what handsets they registered, and the destruction of both.

# Two adapters, not one

notifications is two seams — an inbox and a device registry — and the whole
argument for that split is that a service may have one and not the other: an
application with a bell icon and no mobile app implements [notifications.Inbox]
alone. So this package ships two pairs rather than one over
notifications.Store, and each half registers under its own key:
[DefaultInboxKey] and [DefaultDeviceKey]. A deployment with no handsets
registers two adapters instead of four, and nothing forces it to hold a registry
it does not have.

It also lets the two state their own case. An inbox row and a device token are
destroyed for different reasons, and an operator reading an erasure report sees
two counts rather than a total that hides which table it came from —
dataprivacy.Registry keys collectors and erasers in separate namespaces, so a
deployment that wants the export of one and the erasure of both says exactly
that.

# Why this is a package rather than four methods on the store

notifications would otherwise import dataprivacy, which imports operations,
which imports the queue and the scheduler — so a service that tells somebody
their order shipped and runs no privacy pipeline would compile all of it. The
seam goes here for the same reason dataprivacy/auditerasure exists, and it costs
one constructor argument.

# Why both erasures delete

An inbox row is free text an application wrote for one person to read, and
nothing can promise text somebody typed names nobody: the title, the body and
the link are the part that identifies people, so a "kept but anonymized"
notification would be a notification that still says who it is about. Archiving
is not an answer either — notifications.Inbox.ArchiveNotification stamps the row
and leaves all three — which is why the store grew
notifications.Inbox.DeleteNotificationsForPrincipal rather than this package
reaching for the write that was already there. The rows go.

A device token is the more urgent of the two. It is a stable identifier a third
party can address somebody's handset with, stored in the clear, and it keeps
working until either the provider rejects it or something deletes the row — so a
subject erased everywhere else is a subject whose phone can still be pushed to.
notifications.Registry.RevokeDevice removes one handset its owner named, which is
a sign-out; an erasure has no list of ids to name, and
notifications.Registry.DeleteDevicesForPrincipal is the write that needs none.

Nothing is retained, and so nothing is reported as retained. An operator whose
jurisdiction requires a record of what it told somebody registers no inbox eraser
— the notifications then survive an erasure, which is a decision somebody has
made rather than one this package made for them, and the device eraser is
unaffected either way.

# The export carries the token

A device token in an artifact looks like a thing to redact, and it is not. The
right of access is a right to what the table holds about the person asking, the
token is the row's most consequential fact about them, and an export that
withheld it would be answering "what may we show you" rather than "what do we
have". What follows from that is about the artifact rather than about this
package: dataprivacy encrypts one and expires it, and a deployment that considers
the token too sensitive to put in a file that leaves the building registers no
device collector. The eraser is the half that matters for the token anyway.

# Scopes

Every read and write in notifications is scoped, and a subject access request may
arrive without a scope — the request's confinement names nobody for the plain
"give me my data". So all four halves take a [ScopeResolver]: the mapping from a
subject to the scopes their notifications may be in, which is a question about
the consumer's tenancy model rather than about these tables.

It is a constructor argument rather than an option with a default, because there
is no default that is right twice. A deployment with one tenant wants
[FixedScopes] over tenancy.Global(); one whose requests always name their scope
wants [RequestScope]; one that resolves a person to their accounts has to ask its
own directory. A resolver that silently answered "the global scope" for the third
of those would export nothing and erase nothing, and report success for both.

# Subjects are principals

notifications keys both tables on a principal — a user id in every deployment
this module has, but a string there, because notifications does not own the
directory — so a dataprivacy.Subject's id is the principal, and there is no
vocabulary to bridge. A subject whose type is not a person is resolved by the
same rule: whatever id the request carries is what an inbox row would have been
filed under, and one that matches nothing collects and erases nothing.

# Executors

Every read and write in notifications runs on an executor its caller supplies,
and the two kinds of half get theirs from different places. dataprivacy.Eraser.Erase
is handed the request's database.Tx, so the erasers pass that straight down and
a subject's notifications and handsets commit with the rest of their footprint.
dataprivacy.Collector.Collect is handed nothing, because an export is a read and
there is no transaction for it to be part of — so the collectors take the
executor once, at construction, and every collection runs on it.

That is a database.SQLQueryExecutor rather than a database.Client: a collector
reads and does nothing else, and the narrower type is also the one that lets a
consumer hand it a Tx where an export genuinely has to see a transaction's own
writes.

# Observability

None of the four instruments anything of its own. Everything they do is a call
into the store, which spans and logs every read and write it makes, and a second
span around a loop of those would name the same work twice.
*/
package privacy

import (
	"context"
	"encoding/json"

	"github.com/primandproper/platform-go/v14/dataprivacy"
	"github.com/primandproper/platform-go/v14/notifications"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// The registry keys these are normally registered under. Each names the section
// an export's artifact carries that half in, and the prefix of any entries an
// outcome reports.
//
// They are dotted rather than one key called "notifications", because they are
// two tables with two rulings and dataprivacy.Registry keys each adapter
// separately — a report that summed them would be a report an operator cannot
// read either half out of. The dot is a segment separator dataprivacy's own key
// rule allows for exactly this.
const (
	DefaultInboxKey  = "notifications.inbox"
	DefaultDeviceKey = "notifications.devices"
)

// The sentinels this package returns.
var (
	// ErrNilInbox indicates a nil notifications.Inbox.
	ErrNilInbox = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil notifications inbox for the privacy adapter")

	// ErrNilRegistry indicates a nil notifications.Registry.
	ErrNilRegistry = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil notifications device registry for the privacy adapter")

	// ErrNilScopeResolver indicates a nil ScopeResolver. It is required, and
	// refusing it here is what stops an export that quietly covers no scope from
	// being discovered by the subject.
	ErrNilScopeResolver = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil notifications privacy scope resolver")

	// ErrNilExecutor indicates a nil executor. Every half runs on one somebody
	// else supplies — a collector's at construction, an eraser's per request —
	// because notifications keeps no connection of its own to fall back to.
	ErrNilExecutor = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil notifications privacy query executor")

	// ErrUnscopedRequest indicates a request that names no scope, handed to
	// [RequestScope], which has nowhere else to get one.
	ErrUnscopedRequest = platformerrors.New("notifications request names no scope")
)

// ScopeResolver names the scopes a subject's notifications may be in.
//
// Returning no scopes is legitimate and means the subject has nothing here: a
// collector reports the domain as holding nothing, and an eraser destroys
// nothing. Returning too many is how one subject's erasure reaches another
// tenant's inbox, so it is worth being exact.
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
// section, and would be missing every notification the subject was ever sent.
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

// InboxCollector returns what a subject was told.
type InboxCollector struct {
	inbox   notifications.Inbox
	reader  database.SQLQueryExecutor
	resolve ScopeResolver
}

var _ dataprivacy.Collector = (*InboxCollector)(nil)

// NewInboxCollector builds the collector over an inbox, the executor its reads
// run on, and a scope resolver. All three are required.
//
// The executor is a constructor argument because dataprivacy.Collector.Collect
// has nowhere to put one: an export is a read, and the seam that asks for it
// hands over a subject and nothing else. Client.Reader() is what a consumer
// ordinarily passes; a Tx is what it passes when the export has to see writes
// that transaction has not committed.
func NewInboxCollector(
	inbox notifications.Inbox,
	reader database.SQLQueryExecutor,
	resolve ScopeResolver,
) (*InboxCollector, error) {
	if inbox == nil {
		return nil, ErrNilInbox
	}

	if reader == nil {
		return nil, ErrNilExecutor
	}

	if resolve == nil {
		return nil, ErrNilScopeResolver
	}

	return &InboxCollector{inbox: inbox, reader: reader, resolve: resolve}, nil
}

// Collect implements dataprivacy.Collector.
//
// It pages each scope's inbox to the end through dataprivacy.CollectAll, because
// a collector that read one page and stopped would return a truncated subject
// access request — well-formed, present, and missing everything past the first
// page.
//
// It asks for archived notifications as well as live ones, on a copy of the
// filter it is handed. A notification somebody dismissed is still something they
// were told, and an export showing only what they had not yet cleared would
// answer a different question than the one the right of access asks — the same
// reading billing/privacy takes of an archived subscription.
func (c *InboxCollector) Collect(
	ctx context.Context,
	requestScope tenancy.Scope,
	subject dataprivacy.Subject,
) (json.RawMessage, error) {
	scopes, err := c.resolve(ctx, requestScope, subject)
	if err != nil {
		return nil, platformerrors.Wrap(err, "resolving notification scopes for subject")
	}

	var told []notifications.Notification

	for _, scope := range scopes {
		page, collectErr := dataprivacy.CollectAll(ctx,
			func(ctx context.Context, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[notifications.Notification], error) {
				everything := *filter
				everything.IncludeArchived = new(true)

				return c.inbox.ListNotifications(ctx, c.reader, scope, subject.ID, &everything)
			})
		if collectErr != nil {
			return nil, platformerrors.Wrapf(collectErr, "collecting notifications in scope %q", scope)
		}

		told = append(told, page...)
	}

	return dataprivacy.Fragment(len(told) > 0, told)
}

// InboxEraser destroys what a subject was told.
type InboxEraser struct {
	inbox   notifications.Inbox
	resolve ScopeResolver
}

var _ dataprivacy.Eraser = (*InboxEraser)(nil)

// NewInboxEraser builds the eraser over an inbox and a scope resolver. Both are
// required.
func NewInboxEraser(inbox notifications.Inbox, resolve ScopeResolver) (*InboxEraser, error) {
	if inbox == nil {
		return nil, ErrNilInbox
	}

	if resolve == nil {
		return nil, ErrNilScopeResolver
	}

	return &InboxEraser{inbox: inbox, resolve: resolve}, nil
}

// Erase implements dataprivacy.Eraser.
//
// It runs in the request's transaction and uses the executor it is given, so the
// notifications and the rest of the subject's footprint commit or roll back
// together. A subject who was never told anything erases nothing and is not an
// error.
func (e *InboxEraser) Erase(
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
			platformerrors.Wrap(err, "resolving notification scopes for subject")
	}

	var outcome dataprivacy.ErasureOutcome

	for _, scope := range scopes {
		deleted, deleteErr := e.inbox.DeleteNotificationsForPrincipal(ctx, tx, scope, subject.ID)
		if deleteErr != nil {
			return dataprivacy.ErasureOutcome{},
				platformerrors.Wrapf(deleteErr, "erasing notifications in scope %q", scope)
		}

		outcome.Deleted += deleted
	}

	return outcome, nil
}

// DeviceCollector returns the handsets a subject registered.
type DeviceCollector struct {
	registry notifications.Registry
	reader   database.SQLQueryExecutor
	resolve  ScopeResolver
}

var _ dataprivacy.Collector = (*DeviceCollector)(nil)

// NewDeviceCollector builds the collector over a registry, the executor its
// reads run on, and a scope resolver. All three are required.
//
// The executor is a constructor argument for the reason
// [NewInboxCollector] gives.
func NewDeviceCollector(
	registry notifications.Registry,
	reader database.SQLQueryExecutor,
	resolve ScopeResolver,
) (*DeviceCollector, error) {
	if registry == nil {
		return nil, ErrNilRegistry
	}

	if reader == nil {
		return nil, ErrNilExecutor
	}

	if resolve == nil {
		return nil, ErrNilScopeResolver
	}

	return &DeviceCollector{registry: registry, reader: reader, resolve: resolve}, nil
}

// Collect implements dataprivacy.Collector.
//
// It pages each scope's registrations to the end through
// dataprivacy.CollectAll, and asks for nothing archived: the registry has no
// archived_at, because a token is revoked or invalidated rather than stamped, so
// every row it holds is a live one. Each carries the token — see the package
// documentation for why that is the export rather than a leak.
func (c *DeviceCollector) Collect(
	ctx context.Context,
	requestScope tenancy.Scope,
	subject dataprivacy.Subject,
) (json.RawMessage, error) {
	scopes, err := c.resolve(ctx, requestScope, subject)
	if err != nil {
		return nil, platformerrors.Wrap(err, "resolving device scopes for subject")
	}

	var registered []notifications.Device

	for _, scope := range scopes {
		page, collectErr := dataprivacy.CollectAll(ctx,
			func(ctx context.Context, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[notifications.Device], error) {
				return c.registry.ListDevices(ctx, c.reader, scope, subject.ID, filter)
			})
		if collectErr != nil {
			return nil, platformerrors.Wrapf(collectErr, "collecting devices in scope %q", scope)
		}

		registered = append(registered, page...)
	}

	return dataprivacy.Fragment(len(registered) > 0, registered)
}

// DeviceEraser removes the handsets a subject registered.
type DeviceEraser struct {
	registry notifications.Registry
	resolve  ScopeResolver
}

var _ dataprivacy.Eraser = (*DeviceEraser)(nil)

// NewDeviceEraser builds the eraser over a registry and a scope resolver. Both
// are required.
func NewDeviceEraser(registry notifications.Registry, resolve ScopeResolver) (*DeviceEraser, error) {
	if registry == nil {
		return nil, ErrNilRegistry
	}

	if resolve == nil {
		return nil, ErrNilScopeResolver
	}

	return &DeviceEraser{registry: registry, resolve: resolve}, nil
}

// Erase implements dataprivacy.Eraser.
//
// It runs in the request's transaction and uses the executor it is given, so the
// handsets stop being addressable at the same moment the rest of the subject's
// footprint goes. A subject with no registrations erases nothing and is not an
// error.
//
// What it does not do is tell the providers. APNs and FCM learn a token is dead
// by being pushed to and rejecting it, which is the direction that feedback runs
// in — see notifications.Registry.InvalidateDeviceToken — and there is nothing to
// push to once the rows are gone. Deleting the row is what stops the push.
func (e *DeviceEraser) Erase(
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
			platformerrors.Wrap(err, "resolving device scopes for subject")
	}

	var outcome dataprivacy.ErasureOutcome

	for _, scope := range scopes {
		deleted, deleteErr := e.registry.DeleteDevicesForPrincipal(ctx, tx, scope, subject.ID)
		if deleteErr != nil {
			return dataprivacy.ErasureOutcome{},
				platformerrors.Wrapf(deleteErr, "erasing devices in scope %q", scope)
		}

		outcome.Deleted += deleted
	}

	return outcome, nil
}
