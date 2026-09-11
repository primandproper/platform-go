package oauth2serverstore

import (
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/oauth2serverstore/internal/oauth2serverdb"

	"github.com/primandproper/primitives-go/v2/authentication/oauth2server"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// The generated row structs a well-formed row of each kind comes back as.
//
// The conversions are exercised against these rather than against a real query
// for one reason: every column here can be corrupted independently, and what
// the cases below prove is that each one is decoded and named separately. A
// conversion that wrapped every decode failure in the same description would
// still pass a test that corrupted one column at a time through SQL.
func clientRow() oauth2serverdb.GetClientRow {
	return oauth2serverdb.GetClientRow{
		ID:                      "client_1",
		SecretHash:              "digest",
		Name:                    "Conformance Client",
		RedirectUris:            `["https://client.example/cb"]`,
		GrantTypes:              `["authorization_code"]`,
		ResponseTypes:           `["code"]`,
		Scopes:                  `["read"]`,
		TokenEndpointAuthMethod: "client_secret_basic",
		CreatedAt:               time.Now().UTC(),
	}
}

func codeRow() oauth2serverdb.GetAuthorizationCodeRow {
	now := time.Now().UTC()

	return oauth2serverdb.GetAuthorizationCodeRow{
		Hash:          "hash",
		ClientID:      "client_1",
		FamilyID:      "family_1",
		RedirectURI:   "https://client.example/cb",
		CodeChallenge: "challenge",
		Nonce:         "nonce",
		SubjectID:     "user_1",
		SubjectClaims: `{"account_id":"acct_9"}`,
		Scopes:        `["read"]`,
		Resources:     `["https://api.example/"]`,
		IssuedAt:      now,
		ExpiresAt:     now.Add(time.Minute),
	}
}

func accessRow() oauth2serverdb.GetAccessTokenRow {
	now := time.Now().UTC()

	return oauth2serverdb.GetAccessTokenRow{
		Hash:          "hash",
		ClientID:      "client_1",
		FamilyID:      "family_1",
		SubjectID:     "user_1",
		SubjectClaims: `{"account_id":"acct_9"}`,
		Scopes:        `["read"]`,
		Audience:      `["https://api.example/"]`,
		IssuedAt:      now,
		ExpiresAt:     now.Add(time.Minute),
	}
}

func refreshRow() oauth2serverdb.GetRefreshTokenRow {
	now := time.Now().UTC()

	return oauth2serverdb.GetRefreshTokenRow{
		Hash:          "hash",
		ClientID:      "client_1",
		FamilyID:      "family_1",
		SubjectID:     "user_1",
		SubjectClaims: `{"account_id":"acct_9"}`,
		Scopes:        `["read"]`,
		Audience:      `["https://api.example/"]`,
		Resources:     `["https://api.example/"]`,
		IssuedAt:      now,
		ExpiresAt:     now.Add(time.Minute),
	}
}

// notJSON is what a text column holds when something put a value in it that
// this package did not.
const notJSON = "not json"

func TestRows_WellFormedRows(T *testing.T) {
	T.Parallel()

	T.Run("read every column into the field the projection names", func(t *testing.T) {
		t.Parallel()

		row := clientRow()
		client, err := clientFromRow(&row)
		must.NoError(t, err)
		test.EqOp(t, "client_1", client.ID)
		test.Eq(t, []string{"https://client.example/cb"}, client.RedirectURIs)
		test.Eq(t, []string{"authorization_code"}, client.GrantTypes)
		test.Eq(t, []string{"code"}, client.ResponseTypes)
		test.Eq(t, []string{"read"}, client.Scopes)

		// A NULL expiry reads back as the zero time, which is "never" rather
		// than "lapsed in year one".
		test.True(t, client.ExpiresAt.IsZero())

		codeSource := codeRow()
		code, err := codeFromRow(&codeSource)
		must.NoError(t, err)
		test.EqOp(t, "user_1", code.Subject.ID)

		// The family a replay of this code would revoke by. It is read out of
		// the code's own row rather than out of the tokens it minted, which is
		// what makes the replay answerable at all.
		test.EqOp(t, "family_1", code.FamilyID)
		test.Eq(t, map[string]string{"account_id": "acct_9"}, code.Subject.Claims)
		test.Eq(t, []string{"https://api.example/"}, code.Resources)
		test.True(t, code.RedeemedAt.IsZero())

		accessSource := accessRow()
		access, err := accessTokenFromRow(&accessSource)
		must.NoError(t, err)
		test.EqOp(t, "family_1", access.FamilyID)
		test.Eq(t, []string{"https://api.example/"}, access.Audience)

		refreshSource := refreshRow()
		refresh, err := refreshTokenFromRow(&refreshSource)
		must.NoError(t, err)
		test.Eq(t, []string{"https://api.example/"}, refresh.Resources)
		test.True(t, refresh.RevokedAt.IsZero())
	})

	// Every engine hands a timestamp back in a zone of its own choosing, and
	// every deadline here is compared against a UTC now.
	T.Run("normalize every timestamp to UTC", func(t *testing.T) {
		t.Parallel()

		zone := time.FixedZone("UTC+7", 7*60*60)
		stamped := time.Date(2026, time.August, 15, 12, 0, 0, 0, time.UTC).In(zone)

		row := refreshRow()
		row.IssuedAt = stamped
		row.ExpiresAt = stamped
		row.RedeemedAt = &stamped
		row.RevokedAt = &stamped

		token, err := refreshTokenFromRow(&row)
		must.NoError(t, err)

		for _, got := range []time.Time{token.IssuedAt, token.ExpiresAt, token.RedeemedAt, token.RevokedAt} {
			test.EqOp(t, time.UTC, got.Location())
		}
	})
}

// Every text column that holds JSON, and the description its decode failure
// carries.
//
// The description is what an operator has to work from: SQL has nothing to say
// about whether a TEXT column holds the JSON this package put there, so the
// first anyone hears of a bad row is this error, and "decoding string list"
// four times over would not say which column.
func TestRows_UndecodableColumns(T *testing.T) {
	T.Parallel()

	for _, tc := range []struct {
		convert func() error
		name    string
		wantErr string
	}{
		{name: "client redirect_uris", wantErr: "decoding registered redirect URIs", convert: func() error {
			row := clientRow()
			row.RedirectUris = notJSON
			_, err := clientFromRow(&row)

			return err
		}},
		{name: "client grant_types", wantErr: "decoding registered grant types", convert: func() error {
			row := clientRow()
			row.GrantTypes = notJSON
			_, err := clientFromRow(&row)

			return err
		}},
		{name: "client response_types", wantErr: "decoding registered response types", convert: func() error {
			row := clientRow()
			row.ResponseTypes = notJSON
			_, err := clientFromRow(&row)

			return err
		}},
		{name: "client scopes", wantErr: "decoding registered scopes", convert: func() error {
			row := clientRow()
			row.Scopes = notJSON
			_, err := clientFromRow(&row)

			return err
		}},

		{name: "code subject_claims", wantErr: "decoding authorization code subject claims", convert: func() error {
			row := codeRow()
			row.SubjectClaims = notJSON
			_, err := codeFromRow(&row)

			return err
		}},
		{name: "code scopes", wantErr: "decoding authorization code scopes", convert: func() error {
			row := codeRow()
			row.Scopes = notJSON
			_, err := codeFromRow(&row)

			return err
		}},
		{name: "code resources", wantErr: "decoding authorization code resources", convert: func() error {
			row := codeRow()
			row.Resources = notJSON
			_, err := codeFromRow(&row)

			return err
		}},

		{name: "access subject_claims", wantErr: "decoding access token subject claims", convert: func() error {
			row := accessRow()
			row.SubjectClaims = notJSON
			_, err := accessTokenFromRow(&row)

			return err
		}},
		{name: "access scopes", wantErr: "decoding access token scopes", convert: func() error {
			row := accessRow()
			row.Scopes = notJSON
			_, err := accessTokenFromRow(&row)

			return err
		}},
		{name: "access audience", wantErr: "decoding access token audience", convert: func() error {
			row := accessRow()
			row.Audience = notJSON
			_, err := accessTokenFromRow(&row)

			return err
		}},

		{name: "refresh subject_claims", wantErr: "decoding refresh token subject claims", convert: func() error {
			row := refreshRow()
			row.SubjectClaims = notJSON
			_, err := refreshTokenFromRow(&row)

			return err
		}},
		{name: "refresh scopes", wantErr: "decoding refresh token scopes", convert: func() error {
			row := refreshRow()
			row.Scopes = notJSON
			_, err := refreshTokenFromRow(&row)

			return err
		}},
		{name: "refresh audience", wantErr: "decoding refresh token audience", convert: func() error {
			row := refreshRow()
			row.Audience = notJSON
			_, err := refreshTokenFromRow(&row)

			return err
		}},
		{name: "refresh resources", wantErr: "decoding refresh token resources", convert: func() error {
			row := refreshRow()
			row.Resources = notJSON
			_, err := refreshTokenFromRow(&row)

			return err
		}},
	} {
		T.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := tc.convert()
			must.Error(t, err)
			test.StrContains(t, err.Error(), tc.wantErr)
		})
	}
}

