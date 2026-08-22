package database

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/authentication/oauth2server"
	"github.com/primandproper/platform-go/v13/database/dialect"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// allDialects is every dialect this package ships DDL for. The rendering cases
// run over all three because only one of them is reachable from `make test` —
// the SQLite store — and the other two are otherwise proved only by a container
// run.
var allDialects = []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite}

func TestTableName(T *testing.T) {
	T.Parallel()

	T.Run("renders the schema's own names under no namespace", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "oauth2_clients", tableName(DefaultTablePrefix, tableClients))
		test.EqOp(t, "oauth2_authorization_codes", tableName(DefaultTablePrefix, tableCodes))
		test.EqOp(t, "oauth2_access_tokens", tableName(DefaultTablePrefix, tableAccess))
		test.EqOp(t, "oauth2_refresh_tokens", tableName(DefaultTablePrefix, tableRefresh))
	})

	T.Run("prepends a namespace with one separator", func(t *testing.T) {
		t.Parallel()

		// The separator belongs to database/ddl, so a caller supplies "tenant"
		// rather than "tenant_" and cannot render tenant__oauth2_clients.
		test.EqOp(t, "tenant_oauth2_clients", tableName("tenant", tableClients))
	})
}

// The projections and their scans are written to agree, and a mutation to
// either is silent — a SELECT and a Scan that disagree compile fine and put a
// value in the wrong field. The column lists are pinned here so the agreement is
// asserted rather than assumed.
func TestColumnLists(T *testing.T) {
	T.Parallel()

	T.Run("name every column the scan functions read, in order", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "id, secret_hash, name, redirect_uris, grant_types, response_types, "+
			"scopes, token_endpoint_auth_method, created_at, expires_at", clientColumns)

		test.EqOp(t, "hash, client_id, family_id, redirect_uri, code_challenge, nonce, "+
			"subject_id, subject_claims, scopes, resources, issued_at, expires_at, "+
			"redeemed_at", codeColumns)

		test.EqOp(t, "hash, client_id, family_id, subject_id, subject_claims, scopes, "+
			"audience, issued_at, expires_at, revoked_at", accessColumns)

		test.EqOp(t, "hash, client_id, family_id, subject_id, subject_claims, scopes, "+
			"audience, resources, issued_at, expires_at, redeemed_at, revoked_at", refreshColumns)
	})
}

func TestIgnorePrefix(T *testing.T) {
	T.Parallel()

	T.Run("renders each dialect's own way of skipping a duplicate", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "IGNORE ", ignorePrefix(dialect.MySQL))
		test.EqOp(t, "OR IGNORE ", ignorePrefix(dialect.SQLite))

		// Postgres has no prefix and takes an ON CONFLICT clause instead, which
		// the insert builders append.
		test.EqOp(t, "", ignorePrefix(dialect.Postgres))
	})

	T.Run("renders nothing for a dialect it does not know", func(t *testing.T) {
		t.Parallel()

		// Unreachable through NewStore, which refuses an invalid dialect — but
		// a prefix invented for one would be SQL no engine accepts.
		test.EqOp(t, "", ignorePrefix(dialect.Dialect("cockroach")))
	})
}

// The insert-ignore clause is what makes a duplicate report zero rows affected
// instead of raising a dialect-specific SQLSTATE, which is the whole of how
// ErrRecordExists and ErrClientExists are detected without parsing driver
// errors. Losing it on one dialect turns a duplicate into an error nobody maps.
func TestInsertBuilders_SkipDuplicates(T *testing.T) {
	T.Parallel()

	for _, d := range allDialects {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			clientQuery, clientArgs := buildInsertClient(d, "oauth2_clients", &oauth2server.Client{})
			codeQuery, codeArgs := buildInsertCode(d, "oauth2_authorization_codes", &oauth2server.AuthorizationCode{})
			accessQuery, accessArgs := buildInsertAccess(d, "oauth2_access_tokens", &oauth2server.AccessToken{})
			refreshQuery, refreshArgs := buildInsertRefresh(d, "oauth2_refresh_tokens", &oauth2server.RefreshToken{})

			for _, query := range []string{clientQuery, codeQuery, accessQuery, refreshQuery} {
				if d == dialect.Postgres {
					test.StrContains(t, query, "ON CONFLICT")
					test.StrNotContains(t, query, "IGNORE")

					continue
				}

				test.StrContains(t, query, "IGNORE ")
				test.StrNotContains(t, query, "ON CONFLICT")
			}

			// The clients table conflicts on its own primary key, which is the
			// identifier rather than the hash the three credential tables use.
			if d == dialect.Postgres {
				test.StrContains(t, clientQuery, "ON CONFLICT (id) DO NOTHING")
				test.StrContains(t, codeQuery, onHashConflict)
			}

			// One bind marker per column, which is what a mismatch between a
			// builder's column constant and its argument list looks like from
			// out here.
			test.SliceLen(t, 10, clientArgs)
			test.SliceLen(t, 13, codeArgs)
			test.SliceLen(t, 10, accessArgs)
			test.SliceLen(t, 12, refreshArgs)

			test.EqOp(t, len(clientArgs), strings.Count(clientQuery, placeholderMark(d)))
			test.EqOp(t, len(codeArgs), strings.Count(codeQuery, placeholderMark(d)))
			test.EqOp(t, len(accessArgs), strings.Count(accessQuery, placeholderMark(d)))
			test.EqOp(t, len(refreshArgs), strings.Count(refreshQuery, placeholderMark(d)))
		})
	}
}

