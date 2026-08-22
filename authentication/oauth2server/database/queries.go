package database

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/primandproper/platform-go/v13/authentication/oauth2server"
	"github.com/primandproper/platform-go/v13/database/ddl"
	"github.com/primandproper/platform-go/v13/database/dialect"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

// The four table name suffixes, qualified by the configured prefix. They are
// built in Go and have to agree with the DDL exactly, which is what ddl.Qualify
// is for.
const (
	tableClients = "oauth2_clients"
	tableCodes   = "oauth2_authorization_codes"
	tableAccess  = "oauth2_access_tokens"
	tableRefresh = "oauth2_refresh_tokens"
)

// The projections each read scans. Declared once so a SELECT and its Scan
// cannot drift, and ordered to match the scan functions below.
const (
	clientColumns = "id, secret_hash, name, redirect_uris, grant_types, response_types, scopes, " +
		"token_endpoint_auth_method, created_at, expires_at"

	codeColumns = "hash, client_id, family_id, redirect_uri, code_challenge, nonce, subject_id, " +
		"subject_claims, scopes, resources, issued_at, expires_at, redeemed_at"

	accessColumns = "hash, client_id, family_id, subject_id, subject_claims, scopes, audience, " +
		"issued_at, expires_at, revoked_at"

	refreshColumns = "hash, client_id, family_id, subject_id, subject_claims, scopes, audience, " +
		"resources, issued_at, expires_at, redeemed_at, revoked_at"
)

// A note on timestamps, because one dialect does something surprising.
//
// Every time this package binds is a UTC time.Time truncated to microseconds.
// Postgres and MySQL store these as real temporal types; SQLite does not —
// modernc's driver stores a bound time.Time as Go's own String() rendering, so
// `expires_at > ?` there is a string comparison.
//
// That is still correct, because the rendering begins with a fixed-width
// "YYYY-MM-DD HH:MM:SS" prefix and everything is UTC, so lexical order is
// chronological order. It stops being correct the moment a value is bound in a
// non-UTC location, so do not remove the .UTC() calls at the binding sites.

// onHashConflict is Postgres' "skip a duplicate row" clause for the three
// credential tables, which all key on the same column name. It is a constant
// rather than three literals because the three inserts have to agree with each
// other and with the DDL's primary keys.
const onHashConflict = " ON CONFLICT (hash) DO NOTHING"

// tableName renders one of this package's tables under a namespace.
func tableName(prefix, suffix string) string {
	return ddl.Qualify(prefix) + suffix
}

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

// nullableTime renders a Go zero time as a SQL NULL.
//
// The distinction is load-bearing in three places: a client with no expiry, a
// credential that has not been redeemed, and a token that has not been revoked.
// Storing the zero time instead would make "never expires" indistinguishable
// from "expired in year one", and would turn every `IS NULL` predicate into a
// comparison against a magic date.
func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}

	return t.UTC()
}

// readTime reads a nullable timestamp back, as the zero time when NULL.
func readTime(t sql.NullTime) time.Time {
	if !t.Valid {
		return time.Time{}
	}

	return t.Time.UTC()
}

// buildSelectClient renders the read of one registration.
func buildSelectClient(d dialect.Dialect, table, clientID string) (query string, args []any) {
	return fmt.Sprintf("SELECT %s FROM %s WHERE id = %s", clientColumns, table, d.Placeholder(1)),
		[]any{clientID}
}

// buildInsertClient renders a registration, ignoring one already there.
//
// The conflict clause is what makes ErrClientExists reportable without parsing
// a driver's error: a duplicate primary key leaves zero rows affected instead
// of raising a dialect-specific SQLSTATE.
func buildInsertClient(d dialect.Dialect, table string, c *oauth2server.Client) (query string, args []any) {
	const columns = 10

	query = fmt.Sprintf("INSERT %sINTO %s (%s) VALUES (%s)",
		ignorePrefix(d), table, clientColumns, d.Placeholders(1, columns))

	if d == dialect.Postgres {
		query += " ON CONFLICT (id) DO NOTHING"
	}

	return query, []any{
		c.ID, c.SecretHash, c.Name,
		encodeStrings(c.RedirectURIs), encodeStrings(c.GrantTypes),
		encodeStrings(c.ResponseTypes), encodeStrings(c.Scopes),
		c.TokenEndpointAuthMethod, c.CreatedAt.UTC(), nullableTime(c.ExpiresAt),
	}
}

