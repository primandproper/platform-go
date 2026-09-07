package grpc

import (
	"time"

	platformerrors "github.com/primandproper/platform-go/v14/errors"
	"github.com/primandproper/platform-go/v14/identity"
	"github.com/primandproper/platform-go/v14/identity/identitypb"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// The conversions between identity's Go types and the generated messages.
//
// They are hand-written and one-per-type rather than reflective, for the reason
// filtering/grpc's are: the interesting lines are the ones where the two
// representations disagree, and a reflective converter hides exactly those. Here
// the disagreements are four, and each is load-bearing.
//
// # The credential fields have no proto side
//
// User.HashedPassword, User.TwoFactorSecret, User.EmailAddressVerificationToken
// and Invitation.Token are absent from the schema, so there is no line here that
// could carry one. That is the point of putting the guarantee in the schema
// rather than in a Redacted call somebody has to remember: a converter that
// forgot to clear a field would compile, and this one cannot be written.
//
// # Scope has no proto side either
//
// Every Go type here carries a tenancy.Scope and no message does. Going out, it
// is dropped — a client learns nothing about the shape of the deployment it is
// talking to. Coming in, it is left zero, and the RPC that received the message
// binds the scope off the principal. A converter that set it from a request
// field is the cross-tenant read this package's principal seam exists to
// prevent, so no such field exists to set it from.
//
// # A zero time is absent, not the epoch
//
// The Go types use time.Time for a stamp that is always set and *time.Time for
// one that may not be. Both become an absent message rather than a zero
// timestamp, and come back as the zero value rather than 1970 — because a
// client rendering "archived 1st January 1970" is worse than one rendering
// nothing, and because protobuf already has a word for absent.
//
// # The enums are closed, and an unrecognized one is refused
//
// The Go statuses are named strings with a Valid method; the proto ones are
// enums with an UNSPECIFIED zero. Going out, a value the Go type does not
// recognize becomes UNSPECIFIED. Coming in, UNSPECIFIED and anything unknown are
// errors.ErrUnrecognizedInputValue rather than a default, because the default
// for an account status is "good" and guessing it reinstates a banned user.

// timeToProto renders a stamp that is always set. The zero time is absent.
func timeToProto(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}

	return timestamppb.New(t)
}

// timePointerToProto renders a stamp that may not be set.
func timePointerToProto(t *time.Time) *timestamppb.Timestamp {
	if t == nil {
		return nil
	}

	return timeToProto(*t)
}

// timeFromProto reads a stamp back. An absent one is the zero time.
func timeFromProto(ts *timestamppb.Timestamp) time.Time {
	if ts == nil {
		return time.Time{}
	}

	return ts.AsTime()
}

// timePointerFromProto reads an optional stamp back. An absent one is nil, and
// so is a zero one — the two are the same fact, and collapsing them here is
// what makes a round trip through the wire idempotent rather than turning a
// never-archived row into one archived at the epoch.
func timePointerFromProto(ts *timestamppb.Timestamp) *time.Time {
	if ts == nil {
		return nil
	}

	t := ts.AsTime()
	if t.IsZero() {
		return nil
	}

	return &t
}

// accountStatusToProto maps identity's status onto the enum. A status this
// package does not recognize becomes UNSPECIFIED, which is the honest rendering
// of a column written by something else.
func accountStatusToProto(s identity.AccountStatus) identitypb.AccountStatus {
	switch s {
	case identity.StatusUnverified:
		return identitypb.AccountStatus_ACCOUNT_STATUS_UNVERIFIED
	case identity.StatusGood:
		return identitypb.AccountStatus_ACCOUNT_STATUS_GOOD
	case identity.StatusBanned:
		return identitypb.AccountStatus_ACCOUNT_STATUS_BANNED
	case identity.StatusTerminated:
		return identitypb.AccountStatus_ACCOUNT_STATUS_TERMINATED
	default:
		return identitypb.AccountStatus_ACCOUNT_STATUS_UNSPECIFIED
	}
}

