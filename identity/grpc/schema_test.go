package grpc_test

import (
	"slices"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/identity"
	identitygrpc "github.com/primandproper/platform-go/v14/identity/grpc"
	"github.com/primandproper/platform-go/v14/tenancy"

	"github.com/shoenig/test"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// TestNoResponseMessageCarriesASecret is the guarantee the proto's own
// documentation claims, asserted against the schema rather than against the
// converters.
//
// The claim is that a credential cannot reach a client because there is no field
// for one — which is a stronger property than "the converter clears it", and is
// only true for as long as nobody adds the field. This is what notices if
// somebody does.
func TestNoResponseMessageCarriesASecret(T *testing.T) {
	T.Parallel()

	// The Go fields identity keeps out of every response, by the name a proto
	// field for one would most plausibly have.
	forbidden := []string{
		"hashed_password",
		"password",
		"two_factor_secret",
		"email_address_verification_token",
		"token",
		"secret",
	}

	// The two requests that legitimately carry a token: it arrives from a link,
	// which is the only direction a token travels here.
	tokenBearingRequests := []string{"AcceptInvitationRequest", "RejectInvitationRequest"}

	file := identityFileDescriptor(T)
	messages := file.Messages()

	for i := range messages.Len() {
		msg := messages.Get(i)
		name := string(msg.Name())

		if slices.Contains(tokenBearingRequests, name) {
			continue
		}

		T.Run(name, func(t *testing.T) {
			t.Parallel()

			fields := msg.Fields()
			for j := range fields.Len() {
				field := string(fields.Get(j).Name())
				test.False(t, slices.Contains(forbidden, field), test.Sprintf(
					"%s.%s puts a credential on the wire", name, field))
			}
		})
	}
}

// TestNoMessageCarriesAScope is the other schema-level guarantee, and the more
// load-bearing of the two.
//
// A scope field on a request is a cross-tenant read: the store filters on the
// scope it is handed, so a scope the caller chose makes that filter answer to
// the caller. The server binds it off the principal, and this asserts there is
// nowhere else it could come from.
func TestNoMessageCarriesAScope(T *testing.T) {
	T.Parallel()

	forbidden := []string{"scope", "tenant", "tenant_id", "owner", "directory"}

	file := identityFileDescriptor(T)
	messages := file.Messages()

	for i := range messages.Len() {
		msg := messages.Get(i)
		name := string(msg.Name())

		T.Run(name, func(t *testing.T) {
			t.Parallel()

			fields := msg.Fields()
			for j := range fields.Len() {
				field := string(fields.Get(j).Name())
				test.False(t, slices.Contains(forbidden, field), test.Sprintf(
					"%s.%s lets a client name the tenant its request is answered in", name, field))
			}
		})
	}
}

// TestEveryEnumReservesItsZero checks that each enum has an UNSPECIFIED zero
// value, which is what makes "the client did not set this" distinguishable from
// a real value — and, for the two enums the converters refuse it on, what makes
// an unset status an error rather than a reinstatement.
func TestEveryEnumReservesItsZero(T *testing.T) {
	T.Parallel()

	file := identityFileDescriptor(T)
	enums := file.Enums()

	test.Greater(T, 0, enums.Len(), test.Sprint("identity.proto declares no enums, so this asserted nothing"))

	for i := range enums.Len() {
		enum := enums.Get(i)

		zero := enum.Values().ByNumber(0)
		test.NotNil(T, zero, test.Sprintf("enum %s has no zero value", enum.Name()))

		if zero != nil {
			test.StrHasSuffix(T, "_UNSPECIFIED", string(zero.Name()), test.Sprintf(
				"enum %s's zero value is %s, which reads as a real value", enum.Name(), zero.Name()))
		}
	}
}

// TestAConvertedUserCarriesNoCredential is the converter half of the schema
// guarantee: a user holding every secret the Go type can hold renders a message,
// and nothing about that message can be shown to contain one.
//
// It is deliberately redundant with TestNoResponseMessageCarriesASecret. That
// one says the schema has no field; this one says the value that would have
// filled it is dropped rather than smuggled into another.
func TestAConvertedUserCarriesNoCredential(T *testing.T) {
	T.Parallel()

	secrets := []string{"hunter2-argon2-hash", "JBSWY3DPEHPK3PXP", "verification-token-abc"}

	user := &identity.User{
		ID:                            "user_1",
		Username:                      "somebody",
		EmailAddress:                  "somebody@example.com",
		HashedPassword:                secrets[0],
		TwoFactorSecret:               secrets[1],
		EmailAddressVerificationToken: secrets[2],
		Scope:                         tenancy.Of("acme"),
		CreatedAt:                     time.Now().UTC(),
	}

	rendered := identitygrpc.UserToProto(user).String()

	for _, secret := range secrets {
		test.StrNotContains(T, rendered, secret, test.Sprintf(
			"a converted user renders %q somewhere in its message", secret))
	}

	test.StrNotContains(T, rendered, "acme", test.Sprint("a converted user renders its scope"))
}

// TestAConvertedInvitationCarriesNoToken is the same for the other secret.
func TestAConvertedInvitationCarriesNoToken(T *testing.T) {
	T.Parallel()

	invitation := &identity.Invitation{
		ID:               "invite_1",
		BelongsToAccount: "account_1",
		FromUser:         "user_1",
		ToEmail:          "somebody@example.com",
		Token:            "the-secret-link-token",
		Scope:            tenancy.Of("acme"),
		Status:           identity.InvitationPending,
	}

	rendered := identitygrpc.InvitationToProto(invitation).String()

	test.StrNotContains(T, rendered, "the-secret-link-token")
	test.StrNotContains(T, rendered, "acme")
}

func identityFileDescriptor(t *testing.T) protoreflect.FileDescriptor {
	t.Helper()

	return serviceDescriptor(t).ParentFile()
}