// buildDeleteClient renders the removal of one registration.
func buildDeleteClient(d dialect.Dialect, table, clientID string) (query string, args []any) {
	return fmt.Sprintf("DELETE FROM %s WHERE id = %s", table, d.Placeholder(1)), []any{clientID}
}

// buildInsertCode renders an issued authorization code.
func buildInsertCode(d dialect.Dialect, table string, c *oauth2server.AuthorizationCode) (query string, args []any) {
	const columns = 13

	query = fmt.Sprintf("INSERT %sINTO %s (%s) VALUES (%s)",
		ignorePrefix(d), table, codeColumns, d.Placeholders(1, columns))

	if d == dialect.Postgres {
		query += onHashConflict
	}

	return query, []any{
		c.Hash, c.ClientID, c.FamilyID, c.RedirectURI, c.CodeChallenge, c.Nonce, c.Subject.ID,
		encodeClaims(c.Subject.Claims), encodeStrings(c.Scopes), encodeStrings(c.Resources),
		c.IssuedAt.UTC(), c.ExpiresAt.UTC(), nullableTime(c.RedeemedAt),
	}
}

// buildConsumeCode renders the one statement that makes an authorization code
// single-use.
//
// The predicate is the whole guarantee. `redeemed_at IS NULL` is what makes two
// concurrent redemptions resolve to exactly one winner, and `expires_at > now`
// is what closes the window in which a code expires between a read and the
// write that follows it. A store that checked either of those in Go would have
// both races.
func buildConsumeCode(d dialect.Dialect, table, hash string, now time.Time) (query string, args []any) {
	return fmt.Sprintf(
			"UPDATE %s SET redeemed_at = %s WHERE hash = %s AND redeemed_at IS NULL AND expires_at > %s",
			table, d.Placeholder(1), d.Placeholder(2), d.Placeholder(3),
		),
		[]any{now.UTC(), hash, now.UTC()}
}

// buildSelectCode renders the read of one authorization code.
func buildSelectCode(d dialect.Dialect, table, hash string) (query string, args []any) {
	return fmt.Sprintf("SELECT %s FROM %s WHERE hash = %s", codeColumns, table, d.Placeholder(1)),
		[]any{hash}
}

// buildInsertAccess renders an issued access token.
func buildInsertAccess(d dialect.Dialect, table string, t *oauth2server.AccessToken) (query string, args []any) {
	const columns = 10

	query = fmt.Sprintf("INSERT %sINTO %s (%s) VALUES (%s)",
		ignorePrefix(d), table, accessColumns, d.Placeholders(1, columns))

	if d == dialect.Postgres {
		query += onHashConflict
	}

	return query, []any{
		t.Hash, t.ClientID, t.FamilyID, t.Subject.ID,
		encodeClaims(t.Subject.Claims), encodeStrings(t.Scopes), encodeStrings(t.Audience),
		t.IssuedAt.UTC(), t.ExpiresAt.UTC(), nullableTime(t.RevokedAt),
	}
}

// buildSelectAccess renders the read of one access token.
func buildSelectAccess(d dialect.Dialect, table, hash string) (query string, args []any) {
	return fmt.Sprintf("SELECT %s FROM %s WHERE hash = %s", accessColumns, table, d.Placeholder(1)),
		[]any{hash}
}