// AccountStatusFromProto reads a status off the wire.
//
// UNSPECIFIED is an error rather than a default. The only sensible default would
// be one of the four, and the one a client most often means — "good" — is the
// one that reinstates a banned user when a field was left unset by mistake.
func AccountStatusFromProto(s identitypb.AccountStatus) (identity.AccountStatus, error) {
	switch s {
	case identitypb.AccountStatus_ACCOUNT_STATUS_UNVERIFIED:
		return identity.StatusUnverified, nil
	case identitypb.AccountStatus_ACCOUNT_STATUS_GOOD:
		return identity.StatusGood, nil
	case identitypb.AccountStatus_ACCOUNT_STATUS_BANNED:
		return identity.StatusBanned, nil
	case identitypb.AccountStatus_ACCOUNT_STATUS_TERMINATED:
		return identity.StatusTerminated, nil
	case identitypb.AccountStatus_ACCOUNT_STATUS_UNSPECIFIED:
		return "", platformerrors.Wrap(platformerrors.ErrUnrecognizedInputValue, "account status is unset")
	default:
		return "", platformerrors.Wrapf(platformerrors.ErrUnrecognizedInputValue, "account status %q", s.String())
	}
}

func billingStatusToProto(s identity.BillingStatus) identitypb.BillingStatus {
	switch s {
	case identity.BillingUnpaid:
		return identitypb.BillingStatus_BILLING_STATUS_UNPAID
	case identity.BillingTrial:
		return identitypb.BillingStatus_BILLING_STATUS_TRIAL
	case identity.BillingPaid:
		return identitypb.BillingStatus_BILLING_STATUS_PAID
	case identity.BillingSuspended:
		return identitypb.BillingStatus_BILLING_STATUS_SUSPENDED
	default:
		return identitypb.BillingStatus_BILLING_STATUS_UNSPECIFIED
	}
}

// billingStatusFromProto is the inverse, and returns the empty status for
// UNSPECIFIED rather than an error: no RPC here accepts a billing status from a
// client — that column moves through the billing writer alone — so this exists
// for the round trip and not for a request.
func billingStatusFromProto(s identitypb.BillingStatus) identity.BillingStatus {
	switch s {
	case identitypb.BillingStatus_BILLING_STATUS_UNPAID:
		return identity.BillingUnpaid
	case identitypb.BillingStatus_BILLING_STATUS_TRIAL:
		return identity.BillingTrial
	case identitypb.BillingStatus_BILLING_STATUS_PAID:
		return identity.BillingPaid
	case identitypb.BillingStatus_BILLING_STATUS_SUSPENDED:
		return identity.BillingSuspended
	case identitypb.BillingStatus_BILLING_STATUS_UNSPECIFIED:
		return ""
	default:
		return ""
	}
}

func invitationStatusToProto(s identity.InvitationStatus) identitypb.InvitationStatus {
	switch s {
	case identity.InvitationPending:
		return identitypb.InvitationStatus_INVITATION_STATUS_PENDING
	case identity.InvitationAccepted:
		return identitypb.InvitationStatus_INVITATION_STATUS_ACCEPTED
	case identity.InvitationRejected:
		return identitypb.InvitationStatus_INVITATION_STATUS_REJECTED
	case identity.InvitationCancelled:
		return identitypb.InvitationStatus_INVITATION_STATUS_CANCELLED
	default:
		return identitypb.InvitationStatus_INVITATION_STATUS_UNSPECIFIED
	}
}

func invitationStatusFromProto(s identitypb.InvitationStatus) identity.InvitationStatus {
	switch s {
	case identitypb.InvitationStatus_INVITATION_STATUS_PENDING:
		return identity.InvitationPending
	case identitypb.InvitationStatus_INVITATION_STATUS_ACCEPTED:
		return identity.InvitationAccepted
	case identitypb.InvitationStatus_INVITATION_STATUS_REJECTED:
		return identity.InvitationRejected
	case identitypb.InvitationStatus_INVITATION_STATUS_CANCELLED:
		return identity.InvitationCancelled
	case identitypb.InvitationStatus_INVITATION_STATUS_UNSPECIFIED:
		return ""
	default:
		return ""
	}
}

// AgreementFromProto reads one of the documents a user can accept.
//
// UNSPECIFIED is an error for the reason AccountStatusFromProto gives: this one
// decides which compliance column gets stamped, and stamping the wrong one is a
// record that says somebody agreed to something they did not read.
func AgreementFromProto(a identitypb.Agreement) (identity.Agreement, error) {
	switch a {
	case identitypb.Agreement_AGREEMENT_TERMS_OF_SERVICE:
		return identity.TermsOfService, nil
	case identitypb.Agreement_AGREEMENT_PRIVACY_POLICY:
		return identity.PrivacyPolicy, nil
	case identitypb.Agreement_AGREEMENT_UNSPECIFIED:
		return "", platformerrors.Wrap(platformerrors.ErrUnrecognizedInputValue, "agreement is unset")
	default:
		return "", platformerrors.Wrapf(platformerrors.ErrUnrecognizedInputValue, "agreement %q", a.String())
	}
}

