package grpc

import (
	"github.com/primandproper/platform-go/v14/authentication/passkeys"
	"github.com/primandproper/platform-go/v14/authentication/passkeys/passkeyspb"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// PasskeyToProto renders a stored passkey as a settings page lists it. It
// carries neither the public key nor the sign count; see passkeys.proto for
// why.
//
// A nil credential renders nil.
func PasskeyToProto(c *passkeys.Credential) *passkeyspb.Passkey {
	if c == nil {
		return nil
	}

	out := &passkeyspb.Passkey{
		Id:           c.ID,
		FriendlyName: c.FriendlyName,
		Transports:   c.Transports,
		CredentialId: c.CredentialID,
		CreatedAt:    timestamppb.New(c.CreatedAt),
	}

	if c.LastUsedAt != nil {
		out.LastUsedAt = timestamppb.New(*c.LastUsedAt)
	}

	if c.ArchivedAt != nil {
		out.ArchivedAt = timestamppb.New(*c.ArchivedAt)
	}

	return out
}
