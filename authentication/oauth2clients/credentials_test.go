package oauth2clients

import (
	"strings"
	"testing"

	"github.com/primandproper/primitives-go/v2/authentication/oauth2server"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// TestValidateClientRefusesARowNothingCouldUse pins the three checks the
// schema's NOT NULL cannot make.
//
// The empty string is a value in every one of these columns, so a row missing
// one of them is written happily and is then a registration nothing can resolve,
// authenticate, or address. Each is a separate sentinel because each is fixed
// somewhere else.
func TestValidateClientRefusesARowNothingCouldUse(T *testing.T) {
	T.Parallel()

	// The registration these cases each break one field of.
	whole := func() *Client {
		return &Client{
			ID:           "row_1",
			ClientID:     "cid_1",
			SecretHash:   hashSecret("s3cret"),
			Name:         "test client",
			RedirectURIs: []string{testRedirect},
		}
	}

	must.NoError(T, validateClient(whole()))

	for name, tc := range map[string]struct {
		omit func(*Client)
		want error
	}{
		"no row identifier":    {omit: func(c *Client) { c.ID = "" }, want: ErrEmptyID},
		"no client identifier": {omit: func(c *Client) { c.ClientID = "" }, want: ErrEmptyClientID},
		"no name":              {omit: func(c *Client) { c.Name = "" }, want: ErrEmptyName},
		"no redirect URIs":     {omit: func(c *Client) { c.RedirectURIs = nil }, want: ErrNoRedirectURIs},
	} {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			client := whole()
			tc.omit(client)

			test.ErrorIs(t, validateClient(client), tc.want)
		})
	}

	T.Run("no secret digest", func(t *testing.T) {
		t.Parallel()

		// The one without a sentinel of its own: nothing outside this package
		// supplies a digest, so a caller cannot be told which field to fix
		// because there is no field they filled in.
		client := whole()
		client.SecretHash = ""

		test.Error(t, validateClient(client))
	})
}

// TestValidateClientBoundsTheDescription pins the one bound this table needs in
// Go.
//
// The create is an insert-ignore, and MySQL's IGNORE downgrades a value too long
// for its column to a warning that truncates and stores it — a registration that
// reports success and holds a description nobody wrote. description is the only
// VARCHAR here carrying caller-supplied prose, because it takes a DEFAULT and
// MySQL takes no literal default on a TEXT column; name and the two lists are
// TEXT on all three dialects.
func TestValidateClientBoundsTheDescription(T *testing.T) {
	T.Parallel()

	whole := func() *Client {
		return &Client{
			ID:           "row_1",
			ClientID:     "cid_1",
			SecretHash:   hashSecret("s3cret"),
			Name:         "test client",
			RedirectURIs: []string{testRedirect},
		}
	}

	// One byte over, because one byte over is the case a limit written down as
	// the wrong number still passes.
	T.Run("refuses a description one byte over the column", func(t *testing.T) {
		t.Parallel()

		client := whole()
		client.Description = strings.Repeat("d", MaxDescriptionLength+1)

		err := validateClient(client)

		test.ErrorIs(t, err, ErrDescriptionTooLong)

		// Answered as a bad request by the platform mapper rather than by a case
		// of this package's own — see internal/sentinelmatrix.
		test.ErrorIs(t, err, platformerrors.ErrUnrecognizedInputValue)
	})

	T.Run("admits a description exactly at the column", func(t *testing.T) {
		t.Parallel()

		client := whole()
		client.Description = strings.Repeat("d", MaxDescriptionLength)

		must.NoError(t, validateClient(client))
	})

	// The same bound reached through the other caller. An update supplies the
	// descriptive fields too, and a registry that refused an over-long
	// description at create and took one at update would be bounded by whichever
	// call arrived.
	T.Run("bounds an update's description as well", func(t *testing.T) {
		t.Parallel()

		over := validateDescriptive("test client",
			strings.Repeat("d", MaxDescriptionLength+1), []string{testRedirect})
		test.ErrorIs(t, over, ErrDescriptionTooLong)

		at := validateDescriptive("test client",
			strings.Repeat("d", MaxDescriptionLength), []string{testRedirect})
		must.NoError(t, at)
	})
}

// TestValidateClientRefusesAURIThatAuthorizeWouldRefuse is why this check runs
// at registration rather than at /authorize.
//
// It is oauth2server.ValidateRedirectURI — the same function the authorization
// server applies — so this registry cannot accept a URI that server would later
// reject. Here the failure names the URI; there it is a client that "does not
// work".
func TestValidateClientRefusesAURIThatAuthorizeWouldRefuse(T *testing.T) {
	T.Parallel()

	for _, uri := range []string{"not a uri", "https://example.test/callback#fragment", ""} {
		client := &Client{
			ID:           "row_1",
			ClientID:     "cid_1",
			SecretHash:   hashSecret("s3cret"),
			Name:         "test client",
			RedirectURIs: []string{uri},
		}

		err := validateClient(client)
		test.ErrorIs(T, err, ErrInvalidRedirectURI, test.Sprintf("redirect URI %q was accepted", uri))

		// The URI is in the message, because the whole value of refusing here
		// rather than at /authorize is that somebody can see which one it was.
		if uri != "" {
			test.StrContains(T, err.Error(), uri)
		}
	}
}

// TestGenerateCredentialsMintsTwoDistinctValues is the default generator's
// contract, and the halves are not interchangeable: one is published in
// configuration files and the other is a credential.
func TestGenerateCredentialsMintsTwoDistinctValues(T *testing.T) {
	T.Parallel()

	clientID, secret, err := generateCredentials()
	must.NoError(T, err)

	test.NotEq(T, "", clientID)
	test.NotEq(T, "", secret)
	test.NotEq(T, clientID, secret)

	// Hex rather than base64url: a client_id is pasted into configuration files
	// and read aloud over support calls, so it carries no case sensitivity and
	// nothing a shell or a URL will argue about. The entropy is in the byte
	// count, and the hex rendering is two characters per byte.
	test.EqOp(T, ClientIDByteLength*2, len(clientID))
	test.EqOp(T, SecretByteLength*2, len(secret))
	test.EqOp(T, strings.ToLower(clientID), clientID)

	// Two calls do not agree, which is the only property that matters about the
	// randomness at this level.
	otherID, otherSecret, err := generateCredentials()
	must.NoError(T, err)
	test.NotEq(T, clientID, otherID)
	test.NotEq(T, secret, otherSecret)
}

// TestHashSecretIsTheAuthorizationServersDigest is the reason there is no second
// implementation here.
//
// A registration written by this package is authenticated by oauth2server's
// /token endpoint against oauth2server's digest. Two encodings of "hex of sha256
// of a client secret" could drift, and the symptom would be every client this
// registry issued failing to authenticate for a reason neither package's own
// tests would show.
func TestHashSecretIsTheAuthorizationServersDigest(T *testing.T) {
	T.Parallel()

	test.EqOp(T, oauth2server.Hash("s3cret"), hashSecret("s3cret"))

	// And it is a digest rather than the secret.
	test.NotEq(T, "s3cret", hashSecret("s3cret"))
}
