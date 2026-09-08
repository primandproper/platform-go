package database

import (
	"encoding/json"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/oauth2server/database/internal/oauth2serverdb"

	"github.com/primandproper/primitives-go/authentication/oauth2server"
	platformerrors "github.com/primandproper/primitives-go/errors"
)

// The typed seam between the generated package and the record types.
//
// internal/oauth2serverdb is sqlc-gen-unison's output: one params and one row
// struct per statement, the same on all three dialects. These functions are the
// whole of what this package does with them — a row becomes the record, a
// record becomes the params — and every one is a struct literal on purpose. A
// renamed or retyped column changes the generated struct, and every conversion
// here stops compiling; the scan-by-position pairing these replaced reported
// the same mistake as a runtime scan error, or, for two columns of the same
// type, as a token whose subject was its client identifier.
//
// The row structs are nominal per statement, so a code's row cannot convert to
// a refresh token's even where the columns agree. Restating the fields is the
// cost; the compiler checking every field name is what it buys.

// encodeStrings renders a string slice for a text column.
//
// JSON rather than the module's encoding.Codec, and that is a deliberate
// narrowing rather than an omission. What goes in these columns is a handful of
// scopes and redirect URIs, and the reason to read them is almost always a
// human answering "why was this client allowed to do that" — so the value of
// `["read","write"]` being legible in a psql session outweighs anything a
// binary encoding would save on rows this small. A codec option would be an
// option to make that worse.
//
// It returns no error, unlike its decoding counterpart, because there is no
// []string encoding/json refuses: a string is always marshalable, and invalid
// UTF-8 is replaced rather than rejected. An error return here would be a branch
// no test could reach and no caller could trigger.
func encodeStrings(values []string) string {
	if len(values) == 0 {
		// "[]" rather than "" so the column is always valid JSON, and a reader
		// never has to treat empty as a third case beside "none" and "some".
		return "[]"
	}

	//nolint:errcheck,errchkjson // json.Marshal cannot fail for []string; see the doc comment.
	encoded, _ := json.Marshal(values)

	return string(encoded)
}

// decodeStrings parses a string slice out of a text column.
func decodeStrings(encoded string) ([]string, error) {
	if encoded == "" || encoded == "[]" {
		// A nil slice, not an error and not an empty one: to every caller in
		// this package they are the same thing, and returning nil keeps a
		// record read back out of the database identical to the one the memory
		// store hands over.
		return nil, nil
	}

	var values []string
	if err := json.Unmarshal([]byte(encoded), &values); err != nil {
		return nil, platformerrors.Wrap(err, "decoding string list")
	}

	return values, nil
}

// encodeClaims renders a Subject's application-shaped claims for a text column.
// As with encodeStrings, a map[string]string has no encoding failure to report.
func encodeClaims(claims map[string]string) string {
	if len(claims) == 0 {
		return "{}"
	}

	//nolint:errcheck,errchkjson // json.Marshal cannot fail for map[string]string.
	encoded, _ := json.Marshal(claims)

	return string(encoded)
}

// decodeClaims parses a Subject's claims out of a text column.
func decodeClaims(encoded string) (map[string]string, error) {
	if encoded == "" || encoded == "{}" {
		// As with decodeStrings: a nil map is the empty map, and matching what
		// the memory store returns is what keeps the conformance suite honest.
		return nil, nil //nolint:nilnil // a nil map is the empty map, not a missing value
	}

	var claims map[string]string
	if err := json.Unmarshal([]byte(encoded), &claims); err != nil {
		return nil, platformerrors.Wrap(err, "decoding subject claims")
	}

	return claims, nil
}

// nullableTime renders a Go zero time as the absent argument a nullable column
// stores as NULL.
//
// The distinction is load-bearing in three places: a client with no expiry, a
// credential that has not been redeemed, and a token that has not been revoked.
// Storing the zero time instead would make "never expires" indistinguishable
// from "expired in year one", and would turn every `IS NULL` predicate in the
// corpus into a comparison against a magic date.
func nullableTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}

	utc := t.UTC()

	return &utc
}

// stamp is nullableTime for a time that is always there: the instant a consume
// or a revocation writes.
//
// The generated params carry those as pointers even though the statement binds
// them through sqlc.arg rather than sqlc.narg, because sqlc reads an argument's
// nullability off the column it assigns — and these columns are nullable, which
// is what the guards beside them are written against. What the store never does
// is pass nil: a revocation with no instant would blank the record of when the
// token stopped working.
func stamp(t time.Time) *time.Time {
	utc := t.UTC()

	return &utc
}

