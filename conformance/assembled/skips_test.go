package assembled_test

import (
	"path"
	"slices"
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"
)

// expectedSkip is a skip a run of this harness is allowed to make, and why it
// is not a regression.
type expectedSkip struct {
	// test is the skipped test's name below the run, as t.Name spells it,
	// matched with path.Match so one entry can name each subtest of a read.
	test string
	// why is the reason it is expected. It is not compared against what the
	// skip printed; it is here for whoever reads the list.
	why string
}

// Every run of this harness sets every seam and action to the value that makes
// an assertion run, so a skip it makes is either one of these or a wiring
// regression. A suite that skips on what the server answered passes hardest
// when the server is broken, and a harness that never looked at which skips
// fired would be green through exactly that.
var (
	// pagedReadsWithNoPage are the paged reads a sweep cannot give arguments
	// that answer a page, in every run.
	pagedReadsWithNoPage = []expectedSkip{
		{
			test: "pagination/settings_ListValuesForDefinition/*",
			why:  "the read is keyed on a definition the sweep did not define, so the plain request is answered NotFound",
		},
		{
			test: "pagination/identity_ListInvitationsForEmailAddress/*",
			why:  "the read needs a verified address the sweep's caller does not have, so it is answered FailedPrecondition",
		},
	}

	// unwiredPrincipalPermissions is the one promise service cannot be asked
	// to keep: it builds identity's server with no permission resolver.
	unwiredPrincipalPermissions = expectedSkip{
		test: "identity/users/the_principal's_permissions_follow_the_account_the_read_resolved",
		why:  "service builds identity's server without WithPermissionResolver, so Seams.PrincipalPermissions is false",
	}

	// gatedHandles is the assertion only a deployment that lets a session move
	// an address reaches. service builds identity's server with the gate on,
	// so the claim it starts from is refused at UpdateProfile — which the
	// users assertions hold it to instead.
	gatedHandles = expectedSkip{
		test: "identity/invitations/a_caller_who_has_not_verified_their_address_is_refused_its_invitations",
		why:  "service builds identity's server with re-authenticated handles, so Seams.ReauthenticatedHandlesDisabled is false",
	}

	// openRegistration is the assertion only a deployment that closed its
	// sign-up door reaches. service builds sign-in's server with the door open,
	// so every registration the suites make goes through it instead.
	openRegistration = expectedSkip{
		test: "signin/registration/a_closed_sign-up_door_is_refused_by_name",
		why:  "the harness leaves sign-in's Registration block open, so service builds the server without WithoutOpenRegistration and Seams.RegistrationClosed is false",
	}

	// membersSkips are what the run reserving nothing may skip.
	membersSkips = append(slices.Clone(pagedReadsWithNoPage),
		unwiredPrincipalPermissions,
		gatedHandles,
		openRegistration,
		expectedSkip{
			test: "reservations",
			why:  "the run reserves nothing, so there is no reservation to hold it to",
		},
	)

	// staffSkips are what the run reserving staff calls may skip. Its
	// reserved calls are made by an administrator minted for them, so the
	// reservation itself costs no assertion here.
	staffSkips = append(slices.Clone(pagedReadsWithNoPage), unwiredPrincipalPermissions, gatedHandles, openRegistration)

	// rosterSkips are what the empty-requests run may skip: nothing.
	rosterSkips []expectedSkip

	// admittingSkips are what the run whose registrations issue a second
	// factor may skip. Its door is open too, so the closed-door assertion
	// skips there as it does everywhere.
	admittingSkips = []expectedSkip{
		openRegistration,
		{
			test: "signin/self/proving_a_second_factor_nobody_issued_is_refused_as_a_precondition",
			why:  "the run's registration policy issues every registrant a secret, so Seams.RegistrationIssuesSecondFactor is true and nobody holds none",
		},
	}

	// unconfirmedWaitlists are what a run whose waitlists wait at once may
	// skip besides, in both runs that reach the waitlists suite: with no Links
	// block there is no confirmation to follow, and Actions.WaitlistLinks is
	// nil because the harness declared it so.
	unconfirmedWaitlists = []expectedSkip{
		{
			test: "waitlists/the_signup_page/an_unconfirmed_signup_cannot_be_invited,_and_its_link_confirms_it_once",
			why:  "the run's waitlists wait at once, so Actions.WaitlistLinks is nil and no join is confirmed",
		},
		{
			test: "waitlists/the_signup_page/the_unsubscribe_link_in_the_confirmation_takes_the_address_off_the_list_with_nobody_signed_in",
			why:  "the run's waitlists wait at once, so no confirmation is mailed and it carries no unsubscribe link",
		},
	}
)

// suiteSkips is expected with the skips a run whose waitlists are configured
// as waitlists says added, for the two runs that reach the waitlists suite.
func suiteSkips(expected []expectedSkip, waitlists waitlistConfirmation) []expectedSkip {
	if waitlists == confirmsWaitlists {
		return expected
	}

	return append(slices.Clone(expected), unconfirmedWaitlists...)
}

// expectSkips holds the run t is about to make to the skips expected names:
// once every suite beneath it has finished, it fails t for every skip no entry
// names, and for every entry that did not fire — a stale entry is a skip
// somebody fixed, and left standing it would excuse the same skip coming back.
//
// A cleanup rather than a call after the run, because the suites run as
// parallel subtests: the run returns once they have paused, and only a cleanup
// runs after they have all finished.
func expectSkips(t *testing.T, expected []expectedSkip) {
	t.Helper()

	t.Cleanup(func() { checkSkips(t, conformance.Skips(t), expected) })
}

// checkSkips holds the skips a run made to the ones it expected.
func checkSkips(t *testing.T, made []conformance.Skipped, expected []expectedSkip) {
	t.Helper()

	unexpected, unfired := compareSkips(made, expected)

	for i := range unexpected {
		t.Errorf("conformance: %s skipped, and nothing on this harness's expected list names it: %s",
			unexpected[i].Test, unexpected[i].Reason)
	}

	// A run that already failed may have failed the test an entry names
	// rather than skipping it, so an unfired entry is only news in a green one.
	if t.Failed() {
		return
	}

	for i := range unfired {
		t.Errorf("conformance: the expected skip %q did not fire; if it was fixed, take it off the list (%s)",
			unfired[i].test, unfired[i].why)
	}
}

// compareSkips reports the skips made that no entry names, and the entries no
// skip made matched.
func compareSkips(made []conformance.Skipped, expected []expectedSkip) (unexpected []conformance.Skipped, unfired []expectedSkip) {
	fired := make([]bool, len(expected))

	for i := range made {
		matched := false

		for j := range expected {
			if ok, err := path.Match(expected[j].test, made[i].Test); err == nil && ok {
				fired[j], matched = true, true
			}
		}

		if !matched {
			unexpected = append(unexpected, made[i])
		}
	}

	for j := range expected {
		if !fired[j] {
			unfired = append(unfired, expected[j])
		}
	}

	return unexpected, unfired
}
