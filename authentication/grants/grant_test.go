package grants

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestScopes_RoundTrip(T *testing.T) {
	T.Parallel()

	T.Run("a list reads back as itself", func(t *testing.T) {
		t.Parallel()

		scopes := []string{"openid", "email", "https://www.googleapis.com/auth/calendar"}

		encoded, err := encodeScopes(scopes)
		must.NoError(t, err)
		test.EqOp(t, "openid email https://www.googleapis.com/auth/calendar", encoded)
		test.Eq(t, scopes, decodeScopes(encoded))
	})

	T.Run("no scopes is the empty column", func(t *testing.T) {
		t.Parallel()

		encoded, err := encodeScopes(nil)
		must.NoError(t, err)
		test.EqOp(t, "", encoded)
		test.Nil(t, decodeScopes(encoded))
	})

	T.Run("a token that would not round-trip is refused", func(t *testing.T) {
		t.Parallel()

		for _, scope := range []string{"", "two words", "tab\there", `quo"te`, `back\slash`} {
			_, err := encodeScopes([]string{scope})
			test.ErrorIs(t, err, ErrInvalidGrantedScope, test.Sprintf("scope %q", scope))
		}
	})

	T.Run("a list longer than the column is refused", func(t *testing.T) {
		t.Parallel()

		_, err := encodeScopes([]string{strings.Repeat("s", MaxGrantedScopesLength+1)})
		test.ErrorIs(t, err, ErrValueTooLong)
	})
}

func TestRevocationReason_Valid(t *testing.T) {
	t.Parallel()

	test.True(t, RevokedByConsumer.valid())
	test.True(t, RevokedByProvider.valid())
	test.False(t, RevocationReason("").valid())
	test.False(t, RevocationReason("expired").valid())
}

func TestGrant_JSONCarriesNoToken(t *testing.T) {
	t.Parallel()

	grant := &Grant{AccessToken: "secret-access", RefreshToken: "secret-refresh", Provider: "google"}

	encoded, err := json.Marshal(grant)
	must.NoError(t, err)
	test.StrNotContains(t, string(encoded), "secret-access")
	test.StrNotContains(t, string(encoded), "secret-refresh")
	test.StrContains(t, string(encoded), "google")
}