// agreementsFromProto reads a list, refusing the whole list for one bad entry.
func agreementsFromProto(in []identitypb.Agreement) ([]identity.Agreement, error) {
	out := make([]identity.Agreement, 0, len(in))

	for _, a := range in {
		agreement, err := AgreementFromProto(a)
		if err != nil {
			return nil, err
		}

		out = append(out, agreement)
	}

	return out, nil
}

// UserToProto renders a user for the wire.
//
// It carries no credential and no scope; see this file's header. A nil user is a
// nil message rather than an empty one, so "no user" survives the trip as
// itself.
func UserToProto(u *identity.User) *identitypb.User {
	if u == nil {
		return nil
	}

	return &identitypb.User{
		Id:                         u.ID,
		Username:                   u.Username,
		EmailAddress:               u.EmailAddress,
		FirstName:                  u.FirstName,
		LastName:                   u.LastName,
		AccountStatus:              accountStatusToProto(u.AccountStatus),
		AccountStatusExplanation:   u.AccountStatusExplanation,
		ServiceRoles:               u.ServiceRoles,
		RequiresPasswordChange:     u.RequiresPasswordChange,
		CreatedAt:                  timeToProto(u.CreatedAt),
		LastUpdatedAt:              timePointerToProto(u.LastUpdatedAt),
		ArchivedAt:                 timePointerToProto(u.ArchivedAt),
		EmailAddressVerifiedAt:     timePointerToProto(u.EmailAddressVerifiedAt),
		PasswordLastChangedAt:      timePointerToProto(u.PasswordLastChangedAt),
		TwoFactorSecretVerifiedAt:  timePointerToProto(u.TwoFactorSecretVerifiedAt),
		LastAcceptedTermsOfService: timePointerToProto(u.LastAcceptedTermsOfService),
		LastAcceptedPrivacyPolicy:  timePointerToProto(u.LastAcceptedPrivacyPolicy),
	}
}

// UsersToProto renders a slice.
func UsersToProto(in []*identity.User) []*identitypb.User {
	out := make([]*identitypb.User, 0, len(in))
	for _, u := range in {
		out = append(out, UserToProto(u))
	}

	return out
}

// UserFromProto reads a user back off the wire.
//
// It exists for a Go consumer of the generated client that would rather hold an
// identity.User than a message, and it is exact for every field the schema
// carries. Scope and the credential columns are not among them and come back
// zero — see this file's header. Nothing on the server calls it: no RPC here
// accepts a whole user from a client.
func UserFromProto(u *identitypb.User) *identity.User {
	if u == nil {
		return nil
	}

	return &identity.User{
		ID:                         u.GetId(),
		Username:                   u.GetUsername(),
		EmailAddress:               u.GetEmailAddress(),
		FirstName:                  u.GetFirstName(),
		LastName:                   u.GetLastName(),
		AccountStatus:              accountStatusOrEmpty(u.GetAccountStatus()),
		AccountStatusExplanation:   u.GetAccountStatusExplanation(),
		ServiceRoles:               u.GetServiceRoles(),
		RequiresPasswordChange:     u.GetRequiresPasswordChange(),
		CreatedAt:                  timeFromProto(u.GetCreatedAt()),
		LastUpdatedAt:              timePointerFromProto(u.GetLastUpdatedAt()),
		ArchivedAt:                 timePointerFromProto(u.GetArchivedAt()),
		EmailAddressVerifiedAt:     timePointerFromProto(u.GetEmailAddressVerifiedAt()),
		PasswordLastChangedAt:      timePointerFromProto(u.GetPasswordLastChangedAt()),
		TwoFactorSecretVerifiedAt:  timePointerFromProto(u.GetTwoFactorSecretVerifiedAt()),
		LastAcceptedTermsOfService: timePointerFromProto(u.GetLastAcceptedTermsOfService()),
		LastAcceptedPrivacyPolicy:  timePointerFromProto(u.GetLastAcceptedPrivacyPolicy()),
	}
}

// accountStatusOrEmpty is the total version of AccountStatusFromProto, for the
// read direction where there is no request to refuse.
func accountStatusOrEmpty(s identitypb.AccountStatus) identity.AccountStatus {
	status, err := AccountStatusFromProto(s)
	if err != nil {
		return ""
	}

	return status
}

