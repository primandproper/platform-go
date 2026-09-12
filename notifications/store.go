package notifications

import (
	"context"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// Two seams rather than one, and the split is the difference in lifecycle.
//
// An inbox row is created, read, and archived: it is a durable record of what
// somebody was told, and it outlives the delivery. A device token is registered
// and then either revoked by its owner or destroyed on the provider's word: it
// is a routing fact with no history worth keeping, and a soft delete would leave
// rows every send path has to remember to exclude.
//
// They are separate interfaces because they have separate consumers. A service
// with an in-app inbox and no mobile app implements one; a service that pushes
// to handsets and shows nothing in-app implements the other. One interface
// carrying both would make each of those a set of methods somebody has to stub.
//
// [SQLStore] implements both against one schema, so adopting them together is
// one constructor and one migration.
//
// # The transaction is the caller's, except where there is no caller
//
// Every write a consumer calls takes a database.Tx and every read takes the
// wider database.SQLQueryExecutor, which is the module's store convention
// rather than anything this package invented. There is no form of any write
// that opens a transaction of its own, and here that absence is the point of
// the port: a notification is almost always *about* something else that was
// just written. Filed in a transaction of the store's own, it survives the
// rollback of the operation it describes — so a refused order still tells
// somebody their order was placed, and the failure runs in the direction the
// user can see. A signature that cannot express that is better than a doc
// warning against it.
//
// The read takes the wider type so that one method serves both moments. A
// client polling its inbox holds no transaction and passes Client.Reader(); a
// service that has just filed a notification passes the Tx it filed through,
// and sees it. A read narrowed to Tx would have forced the first caller into a
// transaction it has no use for, and one narrowed to Client.Reader() would have
// read a database that does not yet hold the row its caller just wrote.
//
// A caller with genuinely nothing to join opens one with Client.WithTransaction
// and passes the Tx it is handed. An implementation that is not a SQL store
// still takes these types; one with no transaction of its own ignores the
// executor, and the seam stays one signature rather than one per backing.
//
// [Registry.InvalidateDeviceToken] is the exception and takes neither. It is
// the registry servicing itself on a provider's word rather than answering a
// consumer, and it says so on its own doc.
//
// # Nine of these twelve are on the wire and three are not
//
// notifications/grpc serves the inbox as six RPCs — ListNotifications,
// ListUnreadNotifications, GetNotification, MarkNotificationRead,
// MarkAllNotificationsRead and ArchiveNotification — and the registry as three:
// RegisterDevice, ListDevices and RevokeDevice. Those nine are what a bell icon
// and a device screen are made of, and the second three have a caller that is
// literally a handset.
//
// The three that stay off it are three *different* shapes of machinery rather
// than three instances of one, which is what makes this package the place to
// read the distinction: [Inbox.CreateNotification] is the transactional
// companion, [Registry.ListDevicesByPrincipals] is the internal fan-out, and
// [Registry.InvalidateDeviceToken] is the provider callback hook. Each argues
// its own case on its own method below, because a reader of this file is
// standing where the question occurs to them.
//
// # The scope is an argument, on every method
//
// That includes the two writes that take a whole entity. [Inbox.CreateNotification]
// and [Registry.RegisterDevice] read the scope off the argument rather than off
// Notification.Scope or Device.Scope, and the alternative — letting an entity
// that carries a scope supply its own — was considered and rejected for the
// reason comments states it: the module's rule is that a scope goes into the
// query bound as a tenancy.Scope rather than derived from some other value, and
// an entity field is exactly the derivation that rule exists to rule out. An
// entity whose scope disagrees with the argument is [ErrScopeMismatch] rather
// than either value quietly winning; one that names none adopts the argument.
//
// # Five of these writes hand back the row they moved, and two do not
//
// [Inbox.CreateNotification], [Inbox.MarkNotificationRead],
// [Inbox.ArchiveNotification], [Registry.RegisterDevice] and
// [Registry.RevokeDevice] each answer with the row the statement left behind,
// read on the caller's transaction. None of them writes to anything the caller
// still holds: the two that take an entity work on a copy of it, and the three
// that take an id have nothing to write to.
//
// It is not a convenience, and the reason is the transaction. The stamps on
// these rows are the server's clock rather than anything a caller could
// assemble, and a caller inside an uncommitted transaction has no other way to
// read them back. The audit entry describing one of these writes is written
// beside it — same transaction, same request — so without this the caller reads
// first and writes second, and its record then describes the row as it stood a
// statement earlier rather than as the statement left it.
//
// Two of the five could not be answered by a later read at all, which is what
// makes the boundary a line rather than a preference. An archived notification
// is invisible to every single-row read here, because excluding archived rows
// is what "the inbox" means; a revoked device is invisible to everything,
// because the row is deleted. The other three are reachable a statement later
// and hand the row back anyway, because a module with two spellings of "what
// did I just write" is a module where the answer depends on which method you
// called.
//
// The two that do not are the two with no row to describe.
// [Inbox.MarkAllNotificationsRead] moves a set rather than a row and reports how
// many; [Registry.InvalidateDeviceToken] is machinery — it takes neither
// executor nor scope, and it is idempotent, so a token already gone is the
// state its caller asked for and there is nothing that was moved.
//
// The rejected spelling was mutating the caller's argument in place, which
// delivers the same guarantee — comments.Store.CreateComment does exactly that.
// Returning is the one this module already has more of, and the only one
// available to a write that takes an id rather than an entity, which is what
// made it the module's answer rather than this package's.

// Store is both seams at once, and it exists for the layer that has to name one
// of them.
//
// A store assembled from configuration has to come back as something, and the
// two interfaces below are two things. notificationscfg.NewStore returning the
// concrete *SQLStore was the alternative, and it makes every method that type
// happens to export part of what a deployment assembled from configuration may
// depend on — a surface no other package here hands out, and one that cannot be
// taken back inside a major version. Naming the pair costs a line and puts the
// narrowing where every sibling package already does it.
//
// It does not retract the split the top of this file argues for. A consumer
// still depends on the half it uses: notifications/grpc takes an [Inbox] and a
// [Registry] separately though one value satisfies both, an application with no
// mobile app implements [Inbox] alone, and nothing here is reachable that was
// not reachable through one of them. This interface names the pair rather than
// joining them.
type Store interface {
	Inbox
	Registry
}

// Inbox is the persistence seam for in-app notifications.
//
// This package ships a SQL implementation ([NewSQLStore]) together with the DDL
// it needs (notifications/migrations), so adopting it does not mean writing this.
// The interface exists because an inbox and its storage are genuinely separable,
// and an application with its own schema conventions should not have to fork the
// package to keep them.
//
// Every method takes a tenancy.Scope and a principal, and none of them offers a
// variant that omits either — an implementation must filter on both rather than
// treat them as hints. The scope is the tenancy doctrine; the principal is this
// package's own, and it is load-bearing for the same reason: a notification
// addressed to somebody is not a row the rest of their tenant may read, so a
// read keyed on the scope alone would let any member of an account read any
// other member's inbox by id.
type Inbox interface {
	// CreateNotification files one notification through the caller's
	// transaction, so it commits with whatever the caller writes beside it — the
	// order, the invitation, the failed payment the notification is about — and
	// answers with the row it wrote: the id it assigned where the caller left one
	// empty, the scope the call named, and the creation time the database
	// stamped. A nil tx is an error wrapping ErrNilExecutor, and a refused write
	// answers with a nil Notification.
	//
	// It leaves the Notification it was handed alone. What this call settled is
	// on the row it returns and nowhere else, so a caller that files a
	// notification and then serializes its own argument into a response
	// serializes an empty id and a creation time in the year one.
	//
	// A Notification.Scope that disagrees with the scope argument is
	// ErrScopeMismatch; one that names none adopts the argument.
	//
	// It is off the wire, and the first sentence is why. This is the
	// transactional companion: the realistic caller is the consumer's own code,
	// telling somebody about the thing it is in the middle of writing. An RPC
	// moves the write into a transaction of its own, on the far side of a
	// network, at a moment the caller does not choose — so a refused order still
	// tells somebody their order was placed, and the failure runs in the
	// direction the user can see. It is the same fact audit.Recorder states
	// about an audit entry, and the reason that method takes a Tx as well.
	//
	// A process that wants to notify somebody about something it did not cause
	// is describing an RPC of its own, whose handler owns the transaction this
	// write belongs in — which is exactly what notifications/grpc's own writes
	// do with Client.WithTransaction.
	CreateNotification(ctx context.Context, tx database.Tx, scope tenancy.Scope, notification *Notification) (*Notification, error)

	// GetNotification reads one of the principal's live notifications. It
	// returns an error wrapping ErrNotificationNotFound when the notification
	// does not exist, has been archived, or belongs to somebody else — which are
	// the same answer from here. A nil q is an error wrapping ErrNilExecutor.
	GetNotification(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, principal, notificationID string) (*Notification, error)

	// ListNotifications pages the principal's inbox, in the direction the filter
	// names. A nil q is an error wrapping ErrNilExecutor.
	ListNotifications(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, principal string, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[Notification], error)

	// ListUnreadNotifications is ListNotifications restricted to what the
	// principal has not read.
	//
	// It is a separate method rather than a flag, because unread is "read_at is
	// absent" and there is no value a caller could pass to relax it. The badge
	// count every client asks for first is on the result's pagination: the
	// filtered count is of everything unread, not of the page.
	ListUnreadNotifications(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, principal string, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[Notification], error)

	// MarkNotificationRead stamps one notification as read, now, through the
	// caller's transaction, and answers with the row as the statement left it. A
	// nil tx is an error wrapping ErrNilExecutor.
	//
	// It is idempotent and does not move the stamp: a notification the principal
	// has already read reports success and comes back carrying the time it was
	// first read, which is what a digest and a re-notify both read. That makes
	// the returned ReadAt the answer to "when", whether this call wrote it or
	// found it — which the error alone could not distinguish, since both are
	// success. A notification that is not in the inbox — archived, absent, or
	// somebody else's — is an error wrapping ErrNotificationNotFound and a nil
	// Notification.
	MarkNotificationRead(ctx context.Context, tx database.Tx, scope tenancy.Scope, principal, notificationID string) (*Notification, error)

	// MarkAllNotificationsRead stamps everything the principal has not read
	// through the caller's transaction, and reports how many that was.
	//
	// The count is the answer rather than a diagnostic: it is what was sitting
	// unread at the moment the statement ran, and there is no cheaper way to
	// learn it. It is the count this transaction wrote, so a caller that unwinds
	// has marked nothing — and the number they were handed describes a state
	// that never committed.
	MarkAllNotificationsRead(ctx context.Context, tx database.Tx, scope tenancy.Scope, principal string) (int64, error)

	// ArchiveNotification dismisses one notification through the caller's
	// transaction, leaving the row for whoever asks later what somebody was
	// told, and answers with the row it archived — carrying the ArchivedAt the
	// statement stamped. A nil tx is an error wrapping ErrNilExecutor.
	//
	// It is the write here whose result nothing else on this interface can
	// reach. Every single-row read excludes archived rows, because excluding them
	// is what "the inbox" means, so the read that would describe what this did is
	// the one read written not to find it. A paged list with
	// filtering.QueryFilter.IncludeArchived still walks the row, and that is a
	// page rather than an answer to "what did I just archive".
	//
	// A notification already archived is an error wrapping
	// ErrNotificationNotFound, because an archived notification is not in the
	// inbox and this method addresses the inbox.
	ArchiveNotification(ctx context.Context, tx database.Tx, scope tenancy.Scope, principal, notificationID string) (*Notification, error)
}

// Registry is the persistence seam for device tokens: what a push is addressed
// to, and the half of this package that goes wrong quietly.
//
// A provider reports an invalid or expired token on send, and a registry that
// never prunes them keeps pushing into the void while reporting success. That
// feedback loop is why the registry lives beside the senders rather than in the
// consumer: [Registry.InvalidateDeviceToken] is the hook
// notifications/mobile calls with what the provider said, and a token it removes
// is gone rather than flagged.
type Registry interface {
	// RegisterDevice records a device token through the caller's transaction,
	// under the scope the call names, and answers with the row that is there
	// afterwards. A nil tx is an error wrapping ErrNilExecutor.
	//
	// It converges on (platform, token) rather than inserting, because the token
	// is the handset and a handset re-registers on every app launch and every
	// token rotation. A token already registered to somebody else moves to the
	// principal registering it now, keeping its id and its creation time: a
	// handset that changes hands has one owner, and a registry that kept both
	// would deliver the previous owner's notifications to the new one.
	//
	// So the row it hands back is the registration the token has now rather than
	// the registration this call described, and on a re-registration those are
	// not the same thing. A first registration answers with the row just
	// inserted: the id the caller supplied or the one this assigned, the
	// last-seen time the caller supplied or the clock's, and the creation time
	// the database stamped. A re-registration answers with the pre-existing row
	// the write converged on — its id and its creation time, carrying this call's
	// principal, scope and last-seen stamp. A caller that assumed otherwise would
	// hold an id no row has, and revoke nothing when the user signs out.
	//
	// It leaves the Device it was handed alone; the returned row is where the id
	// and the stamps are. That read-back runs on tx, so it is the row this
	// transaction just converged on.
	//
	// A Device.Scope that disagrees with the scope argument is ErrScopeMismatch;
	// one that names none adopts the argument.
	RegisterDevice(ctx context.Context, tx database.Tx, scope tenancy.Scope, device *Device) (*Device, error)

	// ListDevices pages the principal's registered devices. A nil q is an error
	// wrapping ErrNilExecutor.
	ListDevices(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, principal string, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[Device], error)

	// ListDevicesByPrincipals reads every device registered to any of the named
	// principals, in one query.
	//
	// It is the read a fan-out makes: a notification addressed to thirty members
	// of an account is thirty inbox rows and one query for the tokens to push
	// to, rather than thirty round trips returning two rows each. An empty set of
	// principals is an empty answer and no query.
	//
	// Passed the transaction the inbox rows were written in, it sees the devices
	// that transaction registered — which is the fan-out that pushes to a
	// handset registered moments earlier in the same request.
	//
	// It is off the wire, and the two paragraphs above are why. This is the
	// internal fan-out — a sender asking itself where to push on the way to its
	// own work — so the caller who would reach for it over an RPC is not a
	// caller at all. It also names other people's principals by construction,
	// which is the one request field notifications.proto reserves the name of:
	// every RPC on that surface reads the recipient off the connection, and a
	// method taking a list of them would be the hole in that. A client that
	// wants to know what handsets it has asks ListDevices, which is the same
	// question bounded by the person asking.
	ListDevicesByPrincipals(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, principals []string) ([]*Device, error)

	// RevokeDevice removes one of the principal's registrations through the
	// caller's transaction — a sign-out, or a device somebody no longer has — and
	// answers with the registration it removed. The row is deleted rather than
	// archived. A registration that is not there is an error wrapping
	// ErrDeviceNotFound and a nil Device, and a nil tx is an error wrapping
	// ErrNilExecutor.
	//
	// It is the write with the least choice about answering: the row is gone from
	// every executor the moment this transaction commits, so a caller recording
	// which handset stopped being addressable has nowhere else to read it from.
	// The row is therefore read before the delete rather than after, which is the
	// only order a deletion leaves available; the delete is keyed on the same
	// scope, principal and id, and guards on its own count, so a row that moved
	// between the two is ErrDeviceNotFound rather than a value handed back for a
	// deletion that did not happen.
	//
	// A sign-out is the ordinary caller, and a sign-out is several writes: the
	// session ends, the refresh token is revoked, the handset stops being
	// addressable. Those are one fact, and this is the write that joins them.
	RevokeDevice(ctx context.Context, tx database.Tx, scope tenancy.Scope, principal, deviceID string) (*Device, error)

	// InvalidateDeviceToken removes a token the provider has permanently
	// rejected, whoever it belongs to.
	//
	// It is the one method here that takes neither a scope nor an executor, and
	// both omissions are the point rather than an oversight.
	//
	// The scope first. What a provider hands back is a token: APNs answers a
	// push with Unregistered, FCM with UNREGISTERED, and neither knows or
	// reports which tenant's directory the handset was registered under. A
	// scoped variant would need the caller to already know the answer this
	// exists to act on, and a sender that guessed wrong would leave the dead
	// token in place. The token identifies one row across the whole registry —
	// see the unique index in notifications/migrations — so naming it is naming
	// exactly one device.
	//
	// The executor for the same reason one layer down. This is the registry
	// servicing itself: the caller is a send path reacting to a provider's
	// verdict, there is no consumer request behind it and so no transaction of
	// anybody's to join, and what the sender is doing at the moment it calls
	// this is a network round trip rather than a write. An implementation runs
	// it on a connection of its own; [SQLStore] uses the client it was built
	// with, which is what that constructor still takes one for. It is the
	// reading metering takes of its flush settlements and its reaper.
	//
	// It is idempotent: a token already gone is the state the caller asked for,
	// and it reports no error. That matters because the caller is a send path,
	// and a push must not fail because two workers pruned the same dead token.
	//
	// It takes the platform and token as plain strings, which is what
	// notifications/mobile.TokenInvalidator requires and what makes a Registry
	// wirable into a sender without either package importing the other.
	//
	// It is off the wire, and here the reason is security rather than shape.
	// This is the provider callback hook: what it acts on is a verdict APNs or
	// FCM actually returned, and the sender that calls it is the code that
	// received the verdict. Published as an RPC it becomes a call that deletes
	// any handset's registration, in any tenant, on the say-so of a caller
	// claiming a provider said so — because everything that makes the in-process
	// version safe, the scope it does not need and the transaction it does not
	// join, is exactly what makes the remote version unbounded. What a person
	// signing a handset out wants is RevokeDevice, which names a device they own
	// and is scoped and principal-bound like everything else on that surface.
	InvalidateDeviceToken(ctx context.Context, platform, token string) error
}
