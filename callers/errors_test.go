package callers_test

import (
	"testing"

	"github.com/primandproper/platform-go/v14/callers"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"

	"github.com/shoenig/test"
)

// TestTargetNotPermittedSurvivesWrapping pins what every authorizer in the
// module is promised on the way in: a refusal wrapped with context of its own is
// still a refusal.
//
// Each surface distinguishes "refused" from "could not decide" with errors.Is
// and answers them with different codes — PermissionDenied or NotFound for the
// first, Internal for the second — so an implementation that added context and
// stopped matching would have its refusal answered as an unavailable database,
// which tells a consumer to widen a policy that was working.
func TestTargetNotPermittedSurvivesWrapping(T *testing.T) {
	T.Parallel()

	wrapped := platformerrors.Wrap(callers.ErrTargetNotPermitted, "reading the caller's memberships")

	test.ErrorIs(T, wrapped, callers.ErrTargetNotPermitted)
}

// TestTargetNotPermittedKeepsItsWording pins the message, which is load-bearing
// for a reason that has nothing to do with anybody reading it.
//
// This sentinel is raised by a server and matched by clients across a gRPC
// connection, where the value the server declared cannot survive: the encoding
// interceptor rebuilds it in another process, and what errors.Is matches on is
// the mark — the message cockroachdb recorded for it. So the wording is the
// identity, it is unique in this module, and editing it is a wire change rather
// than a copy edit. internal/sentinelmatrix's uniqueness test is the other half
// of that rule.
func TestTargetNotPermittedKeepsItsWording(T *testing.T) {
	T.Parallel()

	test.EqError(T, callers.ErrTargetNotPermitted, "the caller may not act on the named target")
}
