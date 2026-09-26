package grants

import (
	"context"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/grants/internal/grantsdb"
	"github.com/primandproper/platform-go/v14/authentication/grants/internal/queries"
)

// The typed seam between the generated package and the domain type.
//
// Every conversion is a struct literal, so a renamed or retyped column is a
// compile failure here rather than a scan error at run time. The row structs are
// nominal per statement; the three that project GrantColumns in the same order
// convert to GetGrantRow, and that conversion is where their agreement is
// asserted.

// openRow converts a live row and opens both of its tokens for the caller.
//
// It is the only converter that decrypts, and it is reached only from reads and
// writes that answer a caller about a live grant — never from the revocation's
// read-back, whose columns are empty, nor from the export's, which never
// selects them.
func (s *SQLStore) openRow(ctx context.Context, r *grantsdb.GetGrantRow) (*Grant, error) {
	grant := metadataFromRow(r)

	access, err := s.open(ctx, r.Scope, r.Subject, r.Provider, queries.AccessTokenColumn, r.AccessToken)
	if err != nil {
		return nil, err
	}

	refresh, err := s.open(ctx, r.Scope, r.Subject, r.Provider, queries.RefreshTokenColumn, r.RefreshToken)
	if err != nil {
		return nil, err
	}

	grant.AccessToken = access
	grant.RefreshToken = refresh

	return grant, nil
}

// metadataFromRow converts a row without opening either token.
func metadataFromRow(r *grantsdb.GetGrantRow) *Grant {
	return &Grant{
		CreatedAt:            r.CreatedAt.UTC(),
		LastUpdatedAt:        utcPtr(r.LastUpdatedAt),
		AccessTokenExpiresAt: utcPtr(r.AccessTokenExpiresAt),
		RevokedAt:            utcPtr(r.ArchivedAt),
		ID:                   r.ID,
		Scope:                r.Scope,
		Subject:              r.Subject,
		Provider:             r.Provider,
		ProviderAccountID:    r.ProviderAccountID,
		RevocationReason:     RevocationReason(r.RevocationReason),
		GrantedScopes:        decodeScopes(r.GrantedScopes),
	}
}

// grantFromListRow converts one row of the export's read, which projects
// MetadataColumns and so has no token field to convert.
func grantFromListRow(r *grantsdb.ListGrantsForSubjectsRow) *Grant {
	return &Grant{
		CreatedAt:            r.CreatedAt.UTC(),
		LastUpdatedAt:        utcPtr(r.LastUpdatedAt),
		AccessTokenExpiresAt: utcPtr(r.AccessTokenExpiresAt),
		RevokedAt:            utcPtr(r.ArchivedAt),
		ID:                   r.ID,
		Scope:                r.Scope,
		Subject:              r.Subject,
		Provider:             r.Provider,
		ProviderAccountID:    r.ProviderAccountID,
		RevocationReason:     RevocationReason(r.RevocationReason),
		GrantedScopes:        decodeScopes(r.GrantedScopes),
	}
}

// utcPtr normalizes an optional timestamp to UTC, preserving absence. Postgres
// hands back a time in the session's zone, MySQL in the server's, and SQLite
// whatever the string parsed as.
func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}

	utc := t.UTC()

	return &utc
}

// expiryPtr renders a token's expiry for its nullable column: the zero time is
// "the provider did not say", which is NULL rather than the year one.
func expiryPtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}

	utc := t.UTC()

	return &utc
}
