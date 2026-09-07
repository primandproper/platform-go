package grpc_test

import (
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/identity"
	identitygrpc "github.com/primandproper/platform-go/v14/identity/grpc"
	"github.com/primandproper/platform-go/v14/identity/identitypb"
	"github.com/primandproper/platform-go/v14/pointer"
	"github.com/primandproper/platform-go/v14/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// The round trips below assert that everything the schema carries survives the
// wire unchanged. They do not assert that the Go value is recovered whole,
// because it deliberately is not: Scope and the credential columns have no proto
// side, so they come back zero. Each test states which fields it has zeroed and
// why, rather than comparing against a value assembled to match.

func fullUser() *identity.User {
	created := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	updated := created.Add(time.Hour)

	return &identity.User{
		ID:                         "user_1",
		Username:                   "somebody",
		EmailAddress:               "somebody@example.com",
		FirstName:                  "Some",
		LastName:                   "Body",
		AccountStatus:              identity.StatusGood,
		AccountStatusExplanation:   "reinstated",
		ServiceRoles:               []string{"operator", "support"},
		RequiresPasswordChange:     true,
		CreatedAt:                  created,
		LastUpdatedAt:              &updated,
		EmailAddressVerifiedAt:     &updated,
		PasswordLastChangedAt:      &updated,
		TwoFactorSecretVerifiedAt:  &updated,
		LastAcceptedTermsOfService: &updated,
		LastAcceptedPrivacyPolicy:  &updated,
	}
}

func TestUserRoundTrip(T *testing.T) {
	T.Parallel()

	want := fullUser()

	got := identitygrpc.UserFromProto(identitygrpc.UserToProto(want))
	must.NotNil(T, got)

	test.Eq(T, want, got)
}

// TestUserRoundTripDropsWhatTheSchemaHasNoFieldFor is the companion, and it is
// the interesting half: a user carrying a scope and three credentials comes back
// without them, and that is correct rather than lossy.
func TestUserRoundTripDropsWhatTheSchemaHasNoFieldFor(T *testing.T) {
	T.Parallel()

	want := fullUser()
	want.HashedPassword = "argon2-hash"
	want.TwoFactorSecret = "totp-secret"
	want.EmailAddressVerificationToken = "verify-me"

	got := identitygrpc.UserFromProto(identitygrpc.UserToProto(want))
	must.NotNil(T, got)

	test.EqOp(T, "", got.HashedPassword)
	test.EqOp(T, "", got.TwoFactorSecret)
	test.EqOp(T, "", got.EmailAddressVerificationToken)
	// The zero Scope is undecided rather than global, and that is the safe end
	// of the two: an undecided scope is refused by the driver when a store binds
	// it, where a global one would match a real directory. A caller reading a
	// user off the wire has to say which directory it is for, and cannot be
	// handed a default that quietly works.
	test.EqOp(T, tenancy.Scope{}, got.Scope, test.Sprint("a user read off the wire carried a directory"))
	test.False(T, got.Scope.IsGlobal(), test.Sprint("an unset scope should not read as the global one"))
}

func TestUserZeroValuesRoundTrip(T *testing.T) {
	T.Parallel()

	// Every optional stamp absent, every slice empty, the status unset. This is
	// the shape a partially-populated row takes, and the one where an absent
	// timestamp could come back as the epoch.
	want := &identity.User{ID: "user_1", Username: "somebody"}

	got := identitygrpc.UserFromProto(identitygrpc.UserToProto(want))
	must.NotNil(T, got)

	test.Nil(T, got.LastUpdatedAt, test.Sprint("an absent stamp came back set"))
	test.Nil(T, got.ArchivedAt)
	test.True(T, got.CreatedAt.IsZero(), test.Sprint("a zero CreatedAt came back as the epoch"))
	test.EqOp(T, identity.AccountStatus(""), got.AccountStatus)
}

func TestNilConvertsToNil(T *testing.T) {
	T.Parallel()

	// A nil entity is a nil message rather than an empty one, so "no user"
	// survives the trip as itself rather than as a user with no name.
	test.Nil(T, identitygrpc.UserToProto(nil))
	test.Nil(T, identitygrpc.AccountToProto(nil))
	test.Nil(T, identitygrpc.MembershipToProto(nil))
	test.Nil(T, identitygrpc.InvitationToProto(nil))
	test.Nil(T, identitygrpc.PrincipalToProto(nil))
	test.Nil(T, identitygrpc.RegistrationToProto(nil))
	test.Nil(T, identitygrpc.AcceptanceToProto(nil))

	test.Nil(T, identitygrpc.MembershipWithUserToProto(nil))

	test.Nil(T, identitygrpc.UserFromProto(nil))
	test.Nil(T, identitygrpc.AccountFromProto(nil))
	test.Nil(T, identitygrpc.MembershipFromProto(nil))
	test.Nil(T, identitygrpc.InvitationFromProto(nil))
	test.Nil(T, identitygrpc.PrincipalFromProto(nil))
	test.Nil(T, identitygrpc.MembershipWithUserFromProto(nil))
}

// TestAnAccountWithNoBillingAddressRoundTripsAsAnEmptyOne is the shape the Go
// type decides rather than the schema: Account.BillingAddress is a value, so an
// account that has none renders a message whose fields are all empty, and
// BillingAddress.Zero is what tells the two apart. The trip back has to produce
// a zero address rather than something a caller would store as a real one.
func TestAnAccountWithNoBillingAddressRoundTripsAsAnEmptyOne(T *testing.T) {
	T.Parallel()

	rendered := identitygrpc.AccountToProto(&identity.Account{Name: "an account"})
	must.NotNil(T, rendered)

	got := identitygrpc.AccountFromProto(rendered)
	must.NotNil(T, got)
	test.True(T, got.BillingAddress.Zero(),
		test.Sprint("an account with no billing address came back with one"))

	// A message the wire did not carry at all reads back the same way, which is
	// what a client generated from this schema sends when it has no address.
	fromNothing := identitygrpc.AccountFromProto(&identitypb.Account{Name: "an account"})
	must.NotNil(T, fromNothing)
	test.True(T, fromNothing.BillingAddress.Zero())
}

// TestAStatusThisPackageDoesNotRecognizeRendersUnspecified covers the two enums
// no RPC accepts from a client. Their columns are written by something else —
// the billing writer, an answer to an invitation — so a value this package's
// constants do not name is a column it does not understand, and UNSPECIFIED is
// the honest rendering of that. Guessing the nearest one would report a
// suspended account as paid.
func TestAStatusThisPackageDoesNotRecognizeRendersUnspecified(T *testing.T) {
	T.Parallel()

	account := identitygrpc.AccountToProto(&identity.Account{BillingStatus: identity.BillingStatus("cursed")})
	must.NotNil(T, account)
	test.EqOp(T, identitypb.BillingStatus_BILLING_STATUS_UNSPECIFIED, account.GetBillingStatus())

	invitation := identitygrpc.InvitationToProto(
		&identity.Invitation{Status: identity.InvitationStatus("cursed")})
	must.NotNil(T, invitation)
	test.EqOp(T, identitypb.InvitationStatus_INVITATION_STATUS_UNSPECIFIED, invitation.GetStatus())
}

// TestAnUnspecifiedStatusReadsBackAsTheEmptyOne is the other direction, and it
// is deliberately not an error where AccountStatusFromProto's is: no request
// carries either of these, so what reaches these two is a round trip rather
// than a client's input, and the empty status is what the column holds for a
// row that never had one.
func TestAnUnspecifiedStatusReadsBackAsTheEmptyOne(T *testing.T) {
	T.Parallel()

	account := identitygrpc.AccountFromProto(&identitypb.Account{
		BillingStatus: identitypb.BillingStatus_BILLING_STATUS_UNSPECIFIED,
	})
	must.NotNil(T, account)
	test.EqOp(T, identity.BillingStatus(""), account.BillingStatus)

	unknownStatus := identitygrpc.AccountFromProto(&identitypb.Account{BillingStatus: identitypb.BillingStatus(9999)})
	must.NotNil(T, unknownStatus)
	test.EqOp(T, identity.BillingStatus(""), unknownStatus.BillingStatus)

	invitation := identitygrpc.InvitationFromProto(&identitypb.Invitation{
		Status: identitypb.InvitationStatus_INVITATION_STATUS_UNSPECIFIED,
	})
	must.NotNil(T, invitation)
	test.EqOp(T, identity.InvitationStatus(""), invitation.Status)

	unknownInvitation := identitygrpc.InvitationFromProto(
		&identitypb.Invitation{Status: identitypb.InvitationStatus(9999)})
	must.NotNil(T, unknownInvitation)
	test.EqOp(T, identity.InvitationStatus(""), unknownInvitation.Status)
}

func TestAccountRoundTrip(T *testing.T) {
	T.Parallel()

	created := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	updated := created.Add(time.Hour)

	want := &identity.Account{
		ID:                          "account_1",
		Name:                        "Acme",
		OwnerUserID:                 "user_1",
		BillingStatus:               identity.BillingPaid,
		PaymentProcessorCustomerID:  "cus_123",
		TimeZone:                    "Europe/London",
		SubscriptionPlanID:          pointer.To("plan_gold"),
		CreatedAt:                   created,
		LastUpdatedAt:               &updated,
		LastPaymentProviderSyncedAt: &updated,
		BillingAddress: identity.BillingAddress{
			Line1: "1 Example Street", City: "London", PostalCode: "E1 1AA", Country: "GB", Phone: "+44",
		},
	}

	got := identitygrpc.AccountFromProto(identitygrpc.AccountToProto(want))
	must.NotNil(T, got)

	test.Eq(T, want, got)
}

// TestAccountSubscriptionPlanStaysOptional covers the one field on Account with
// explicit presence: absent and empty-string are different plans, and collapsing
// them would make "no plan" indistinguishable from "a plan named nothing".
func TestAccountSubscriptionPlanStaysOptional(T *testing.T) {
	T.Parallel()

	none := identitygrpc.AccountFromProto(identitygrpc.AccountToProto(&identity.Account{ID: "a"}))
	must.NotNil(T, none)
	test.Nil(T, none.SubscriptionPlanID)

	empty := identitygrpc.AccountFromProto(
		identitygrpc.AccountToProto(&identity.Account{ID: "a", SubscriptionPlanID: pointer.To("")}))
	must.NotNil(T, empty)
	must.NotNil(T, empty.SubscriptionPlanID)
	test.EqOp(T, "", *empty.SubscriptionPlanID)
}

func TestMembershipRoundTrip(T *testing.T) {
	T.Parallel()

	created := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	want := &identity.Membership{
		ID:               "membership_1",
		BelongsToUser:    "user_1",
		BelongsToAccount: "account_1",
		Roles:            []string{"admin"},
		DefaultAccount:   true,
		CreatedAt:        created,
	}

	got := identitygrpc.MembershipFromProto(identitygrpc.MembershipToProto(want))
	must.NotNil(T, got)

	test.Eq(T, want, got)
}

func TestMembershipWithUserRoundTrip(T *testing.T) {
	T.Parallel()

	want := &identity.MembershipWithUser{
		User:             fullUser(),
		ID:               "membership_1",
		BelongsToUser:    "user_1",
		BelongsToAccount: "account_1",
		Roles:            []string{"admin"},
		CreatedAt:        time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC),
	}

	got := identitygrpc.MembershipWithUserFromProto(identitygrpc.MembershipWithUserToProto(want))
	must.NotNil(T, got)

	test.Eq(T, want, got)
}

