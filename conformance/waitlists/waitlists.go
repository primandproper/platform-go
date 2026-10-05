package waitlists

import (
	"testing"

	"github.com/primandproper/platform-go/v15/conformance"
	"github.com/primandproper/platform-go/v15/waitlists/waitlistspb"
)

// surface is this suite's name, and the key a subject's per-surface scope is
// read by.
const surface = "waitlists"

// The calls this suite makes, as the names a caller is minted to make them by,
// or that a visitor reaches with nobody on the call.
const (
	archiveList               = waitlistspb.WaitlistsService_ArchiveList_FullMethodName
	archiveSignup             = waitlistspb.WaitlistsService_ArchiveSignup_FullMethodName
	confirmSignup             = waitlistspb.WaitlistsService_Confirm_FullMethodName
	convert                   = waitlistspb.WaitlistsService_Convert_FullMethodName
	createList                = waitlistspb.WaitlistsService_CreateList_FullMethodName
	getList                   = waitlistspb.WaitlistsService_GetList_FullMethodName
	getSignup                 = waitlistspb.WaitlistsService_GetSignup_FullMethodName
	getSignupByContact        = waitlistspb.WaitlistsService_GetSignupByContact_FullMethodName
	invite                    = waitlistspb.WaitlistsService_Invite_FullMethodName
	joinList                  = waitlistspb.WaitlistsService_Join_FullMethodName
	listLists                 = waitlistspb.WaitlistsService_ListLists_FullMethodName
	listOpenLists             = waitlistspb.WaitlistsService_ListOpenLists_FullMethodName
	listSignups               = waitlistspb.WaitlistsService_ListSignups_FullMethodName
	listSignupsForSubject     = waitlistspb.WaitlistsService_ListSignupsForSubject_FullMethodName
	unsubscribe               = waitlistspb.WaitlistsService_Unsubscribe_FullMethodName
	updateList                = waitlistspb.WaitlistsService_UpdateList_FullMethodName
	updateSignupNotes         = waitlistspb.WaitlistsService_UpdateSignupNotes_FullMethodName
	withdraw                  = waitlistspb.WaitlistsService_Withdraw_FullMethodName
	withdrawSignupsForSubject = waitlistspb.WaitlistsService_WithdrawSignupsForSubject_FullMethodName
)

// Suite is the signup surface's behavioral assertions.
func Suite() conformance.Suite {
	return conformance.Suite{
		Name:    surface,
		Mounted: func(s conformance.Surfaces) bool { return s.Waitlists != nil },
		Run:     run,
	}
}

func run(t *testing.T, s *conformance.Session) {
	t.Helper()

	t.Run("lists", func(t *testing.T) {
		t.Parallel()
		lists(t, s)
	})
	t.Run("signups", func(t *testing.T) {
		t.Parallel()
		signups(t, s)
	})
	t.Run("the signup page", func(t *testing.T) {
		t.Parallel()
		signupPage(t, s)
	})
	t.Run("erasure", func(t *testing.T) {
		t.Parallel()
		erasure(t, s)
	})
}