// The params half of the seam. What a record encodes into is what the INSERT
// binds, so a list encoded one way here and read the other way back is a
// registration whose redirect URIs come back as one string.
func TestRows_CreateParams(T *testing.T) {
	T.Parallel()

	T.Run("encode every list column as the JSON its column holds", func(t *testing.T) {
		t.Parallel()

		params := createClientParams(&oauth2server.Client{
			ID:           "client_1",
			RedirectURIs: []string{"https://client.example/cb"},
			GrantTypes:   []string{"authorization_code"},
			CreatedAt:    time.Now(),
		})

		test.EqOp(t, `["https://client.example/cb"]`, params.RedirectUris)
		test.EqOp(t, `["authorization_code"]`, params.GrantTypes)

		// The columns nothing was supplied for are the empty JSON value rather
		// than the empty string, so a reader never has to treat empty as a
		// third case beside "none" and "some".
		test.EqOp(t, "[]", params.ResponseTypes)
		test.EqOp(t, "[]", params.Scopes)

		// And a registration with no expiry binds absent rather than the zero
		// time, which is what its sweep's IS NOT NULL is written against.
		test.Nil(t, params.ExpiresAt)
		test.EqOp(t, time.UTC, params.CreatedAt.Location())
	})

	T.Run("carry every credential's claims and deadlines", func(t *testing.T) {
		t.Parallel()

		now := time.Now().UTC()
		subject := oauth2server.Subject{ID: "user_1", Claims: map[string]string{"account_id": "acct_9"}}

		code := createAuthorizationCodeParams(&oauth2server.AuthorizationCode{
			Hash: "h", Subject: subject, IssuedAt: now, ExpiresAt: now.Add(time.Minute),
		})
		test.EqOp(t, `{"account_id":"acct_9"}`, code.SubjectClaims)
		test.Nil(t, code.RedeemedAt)

		access := createAccessTokenParams(&oauth2server.AccessToken{
			Hash: "h", Subject: subject, IssuedAt: now, ExpiresAt: now.Add(time.Minute),
		})
		test.EqOp(t, `{"account_id":"acct_9"}`, access.SubjectClaims)
		test.Nil(t, access.RevokedAt)

		refresh := createRefreshTokenParams(&oauth2server.RefreshToken{
			Hash: "h", Subject: subject, IssuedAt: now, ExpiresAt: now.Add(time.Minute),
			Resources: []string{"https://api.example/"},
		})
		test.EqOp(t, `["https://api.example/"]`, refresh.Resources)
		test.Nil(t, refresh.RedeemedAt)
		test.Nil(t, refresh.RevokedAt)
	})
}

