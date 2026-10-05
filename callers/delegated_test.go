package callers_test

import (
	"testing"

	"github.com/primandproper/platform-go/v15/callers"

	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
)

// delegatedPrincipal is a session type that carries the second slot on every
// value, delegated or not.
type delegatedPrincipal struct {
	actorID string
	testPrincipal
}

var _ callers.Delegated = (*delegatedPrincipal)(nil)

func (p *delegatedPrincipal) ActorID() string { return p.actorID }

func TestActorOf(T *testing.T) {
	T.Parallel()

	subject := testPrincipal{userID: "subject", scope: tenancy.Global()}

	cases := map[string]struct {
		principal     callers.Principal
		wantActor     string
		wantDelegated string
	}{
		"nobody": {
			principal: nil,
		},
		"a principal with no notion of delegation acts for itself": {
			principal: &subject,
			wantActor: "subject",
		},
		"an empty actor is not delegated": {
			principal: &delegatedPrincipal{testPrincipal: subject},
			wantActor: "subject",
		},
		"an actor naming the user is the user acting for themselves": {
			principal: &delegatedPrincipal{actorID: "subject", testPrincipal: subject},
			wantActor: "subject",
		},
		"a delegated principal is acted for by its actor": {
			principal:     &delegatedPrincipal{actorID: "operator", testPrincipal: subject},
			wantActor:     "operator",
			wantDelegated: "operator",
		},
	}

	for name, tc := range cases {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			test.EqOp(t, tc.wantActor, callers.ActorOf(tc.principal))
			test.EqOp(t, tc.wantDelegated, callers.DelegatedActor(tc.principal))

			if tc.principal != nil {
				test.EqOp(t, "subject", tc.principal.UserID(),
					test.Sprint("delegation never moves the subject out of UserID"))
			}
		})
	}
}
