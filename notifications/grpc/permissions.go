package grpc

import (
	"github.com/primandproper/platform-go/v14/notifications/notificationspb"

	"github.com/primandproper/primitives-go/v2/authorization"
	authzgrpc "github.com/primandproper/primitives-go/v2/authorization/grpc"
)

// The permissions this service's methods require, in authorization's
// vocabulary.
//
// They are declared here rather than in the authorization package because
// authorization is a primitive: it owns what a Permission *is* — a string, a
// set, a role that inherits — and owns no domain's names.
//
// The strings are dotted and namespaced so that a consumer composing several
// domains' fragments cannot have two of them collide on "read". They are values
// rather than an enum because a consumer's policy is data — a YAML file, a table
// of roles — and it has to be able to name one without importing Go.
//
// Every RPC here acts on the caller's own rows, and every one of them still
// requires a grant. That is the finding authentication/oauth2clients recorded
// when it withdrew a self-service half reachable behind no permission at all:
// owning the row answers which rows, not whether this deployment offers the
// call. A build with no mobile client grants nobody the device permissions and
// the three device RPCs are closed, which is a decision in the policy rather
// than an interceptor that has to be told to skip them.
//
// Six of them over nine RPCs, and the two collapses are deliberate. Each read
// and its list share one grant, because they answer the same question at two
// cardinalities and a grant that separated them would let a consumer allow
// enumeration while forbidding the read it enumerates into. A consumer who wants
// the page behind a stronger grant than the get overrides the map, which is what
// [Permissions] returning a fresh one is for.
const (
	// PermissionReadInbox covers reading one notification and paging the inbox,
	// unread included.
	//
	// It is the grant a bell icon needs and the one every signed-in caller in a
	// typical policy holds. What it discloses is what somebody was told, which is
	// bounded by the principal on the connection: it never reaches another
	// person's inbox, on any of the three RPCs it covers.
	PermissionReadInbox authorization.Permission = "notifications.inbox.read"

	// PermissionMarkInboxRead covers stamping one notification read and stamping
	// them all.
	//
	// One grant for both because they are the same act at two cardinalities, and
	// because the second is the one a client makes when somebody opens the panel
	// — a policy that allowed the single and forbade the bulk would produce an
	// inbox that empties one row at a time and errors on the button that says
	// "mark all read".
	PermissionMarkInboxRead authorization.Permission = "notifications.inbox.mark_read"

	// PermissionArchiveInbox covers dismissing a notification.
	//
	// It is separate from marking one read because they are different amounts of
	// trust over the same row: reading is a stamp a digest consults, and
	// archiving takes the row out of the inbox for good. The row itself survives
	// — this package archives rather than deletes, so what somebody was told is
	// still answerable — and that is what keeps this a lighter grant than it
	// looks.
	PermissionArchiveInbox authorization.Permission = "notifications.inbox.archive"

	// PermissionRegisterDevices covers a handset registering itself.
	//
	// It is the grant a mobile client holds and the only write on the registry
	// half a person's own credential makes. It cannot register a handset to
	// anybody else: the principal comes off the connection and
	// notifications.proto has no field for one.
	PermissionRegisterDevices authorization.Permission = "notifications.devices.register"

	// PermissionReadDevices covers paging the caller's own registrations.
	//
	// It discloses no device token, because no message on this surface carries
	// one. What it does disclose is how many handsets somebody has and when each
	// was last seen, which is why it is a grant and not a method every caller
	// reaches.
	PermissionReadDevices authorization.Permission = "notifications.devices.read"

	// PermissionRevokeDevices covers removing one of the caller's registrations.
	//
	// A sign-out is the ordinary caller, and a sign-out is several writes — the
	// session ends, the refresh token is revoked, the handset stops being
	// addressable. A deployment whose sign-out runs in-process grants this to
	// nobody and calls the store directly; one whose mobile client tidies up
	// after itself grants it to the person signing out.
	PermissionRevokeDevices authorization.Permission = "notifications.devices.revoke"
)

// Permissions is the default map from method name to what it requires: every
// RPC this service declares, and nothing else.
//
// Every method is in it. There is no second set of methods that require nothing
// — this service serves no RPC a caller reaches without a grant — so a method
// missing from this map is a bug rather than a decision, and the suite reads the
// service descriptor rather than a list in order to say so.
//
// The keys are the generated full method name constants rather than strings, so
// an RPC renamed in the .proto is a compile error here instead of a method that
// silently requires nothing.
//
// It returns a fresh map each call, so a consumer composing it into their own
// policy and then overriding an entry is editing their copy.
func Permissions() map[string][]authorization.Permission {
	return map[string][]authorization.Permission{
		notificationspb.NotificationsService_ListNotifications_FullMethodName:       {PermissionReadInbox},
		notificationspb.NotificationsService_ListUnreadNotifications_FullMethodName: {PermissionReadInbox},
		notificationspb.NotificationsService_GetNotification_FullMethodName:         {PermissionReadInbox},
		notificationspb.NotificationsService_MarkNotificationRead_FullMethodName:    {PermissionMarkInboxRead},
		notificationspb.NotificationsService_MarkAllNotificationsRead_FullMethodName: {
			PermissionMarkInboxRead,
		},
		notificationspb.NotificationsService_ArchiveNotification_FullMethodName: {PermissionArchiveInbox},
		notificationspb.NotificationsService_RegisterDevice_FullMethodName:      {PermissionRegisterDevices},
		notificationspb.NotificationsService_ListDevices_FullMethodName:         {PermissionReadDevices},
		notificationspb.NotificationsService_RevokeDevice_FullMethodName:        {PermissionRevokeDevices},
	}
}

// Require declares every method of this service on a requirements builder.
//
// It is one line over Permissions today and is the exported name anyway, because
// authorization/grpc is fail-closed: a method declared nowhere is denied, and
// what a consumer needs is a call that stays correct when this service's method
// set changes.
//
// A nil builder is tolerated and returns nil, so composing several domains'
// fragments in a chain does not need a nil check per link.
func Require(b *authzgrpc.RequirementsBuilder) *authzgrpc.RequirementsBuilder {
	if b == nil {
		return nil
	}

	return b.RequireAll(Permissions())
}
