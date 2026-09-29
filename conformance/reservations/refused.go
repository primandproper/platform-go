package reservations

import (
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"
	"github.com/primandproper/platform-go/v14/identity/identitypb"
	"github.com/primandproper/platform-go/v14/settings/settingspb"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The reasons an entry in emptyRequestRefused gives.
const (
	identityTarget = "identity's target authorizer refuses an empty account, user or invitation id " +
		"as a target the caller may not act on"
	settingsSubject = "settings' subject authorizer refuses an empty subject as one the caller may not act on"
)

// emptyRequestRefused are the calls on this module's surfaces whose handler
// refuses an empty request as PermissionDenied whoever makes it, each with the
// reason.
//
// It is what lets assertReserved tell a handler's refusal from a deployment
// refusing its own administrator. Both answer the control's empty request with
// PermissionDenied, and read off the answer alone they are the same skip — so a
// deployment whose interceptor refused administrators every reserved call
// would skip every entry and pass. Written down here, the skip is a decision
// this module made about its own handlers, and an administrator refused a call
// not listed is the failure it looks like.
//
// An entry is a claim about the handler behind the default seams, and
// RosterSuite holds each one to it on this module's assembled subject, so a
// handler that stops refusing an empty request reds there rather than leaving
// an entry here that excuses nothing.
var emptyRequestRefused = map[string]string{
	identitypb.IdentityService_ArchiveAccount_FullMethodName:           identityTarget,
	identitypb.IdentityService_CancelInvitation_FullMethodName:         identityTarget,
	identitypb.IdentityService_GetAccount_FullMethodName:               identityTarget,
	identitypb.IdentityService_GetInvitation_FullMethodName:            identityTarget,
	identitypb.IdentityService_GetMembership_FullMethodName:            identityTarget,
	identitypb.IdentityService_Invite_FullMethodName:                   identityTarget,
	identitypb.IdentityService_ListAccountMembers_FullMethodName:       identityTarget,
	identitypb.IdentityService_ListAccountsForUser_FullMethodName:      identityTarget,
	identitypb.IdentityService_ListMembershipsForUser_FullMethodName:   identityTarget,
	identitypb.IdentityService_RemoveMembership_FullMethodName:         identityTarget,
	identitypb.IdentityService_SetMembershipRoles_FullMethodName:       identityTarget,
	identitypb.IdentityService_TransferAccountOwnership_FullMethodName: identityTarget,

	settingspb.SettingsService_ClearValue_FullMethodName:           settingsSubject,
	settingspb.SettingsService_GetValue_FullMethodName:             settingsSubject,
	settingspb.SettingsService_ListValuesForSubject_FullMethodName: settingsSubject,
	settingspb.SettingsService_Resolve_FullMethodName:              settingsSubject,
	settingspb.SettingsService_ResolveAll_FullMethodName:           settingsSubject,
	settingspb.SettingsService_SetValue_FullMethodName:             settingsSubject,
}

// RosterSuite holds this package's record of which handlers refuse an empty
// request to what those handlers do: for each call it lists whose surface the
// subject mounts, an administrator's empty request is refused as
// PermissionDenied.
//
// It is for this module's own assembled subject, and is not among
// conformance/all's suites. The refusals it lists come from row authorizers,
// which are seams: a consumer who supplies its own may answer an empty target
// some other way, and Suite takes that as the control passing rather than as a
// broken deployment. Where the authorizers are this module's, a listed call
// that answers otherwise is an entry that no longer describes its handler.
func RosterSuite() conformance.Suite {
	return conformance.Suite{
		Name: "reservations roster",

		// Which of the listed calls' surfaces are mounted is read inside.
		Mounted: func(conformance.Surfaces) bool { return true },
		Run:     runRoster,
	}
}

func runRoster(t *testing.T, s *conformance.Session) {
	t.Helper()

	probe := s.Subject(t)

	for full, reason := range emptyRequestRefused {
		t.Run(full, func(t *testing.T) {
			t.Parallel()

			surface, method := resolve(t, full)
			if !surface.Mounted(probe.Surfaces) {
				t.Skipf("conformance: this subject mounts no %s surface to make %s on", surface.Name, full)
			}

			admin := s.Subject(t, conformance.AsAdmin(), conformance.Making(full))

			err := call(admin.Context(t.Context()), admin.Conn, full, method)
			if code := status.Code(err); code != codes.PermissionDenied {
				t.Errorf("%s answered an administrator's empty request with %s (%v), and emptyRequestRefused says %s; "+
					"delete the entry, since it now excuses a skip nothing causes", full, code, err, reason)
			}
		})
	}
}