// buildInsertRefresh renders an issued refresh token.
func buildInsertRefresh(d dialect.Dialect, table string, t *oauth2server.RefreshToken) (query string, args []any) {
	const columns = 12

	query = fmt.Sprintf("INSERT %sINTO %s (%s) VALUES (%s)",
		ignorePrefix(d), table, refreshColumns, d.Placeholders(1, columns))

	if d == dialect.Postgres {
		query += onHashConflict
	}

	return query, []any{
		t.Hash, t.ClientID, t.FamilyID, t.Subject.ID,
		encodeClaims(t.Subject.Claims), encodeStrings(t.Scopes),
		encodeStrings(t.Audience), encodeStrings(t.Resources),
		t.IssuedAt.UTC(), t.ExpiresAt.UTC(), nullableTime(t.RedeemedAt), nullableTime(t.RevokedAt),
	}
}

// buildConsumeRefresh renders the one statement that makes a refresh token
// single-use.
//
// `revoked_at IS NULL` is in the predicate as well as `redeemed_at IS NULL`,
// which is what keeps a revoked token from reading as a replay: it was never
// exchanged, so reporting reuse would revoke a family every time somebody signs
// out and their client retries.
func buildConsumeRefresh(d dialect.Dialect, table, hash string, now time.Time) (query string, args []any) {
	return fmt.Sprintf(
			"UPDATE %s SET redeemed_at = %s WHERE hash = %s AND redeemed_at IS NULL AND revoked_at IS NULL AND expires_at > %s",
			table, d.Placeholder(1), d.Placeholder(2), d.Placeholder(3),
		),
		[]any{now.UTC(), hash, now.UTC()}
}

// buildSelectRefresh renders the read of one refresh token.
func buildSelectRefresh(d dialect.Dialect, table, hash string) (query string, args []any) {
	return fmt.Sprintf("SELECT %s FROM %s WHERE hash = %s", refreshColumns, table, d.Placeholder(1)),
		[]any{hash}
}

// buildRevokeOne renders the revocation of a single token by hash.
//
// `revoked_at IS NULL` keeps it idempotent in the way the caller needs: a
// second revocation reports zero rows rather than moving the timestamp, so the
// record still says when the token actually stopped working.
func buildRevokeOne(d dialect.Dialect, table, hash string, now time.Time) (query string, args []any) {
	return fmt.Sprintf(
			"UPDATE %s SET revoked_at = %s WHERE hash = %s AND revoked_at IS NULL",
			table, d.Placeholder(1), d.Placeholder(2),
		),
		[]any{now.UTC(), hash}
}

// buildRevokeFamily renders the revocation of every unrevoked token in a family.
func buildRevokeFamily(d dialect.Dialect, table, familyID string, now time.Time) (query string, args []any) {
	return fmt.Sprintf(
			"UPDATE %s SET revoked_at = %s WHERE family_id = %s AND revoked_at IS NULL",
			table, d.Placeholder(1), d.Placeholder(2),
		),
		[]any{now.UTC(), familyID}
}

// buildSweep renders the removal of every row past its deadline.
//
// A revoked-but-unexpired token is deliberately not swept: a resource server
// holding one is entitled to be told "no" rather than to have its request read
// as carrying a token nobody ever issued.
func buildSweep(d dialect.Dialect, table string, now time.Time) (query string, args []any) {
	return fmt.Sprintf("DELETE FROM %s WHERE expires_at <= %s", table, d.Placeholder(1)),
		[]any{now.UTC()}
}

// buildSweepClients renders the removal of lapsed registrations.
//
// The IS NOT NULL is what keeps a registration with no expiry — the zero time,
// stored as NULL — from being swept by a predicate that would otherwise read it
// as having lapsed at the beginning of time.
func buildSweepClients(d dialect.Dialect, table string, now time.Time) (query string, args []any) {
	return fmt.Sprintf("DELETE FROM %s WHERE expires_at IS NOT NULL AND expires_at <= %s",
			table, d.Placeholder(1)),
		[]any{now.UTC()}
}

// ignorePrefix renders the dialect's "skip a duplicate row" clause prefix.
// Postgres has none and takes an ON CONFLICT clause instead, which the insert
// builders append.
func ignorePrefix(d dialect.Dialect) string {
	switch d {
	case dialect.MySQL:
		return "IGNORE "
	case dialect.SQLite:
		return "OR IGNORE "
	case dialect.Postgres:
		return ""
	default:
		return ""
	}
}
