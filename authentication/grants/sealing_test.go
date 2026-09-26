package grants

import (
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/grants/internal/queries"

	"github.com/primandproper/primitives-go/v2/cryptography/encryption"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// TestSealing_BindsTheRowAndTheColumn is the associated data, checked one part
// at a time: a ciphertext opens where it was sealed and nowhere else.
func TestSealing_BindsTheRowAndTheColumn(T *testing.T) {
	T.Parallel()

	store := &SQLStore{encryptor: newTestEncryptor(T)}

	sealed, err := store.seal(T.Context(), testScope, "studio_1", "google", queries.AccessTokenColumn, "token")
	must.NoError(T, err)

	T.Run("opens where it was sealed", func(t *testing.T) {
		t.Parallel()

		opened, openErr := store.open(t.Context(), testScope, "studio_1", "google", queries.AccessTokenColumn, sealed)
		must.NoError(t, openErr)
		test.EqOp(t, "token", opened)
	})

	for name, where := range map[string]struct {
		subject, provider string
		column            string
		scope             tenancy.Scope
	}{
		"another scope":    {scope: otherScope, subject: "studio_1", provider: "google", column: queries.AccessTokenColumn},
		"the global scope": {scope: tenancy.Global(), subject: "studio_1", provider: "google", column: queries.AccessTokenColumn},
		"another subject":  {scope: testScope, subject: "studio_2", provider: "google", column: queries.AccessTokenColumn},
		"another provider": {scope: testScope, subject: "studio_1", provider: "microsoft", column: queries.AccessTokenColumn},
		"the other column": {scope: testScope, subject: "studio_1", provider: "google", column: queries.RefreshTokenColumn},
	} {
		T.Run("does not open in "+name, func(t *testing.T) {
			t.Parallel()

			_, openErr := store.open(t.Context(), where.scope, where.subject, where.provider, where.column, sealed)
			test.ErrorIs(t, openErr, encryption.ErrAuthenticationFailed)
		})
	}
}

// TestAssociatedData_IsUnambiguous is why the parts are length-prefixed: a
// delimiter would let two different tuples encode the same bytes by moving it
// from one field into the next.
func TestAssociatedData_IsUnambiguous(t *testing.T) {
	t.Parallel()

	test.NotEq(t,
		associatedData(testScope, "a:b", "c", queries.AccessTokenColumn),
		associatedData(testScope, "a", "b:c", queries.AccessTokenColumn),
	)
	test.NotEq(t,
		associatedData(testScope, "ab", "", queries.AccessTokenColumn),
		associatedData(testScope, "a", "b", queries.AccessTokenColumn),
	)
}
