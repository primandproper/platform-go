/*
Package all links every conformance suite this module ships.

It is a package of its own so that linking all of them is a choice. A consumer
wiring one surface imports that surface's suite and links one set of protobuf
bindings; a consumer running a whole service imports this and gets the lot,
with the ones they did not mount skipping themselves.

	conformanceall.Run(t, seams)

It is the same shape service.Register has one level up — what runs is what the
subject turned out to have, and an absence is an absence rather than a failure.
*/
package all

import (
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"
	conformanceanonymous "github.com/primandproper/platform-go/v14/conformance/anonymous"
	conformanceaudit "github.com/primandproper/platform-go/v14/conformance/audit"
	conformancebilling "github.com/primandproper/platform-go/v14/conformance/billing"
	conformancecomments "github.com/primandproper/platform-go/v14/conformance/comments"
	conformancedataprivacy "github.com/primandproper/platform-go/v14/conformance/dataprivacy"
	conformancefilters "github.com/primandproper/platform-go/v14/conformance/filters"
	conformanceidentity "github.com/primandproper/platform-go/v14/conformance/identity"
	conformanceissuereports "github.com/primandproper/platform-go/v14/conformance/issuereports"
	conformancemediaregistry "github.com/primandproper/platform-go/v14/conformance/mediaregistry"
	conformancenotifications "github.com/primandproper/platform-go/v14/conformance/notifications"
	conformanceoauth2clients "github.com/primandproper/platform-go/v14/conformance/oauth2clients"
	conformancepagination "github.com/primandproper/platform-go/v14/conformance/pagination"
	conformancepasswordreset "github.com/primandproper/platform-go/v14/conformance/passwordreset"
	conformancesettings "github.com/primandproper/platform-go/v14/conformance/settings"
	conformancesignin "github.com/primandproper/platform-go/v14/conformance/signin"
	conformancewaitlists "github.com/primandproper/platform-go/v14/conformance/waitlists"
	conformancewebhooks "github.com/primandproper/platform-go/v14/conformance/webhooks"
)

// Suites is every suite, in the order they run.
//
// A slice rather than a registry: what is in it is readable from this file,
// and a suite joins by being named here rather than by an init somewhere that
// linking it would fire.
func Suites() []conformance.Suite {
	return []conformance.Suite{
		// The cross-cutting one goes first: it reads every mounted surface's
		// descriptor, so a subject whose wiring refuses everybody finds out
		// here rather than in twelve surfaces' worth of confusing failures.
		conformanceanonymous.Suite(),
		conformancefilters.Suite(),
		conformancepagination.Suite(),

		conformanceaudit.Suite(),
		conformanceidentity.Suite(),
		conformancedataprivacy.Suite(),
		conformancemediaregistry.Suite(),
		conformancesettings.Suite(),
		conformancebilling.Suite(),
		conformanceoauth2clients.Suite(),
		conformancepasswordreset.Suite(),
		conformancewaitlists.Suite(),
		conformanceissuereports.Suite(),
		conformancenotifications.Suite(),
		conformancecomments.Suite(),
		conformancewebhooks.Suite(),
		conformancesignin.Suite(),
	}
}

// Run asserts every suite against the subject the seams describe.
//
//nolint:gocritic // hugeParam: Seams is taken by value, as conformance.Run takes it, so a subject cannot change what a run holds after handing it over
func Run(t *testing.T, seams conformance.Seams) {
	t.Helper()

	conformance.Run(t, seams, Suites()...)
}