func TestInvitationRoundTrip(T *testing.T) {
	T.Parallel()

	created := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	want := &identity.Invitation{
		ID:               "invite_1",
		BelongsToAccount: "account_1",
		FromUser:         "user_1",
		ToUser:           pointer.To("user_2"),
		ToEmail:          "somebody@example.com",
		ToName:           "Some Body",
		Status:           identity.InvitationAccepted,
		Note:             "join us",
		StatusNote:       "glad to",
		Roles:            []string{"member"},
		ExpiresAt:        created.Add(24 * time.Hour),
		CreatedAt:        created,
	}

	got := identitygrpc.InvitationFromProto(identitygrpc.InvitationToProto(want))
	must.NotNil(T, got)

	test.Eq(T, want, got)
}

// TestInvitationToUserStaysOptional covers the presence field on Invitation: an
// unanswered invitation names nobody, and a nil there is not the same as an
// answer from a user with an empty id.
func TestInvitationToUserStaysOptional(T *testing.T) {
	T.Parallel()

	got := identitygrpc.InvitationFromProto(identitygrpc.InvitationToProto(&identity.Invitation{ID: "i"}))
	must.NotNil(T, got)
	test.Nil(T, got.ToUser)
}

func TestPrincipalRoundTrip(T *testing.T) {
	T.Parallel()

	want := &identity.Principal{
		User:            fullUser(),
		ActiveAccountID: "account_1",
		Memberships: []*identity.Membership{
			{ID: "m1", BelongsToUser: "user_1", BelongsToAccount: "account_1", DefaultAccount: true},
		},
	}

	got := identitygrpc.PrincipalFromProto(identitygrpc.PrincipalToProto(want))
	must.NotNil(T, got)

	test.Eq(T, want, got)
}