// readTime reads a nullable timestamp back, as the zero time when NULL.
//
// Normalized to UTC unconditionally, because the three engines each hand a
// timestamp back in a zone of their own choosing — Postgres in the session's,
// MySQL in the server's, SQLite in whatever the stored text parsed as — and
// every deadline here is compared against a UTC now.
func readTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}

	return t.UTC()
}

// createClientParams is a registration as its INSERT takes it.
//
// created_at is supplied rather than defaulted, unlike every conventional table
// in this module: the registration's creation time is the authorization
// server's, stamped from the same clock as the expiry beside it. See
// internal/queries.
func createClientParams(c *oauth2server.Client) oauth2serverdb.CreateClientParams {
	return oauth2serverdb.CreateClientParams{
		ID:                      c.ID,
		SecretHash:              c.SecretHash,
		Name:                    c.Name,
		RedirectUris:            encodeStrings(c.RedirectURIs),
		GrantTypes:              encodeStrings(c.GrantTypes),
		ResponseTypes:           encodeStrings(c.ResponseTypes),
		Scopes:                  encodeStrings(c.Scopes),
		TokenEndpointAuthMethod: c.TokenEndpointAuthMethod,
		CreatedAt:               c.CreatedAt.UTC(),
		ExpiresAt:               nullableTime(c.ExpiresAt),
	}
}

// clientFromRow reads one registration back.
func clientFromRow(r *oauth2serverdb.GetClientRow) (*oauth2server.Client, error) {
	client := &oauth2server.Client{
		CreatedAt:               r.CreatedAt.UTC(),
		ExpiresAt:               readTime(r.ExpiresAt),
		ID:                      r.ID,
		SecretHash:              r.SecretHash,
		Name:                    r.Name,
		TokenEndpointAuthMethod: r.TokenEndpointAuthMethod,
	}

	var err error
	if client.RedirectURIs, err = decodeStrings(r.RedirectUris); err != nil {
		return nil, platformerrors.Wrap(err, "decoding registered redirect URIs")
	}
	if client.GrantTypes, err = decodeStrings(r.GrantTypes); err != nil {
		return nil, platformerrors.Wrap(err, "decoding registered grant types")
	}
	if client.ResponseTypes, err = decodeStrings(r.ResponseTypes); err != nil {
		return nil, platformerrors.Wrap(err, "decoding registered response types")
	}
	if client.Scopes, err = decodeStrings(r.Scopes); err != nil {
		return nil, platformerrors.Wrap(err, "decoding registered scopes")
	}

	return client, nil
}

// createAuthorizationCodeParams is an issued code as its INSERT takes it.
func createAuthorizationCodeParams(c *oauth2server.AuthorizationCode) oauth2serverdb.CreateAuthorizationCodeParams {
	return oauth2serverdb.CreateAuthorizationCodeParams{
		Hash:          c.Hash,
		ClientID:      c.ClientID,
		FamilyID:      c.FamilyID,
		RedirectURI:   c.RedirectURI,
		CodeChallenge: c.CodeChallenge,
		Nonce:         c.Nonce,
		SubjectID:     c.Subject.ID,
		SubjectClaims: encodeClaims(c.Subject.Claims),
		Scopes:        encodeStrings(c.Scopes),
		Resources:     encodeStrings(c.Resources),
		IssuedAt:      c.IssuedAt.UTC(),
		ExpiresAt:     c.ExpiresAt.UTC(),
		RedeemedAt:    nullableTime(c.RedeemedAt),
	}
}

// codeFromRow reads one authorization code back.
func codeFromRow(r *oauth2serverdb.GetAuthorizationCodeRow) (*oauth2server.AuthorizationCode, error) {
	code := &oauth2server.AuthorizationCode{
		IssuedAt:      r.IssuedAt.UTC(),
		ExpiresAt:     r.ExpiresAt.UTC(),
		RedeemedAt:    readTime(r.RedeemedAt),
		Hash:          r.Hash,
		ClientID:      r.ClientID,
		FamilyID:      r.FamilyID,
		RedirectURI:   r.RedirectURI,
		CodeChallenge: r.CodeChallenge,
		Nonce:         r.Nonce,
		Subject:       oauth2server.Subject{ID: r.SubjectID},
	}

	var err error
	if code.Subject.Claims, err = decodeClaims(r.SubjectClaims); err != nil {
		return nil, platformerrors.Wrap(err, "decoding authorization code subject claims")
	}
	if code.Scopes, err = decodeStrings(r.Scopes); err != nil {
		return nil, platformerrors.Wrap(err, "decoding authorization code scopes")
	}
	if code.Resources, err = decodeStrings(r.Resources); err != nil {
		return nil, platformerrors.Wrap(err, "decoding authorization code resources")
	}

	return code, nil
}

