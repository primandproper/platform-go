package database

import (
	"database/sql"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/authentication/oauth2server"
	"github.com/primandproper/platform-go/v13/database"
	platformerrors "github.com/primandproper/platform-go/v13/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// stubRow serves one row's columns straight into a scan.
//
// The scan functions are exercised against this rather than against a real
// query for one reason: every column here can be corrupted independently, and
// what the cases below prove is that each one is decoded and named separately.
// A scan that wrapped every decode failure in the same description would still
// pass a test that corrupted one column at a time through SQL.
type stubRow struct {
	err    error
	values []any
}

var _ database.Scanner = (*stubRow)(nil)

func (r *stubRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}

	for i := range dest {
		switch d := dest[i].(type) {
		case *string:
			*d = r.values[i].(string)
		case *time.Time:
			*d = r.values[i].(time.Time)
		case *sql.NullTime:
			*d = r.values[i].(sql.NullTime)
		}
	}

	return nil
}

// The column values a well-formed row of each kind carries, in the order the
// scan functions read them. Corrupting one entry is how the cases below reach
// one decode.
func clientRow() []any {
	return []any{
		"client_1", "digest", "Conformance Client",
		`["https://client.example/cb"]`, `["authorization_code"]`, `["code"]`, `["read"]`,
		"client_secret_basic", time.Now().UTC(), sql.NullTime{},
	}
}

func codeRow() []any {
	now := time.Now().UTC()

	return []any{
		"hash", "client_1", "family_1", "https://client.example/cb", "challenge", "nonce", "user_1",
		`{"account_id":"acct_9"}`, `["read"]`, `["https://api.example/"]`,
		now, now.Add(time.Minute), sql.NullTime{},
	}
}

func accessRow() []any {
	now := time.Now().UTC()

	return []any{
		"hash", "client_1", "family_1", "user_1",
		`{"account_id":"acct_9"}`, `["read"]`, `["https://api.example/"]`,
		now, now.Add(time.Minute), sql.NullTime{},
	}
}

func refreshRow() []any {
	now := time.Now().UTC()

	return []any{
		"hash", "client_1", "family_1", "user_1",
		`{"account_id":"acct_9"}`, `["read"]`, `["https://api.example/"]`, `["https://api.example/"]`,
		now, now.Add(time.Minute), sql.NullTime{}, sql.NullTime{},
	}
}

// corrupt replaces one column with something that is not the JSON it should be.
func corrupt(values []any, index int) []any {
	out := append([]any(nil), values...)
	out[index] = "not json"

	return out
}

func TestScanners_WellFormedRows(T *testing.T) {
	T.Parallel()

	T.Run("read every column into the field the projection names", func(t *testing.T) {
		t.Parallel()

		client, err := scanClient(&stubRow{values: clientRow()})
		must.NoError(t, err)
		test.EqOp(t, "client_1", client.ID)
		test.Eq(t, []string{"https://client.example/cb"}, client.RedirectURIs)
		test.Eq(t, []string{"authorization_code"}, client.GrantTypes)
		test.Eq(t, []string{"code"}, client.ResponseTypes)
		test.Eq(t, []string{"read"}, client.Scopes)

		// A NULL expiry reads back as the zero time, which is "never" rather
		// than "lapsed in year one".
		test.True(t, client.ExpiresAt.IsZero())

		code, err := scanCode(&stubRow{values: codeRow()})
		must.NoError(t, err)
		test.EqOp(t, "user_1", code.Subject.ID)

		// The family a replay of this code would revoke by. It is read out of
		// the code's own row rather than out of the tokens it minted, which is
		// what makes the replay answerable at all.
		test.EqOp(t, "family_1", code.FamilyID)
		test.Eq(t, map[string]string{"account_id": "acct_9"}, code.Subject.Claims)
		test.Eq(t, []string{"https://api.example/"}, code.Resources)
		test.True(t, code.RedeemedAt.IsZero())

		access, err := scanAccess(&stubRow{values: accessRow()})
		must.NoError(t, err)
		test.EqOp(t, "family_1", access.FamilyID)
		test.Eq(t, []string{"https://api.example/"}, access.Audience)

		refresh, err := scanRefresh(&stubRow{values: refreshRow()})
		must.NoError(t, err)
		test.Eq(t, []string{"https://api.example/"}, refresh.Resources)
		test.True(t, refresh.RevokedAt.IsZero())
	})

	T.Run("hand a failed scan straight back", func(t *testing.T) {
		t.Parallel()

		// Unwrapped, because the caller above branches on sql.ErrNoRows and a
		// wrapped one would still match — but the store's readError is the
		// place that decision belongs, not here.
		boom := platformerrors.New("column count mismatch")

		_, err := scanClient(&stubRow{err: boom})
		test.ErrorIs(t, err, boom)

		_, err = scanCode(&stubRow{err: boom})
		test.ErrorIs(t, err, boom)

		_, err = scanAccess(&stubRow{err: boom})
		test.ErrorIs(t, err, boom)

		_, err = scanRefresh(&stubRow{err: boom})
		test.ErrorIs(t, err, boom)
	})
}