func billingAddressToProto(a *identity.BillingAddress) *identitypb.BillingAddress {
	if a == nil {
		return nil
	}

	return &identitypb.BillingAddress{
		Line1:      a.Line1,
		Line2:      a.Line2,
		City:       a.City,
		State:      a.State,
		PostalCode: a.PostalCode,
		Country:    a.Country,
		Phone:      a.Phone,
	}
}

func billingAddressFromProto(a *identitypb.BillingAddress) identity.BillingAddress {
	if a == nil {
		return identity.BillingAddress{}
	}

	return identity.BillingAddress{
		Line1:      a.GetLine1(),
		Line2:      a.GetLine2(),
		City:       a.GetCity(),
		State:      a.GetState(),
		PostalCode: a.GetPostalCode(),
		Country:    a.GetCountry(),
		Phone:      a.GetPhone(),
	}
}

// AccountToProto renders an account for the wire. It carries no scope.
func AccountToProto(a *identity.Account) *identitypb.Account {
	if a == nil {
		return nil
	}

	return &identitypb.Account{
		Id:                          a.ID,
		Name:                        a.Name,
		OwnerUserId:                 a.OwnerUserID,
		BillingStatus:               billingStatusToProto(a.BillingStatus),
		PaymentProcessorCustomerId:  a.PaymentProcessorCustomerID,
		TimeZone:                    a.TimeZone,
		BillingAddress:              billingAddressToProto(&a.BillingAddress),
		SubscriptionPlanId:          optionalString(a.SubscriptionPlanID),
		CreatedAt:                   timeToProto(a.CreatedAt),
		LastUpdatedAt:               timePointerToProto(a.LastUpdatedAt),
		ArchivedAt:                  timePointerToProto(a.ArchivedAt),
		LastPaymentProviderSyncedAt: timePointerToProto(a.LastPaymentProviderSyncedAt),
	}
}

// AccountsToProto renders a slice.
func AccountsToProto(in []*identity.Account) []*identitypb.Account {
	out := make([]*identitypb.Account, 0, len(in))
	for _, a := range in {
		out = append(out, AccountToProto(a))
	}

	return out
}

// AccountFromProto reads an account back off the wire, for a Go consumer of the
// client. Scope comes back zero.
func AccountFromProto(a *identitypb.Account) *identity.Account {
	if a == nil {
		return nil
	}

	return &identity.Account{
		ID:                          a.GetId(),
		Name:                        a.GetName(),
		OwnerUserID:                 a.GetOwnerUserId(),
		BillingStatus:               billingStatusFromProto(a.GetBillingStatus()),
		PaymentProcessorCustomerID:  a.GetPaymentProcessorCustomerId(),
		TimeZone:                    a.GetTimeZone(),
		BillingAddress:              billingAddressFromProto(a.GetBillingAddress()),
		SubscriptionPlanID:          optionalString(a.SubscriptionPlanId),
		CreatedAt:                   timeFromProto(a.GetCreatedAt()),
		LastUpdatedAt:               timePointerFromProto(a.GetLastUpdatedAt()),
		ArchivedAt:                  timePointerFromProto(a.GetArchivedAt()),
		LastPaymentProviderSyncedAt: timePointerFromProto(a.GetLastPaymentProviderSyncedAt()),
	}
}

// MembershipToProto renders a membership for the wire. It carries no scope.
func MembershipToProto(m *identity.Membership) *identitypb.Membership {
	if m == nil {
		return nil
	}

	return &identitypb.Membership{
		Id:               m.ID,
		BelongsToUser:    m.BelongsToUser,
		BelongsToAccount: m.BelongsToAccount,
		Roles:            m.Roles,
		DefaultAccount:   m.DefaultAccount,
		CreatedAt:        timeToProto(m.CreatedAt),
		LastUpdatedAt:    timePointerToProto(m.LastUpdatedAt),
		ArchivedAt:       timePointerToProto(m.ArchivedAt),
	}
}

// MembershipsToProto renders a slice.
func MembershipsToProto(in []*identity.Membership) []*identitypb.Membership {
	out := make([]*identitypb.Membership, 0, len(in))
	for _, m := range in {
		out = append(out, MembershipToProto(m))
	}

	return out
}

