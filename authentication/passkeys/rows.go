package passkeys

import (
	"context"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/passkeys/internal/passkeysdb"

	"github.com/primandproper/primitives-go/v2/tenancy"
)

// The typed seam between the generated package and the domain type.
//
// authentication/passkeys/internal/passkeysdb is sqlc-gen-unison's output: one
// params and one row struct per statement, the same on all three dialects. These
// functions are the whole of what this package does with them — a row becomes a
// Credential, a Credential becomes the params — and every one is a struct literal
// on purpose. A renamed or retyped column changes the generated struct, and every
// conversion here stops compiling; a scan-by-position pairing would report the
// same mistake as a runtime scan error, or worse, as two same-typed columns
// silently transposed.
//
// The row structs are nominal per statement, so a list row cannot convert to a
// get row even where the columns agree — which is why the converters below
// restate the fields rather than casting.

// utcPtr normalizes an optional timestamp to UTC, preserving absence.
//
// Postgres hands back a time in the session's zone, MySQL in the server's, and
// SQLite whatever the string parsed as, so a caller comparing two of those, or
// rendering one into JSON, would otherwise get an answer that depends on where
// the row was read.
func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}

	utc := t.UTC()

	return &utc
}

// createCredentialParams renders a validated credential, under the scope the
// write named and the id it settled on, as the create's arguments.
//
// The convention timestamps are absent because the database owns them, and
// last_used_at is absent because a passkey that has just been registered has
// verified nothing — see authentication/passkeys/internal/queries. The scope
// binds as the Scope the call passed rather than a string derived from it, so an
// unset scope is a driver error instead of a row silently written into the global
// tenant.
func createCredentialParams(scope tenancy.Scope, id, transports string, c *Credential) passkeysdb.CreateCredentialParams {
	return passkeysdb.CreateCredentialParams{
		ID:            id,
		Scope:         scope,
		BelongsToUser: c.BelongsToUser,
		CredentialID:  c.CredentialID,
		PublicKey:     c.PublicKey,
		Transports:    transports,
		FriendlyName:  c.FriendlyName,
		SignCount:     storedSignCount(c.SignCount),
	}
}

// credentialFromRow is the one conversion that builds a Credential; every other
// row shape below restates itself into a GetCredentialRow and comes through here,
// so there is one place a column becomes a field.
//
// It is the only converter that can fail, and both failures are a row this
// package did not write: a sign count outside a WebAuthn counter's range, and a
// transports column holding something other than the encoded list. Reporting
// them is the honest answer — a clamped counter is clone detection that never
// fires, and a silently empty transport list is a login the browser offers the
// wrong authenticators for.
func credentialFromRow(ctx context.Context, r *passkeysdb.GetCredentialRow) (*Credential, error) {
	signCount, err := signCountFromRow(r.SignCount)
	if err != nil {
		return nil, err
	}

	transports, err := decodeTransports(ctx, r.Transports)
	if err != nil {
		return nil, err
	}

	return &Credential{
		CreatedAt:     r.CreatedAt.UTC(),
		LastUpdatedAt: utcPtr(r.LastUpdatedAt),
		LastUsedAt:    utcPtr(r.LastUsedAt),
		ArchivedAt:    utcPtr(r.ArchivedAt),
		ID:            r.ID,
		Scope:         r.Scope,
		BelongsToUser: r.BelongsToUser,
		FriendlyName:  r.FriendlyName,
		Transports:    transports,
		CredentialID:  r.CredentialID,
		PublicKey:     r.PublicKey,
		SignCount:     signCount,
	}, nil
}

// credentialFromArchivedRow converts the archive's read-back, which is the one
// row shape here that casts rather than restating itself.
//
// GetArchivedCredential projects the same list GetCredential does —
// CredentialColumns, in that order — and differs from it only in which rows it
// will look at, so the two row types are one projection rendered twice. The
// conversion is therefore the assertion: the day the two projections stop
// agreeing, in field name, type or order, this stops building rather than filling
// the wrong fields.
func credentialFromArchivedRow(ctx context.Context, r *passkeysdb.GetArchivedCredentialRow) (*Credential, error) {
	row := passkeysdb.GetCredentialRow(*r)

	return credentialFromRow(ctx, &row)
}

// credentialFromLookupRow restates the login's read.
func credentialFromLookupRow(ctx context.Context, r *passkeysdb.GetCredentialByCredentialIDRow) (*Credential, error) {
	row := passkeysdb.GetCredentialRow(*r)

	return credentialFromRow(ctx, &row)
}

// credentialFromListRow restates one row of the user's list.
func credentialFromListRow(ctx context.Context, r *passkeysdb.ListCredentialsForUserRow) (*Credential, error) {
	row := passkeysdb.GetCredentialRow(*r)

	return credentialFromRow(ctx, &row)
}

// credentialFromSubjectListRow restates one row of the export's read.
//
// It is a second converter over the same projection because the row types are
// nominal per statement: ListCredentialsForUsers projects CredentialColumns in
// the same order ListCredentialsForUser does, and differs only in which rows it
// will look at, so the conversion is where that agreement is asserted.
func credentialFromSubjectListRow(ctx context.Context, r *passkeysdb.ListCredentialsForUsersRow) (*Credential, error) {
	row := passkeysdb.GetCredentialRow(*r)

	return credentialFromRow(ctx, &row)
}
