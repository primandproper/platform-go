package passkeys

import (
	"context"
	"math"
	"time"

	"github.com/primandproper/primitives-go/v2/authentication/webauthn"
	"github.com/primandproper/primitives-go/v2/encoding"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/go-webauthn/webauthn/protocol"
)

// Credential is one registered passkey.
//
// It is the row, not the protocol type. primitives-go's webauthn.Credential is
// go-webauthn's own struct — what a ceremony hands back and what one is verified
// against — and this is what a deployment keeps between ceremonies: the same
// bytes, plus the four things a row has that a protocol value does not, which are
// who it belongs to, which tenant, when it was registered and whether it has been
// revoked.
//
// [Credential.WebAuthnCredential] converts one back.
type Credential struct {
	// CreatedAt is when the registration was stored, from the database's clock.
	CreatedAt time.Time
	// LastUpdatedAt is when the row last changed, from the database's clock.
	LastUpdatedAt *time.Time
	// LastUsedAt is when the assertion that last moved SignCount happened, from
	// the clock the caller passed to Store.RecordUse. It is nil until this
	// passkey has signed somebody in, which is what makes "enrolled and never
	// used" answerable from the row.
	LastUsedAt *time.Time
	// ArchivedAt is when the passkey was revoked. A revoked credential is absent
	// from every read on Store.
	ArchivedAt *time.Time
	// ID is the row's identifier, minted by the store where a create supplies
	// none. It is not the credential ID.
	ID string
	// Scope is the tenant the passkey belongs to.
	Scope tenancy.Scope
	// BelongsToUser is the consumer's own user id — not the WebAuthn user
	// handle. Resolving a handle to a user is the consumer's directory's job;
	// see NewUserSource.
	BelongsToUser string
	// FriendlyName is whatever the person called this passkey, for a settings
	// page to render. It means nothing to the protocol.
	FriendlyName string
	// Transports is the authenticator's transport hints, as the registration
	// reported them. They are a hint the browser uses to decide which
	// authenticators to offer, so a round trip that lost them would make a
	// second login slower rather than impossible.
	Transports []string
	// CredentialID is the authenticator's credential ID, the value a login
	// arrives holding and the one the live-rows unique index covers.
	CredentialID []byte
	// PublicKey is the COSE public key an assertion is verified against.
	PublicKey []byte
	// SignCount is the authenticator's signature counter as of the last
	// assertion this credential verified. See Store.RecordUse for why it is the
	// field this table exists for.
	SignCount uint32
}

// transportCodec is how the transports column round-trips.
//
// It is fixed rather than an option, because the encoding is part of this
// package's contract with its own table rather than a caller's preference: a
// store constructed with one codec cannot read rows another one wrote, and there
// is no ceremony-length window here to make the change cheap the way there is in
// authentication/webauthnsessions. JSON is what makes a stored list legible to a
// SELECT, and an array round-trips a transport carrying a comma or a quote,
// which a delimited string would not.
var transportCodec = encoding.NewClientEncoder(encoding.ContentTypeJSON)

// emptyTransports is what an authenticator that reported no transports stores,
// and what the column defaults to. It is the encoding of the empty list rather
// than the empty string, so every row in the column holds a value this package
// can decode.
const emptyTransports = "[]"

// encodeTransports renders the transport hints for the column.
//
// A nil list and an empty one encode identically, which is the right answer for
// a hint: "the authenticator told us nothing" and "the authenticator told us it
// supports nothing" are the same instruction to a browser.
func encodeTransports(ctx context.Context, transports []string) (string, error) {
	if len(transports) == 0 {
		return emptyTransports, nil
	}

	encoded, err := transportCodec.Marshal(ctx, transports)
	if err != nil {
		return "", platformerrors.Wrap(err, "encoding passkey transports")
	}

	return string(encoded), nil
}

// decodeTransports reads the column back.
//
// An empty column decodes to no transports rather than to an error. The default
// is the encoded empty list, so this only happens for a row some other writer
// left blank, and the honest reading of a missing hint is that there is no hint.
func decodeTransports(ctx context.Context, stored string) ([]string, error) {
	if stored == "" || stored == emptyTransports {
		return nil, nil
	}

	var transports []string
	if err := transportCodec.Unmarshal(ctx, []byte(stored), &transports); err != nil {
		return nil, platformerrors.Wrap(err, "decoding passkey transports")
	}

	return transports, nil
}

// storedSignCount widens the counter for the column, which every dialect here
// spells as a 64-bit integer.
func storedSignCount(count uint32) int64 {
	return int64(count)
}

// signCountFromRow narrows a stored counter back, reporting a row this package
// did not write rather than a counter clone detection cannot use. See
// ErrSignCountOutOfRange.
func signCountFromRow(stored int64) (uint32, error) {
	if stored < 0 || stored > math.MaxUint32 {
		return 0, platformerrors.Wrapf(ErrSignCountOutOfRange, "stored sign count %d", stored)
	}

	return uint32(stored), nil
}

// ValidateWithContext reports whether this credential is one the store can
// write: it names a user, carries the authenticator's credential ID, and carries
// the key an assertion will be verified against.
//
// Nothing here validates the scope. The scope is the call's argument rather than
// the entity's field, and Store.CreateCredential reconciles the two — see
// ErrScopeMismatch.
func (c *Credential) ValidateWithContext(_ context.Context) error {
	if c == nil {
		return ErrNilCredential
	}

	if c.BelongsToUser == "" {
		return ErrEmptyUserID
	}

	if len(c.CredentialID) == 0 {
		return ErrEmptyCredentialID
	}

	if len(c.PublicKey) == 0 {
		return ErrEmptyPublicKey
	}

	return nil
}

// WebAuthnCredential renders this row as the protocol type a ceremony verifies
// against, under the backup-eligibility flags the ceremony in hand reported.
//
// The flags are an argument rather than a stored column, and that is the detail a
// from-scratch implementation gets wrong. go-webauthn refuses an assertion whose
// BackupEligible flag disagrees with the one on the credential it is checking,
// and a passkey synced through a platform keychain reports flags that differ from
// whatever the registration saw. Replaying stored flags therefore fails a login
// that is perfectly valid, with an error about flag inconsistency that names
// nothing a deployment can act on. See AuthenticatorFlags.
//
// Nothing else is missing from what a verification needs: the credential ID, the
// public key and the sign count are the three values ValidateLogin reads, and the
// attestation blob this row does not keep is for verifying an authenticator's
// provenance against metadata long after the fact, which is a different feature
// than logging somebody in.
func (c *Credential) WebAuthnCredential(flags AuthenticatorFlags) webauthn.Credential {
	transports := make([]protocol.AuthenticatorTransport, 0, len(c.Transports))
	for _, t := range c.Transports {
		transports = append(transports, protocol.AuthenticatorTransport(t))
	}

	// The three struct literals go-webauthn would want here are not spelled:
	// CredentialFlags and Authenticator are the library's own types and are not
	// among the aliases primitives-go re-exports, so naming them would make this
	// package import go-webauthn twice over to fill two fields.
	credential := webauthn.Credential{
		ID:        c.CredentialID,
		PublicKey: c.PublicKey,
		Transport: transports,
	}

	credential.Flags.BackupEligible = flags.BackupEligible
	credential.Flags.BackupState = flags.BackupState
	credential.Authenticator.SignCount = c.SignCount

	return credential
}