// MembershipFromProto reads a membership back off the wire. Scope comes back
// zero.
func MembershipFromProto(m *identitypb.Membership) *identity.Membership {
	if m == nil {
		return nil
	}

	return &identity.Membership{
		ID:               m.GetId(),
		BelongsToUser:    m.GetBelongsToUser(),
		BelongsToAccount: m.GetBelongsToAccount(),
		Roles:            m.GetRoles(),
		DefaultAccount:   m.GetDefaultAccount(),
		CreatedAt:        timeFromProto(m.GetCreatedAt()),
		LastUpdatedAt:    timePointerFromProto(m.GetLastUpdatedAt()),
		ArchivedAt:       timePointerFromProto(m.GetArchivedAt()),
	}
}

// MembershipWithUserToProto renders a roster row.
//
// The Go type embeds Membership and the message holds it in a field, which is
// the one shape difference in this file that is protobuf's rather than a
// decision: proto3 has no embedding, and flattening the membership into the
// roster row would give a client two messages with the same fields and no way
// to pass one where the other was wanted.
func MembershipWithUserToProto(m *identity.MembershipWithUser) *identitypb.MembershipWithUser {
	if m == nil {
		return nil
	}

	membership := m.Membership

	return &identitypb.MembershipWithUser{
		User:       UserToProto(m.User),
		Membership: MembershipToProto(&membership),
	}
}

// MembershipsWithUserToProto renders a slice.
func MembershipsWithUserToProto(in []*identity.MembershipWithUser) []*identitypb.MembershipWithUser {
	out := make([]*identitypb.MembershipWithUser, 0, len(in))
	for _, m := range in {
		out = append(out, MembershipWithUserToProto(m))
	}

	return out
}

// MembershipWithUserFromProto reads a roster row back off the wire.
func MembershipWithUserFromProto(m *identitypb.MembershipWithUser) *identity.MembershipWithUser {
	if m == nil {
		return nil
	}

	out := &identity.MembershipWithUser{User: UserFromProto(m.GetUser())}
	if membership := MembershipFromProto(m.GetMembership()); membership != nil {
		out.Membership = *membership
	}

	return out
}

// InvitationToProto renders an invitation for the wire.
//
// It carries no token and no scope; see this file's header. The invitations
// reaching this function have already been redacted by the service, and the
// schema is what makes that belt-and-braces rather than the only defense.
func InvitationToProto(i *identity.Invitation) *identitypb.Invitation {
	if i == nil {
		return nil
	}

	return &identitypb.Invitation{
		Id:               i.ID,
		BelongsToAccount: i.BelongsToAccount,
		FromUser:         i.FromUser,
		ToUser:           optionalString(i.ToUser),
		ToEmail:          i.ToEmail,
		ToName:           i.ToName,
		Status:           invitationStatusToProto(i.Status),
		Note:             i.Note,
		StatusNote:       i.StatusNote,
		Roles:            i.Roles,
		ExpiresAt:        timeToProto(i.ExpiresAt),
		CreatedAt:        timeToProto(i.CreatedAt),
		LastUpdatedAt:    timePointerToProto(i.LastUpdatedAt),
		ArchivedAt:       timePointerToProto(i.ArchivedAt),
	}
}

// InvitationsToProto renders a slice.
func InvitationsToProto(in []*identity.Invitation) []*identitypb.Invitation {
	out := make([]*identitypb.Invitation, 0, len(in))
	for _, i := range in {
		out = append(out, InvitationToProto(i))
	}

	return out
}

// InvitationFromProto reads an invitation back off the wire. Token and scope
// come back zero.
func InvitationFromProto(i *identitypb.Invitation) *identity.Invitation {
	if i == nil {
		return nil
	}

	return &identity.Invitation{
		ID:               i.GetId(),
		BelongsToAccount: i.GetBelongsToAccount(),
		FromUser:         i.GetFromUser(),
		ToUser:           optionalString(i.ToUser),
		ToEmail:          i.GetToEmail(),
		ToName:           i.GetToName(),
		Status:           invitationStatusFromProto(i.GetStatus()),
		Note:             i.GetNote(),
		StatusNote:       i.GetStatusNote(),
		Roles:            i.GetRoles(),
		ExpiresAt:        timeFromProto(i.GetExpiresAt()),
		CreatedAt:        timeFromProto(i.GetCreatedAt()),
		LastUpdatedAt:    timePointerFromProto(i.GetLastUpdatedAt()),
		ArchivedAt:       timePointerFromProto(i.GetArchivedAt()),
	}
}

