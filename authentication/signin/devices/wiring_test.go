package devices

import (
	"context"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v15/authentication/signin"
	"github.com/primandproper/platform-go/v15/identity"

	"github.com/primandproper/primitives-go/v2/database"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// TestWiring is the three calls the package documentation tells a deployment to
// write, against a real table: the hook records on the sign-in's transaction,
// a refresh renews the same row, and the annotator reads it back as the
// attributes a listing answers each login with.
func TestWiring(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	origin := browser
	hooks, err := NewHooks(nil, h.store, func(context.Context) Origin { return origin })
	must.NoError(t, err)

	annotate, err := NewAnnotator(h.store, h.client.Reader())
	must.NoError(t, err)

	mint := func(signIn *signin.SignIn) {
		t.Helper()

		must.NoError(t, h.client.WithTransaction(t.Context(), func(tx database.Tx) error {
			return hooks.AfterIssueToken(t.Context(), tx, testScope(), signIn)
		}))
	}

	signIn := &signin.SignIn{
		Principal:             &identity.Principal{User: &identity.User{ID: testUser}},
		FamilyID:              "family_wired",
		ExpiresAt:             h.clock.Now().Add(time.Hour),
		RefreshTokenExpiresAt: h.clock.Now().Add(testTTL),
	}

	mint(signIn)

	// A refresh from another network is the same login, renewed.
	origin = Origin{IPAddress: "198.51.100.4", UserAgent: "Mozilla/5.0"}
	h.clock.advance(time.Hour)
	mint(signIn)

	// An impersonation of the same person records nothing for the subject to be
	// shown.
	impersonation := *signIn
	impersonation.FamilyID = "family_impersonated"
	impersonation.ActorID = "operator_1"
	mint(&impersonation)

	attributes, err := annotate(t.Context(), testScope(), testUser, []string{"family_wired", "family_impersonated"})
	must.NoError(t, err)

	test.Eq(t, map[string]map[string]string{
		"family_wired": {AttributeIPAddress: "198.51.100.4", AttributeUserAgent: "Mozilla/5.0"},
	}, attributes)
}
