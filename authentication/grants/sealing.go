package grants

import (
	"context"
	"encoding/binary"

	"github.com/primandproper/platform-go/v14/authentication/grants/internal/queries"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// associatedDataLabel opens every token's associated data, so a ciphertext this
// table wrote cannot be mistaken for one another component sealed under the same
// keyring with a coincidentally identical tuple.
const associatedDataLabel = queries.GrantsTable

// associatedData binds a sealed token to where it lives: the row's key — scope,
// subject and provider — and the column it is in.
//
// The key is what makes a ciphertext copied into another subject's row fail to
// open. The column is what makes the access token and the refresh token of one
// row two different bindings, so the two cannot be swapped. The row's id is
// deliberately not in it: a re-consent replaces the row with a new id under the
// same key, and a refresh rewrites both tokens anyway, so the id would bind
// nothing the key does not.
//
// Each part is length-prefixed rather than joined by a delimiter, so no choice
// of subject or provider can make two different tuples encode the same bytes.
func associatedData(scope tenancy.Scope, subject, provider, column string) []byte {
	parts := []string{associatedDataLabel, scope.Owner(), subject, provider, column}

	size := 0
	for _, part := range parts {
		size += 4 + len(part)
	}

	out := make([]byte, 0, size)
	for _, part := range parts {
		out = binary.BigEndian.AppendUint32(out, uint32(len(part)))
		out = append(out, part...)
	}

	return out
}

// seal encrypts one token for one column of one row.
func (s *SQLStore) seal(ctx context.Context, scope tenancy.Scope, subject, provider, column, token string) ([]byte, error) {
	sealed, err := s.encryptor.Encrypt(ctx, []byte(token), associatedData(scope, subject, provider, column))
	if err != nil {
		return nil, platformerrors.Wrapf(err, "sealing the grant's %s", column)
	}

	return sealed, nil
}

// open decrypts one token read out of one column of one row. A ciphertext
// lifted from anywhere else fails here, as encryption.ErrAuthenticationFailed.
func (s *SQLStore) open(ctx context.Context, scope tenancy.Scope, subject, provider, column string, sealed []byte) (string, error) {
	token, err := s.encryptor.Decrypt(ctx, sealed, associatedData(scope, subject, provider, column))
	if err != nil {
		return "", platformerrors.Wrapf(err, "opening the grant's %s", column)
	}

	return string(token), nil
}
