package oauth2server_test

import (
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v13/authentication/oauth2server"

	"github.com/shoenig/test"
)

// registrationURIs renders n distinct registrable callbacks.
func registrationURIs(n int) []string {
	uris := make([]string, 0, n)
	for i := range n {
		uris = append(uris, "https://client.example/cb"+string(rune('a'+i%26))+string(rune('a'+i/26)))
	}

	return uris
}

// The bounds the shipped policy enforces, at the boundary rather than near it.
//
// Each is a limit on what one registration costs, and each is checked exactly at
// the number: a bound that refused the value it names would make the constant a
// lie for every reader who took it at its word.
func TestDefaultRegistrationPolicy(T *testing.T) {
	T.Parallel()

	T.Run("a nil request is refused rather than dereferenced", func(t *testing.T) {
		t.Parallel()

		// A replacement policy that delegates to this one has no way to promise
		// its caller passed something.
		test.ErrorIs(t,
			oauth2server.DefaultRegistrationPolicy.AllowRegistration(t.Context(), nil),
			oauth2server.ErrNilRecord)
	})

	T.Run("a client_name at the limit is accepted and one past it is not", func(t *testing.T) {
		t.Parallel()

		at := &oauth2server.RegistrationRequest{
			ClientName:   strings.Repeat("n", oauth2server.MaxClientNameLength),
			RedirectURIs: []string{testRedirectURI},
		}
		test.NoError(t, oauth2server.DefaultRegistrationPolicy.AllowRegistration(t.Context(), at))

		over := &oauth2server.RegistrationRequest{
			ClientName:   strings.Repeat("n", oauth2server.MaxClientNameLength+1),
			RedirectURIs: []string{testRedirectURI},
		}
		test.ErrorIs(t,
			oauth2server.DefaultRegistrationPolicy.AllowRegistration(t.Context(), over),
			oauth2server.ErrClientNameTooLong)
	})

	T.Run("a redirect_uri count at the limit is accepted and one past it is not", func(t *testing.T) {
		t.Parallel()

		at := &oauth2server.RegistrationRequest{RedirectURIs: registrationURIs(oauth2server.MaxRedirectURIs)}
		test.NoError(t, oauth2server.DefaultRegistrationPolicy.AllowRegistration(t.Context(), at))

		over := &oauth2server.RegistrationRequest{RedirectURIs: registrationURIs(oauth2server.MaxRedirectURIs + 1)}
		test.ErrorIs(t,
			oauth2server.DefaultRegistrationPolicy.AllowRegistration(t.Context(), over),
			oauth2server.ErrTooManyRedirectURIs)
	})
}

func TestValidateRedirectURI_Bounds(T *testing.T) {
	T.Parallel()

	T.Run("a URI at the length limit is accepted and one past it is not", func(t *testing.T) {
		t.Parallel()

		const prefix = "https://client.example/"

		at := prefix + strings.Repeat("p", oauth2server.MaxRedirectURILength-len(prefix))
		test.NoError(t, oauth2server.ValidateRedirectURI(at))

		test.ErrorIs(t, oauth2server.ValidateRedirectURI(at+"p"), oauth2server.ErrRedirectURITooLong)
	})

	T.Run("a URI that does not parse is refused as unmatchable", func(t *testing.T) {
		t.Parallel()

		// Not "insecure": there is nothing here to compare exactly against,
		// which is what a client author needs to hear rather than being sent
		// looking for a TLS problem.
		test.ErrorIs(t, oauth2server.ValidateRedirectURI("https://[::1"), oauth2server.ErrRedirectURINotAbsolute)
	})

	T.Run("an https URI with no host is refused", func(t *testing.T) {
		t.Parallel()

		// Parses, has the right scheme, and names nowhere. An exact match
		// against this would match nothing a browser could ever be sent to.
		test.ErrorIs(t, oauth2server.ValidateRedirectURI("https:///callback"), oauth2server.ErrRedirectURINotAbsolute)
	})

	T.Run("an empty URI is refused", func(t *testing.T) {
		t.Parallel()

		test.ErrorIs(t, oauth2server.ValidateRedirectURI(""), oauth2server.ErrRedirectURINotAbsolute)
	})
}