// createAccessTokenParams is an issued access token as its INSERT takes it.
func createAccessTokenParams(t *oauth2server.AccessToken) oauth2serverdb.CreateAccessTokenParams {
	return oauth2serverdb.CreateAccessTokenParams{
		Hash:          t.Hash,
		ClientID:      t.ClientID,
		FamilyID:      t.FamilyID,
		SubjectID:     t.Subject.ID,
		SubjectClaims: encodeClaims(t.Subject.Claims),
		Scopes:        encodeStrings(t.Scopes),
		Audience:      encodeStrings(t.Audience),
		IssuedAt:      t.IssuedAt.UTC(),
		ExpiresAt:     t.ExpiresAt.UTC(),
		RevokedAt:     nullableTime(t.RevokedAt),
	}
}

// accessTokenFromRow reads one access token back.
func accessTokenFromRow(r *oauth2serverdb.GetAccessTokenRow) (*oauth2server.AccessToken, error) {
	token := &oauth2server.AccessToken{
		IssuedAt:  r.IssuedAt.UTC(),
		ExpiresAt: r.ExpiresAt.UTC(),
		RevokedAt: readTime(r.RevokedAt),
		Hash:      r.Hash,
		ClientID:  r.ClientID,
		FamilyID:  r.FamilyID,
		Subject:   oauth2server.Subject{ID: r.SubjectID},
	}

	var err error
	if token.Subject.Claims, err = decodeClaims(r.SubjectClaims); err != nil {
		return nil, platformerrors.Wrap(err, "decoding access token subject claims")
	}
	if token.Scopes, err = decodeStrings(r.Scopes); err != nil {
		return nil, platformerrors.Wrap(err, "decoding access token scopes")
	}
	if token.Audience, err = decodeStrings(r.Audience); err != nil {
		return nil, platformerrors.Wrap(err, "decoding access token audience")
	}

	return token, nil
}

// createRefreshTokenParams is an issued refresh token as its INSERT takes it.
func createRefreshTokenParams(t *oauth2server.RefreshToken) oauth2serverdb.CreateRefreshTokenParams {
	return oauth2serverdb.CreateRefreshTokenParams{
		Hash:          t.Hash,
		ClientID:      t.ClientID,
		FamilyID:      t.FamilyID,
		SubjectID:     t.Subject.ID,
		SubjectClaims: encodeClaims(t.Subject.Claims),
		Scopes:        encodeStrings(t.Scopes),
		Audience:      encodeStrings(t.Audience),
		Resources:     encodeStrings(t.Resources),
		IssuedAt:      t.IssuedAt.UTC(),
		ExpiresAt:     t.ExpiresAt.UTC(),
		RedeemedAt:    nullableTime(t.RedeemedAt),
		RevokedAt:     nullableTime(t.RevokedAt),
	}
}

// refreshTokenFromRow reads one refresh token back.
func refreshTokenFromRow(r *oauth2serverdb.GetRefreshTokenRow) (*oauth2server.RefreshToken, error) {
	token := &oauth2server.RefreshToken{
		IssuedAt:   r.IssuedAt.UTC(),
		ExpiresAt:  r.ExpiresAt.UTC(),
		RedeemedAt: readTime(r.RedeemedAt),
		RevokedAt:  readTime(r.RevokedAt),
		Hash:       r.Hash,
		ClientID:   r.ClientID,
		FamilyID:   r.FamilyID,
		Subject:    oauth2server.Subject{ID: r.SubjectID},
	}

	var err error
	if token.Subject.Claims, err = decodeClaims(r.SubjectClaims); err != nil {
		return nil, platformerrors.Wrap(err, "decoding refresh token subject claims")
	}
	if token.Scopes, err = decodeStrings(r.Scopes); err != nil {
		return nil, platformerrors.Wrap(err, "decoding refresh token scopes")
	}
	if token.Audience, err = decodeStrings(r.Audience); err != nil {
		return nil, platformerrors.Wrap(err, "decoding refresh token audience")
	}
	if token.Resources, err = decodeStrings(r.Resources); err != nil {
		return nil, platformerrors.Wrap(err, "decoding refresh token resources")
	}

	return token, nil
}
