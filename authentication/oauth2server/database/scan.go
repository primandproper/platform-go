package database

import (
	"database/sql"

	"github.com/primandproper/platform-go/v13/authentication/oauth2server"
	"github.com/primandproper/platform-go/v13/database"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

// The scan functions are the other half of the column constants in queries.go.
// They are written next to each other and in the same order for one reason: a
// projection and its Scan that disagree do not fail to compile, they fail at
// runtime with a value in the wrong field, which for these records means a
// token whose subject is its client identifier.

// scanClient reads one registration.
func scanClient(row database.Scanner) (*oauth2server.Client, error) {
	var (
		client        oauth2server.Client
		redirectURIs  string
		grantTypes    string
		responseTypes string
		scopes        string
		expiresAt     sql.NullTime
	)

	if err := row.Scan(
		&client.ID,
		&client.SecretHash,
		&client.Name,
		&redirectURIs,
		&grantTypes,
		&responseTypes,
		&scopes,
		&client.TokenEndpointAuthMethod,
		&client.CreatedAt,
		&expiresAt,
	); err != nil {
		return nil, err
	}

	var err error
	if client.RedirectURIs, err = decodeStrings(redirectURIs); err != nil {
		return nil, platformerrors.Wrap(err, "decoding registered redirect URIs")
	}
	if client.GrantTypes, err = decodeStrings(grantTypes); err != nil {
		return nil, platformerrors.Wrap(err, "decoding registered grant types")
	}
	if client.ResponseTypes, err = decodeStrings(responseTypes); err != nil {
		return nil, platformerrors.Wrap(err, "decoding registered response types")
	}
	if client.Scopes, err = decodeStrings(scopes); err != nil {
		return nil, platformerrors.Wrap(err, "decoding registered scopes")
	}

	client.CreatedAt = client.CreatedAt.UTC()
	client.ExpiresAt = readTime(expiresAt)

	return &client, nil
}

// scanCode reads one authorization code.
func scanCode(row database.Scanner) (*oauth2server.AuthorizationCode, error) {
	var (
		code       oauth2server.AuthorizationCode
		claims     string
		scopes     string
		resources  string
		redeemedAt sql.NullTime
	)

	if err := row.Scan(
		&code.Hash,
		&code.ClientID,
		&code.FamilyID,
		&code.RedirectURI,
		&code.CodeChallenge,
		&code.Nonce,
		&code.Subject.ID,
		&claims,
		&scopes,
		&resources,
		&code.IssuedAt,
		&code.ExpiresAt,
		&redeemedAt,
	); err != nil {
		return nil, err
	}

	var err error
	if code.Subject.Claims, err = decodeClaims(claims); err != nil {
		return nil, platformerrors.Wrap(err, "decoding authorization code subject claims")
	}
	if code.Scopes, err = decodeStrings(scopes); err != nil {
		return nil, platformerrors.Wrap(err, "decoding authorization code scopes")
	}
	if code.Resources, err = decodeStrings(resources); err != nil {
		return nil, platformerrors.Wrap(err, "decoding authorization code resources")
	}

	// Read back as UTC unconditionally: Postgres hands back a time in the
	// session's zone, and every deadline here is compared against a UTC now.
	code.IssuedAt = code.IssuedAt.UTC()
	code.ExpiresAt = code.ExpiresAt.UTC()
	code.RedeemedAt = readTime(redeemedAt)

	return &code, nil
}

// scanAccess reads one access token.
func scanAccess(row database.Scanner) (*oauth2server.AccessToken, error) {
	var (
		token     oauth2server.AccessToken
		claims    string
		scopes    string
		audience  string
		revokedAt sql.NullTime
	)

	if err := row.Scan(
		&token.Hash,
		&token.ClientID,
		&token.FamilyID,
		&token.Subject.ID,
		&claims,
		&scopes,
		&audience,
		&token.IssuedAt,
		&token.ExpiresAt,
		&revokedAt,
	); err != nil {
		return nil, err
	}

	var err error
	if token.Subject.Claims, err = decodeClaims(claims); err != nil {
		return nil, platformerrors.Wrap(err, "decoding access token subject claims")
	}
	if token.Scopes, err = decodeStrings(scopes); err != nil {
		return nil, platformerrors.Wrap(err, "decoding access token scopes")
	}
	if token.Audience, err = decodeStrings(audience); err != nil {
		return nil, platformerrors.Wrap(err, "decoding access token audience")
	}

	token.IssuedAt = token.IssuedAt.UTC()
	token.ExpiresAt = token.ExpiresAt.UTC()
	token.RevokedAt = readTime(revokedAt)

	return &token, nil
}

// scanRefresh reads one refresh token.
func scanRefresh(row database.Scanner) (*oauth2server.RefreshToken, error) {
	var (
		token      oauth2server.RefreshToken
		claims     string
		scopes     string
		audience   string
		resources  string
		redeemedAt sql.NullTime
		revokedAt  sql.NullTime
	)

	if err := row.Scan(
		&token.Hash,
		&token.ClientID,
		&token.FamilyID,
		&token.Subject.ID,
		&claims,
		&scopes,
		&audience,
		&resources,
		&token.IssuedAt,
		&token.ExpiresAt,
		&redeemedAt,
		&revokedAt,
	); err != nil {
		return nil, err
	}

	var err error
	if token.Subject.Claims, err = decodeClaims(claims); err != nil {
		return nil, platformerrors.Wrap(err, "decoding refresh token subject claims")
	}
	if token.Scopes, err = decodeStrings(scopes); err != nil {
		return nil, platformerrors.Wrap(err, "decoding refresh token scopes")
	}
	if token.Audience, err = decodeStrings(audience); err != nil {
		return nil, platformerrors.Wrap(err, "decoding refresh token audience")
	}
	if token.Resources, err = decodeStrings(resources); err != nil {
		return nil, platformerrors.Wrap(err, "decoding refresh token resources")
	}

	token.IssuedAt = token.IssuedAt.UTC()
	token.ExpiresAt = token.ExpiresAt.UTC()
	token.RedeemedAt = readTime(redeemedAt)
	token.RevokedAt = readTime(revokedAt)

	return &token, nil
}