func TestEncoding(T *testing.T) {
	T.Parallel()

	T.Run("an empty list encodes as valid JSON and decodes back to nil", func(t *testing.T) {
		t.Parallel()

		// "[]" rather than "", so a reader of the column never has to treat
		// empty as a third case beside "none" and "some".
		test.EqOp(t, "[]", encodeStrings(nil))
		test.EqOp(t, "[]", encodeStrings([]string{}))

		for _, encoded := range []string{"", "[]"} {
			values, err := decodeStrings(encoded)
			must.NoError(t, err)
			test.SliceEmpty(t, values)
		}
	})

	T.Run("a list round-trips", func(t *testing.T) {
		t.Parallel()

		want := []string{"read", "write"}

		values, err := decodeStrings(encodeStrings(want))
		must.NoError(t, err)
		test.Eq(t, want, values)
	})

	T.Run("empty claims encode as an object and decode back to nil", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "{}", encodeClaims(nil))
		test.EqOp(t, "{}", encodeClaims(map[string]string{}))

		for _, encoded := range []string{"", "{}"} {
			claims, err := decodeClaims(encoded)
			must.NoError(t, err)
			test.MapEmpty(t, claims)
		}
	})

	T.Run("claims round-trip", func(t *testing.T) {
		t.Parallel()

		want := map[string]string{"account_id": "acct_9", "household": "h_2"}

		claims, err := decodeClaims(encodeClaims(want))
		must.NoError(t, err)
		test.Eq(t, want, claims)
	})

	T.Run("a column that is not the JSON it should be is an error", func(t *testing.T) {
		t.Parallel()

		// Reachable by a hand-edited row or a schema somebody reused. It is the
		// one direction that can fail — an encode of a []string or a
		// map[string]string cannot — so it is the one that reports.
		_, err := decodeStrings(`{"not":"a list"}`)
		test.Error(t, err)

		_, err = decodeClaims(`["not an object"]`)
		test.Error(t, err)

		// A claim whose value is not a string. The map is deliberately
		// map[string]string rather than map[string]any so that a value cannot
		// come back out of SQL as a different Go type than it went in as.
		_, err = decodeClaims(`{"account_id":9}`)
		test.Error(t, err)
	})
}