// placeholderMark is the character a dialect's bind markers all start with.
func placeholderMark(d dialect.Dialect) string {
	if d == dialect.Postgres {
		return "$"
	}

	return "?"
}

// The two guarded UPDATEs are the whole reason this store can be swapped for the
// map-backed one. Each predicate is asserted verbatim: a store that dropped one
// clause would pass every test that redeems one credential at a time.
func TestConsumeBuilders_Predicates(T *testing.T) {
	T.Parallel()

	for _, d := range allDialects {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			now := time.Now().UTC()

			code, codeArgs := buildConsumeCode(d, "oauth2_authorization_codes", "abc", now)

			// redeemed_at IS NULL resolves two concurrent redemptions to one
			// winner; expires_at > now closes the window in which a code
			// expires between a read and the write that follows it.
			test.StrContains(t, code, "redeemed_at IS NULL")
			test.StrContains(t, code, "expires_at > ")
			test.SliceLen(t, 3, codeArgs)

			refresh, refreshArgs := buildConsumeRefresh(d, "oauth2_refresh_tokens", "abc", now)

			test.StrContains(t, refresh, "redeemed_at IS NULL")
			test.StrContains(t, refresh, "expires_at > ")

			// And revoked_at IS NULL, which is what keeps a revoked token from
			// reading as a replay — otherwise every sign-out a client retried
			// would revoke the family it just ended.
			test.StrContains(t, refresh, "revoked_at IS NULL")
			test.SliceLen(t, 3, refreshArgs)
		})
	}
}

func TestRevokeAndSweepBuilders(T *testing.T) {
	T.Parallel()

	for _, d := range allDialects {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			now := time.Now().UTC()

			one, oneArgs := buildRevokeOne(d, "oauth2_access_tokens", "abc", now)

			// Idempotent by predicate rather than by a read: a second
			// revocation reports zero rows instead of moving the timestamp, so
			// the record still says when the token actually stopped working.
			test.StrContains(t, one, "revoked_at IS NULL")
			test.SliceLen(t, 2, oneArgs)

			family, familyArgs := buildRevokeFamily(d, "oauth2_refresh_tokens", "fam", now)
			test.StrContains(t, family, "family_id = ")
			test.StrContains(t, family, "revoked_at IS NULL")
			test.SliceLen(t, 2, familyArgs)

			sweep, sweepArgs := buildSweep(d, "oauth2_authorization_codes", now)
			test.StrContains(t, sweep, "DELETE FROM oauth2_authorization_codes WHERE expires_at <= ")
			test.SliceLen(t, 1, sweepArgs)

			clients, clientArgs := buildSweepClients(d, "oauth2_clients", now)

			// The IS NOT NULL is what keeps a registration with no expiry —
			// stored as NULL — from being read as having lapsed at the
			// beginning of time.
			test.StrContains(t, clients, "expires_at IS NOT NULL")
			test.SliceLen(t, 1, clientArgs)
		})
	}
}

func TestSelectBuilders(T *testing.T) {
	T.Parallel()

	for _, d := range allDialects {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			client, args := buildSelectClient(d, "oauth2_clients", "id")
			test.StrContains(t, client, "SELECT "+clientColumns+" FROM oauth2_clients WHERE id = ")
			test.SliceLen(t, 1, args)

			code, _ := buildSelectCode(d, "oauth2_authorization_codes", "h")
			test.StrContains(t, code, "SELECT "+codeColumns+" FROM ")

			access, _ := buildSelectAccess(d, "oauth2_access_tokens", "h")
			test.StrContains(t, access, "SELECT "+accessColumns+" FROM ")

			refresh, _ := buildSelectRefresh(d, "oauth2_refresh_tokens", "h")
			test.StrContains(t, refresh, "SELECT "+refreshColumns+" FROM ")

			del, delArgs := buildDeleteClient(d, "oauth2_clients", "id")
			test.StrContains(t, del, "DELETE FROM oauth2_clients WHERE id = ")
			test.SliceLen(t, 1, delArgs)
		})
	}
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

	T.Run("the zero time is NULL, and NULL reads back as the zero time", func(t *testing.T) {
		t.Parallel()

		// Storing the zero time instead would make "never expires"
		// indistinguishable from "expired in year one", and turn every IS NULL
		// predicate into a comparison against a magic date.
		test.Nil(t, nullableTime(time.Time{}))

		stamped := time.Date(2026, time.August, 15, 12, 0, 0, 0, time.UTC)
		test.Eq(t, any(stamped), nullableTime(stamped))

		// And a NULL read back is the zero time rather than a decode failure,
		// which is what every "has not been redeemed" and "does not lapse"
		// check is written against.
		test.True(t, readTime(sql.NullTime{}).IsZero())
		test.EqOp(t, stamped, readTime(sql.NullTime{Time: stamped, Valid: true}))
	})
}
