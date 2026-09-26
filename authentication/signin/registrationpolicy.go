package signin

import (
	"context"
	"slices"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
)

// RegistrationPolicy is what a consumer's own registration adds to one arriving
// through [Service.Register], and whether it admits the registrant at all. A nil
// error admits the registration as the policy left it, and any other error
// refuses it.
//
// It is [PasswordPolicy]'s sibling, and here for the same reason: a mounted
// transport is what calls Register, and nothing runs between the wire and the
// write. A consumer whose registration does more than the request says — grants
// a service role, names the account, starts the registrant in good standing,
// insists on the terms being accepted — had nowhere to say so but in front of
// the call, so a consumer with a registration of their own could not mount this
// one without the registrants it wrote being people their product did not
// recognize.
//
// # What it may change
//
// It is handed the registration to shape, and anything it sets is what is
// written. The Registration is a copy, and so are its User and Account, so the
// caller's value is left alone as Register leaves it alone: the policy may set
// fields on them, replace either, or supply an Account a request did not name.
// What that reaches:
//
//   - the registrant's standing and service roles, on User.AccountStatus and
//     User.ServiceRoles, which a wire request has no field for and a consumer's
//     product decides;
//   - the account's name and the roles its owner holds, on Account and
//     OwnerRoles;
//   - a second factor minted with the registration, by setting EnrollTOTP;
//   - the agreements the registrant accepted, on Agreements, which Register
//     stamps on the user row it writes.
//
// What it does not reach is the registrant's secrets. User.HashedPassword and
// User.EmailAddressVerificationToken are produced after it returns, from
// Credential and a fresh mint, and overwrite whatever it set; User.TwoFactorSecret
// is overwritten by the minted one when EnrollTOTP is set. A policy that wants a
// registrant to hold a password refuses a registration naming [NoPassword], and
// the password it names still answers to [PasswordPolicy].
//
// # When it runs
//
// First, before the credential is read, anything is hashed or minted, or a
// transaction is opened — so a refusal costs the caller nothing, and a
// registration it refused wrote nobody. It runs for both of identity's
// registrations, and InvitationID is how it tells them apart: Account and
// OwnerRoles are ignored by one that answers an invitation, which joins an
// account that already exists.
//
// What happens with the registrant in the transaction that writes them — an
// audit entry for the agreements, a welcome mail queued behind an outbox row —
// is identity's AfterRegister and AfterRegisterWithInvitation, which run on that
// transaction and see the user the policy shaped, agreement timestamps included.
// A second hook here would be the same moment reached twice.
//
// # What it returns
//
// The error is returned to the caller joined with [ErrRegistrationRefused], the
// policy's own error first, which is the order [PasswordPolicy] documents and
// for the same reason: a policy returning an error the consumer registered with
// errors/grpc.RegisterClientSafeSentinels is what a gRPC client reads — "accept
// the terms of service to register" — and one returning anything else is passed
// over for this package's words. The reason stays REGISTRATION_REFUSED either
// way. The error is also logged and traced, so it must not carry the password
// the registration named.
type RegistrationPolicy func(ctx context.Context, registration *Registration) error

// shapeRegistration applies the service's policy to a registration about to be
// written, and answers with what should be written. With no policy that is the
// registration itself, which Register already treats as read-only.
//
// The copy is deep enough that nothing the policy is handed is shared with the
// caller: the two structs the registration points at, and the slices a policy
// would append to. Anything shallower would let a policy that appended an owner
// role write it into the slice a caller reuses for the next registration.
func (s *Service) shapeRegistration(ctx context.Context, registration *Registration) (*Registration, error) {
	if s.registrationPolicy == nil {
		return registration, nil
	}

	shaped := *registration
	shaped.OwnerRoles = slices.Clone(registration.OwnerRoles)
	shaped.Agreements = slices.Clone(registration.Agreements)

	if registration.User != nil {
		user := *registration.User
		user.ServiceRoles = slices.Clone(registration.User.ServiceRoles)
		shaped.User = &user
	}

	if registration.Account != nil {
		account := *registration.Account
		shaped.Account = &account
	}

	if err := s.registrationPolicy(ctx, &shaped); err != nil {
		return nil, platformerrors.Join(err, ErrRegistrationRefused)
	}

	return &shaped, nil
}