func TestNullableTime(T *testing.T) {
	T.Parallel()

	T.Run("the zero time is absent, and absent reads back as the zero time", func(t *testing.T) {
		t.Parallel()

		// Storing the zero time instead would make "never expires"
		// indistinguishable from "expired in year one", and turn every IS NULL
		// predicate in the corpus into a comparison against a magic date.
		test.Nil(t, nullableTime(time.Time{}))

		stamped := time.Date(2026, time.August, 15, 12, 0, 0, 0, time.UTC)
		must.NotNil(t, nullableTime(stamped))
		test.EqOp(t, stamped, *nullableTime(stamped))

		// And a NULL read back is the zero time rather than a decode failure,
		// which is what every "has not been redeemed" and "does not lapse"
		// check is written against.
		test.True(t, readTime(nil).IsZero())
		test.EqOp(t, stamped, readTime(&stamped))
	})

	T.Run("a stamp is always there, whatever zone it arrived in", func(t *testing.T) {
		t.Parallel()

		// stamp is the instant a consume or a revocation writes, and its
		// parameter is a pointer only because sqlc reads an argument's
		// nullability off the column it assigns. Passing nil there would blank
		// the record of when the token stopped working, so it has no nil case.
		zoned := time.Date(2026, time.August, 15, 12, 0, 0, 0, time.FixedZone("UTC-5", -5*60*60))

		got := stamp(zoned)
		must.NotNil(t, got)
		test.EqOp(t, time.UTC, got.Location())
		test.True(t, got.Equal(zoned))
	})
}