// TestAccountStatusRefusesUnspecified is the enum decision that matters most.
// The default a client most often means when it leaves the field unset is
// "good", and defaulting to it would reinstate a banned user by accident.
func TestAccountStatusRefusesUnspecified(T *testing.T) {
	T.Parallel()

	_, err := identitygrpc.AccountStatusFromProto(identitypb.AccountStatus_ACCOUNT_STATUS_UNSPECIFIED)
	test.Error(T, err)

	_, err = identitygrpc.AccountStatusFromProto(identitypb.AccountStatus(9999))
	test.Error(T, err)
}

func TestAccountStatusRoundTrips(T *testing.T) {
	T.Parallel()

	for _, want := range []identity.AccountStatus{
		identity.StatusUnverified, identity.StatusGood, identity.StatusBanned, identity.StatusTerminated,
	} {
		T.Run(want.String(), func(t *testing.T) {
			t.Parallel()

			rendered := identitygrpc.UserToProto(&identity.User{AccountStatus: want}).GetAccountStatus()

			got, err := identitygrpc.AccountStatusFromProto(rendered)
			must.NoError(t, err)
			test.EqOp(t, want, got)
		})
	}
}

func TestAgreementRefusesUnspecified(T *testing.T) {
	T.Parallel()

	_, err := identitygrpc.AgreementFromProto(identitypb.Agreement_AGREEMENT_UNSPECIFIED)
	test.Error(T, err)

	// And a number the enum does not name, which is what a client generated
	// from a newer schema than this one sends.
	_, err = identitygrpc.AgreementFromProto(identitypb.Agreement(9999))
	test.Error(T, err)

	tos, err := identitygrpc.AgreementFromProto(identitypb.Agreement_AGREEMENT_TERMS_OF_SERVICE)
	must.NoError(T, err)
	test.EqOp(T, identity.TermsOfService, tos)

	privacy, err := identitygrpc.AgreementFromProto(identitypb.Agreement_AGREEMENT_PRIVACY_POLICY)
	must.NoError(T, err)
	test.EqOp(T, identity.PrivacyPolicy, privacy)
}
