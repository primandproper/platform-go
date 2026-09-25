package signin

import (
	"context"

	"github.com/primandproper/platform-go/v14/identity"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/pointer"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// Registrar is what registering needs from identity, and it is optional: a
// service built without [WithRegistrar] registers nobody and refuses
// [Service.Register] with [ErrRegistrationNotConfigured].
//
// It is identity's Service rather than its Store, which is the one seam in this
// package narrowed to the layer above the rows. Both of identity's
// registrations write three rows apiece, assign the account's owner, mint the
// default membership and call a consumer's own hook — all on one transaction —
// and a store-level seam would mean reimplementing the first three here and
// skipping the fourth. What this package adds is the credential, which is the
// part identity may not do.
//
// [github.com/primandproper/platform-go/v14/identity.Service] satisfies it. A
// consumer whose directory is not that one implements the two methods.
type Registrar interface {
	// Register creates the user, the first account they own and the membership
	// between them, in one transaction.
	Register(
		ctx context.Context,
		scope tenancy.Scope,
		user *identity.User,
		account *identity.Account,
		ownerRoles []string,
	) (*identity.Registration, error)

	// RegisterWithInvitation creates the user, answers the invitation in their
	// name and files the membership that answer promised, in one transaction.
	// It mints no account.
	RegisterWithInvitation(
		ctx context.Context,
		scope tenancy.Scope,
		user *identity.User,
		invitationID, token, statusNote string,
	) (*identity.InvitedRegistration, error)
}

// Credential is how a registrant will prove who they are afterwards, named by
// the registration that creates them.
//
// It is a closed set of two — [Password] and [NoPassword] — and it is an
// argument rather than an inference from an empty string. A registration that
// names none is refused, which is the whole reason the type exists: choosing
// email-only authentication and forgetting to wire up a form field look
// identical to a service reading a blank password, and the first is a product
// decision while the second is a bug that mints an account nobody can ever
// reach. The module already refuses to let a no-op be arrived at by omission
// where a provider is being selected, and this is the same rule for the same
// reason.
//
// It is a sealed interface, so a consumer cannot add an arm and this package
// can: a passkey ceremony at registration is another constructor here rather
// than another method on [Service].
type Credential interface {
	// credential seals the interface. Its absence from any type outside this
	// package is what makes a type switch over the arms exhaustive.
	credential()
}

// passwordCredential is a registrant who chose a password.
//
// It holds the plaintext, which is the one thing this package accepts one for:
// [Service.Register] hashes it with the consumer's authenticator and never
// stores, logs, traces or hands a hook what it was given.
type passwordCredential struct {
	plaintext string
}

func (passwordCredential) credential() {}

// noCredential is a registrant who chose no password.
type noCredential struct{}

func (noCredential) credential() {}

// Password names the password a registrant chose.
//
// The plaintext is hashed by [Service.Register] with the authenticator the
// service was built with, and what reaches identity is the hash. An empty
// plaintext is [ErrEmptyPassword] rather than a passwordless registration: a
// caller who means that says [NoPassword], and a form that submitted nothing
// is a bug this package will not turn into an account.
func Password(plaintext string) Credential { return passwordCredential{plaintext: plaintext} }

// NoPassword names a registrant who will hold no password.
//
// It is a supported arrival rather than an unfinished one — identity says as
// much of [github.com/primandproper/platform-go/v14/identity.User.HashedPassword]
// — and it is how a great deal of the world signs in now. What it costs is
// stated rather than left to be discovered: the ways back in for such a person
// are a passkey ceremony
// ([github.com/primandproper/platform-go/v14/authentication/passkeys]) or
// attaching a password later through [Service.AttachPassword], which is what
// the verification mail's link is good for. This package's four doors all prove
// a password, so registering with none and doing neither of those produces
// somebody who can verify their address and then go no further.
func NoPassword() Credential { return noCredential{} }

// Registration is somebody arriving: who they are, what they will own, and how
// they will prove who they are.
//
// It is one value rather than a list of arguments because the list is what a
// registration grows, and every growth would otherwise be a break — which is
// the reading [ClaimsInput] already takes of the same problem.
type Registration struct {
	_ struct{} `json:"-"`

	// User is the registrant, as identity's registrar wants them: the handles,
	// the names, the addresses. The credential fields on it are ignored and
	// overwritten — HashedPassword is produced here from Credential, and
	// EmailAddressVerificationToken is minted here — because a caller who could
	// supply either is a wire client who could choose somebody's secret.
	User *identity.User `json:"user"`

	// Account is the first account the registrant owns, and is required for a
	// registration that mints one. It is ignored by a registration answering an
	// invitation, which joins an account that already exists.
	Account *identity.Account `json:"account"`

	// Credential is how they will prove who they are. It is required: see
	// [Credential] for why naming none is refused rather than read as
	// [NoPassword].
	Credential Credential `json:"-"`

	// InvitationID and InvitationToken name an invitation this registration
	// answers, and are empty for an ordinary one.
	//
	// Naming one changes which of identity's two registrations runs: the
	// registrant is created and the invitation answered in their name on one
	// transaction, and no account is minted, because somebody arriving on an
	// invitation is joining one that already exists. An invitation that has
	// expired, been withdrawn, already been answered or was presented with the
	// wrong token takes the registration down with it.
	InvitationID    string `json:"invitationID"`
	InvitationToken string `json:"-"`

	// InvitationStatusNote is what the answer records about itself, and is
	// carried through to the invitation's row.
	InvitationStatusNote string `json:"invitationStatusNote"`

	// OwnerRoles are the roles the registrant holds in the account they own.
	// They are the consumer's role names and are required for a registration
	// that mints an account: a membership with none is a member who may do
	// nothing. They are ignored by a registration answering an invitation,
	// which takes its roles off the invitation.
	OwnerRoles []string `json:"ownerRoles"`
}

// Registered is what a registration produced.
//
// It is identity's own result plus the one secret no read can hand back. Which
// of the two identity results it was built from is legible from its fields:
// Account is set by a registration that minted one and Invitation by one that
// answered one, and never both.
type Registered struct {
	_ struct{} `json:"-"`

	// User is the registrant as the row holds them — the ID the write minted,
	// the creation time the schema stamped — and is redacted, unlike identity's
	// own registration results. Registering is the one operation here that
	// assembles a credential rather than proving one, and the value it hands
	// back is the only copy of the user that would carry a hash out of it.
	User *identity.User `json:"user"`

	// Account is the account the registrant owns, and is nil for a registration
	// that answered an invitation.
	Account *identity.Account `json:"account"`

	// Membership puts them in an account and is their default, whichever
	// registration ran.
	Membership *identity.Membership `json:"membership"`

	// Invitation is the invitation as it stands after the answer, redacted, and
	// is nil for a registration that minted an account.
	Invitation *identity.Invitation `json:"invitation"`

	// EmailAddressVerificationToken is the secret the registrant's verification
	// link carries. It is minted by this service and is the only copy: the
	// column holds its digest, and no read of any store here can return it.
	//
	// It is what [Service.VerifyEmailAddress] is answered with, which is what
	// promotes the registrant out of identity.StatusUnverified and lets them
	// sign in at all — so a consumer that drops this has registered somebody
	// who cannot get in. Mail it, or queue the mail from the hook identity's
	// registration fires on the transaction that wrote the row.
	EmailAddressVerificationToken string `json:"-"`
}

// Register creates somebody who can then sign in: the user, what they own, and
// the credential they chose, with the hashing on the side of the boundary that
// is allowed to hash.
//
// # Why it is here and not in identity
//
// identity never hashes. It stores what an engine produced, which is why its
// registration takes a user whose HashedPassword is already set and why its
// wire schema carries no password at all — a plaintext one there would put the
// choice of hashing engine into the transport. That is right, and it left a
// gap: a registration over a transport produced a user with no credential, and
// the only methods for attaching one afterwards required the sign-in the user
// could not do. This service holds the authenticator, so this is where a
// registration that carries a credential belongs.
//
// The directory work is still identity's. This hashes, mints the verification
// token, and calls identity's Service — which owns the owner assignment, the
// default account and its own hooks, all in one transaction. Reaching past it
// to the store would mean reimplementing the first two and skipping the third.
//
// # What it decides
//
// Nothing about policy, as everywhere else here. Whether this person may
// register at all, whether a captcha was solved, what the account is named: all
// of it is the consumer's, in front of this call. Whether the password is good
// enough is the consumer's too, and is the one rule this service will apply on
// their behalf, because a mounted transport leaves nowhere in front of this call
// to apply it: a service built with [WithPasswordPolicy] refuses a password the
// policy refuses with [ErrPasswordRefused], before anything is hashed or
// written. What this package will not do is accept a registration that did not
// say how the registrant will prove who they are — see [Credential].
//
// It mints no second-factor secret, which is a departure from the flow some
// applications ship. Enrolment stays behind authentication —
// [Service.RefreshTOTPSecret] and then [Service.VerifyTOTPSecret], both of
// which require a signed-in caller — because an unauthenticated endpoint that
// takes a user ID and confirms whether a code matches is an enumeration oracle
// with a brute-force surface attached. The flow this package does ship is
// register, verify, sign in, enroll.
//
// The registrant lands in identity.StatusUnverified, which admits no sign-in.
// What promotes them is [Service.VerifyEmailAddress], answered with the token
// on the [Registered] this returns, or [Service.CompleteVerification] for a
// consumer who proved what their own registration asked instead.
//
// It requires [WithRegistrar] and refuses with [ErrRegistrationNotConfigured]
// until it has one: a consumer using this service as a credential check over a
// directory somebody else fills registers nobody.
func (s *Service) Register(
	ctx context.Context,
	scope tenancy.Scope,
	registration *Registration,
) (registered *Registered, err error) {
	ctx, op, done := s.begin(ctx, opRegister,
		observability.WithValue(scopeKey, scope.String()),
	)
	defer func() { done(err) }()

	if registration == nil {
		return nil, op.Error(ErrNilRegistration, "registering a user")
	}

	if registration.User == nil {
		return nil, op.Error(identity.ErrNilUser, "registering a user")
	}

	if s.registrar == nil {
		return nil, op.Error(ErrRegistrationNotConfigured, "registering a user")
	}

	// Hashed before anything is written and outside any transaction, which is
	// the shape every credential write here has: argon2 is expensive by design,
	// and holding a write transaction open across it is the mistake this
	// package exists to stop a consumer making.
	hashed, err := s.hashRegistrationCredential(ctx, op, registration.Credential)
	if err != nil {
		return nil, err
	}

	// Minted here rather than taken from the caller. The column holds a digest,
	// so this is the only copy that will ever exist, and a registration arriving
	// over a transport must not be able to choose the secret that proves its own
	// address — which is what accepting one from the caller would allow.
	token, err := s.generateSecret(ctx)
	if err != nil {
		return nil, op.Error(err, "generating an email verification token")
	}

	// The caller's value is left alone, for the reason identity's registrar
	// leaves it alone: a registration that wrote a hash and a live token back
	// onto the argument would hand both to whatever the caller does with it
	// next.
	user := *registration.User
	user.HashedPassword = hashed
	user.EmailAddressVerificationToken = token

	// The deadline is set here, beside the mint, because this is where the clock
	// and the token meet. identity's store requires one and refuses a zero one:
	// a verification link proves an address, promotes the registrant, and is
	// what Service.AttachPassword answers with a first password, so one that
	// never expires is one that still claims this account out of a mailbox years
	// from now. See DefaultVerificationLinkTTL for the window and why it is the
	// longest this package hands out.
	user.EmailAddressVerificationTokenExpiresAt = pointer.To(
		s.clk.Now().UTC().Add(s.verificationLinkTTL))

	if registration.InvitationID != "" || registration.InvitationToken != "" {
		registered, err = s.registerWithInvitation(ctx, scope, registration, &user)
	} else {
		registered, err = s.registerWithAccount(ctx, scope, registration, &user)
	}

	if err != nil {
		return nil, op.Error(err, "registering a user")
	}

	op.Set(userIDKey, registered.User.ID)

	return registered, nil
}

// hashRegistrationCredential turns the credential a registration named into the
// hash identity stores, and is where the closed set is spent.
//
// A registration naming no credential is refused here rather than defaulted,
// which is the whole point of the type: see [Credential].
//
// It reports its own refusals, the way userByVerificationToken does, because
// the descriptions differ and only one of them is about hashing: a credential
// that named no arm and a password arm that named no password are both the
// registration being read, and filing either under a hash that was never
// attempted tells whoever is holding the error the wrong thing about where it
// came from.
func (s *Service) hashRegistrationCredential(
	ctx context.Context,
	op observability.Operation,
	credential Credential,
) (string, error) {
	switch c := credential.(type) {
	case passwordCredential:
		if c.plaintext == "" {
			return "", op.Error(ErrEmptyPassword, "reading a registration's credential")
		}

		if err := s.checkPassword(ctx, c.plaintext); err != nil {
			return "", op.Error(err, "reading a registration's credential")
		}

		hashed, err := s.authenticator.HashPassword(ctx, c.plaintext)
		if err != nil {
			return "", op.Error(err, "hashing a registering user's password")
		}

		return hashed, nil
	case noCredential:
		return "", nil
	default:
		// nil lands here, which is the case this exists for. So does a type from
		// outside this package, which the sealing method makes unconstructable.
		return "", op.Error(ErrNoCredentialNamed, "reading a registration's credential")
	}
}

// registerWithAccount is the ordinary registration: a user, the account they
// own, and the membership between them.
func (s *Service) registerWithAccount(
	ctx context.Context,
	scope tenancy.Scope,
	registration *Registration,
	user *identity.User,
) (*Registered, error) {
	if registration.Account == nil {
		return nil, identity.ErrNilAccount
	}

	result, err := s.registrar.Register(ctx, scope, user, registration.Account, registration.OwnerRoles)
	if err != nil {
		return nil, err
	}

	// A Registrar is a seam a consumer may implement, and one that answered with
	// neither a registration nor an error would otherwise be a panic here rather
	// than a refusal with a name on it.
	if result == nil || result.User == nil {
		return nil, ErrRegistrationIncomplete
	}

	return &Registered{
		User:                          result.User.Redacted(),
		Account:                       result.Account,
		Membership:                    result.Membership,
		EmailAddressVerificationToken: user.EmailAddressVerificationToken,
	}, nil
}

// registerWithInvitation is the registration that answers an invitation, which
// mints no account: the registrant is joining one that already exists.
func (s *Service) registerWithInvitation(
	ctx context.Context,
	scope tenancy.Scope,
	registration *Registration,
	user *identity.User,
) (*Registered, error) {
	result, err := s.registrar.RegisterWithInvitation(
		ctx, scope, user,
		registration.InvitationID,
		registration.InvitationToken,
		registration.InvitationStatusNote,
	)
	if err != nil {
		return nil, err
	}

	if result == nil || result.User == nil {
		return nil, ErrRegistrationIncomplete
	}

	return &Registered{
		User:                          result.User.Redacted(),
		Membership:                    result.Membership,
		Invitation:                    result.Invitation,
		EmailAddressVerificationToken: user.EmailAddressVerificationToken,
	}, nil
}

// verificationTokenBytes is how much randomness a verification link's token
// carries before encoding: thirty-two bytes from a CSPRNG, which is what this
// module's other mailed secrets carry.
//
// It is a constant rather than an option because there is no deployment for
// which a shorter one is right, and the column holds a digest of whatever this
// produces, so the length costs a consumer nothing.
const verificationTokenBytes = 32

// generateSecret mints the secret a verification link carries. It exists once,
// in the [Registered] a registration answers with; what reaches the column is
// its digest, and no read can hand the secret back.
func (s *Service) generateSecret(ctx context.Context) (string, error) {
	secret, err := s.secrets.GenerateBase64EncodedString(ctx, verificationTokenBytes)
	if err != nil {
		return "", platformerrors.Wrap(err, "generating an email verification token")
	}

	return secret, nil
}
