package grpc_test

import (
	"slices"
	"testing"

	notificationsgrpc "github.com/primandproper/platform-go/v14/notifications/grpc"
	"github.com/primandproper/platform-go/v14/notifications/notificationspb"

	"github.com/primandproper/primitives-go/v2/authorization"
	authzgrpc "github.com/primandproper/primitives-go/v2/authorization/grpc"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// serviceMethods is every RPC the generated service descriptor declares, as the
// full method names the interceptor matches on.
//
// It is read off the descriptor rather than written down, which is the whole
// point: a list here would be a third place to forget an RPC, and the tests
// below exist because the first two are easy to forget.
func serviceMethods() []string {
	prefix := "/" + notificationspb.NotificationsService_ServiceDesc.ServiceName + "/"

	out := make([]string, 0, len(notificationspb.NotificationsService_ServiceDesc.Methods))
	for _, m := range notificationspb.NotificationsService_ServiceDesc.Methods {
		out = append(out, prefix+m.MethodName)
	}

	return out
}

// TestEveryMethodIsPermissioned is the reason this file exists, and it is the
// decision this service made rather than a property it happens to have.
//
// Every RPC here acts on the caller's own rows, and it would have been easy to
// conclude that owning the row is the authorization and leave the map half
// empty. That is precisely the arrangement authentication/oauth2clients withdrew
// a whole self-service half over: owning the row answers which rows, not whether
// this deployment offers the call at all. A build with no mobile client grants
// nobody the device permissions.
func TestEveryMethodIsPermissioned(T *testing.T) {
	T.Parallel()

	methods := serviceMethods()
	must.SliceNotEmpty(T, methods, must.Sprint("no methods on the service descriptor, so this asserted nothing"))

	permissions := notificationsgrpc.Permissions()

	for _, method := range methods {
		T.Run(method, func(t *testing.T) {
			t.Parallel()

			_, permissioned := permissions[method]
			test.True(t, permissioned, test.Sprintf(
				"%s is not in Permissions, so nothing says who may call it", method))
		})
	}
}

// TestNoDecisionOutlivesItsMethod is the other direction: a decision naming an
// RPC that no longer exists is a permission a consumer is still granting for
// nothing, and a rename would leave the real method undeclared and therefore
// denied.
func TestNoDecisionOutlivesItsMethod(T *testing.T) {
	T.Parallel()

	methods := serviceMethods()

	for method := range notificationsgrpc.Permissions() {
		test.True(T, slices.Contains(methods, method), test.Sprintf(
			"Permissions names %s, which the service descriptor does not declare", method))
	}
}

// TestNoPermissionIsEmpty catches the decision that looks made and is not: an
// entry mapping a method to an empty slice declares it and requires nothing.
func TestNoPermissionIsEmpty(T *testing.T) {
	T.Parallel()

	for method, permissions := range notificationsgrpc.Permissions() {
		test.SliceNotEmpty(T, permissions, test.Sprintf(
			"%s is declared with no permissions, which grants it to everybody", method))
	}
}

// TestTheTwoHalvesDoNotShareAGrant is what makes the device half separately
// deniable, which is the point of it having grants of its own.
//
// A deployment with a web client and no mobile application grants the three
// inbox permissions and none of the device ones, and its three device RPCs are
// closed by policy rather than by an interceptor that has to be told to skip
// them.
func TestTheTwoHalvesDoNotShareAGrant(T *testing.T) {
	T.Parallel()

	permissions := notificationsgrpc.Permissions()

	inbox := []authorization.Permission{}
	devices := []authorization.Permission{}

	for _, method := range []string{
		notificationspb.NotificationsService_ListNotifications_FullMethodName,
		notificationspb.NotificationsService_ListUnreadNotifications_FullMethodName,
		notificationspb.NotificationsService_GetNotification_FullMethodName,
		notificationspb.NotificationsService_MarkNotificationRead_FullMethodName,
		notificationspb.NotificationsService_MarkAllNotificationsRead_FullMethodName,
		notificationspb.NotificationsService_ArchiveNotification_FullMethodName,
	} {
		inbox = append(inbox, permissions[method]...)
	}

	for _, method := range []string{
		notificationspb.NotificationsService_RegisterDevice_FullMethodName,
		notificationspb.NotificationsService_ListDevices_FullMethodName,
		notificationspb.NotificationsService_RevokeDevice_FullMethodName,
	} {
		devices = append(devices, permissions[method]...)
	}

	must.SliceNotEmpty(T, inbox)
	must.SliceNotEmpty(T, devices)

	for _, p := range devices {
		test.SliceNotContains(T, inbox, p, test.Sprintf(
			"%s is required by an inbox RPC as well as a device one", p))
	}
}

// TestArchivingIsNotReadingOrMarking is the split inside the inbox half.
//
// Reading is a stamp a digest consults; archiving takes a row out of the inbox
// for good. A consumer that wanted them to be one grant maps them to one, which
// is what Permissions returning a fresh map is for.
func TestArchivingIsNotReadingOrMarking(T *testing.T) {
	T.Parallel()

	permissions := notificationsgrpc.Permissions()

	archive := permissions[notificationspb.NotificationsService_ArchiveNotification_FullMethodName]
	must.SliceNotEmpty(T, archive)

	test.SliceNotContains(T, archive, notificationsgrpc.PermissionReadInbox)
	test.SliceNotContains(T, archive, notificationsgrpc.PermissionMarkInboxRead)
}

// TestTheThreeReadsShareOneGrant is the deliberate collapse, and it is the one a
// consumer is most likely to want to undo — which they can, on their own copy.
func TestTheThreeReadsShareOneGrant(T *testing.T) {
	T.Parallel()

	permissions := notificationsgrpc.Permissions()

	for _, method := range []string{
		notificationspb.NotificationsService_ListNotifications_FullMethodName,
		notificationspb.NotificationsService_ListUnreadNotifications_FullMethodName,
		notificationspb.NotificationsService_GetNotification_FullMethodName,
	} {
		test.SliceContains(T, permissions[method], notificationsgrpc.PermissionReadInbox,
			test.Sprintf("%s does not require the inbox read grant", method))
	}
}

// TestPermissionsReturnsAFreshMap keeps a consumer's override from editing every
// other consumer's copy in the same process.
func TestPermissionsReturnsAFreshMap(T *testing.T) {
	T.Parallel()

	first := notificationsgrpc.Permissions()
	for method := range first {
		delete(first, method)
	}

	test.MapNotEmpty(T, notificationsgrpc.Permissions(),
		test.Sprint("Permissions handed back a map a caller could empty for everybody"))
}

// TestRequireDeclaresEveryMethod is the check a consumer writing the RequireAll
// themselves would not have. authorization/grpc is fail-closed, so a method
// Require left out is denied, and a denial for want of a declaration looks
// exactly like a policy somebody meant.
func TestRequireDeclaresEveryMethod(T *testing.T) {
	T.Parallel()

	reqs, err := notificationsgrpc.Require(authzgrpc.NewRequirements()).Build()
	must.NoError(T, err)

	declared := reqs.Methods()

	for _, method := range serviceMethods() {
		test.SliceContains(T, declared, method, test.Sprintf(
			"%s is not declared after Require, so the enforcer will deny it as undeclared", method))
	}
}

// TestRequireToleratesANilBuilder keeps a chain composing several domains'
// fragments from needing a nil check per link.
func TestRequireToleratesANilBuilder(T *testing.T) {
	T.Parallel()

	test.Nil(T, notificationsgrpc.Require(nil))
}
