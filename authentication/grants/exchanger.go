package grants

import (
	"context"
	"errors"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"

	"golang.org/x/oauth2"
)

// Exchanger is the one piece of OAuth2 this package needs: a source of fresh
// tokens for the ones a grant holds.
//
// It is golang.org/x/oauth2's own method, so a consumer satisfies it with the
// *oauth2.Config it already built for the consent — the client id, the secret
// and the provider's endpoints — and nothing else. Exchanging the code at
// consent, PKCE, and the provider's revocation endpoint stay the consumer's:
// they are protocol, and the store's business starts at the tokens.
type Exchanger interface {
	TokenSource(ctx context.Context, t *oauth2.Token) oauth2.TokenSource
}

var _ Exchanger = (*oauth2.Config)(nil)

// invalidGrant is RFC 6749's error code for a refresh token the provider will
// not honor.
const invalidGrant = "invalid_grant"

// Refresh exchanges a grant's refresh token for new tokens at the provider.
//
// It writes nothing and runs no statement, which is why it is a function over a
// Grant rather than a method on the store: a refresh is a round trip to somebody
// else's server, and holding a transaction open across one is holding locks for
// as long as that server takes to answer. The caller reads the grant, calls
// this outside any transaction, and hands the result to Store.Refreshed with the
// access token it refreshed from — which is the compare that decides whether its
// answer or a concurrent replica's is the one stored.
//
// A provider that refuses with invalid_grant is ErrProviderRevoked, wrapping the
// provider's own error. That answer is final: the caller records it with
// Store.Revoke and RevokedByProvider so nothing retries the grant again. Any
// other failure — the provider unreachable, a 500, a context cancelled — is
// returned as it came and is worth retrying.
//
// A grant holding no refresh token is ErrNoRefreshToken without a call being
// made.
func Refresh(ctx context.Context, exchanger Exchanger, grant *Grant) (*Tokens, error) {
	if exchanger == nil {
		return nil, ErrNilExchanger
	}

	if grant == nil {
		return nil, ErrNilGrant
	}

	if grant.RefreshToken == "" {
		return nil, ErrNoRefreshToken
	}

	// Only the refresh token is handed over. A token source handed a valid
	// access token returns it rather than refreshing, and the whole point of
	// this call is a new one — so the source is given nothing it could reuse.
	fresh, err := exchanger.TokenSource(ctx, &oauth2.Token{RefreshToken: grant.RefreshToken}).Token()
	if err != nil {
		if retrieveErr, ok := errors.AsType[*oauth2.RetrieveError](err); ok && retrieveErr.ErrorCode == invalidGrant {
			// Joined rather than wrapped into the sentinel's message, so the
			// provider's error stays reachable through errors.As for a caller
			// that wants its description.
			return nil, platformerrors.Wrap(platformerrors.Join(ErrProviderRevoked, err), "refreshing grant")
		}

		return nil, platformerrors.Wrap(err, "refreshing grant")
	}

	if fresh == nil || fresh.AccessToken == "" {
		return nil, platformerrors.Wrap(ErrEmptyAccessToken, "the provider's refresh response")
	}

	return &Tokens{
		Expiry:       fresh.Expiry,
		AccessToken:  fresh.AccessToken,
		RefreshToken: fresh.RefreshToken,
	}, nil
}
