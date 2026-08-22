package oauth2server_test

import (
	"testing"

	"github.com/primandproper/platform-go/v13/authentication/oauth2server"

	"github.com/shoenig/test"
)

// A nil clone is nil rather than a panic, on all four records.
//
// This is what lets a store write `return record.Clone(), err` on the path where
// the record is absent — which is most of what these methods return — instead of
// branching on nil at every call site and getting one of them wrong.
func TestClone_Nil(T *testing.T) {
	T.Parallel()

	T.Run("a nil record clones to nil", func(t *testing.T) {
		t.Parallel()

		var (
			client  *oauth2server.Client
			code    *oauth2server.AuthorizationCode
			access  *oauth2server.AccessToken
			refresh *oauth2server.RefreshToken
		)

		test.Nil(t, client.Clone())
		test.Nil(t, code.Clone())
		test.Nil(t, access.Clone())
		test.Nil(t, refresh.Clone())
	})

	T.Run("a nil client is public", func(t *testing.T) {
		t.Parallel()

		// The same reasoning: an absent registration holds no secret, so the
		// caller checking Public does not also have to check for nil.
		var client *oauth2server.Client
		test.True(t, client.Public())
	})
}