// PrincipalToProto renders a resolved principal.
func PrincipalToProto(p *identity.Principal) *identitypb.Principal {
	if p == nil {
		return nil
	}

	return &identitypb.Principal{
		User:            UserToProto(p.User),
		ActiveAccountId: p.ActiveAccountID,
		Memberships:     MembershipsToProto(p.Memberships),
	}
}

// PrincipalFromProto reads one back off the wire.
func PrincipalFromProto(p *identitypb.Principal) *identity.Principal {
	if p == nil {
		return nil
	}

	memberships := make([]*identity.Membership, 0, len(p.GetMemberships()))
	for _, m := range p.GetMemberships() {
		memberships = append(memberships, MembershipFromProto(m))
	}

	return &identity.Principal{
		User:            UserFromProto(p.GetUser()),
		ActiveAccountID: p.GetActiveAccountId(),
		Memberships:     memberships,
	}
}

// RegistrationToProto renders what a registration produced.
func RegistrationToProto(r *identity.Registration) *identitypb.Registration {
	if r == nil {
		return nil
	}

	return &identitypb.Registration{
		User:       UserToProto(r.User),
		Account:    AccountToProto(r.Account),
		Membership: MembershipToProto(r.Membership),
	}
}

// AcceptanceToProto renders what accepting an invitation produced.
func AcceptanceToProto(a *identity.Acceptance) *identitypb.Acceptance {
	if a == nil {
		return nil
	}

	return &identitypb.Acceptance{
		Invitation: InvitationToProto(a.Invitation),
		Membership: MembershipToProto(a.Membership),
	}
}

// userFromRegistrationInput builds the User a registration writes.
//
// It assigns only the four fields the input carries. The status, the timestamps
// and the ID are the store's, and a credential is nobody's here — see the
// proto's own documentation on why RegisterRequest has no password.
func userFromRegistrationInput(in *identitypb.UserRegistrationInput) *identity.User {
	if in == nil {
		return nil
	}

	return &identity.User{
		Username:     in.GetUsername(),
		EmailAddress: in.GetEmailAddress(),
		FirstName:    in.GetFirstName(),
		LastName:     in.GetLastName(),
	}
}

// accountFromCreationInput builds the Account a registration writes. The owner
// is the registrant and is assigned by the Service, not here.
func accountFromCreationInput(in *identitypb.AccountCreationInput) *identity.Account {
	if in == nil {
		return nil
	}

	return &identity.Account{
		Name:           in.GetName(),
		TimeZone:       in.GetTimeZone(),
		BillingAddress: billingAddressFromProto(in.GetBillingAddress()),
	}
}

// profileUpdateFromProto reads a profile save.
//
// Presence is the whole reading. Every field is optional on the wire and a
// pointer on the Go side, and the converter carries the one across to the
// other: a field the client left off arrives as nil and the Service leaves
// that column alone, a field the client sent arrives set and is written, and a
// field sent empty clears. The generated pointer fields are what say which,
// and they are read rather than the getters, which return the empty string for
// both absent and empty and so cannot tell a rename from a wipe.
//
// optionalString is the one place that reading is written down, so an input
// message added later cannot take the other one by accident.
func profileUpdateFromProto(in *identitypb.ProfileUpdateInput) *identity.ProfileUpdate {
	if in == nil {
		return nil
	}

	return &identity.ProfileUpdate{
		Username:     optionalString(in.Username),
		EmailAddress: optionalString(in.EmailAddress),
		FirstName:    optionalString(in.FirstName),
		LastName:     optionalString(in.LastName),
	}
}

// accountUpdateFromProto reads an account save, on the same presence reading.
// The billing address is a message and so has presence of its own: absent is
// nil, and a present-but-empty one clears the address.
func accountUpdateFromProto(in *identitypb.AccountUpdateInput) *identity.AccountUpdate {
	if in == nil {
		return nil
	}

	var address *identity.BillingAddress
	if in.GetBillingAddress() != nil {
		a := billingAddressFromProto(in.GetBillingAddress())
		address = &a
	}

	return &identity.AccountUpdate{
		Name:           optionalString(in.Name),
		TimeZone:       optionalString(in.TimeZone),
		BillingAddress: address,
	}
}

// optionalString is a proto3 optional string as the pointer the Service's
// update types use: nil when the field was not on the wire, set — to whatever
// arrived, the empty string included — when it was. It is a copy rather than
// the message's own pointer, so the update does not alias the request.
func optionalString(field *string) *string {
	if field == nil {
		return nil
	}

	value := *field

	return &value
}
