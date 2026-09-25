package grants

import (
	"strings"
	"time"
	"unicode/utf8"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// Grant is one standing credential a third party gave this deployment: the
// tokens a provider handed over when a subject consented, and what is known
// about the account they reach.
//
// The two tokens are plaintext here and ciphertext everywhere else. The store
// seals them before a write and opens them for the caller a read was made for,
// and they carry `json:"-"` so a Grant marshaled to a settings page, a log line
// or a support tool says which account is connected without handing over the
// connection. authentication/grants/privacy exports a shape of its own that has
// no token fields at all.
type Grant struct {
	// CreatedAt is when the consent was stored, from the database's clock. A
	// re-consent replaces the row, so it is the most recent consent's.
	CreatedAt time.Time `json:"createdAt"`
	// LastUpdatedAt is when the row last changed — a refresh or a revocation —
	// from the database's clock.
	LastUpdatedAt *time.Time `json:"lastUpdatedAt,omitempty"`
	// AccessTokenExpiresAt is when the provider said the access token stops
	// working. Nil when it said nothing.
	AccessTokenExpiresAt *time.Time `json:"accessTokenExpiresAt,omitempty"`
	// RevokedAt is when the grant was revoked, by either side; RevocationReason
	// says which. A revoked grant is absent from every live read on Store.
	RevokedAt *time.Time `json:"revokedAt,omitempty"`
	// ID is the row's identifier, minted by the store.
	ID string `json:"id"`
	// Scope is the tenant the grant belongs to.
	Scope tenancy.Scope `json:"scope"`
	// Subject is the consumer's own identifier for who consented.
	Subject string `json:"subject"`
	// Provider is the consumer's label for the third party, e.g. "google".
	Provider string `json:"provider"`
	// ProviderAccountID is the provider's own name for the account the tokens
	// reach, where the consumer recorded one.
	ProviderAccountID string `json:"providerAccountID,omitempty"`
	// RevocationReason is empty for a live grant.
	RevocationReason RevocationReason `json:"revocationReason,omitempty"`
	// AccessToken is the bearer credential a call to the provider carries.
	// Empty on a revoked grant, whose row no longer holds one.
	AccessToken string `json:"-"`
	// RefreshToken is what a refresh exchanges for a new AccessToken. Empty
	// where the provider issued none, and on a revoked grant.
	RefreshToken string `json:"-"`
	// GrantedScopes is the OAuth2 scopes the provider granted, which may be
	// fewer than were asked for.
	GrantedScopes []string `json:"grantedScopes,omitempty"`
}

// Consent is what a finished authorization-code exchange hands the store: who
// consented, to which provider, and the tokens that came back.
//
// It carries no scope. Every write names the scope as an argument, and an input
// with a field of its own would be a second place to say it that could
// disagree with the first.
type Consent struct {
	// Tokens is what the exchange returned. Its AccessToken is required.
	Tokens Tokens
	// Subject is the consumer's own identifier for who consented. Required.
	Subject string
	// Provider is the consumer's label for the third party. Required.
	Provider string
	// ProviderAccountID is the provider's own name for the account, when the
	// consumer knows it.
	ProviderAccountID string
	// GrantedScopes is the OAuth2 scopes the provider granted.
	GrantedScopes []string
}

// Tokens is what a provider's token endpoint answers with, as this package
// stores it.
type Tokens struct {
	// Expiry is when AccessToken stops working. The zero value is "the provider
	// did not say", and is stored as such.
	Expiry time.Time
	// AccessToken is the bearer credential. Required on every write.
	AccessToken string
	// RefreshToken is what a later refresh exchanges. A consent may carry none;
	// a refresh that carries none leaves the stored one standing, which is what
	// every provider that does not rotate refresh tokens means by omitting it.
	RefreshToken string
}

// RevocationReason says which side revoked a grant.
type RevocationReason string

const (
	// RevokedByConsumer is a revocation this deployment asked for: the subject
	// disconnected the account, or the consumer decided to stop using it.
	RevokedByConsumer RevocationReason = "revoked"
	// RevokedByProvider is a revocation the provider reported, by refusing a
	// refresh with invalid_grant. It is recorded so that whatever retries
	// refreshes stops retrying a grant that will never work again.
	RevokedByProvider RevocationReason = "invalid_grant"
)

// valid reports whether r is one of the reasons this package records.
func (r RevocationReason) valid() bool {
	return r == RevokedByConsumer || r == RevokedByProvider
}

// The widths of the columns this table stores a caller's values in, and the
// bounds a write is checked against before one is sent.
//
// They are checked in Go because the three dialects disagree about a value that
// does not fit: MySQL sizes these columns and refuses the row under strict mode,
// naming a column, while Postgres and SQLite store the whole thing. Without the
// bound the same consent is a stored grant on two engines and a driver error on
// the third — see ErrValueTooLong.
const (
	// MaxSubjectLength bounds the consumer's subject identifier.
	MaxSubjectLength = 255
	// MaxProviderLength bounds the provider label.
	MaxProviderLength = 64
	// MaxProviderAccountIDLength bounds the provider's account identifier.
	MaxProviderAccountIDLength = 255
	// MaxGrantedScopesLength bounds the granted scopes as stored: joined by
	// single spaces, as RFC 6749 spells a scope list.
	MaxGrantedScopesLength = 2048
	// MaxTokenLength bounds each token before it is sealed. It is far past any
	// token a provider issues — a signed JWT is a couple of kilobytes — and far
	// inside the sixty-four kilobytes the column holds once the encryptor has
	// added its frame, so what it refuses is a caller storing something that is
	// not a token.
	MaxTokenLength = 16384
)

// validate checks a consent before any statement runs.
func (c *Consent) validate() error {
	if c == nil {
		return ErrNilConsent
	}

	if c.Subject == "" {
		return ErrEmptySubject
	}

	if c.Provider == "" {
		return ErrEmptyProvider
	}

	if err := checkLengths(c.Subject, c.Provider, c.ProviderAccountID); err != nil {
		return err
	}

	if _, err := encodeScopes(c.GrantedScopes); err != nil {
		return err
	}

	return c.Tokens.validate()
}

// validate checks a token set before it is sealed.
func (t *Tokens) validate() error {
	if t == nil {
		return ErrNilTokens
	}

	if t.AccessToken == "" {
		return ErrEmptyAccessToken
	}

	if len(t.AccessToken) > MaxTokenLength {
		return platformerrors.Wrapf(ErrValueTooLong, "access token is %d bytes, over %d", len(t.AccessToken), MaxTokenLength)
	}

	if len(t.RefreshToken) > MaxTokenLength {
		return platformerrors.Wrapf(ErrValueTooLong, "refresh token is %d bytes, over %d", len(t.RefreshToken), MaxTokenLength)
	}

	return nil
}

// checkLengths bounds the three identifying strings by character, which is what
// a VARCHAR width counts.
func checkLengths(subject, provider, accountID string) error {
	bounds := []struct {
		name  string
		value string
		limit int
	}{
		{name: "subject", value: subject, limit: MaxSubjectLength},
		{name: "provider", value: provider, limit: MaxProviderLength},
		{name: "provider account id", value: accountID, limit: MaxProviderAccountIDLength},
	}

	for i := range bounds {
		bound := &bounds[i]
		if n := utf8.RuneCountInString(bound.value); n > bound.limit {
			return platformerrors.Wrapf(ErrValueTooLong, "%s is %d characters, over %d", bound.name, n, bound.limit)
		}
	}

	return nil
}

// encodeScopes renders a scope list as the column stores it: RFC 6749's
// space-delimited form.
//
// A scope token cannot contain a space in that grammar, so a list whose members
// contain one — or are empty — would not round-trip, and is refused rather than
// stored as something that reads back differently.
func encodeScopes(scopes []string) (string, error) {
	for _, scope := range scopes {
		if scope == "" || strings.ContainsFunc(scope, isScopeDelimiter) {
			return "", platformerrors.Wrapf(ErrInvalidGrantedScope, "%q", scope)
		}
	}

	encoded := strings.Join(scopes, " ")
	if n := utf8.RuneCountInString(encoded); n > MaxGrantedScopesLength {
		return "", platformerrors.Wrapf(ErrValueTooLong, "granted scopes are %d characters, over %d", n, MaxGrantedScopesLength)
	}

	return encoded, nil
}

// decodeScopes reverses encodeScopes. An empty column is no scopes.
func decodeScopes(encoded string) []string {
	if encoded == "" {
		return nil
	}

	return strings.Split(encoded, " ")
}

// isScopeDelimiter reports whether r may not appear in an RFC 6749 scope token:
// whitespace, the double quote and the backslash.
func isScopeDelimiter(r rune) bool {
	return r <= ' ' || r == '"' || r == '\\' || r == 0x7f
}