// Every text column that holds JSON, and the description its decode failure
// carries.
//
// The description is what an operator has to work from: SQL has nothing to say
// about whether a TEXT column holds the JSON this package put there, so the
// first anyone hears of a bad row is this error, and "decoding string list"
// four times over would not say which column.
func TestScanners_UndecodableColumns(T *testing.T) {
	T.Parallel()

	for _, tc := range []struct {
		scan    func(database.Scanner) error
		name    string
		wantErr string
		row     []any
		index   int
	}{
		{name: "client redirect_uris", index: 3, wantErr: "decoding registered redirect URIs", row: clientRow(),
			scan: func(r database.Scanner) error { _, err := scanClient(r); return err }},
		{name: "client grant_types", index: 4, wantErr: "decoding registered grant types", row: clientRow(),
			scan: func(r database.Scanner) error { _, err := scanClient(r); return err }},
		{name: "client response_types", index: 5, wantErr: "decoding registered response types", row: clientRow(),
			scan: func(r database.Scanner) error { _, err := scanClient(r); return err }},
		{name: "client scopes", index: 6, wantErr: "decoding registered scopes", row: clientRow(),
			scan: func(r database.Scanner) error { _, err := scanClient(r); return err }},

		{name: "code subject_claims", index: 7, wantErr: "decoding authorization code subject claims", row: codeRow(),
			scan: func(r database.Scanner) error { _, err := scanCode(r); return err }},
		{name: "code scopes", index: 8, wantErr: "decoding authorization code scopes", row: codeRow(),
			scan: func(r database.Scanner) error { _, err := scanCode(r); return err }},
		{name: "code resources", index: 9, wantErr: "decoding authorization code resources", row: codeRow(),
			scan: func(r database.Scanner) error { _, err := scanCode(r); return err }},

		{name: "access subject_claims", index: 4, wantErr: "decoding access token subject claims", row: accessRow(),
			scan: func(r database.Scanner) error { _, err := scanAccess(r); return err }},
		{name: "access scopes", index: 5, wantErr: "decoding access token scopes", row: accessRow(),
			scan: func(r database.Scanner) error { _, err := scanAccess(r); return err }},
		{name: "access audience", index: 6, wantErr: "decoding access token audience", row: accessRow(),
			scan: func(r database.Scanner) error { _, err := scanAccess(r); return err }},

		{name: "refresh subject_claims", index: 4, wantErr: "decoding refresh token subject claims", row: refreshRow(),
			scan: func(r database.Scanner) error { _, err := scanRefresh(r); return err }},
		{name: "refresh scopes", index: 5, wantErr: "decoding refresh token scopes", row: refreshRow(),
			scan: func(r database.Scanner) error { _, err := scanRefresh(r); return err }},
		{name: "refresh audience", index: 6, wantErr: "decoding refresh token audience", row: refreshRow(),
			scan: func(r database.Scanner) error { _, err := scanRefresh(r); return err }},
		{name: "refresh resources", index: 7, wantErr: "decoding refresh token resources", row: refreshRow(),
			scan: func(r database.Scanner) error { _, err := scanRefresh(r); return err }},
	} {
		T.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := tc.scan(&stubRow{values: corrupt(tc.row, tc.index)})
			must.Error(t, err)
			test.StrContains(t, err.Error(), tc.wantErr)
		})
	}
}

// A store built on these scans reports the same failure through its own methods.
func TestStore_ScanFailureSurfaces(T *testing.T) {
	T.Parallel()

	T.Run("names the column an operator has to go and look at", func(t *testing.T) {
		t.Parallel()

		ctx, store := t.Context(), newTestStore(t)

		must.NoError(t, store.CreateClient(ctx, &oauth2server.Client{
			CreatedAt: time.Now().UTC(),
			ID:        "corruptible",
			Scopes:    []string{"read"},
		}))

		_, err := store.db.Writer().ExecContext(ctx,
			"UPDATE oauth2_clients SET scopes = ? WHERE id = ?", "not json", "corruptible")
		must.NoError(t, err)

		got, err := store.GetClient(ctx, "corruptible")
		must.Error(t, err)
		test.Nil(t, got)
		test.StrContains(t, err.Error(), "decoding registered scopes")
	})
}
