package signin

import (
	"context"

	"github.com/primandproper/platform-go/v14/identity"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// Verifications is what the three link-answered and standing-changing
// operations need from identity, and it is optional: a service built without
// [WithVerifications] refuses each of them with
// [ErrVerificationsNotConfigured].
//
// It is separate from [Directory] rather than three more methods on it, and
// that is deliberate. Directory is the interface the component holding
// everybody's passwords depends on, kept to what a sign-in needs so that it
// cannot be made to do more; these three are a different job, wanted by a
// different set of consumers, and adding them would break every implementer of
// an interface a consumer is expected to satisfy themselves.
//
// It is store-level where [Registrar] is service-level, for the reason the
// operations differ: identity's Service opens a transaction per method, and
// marking an address proven and moving the standing it confers are one fact
// that must commit once.
//
// [github.com/primandproper/platform-go/v14/identity.Store] satisfies it.
type Verifications interface {
	// GetUserByEmailVerificationToken reads the live user a verification link
	// names, by the digest of the token rather than by the token. A link that
	// has already been answered matches nobody, because verifying clears the
	// column.
	GetUserByEmailVerificationToken(
		ctx context.Context,
		q database.SQLQueryExecutor,
		scope tenancy.Scope,
		token string,
	) (*identity.User, error)

	// MarkUserEmailAddressVerified stamps the address as proven and burns the
	// token, comparing its digest in the statement's own predicate so that two
	// clicks on one link write once.
	MarkUserEmailAddressVerified(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		userID, token string,
	) error

	// UpdateUserAccountStatus moves a user between statuses. This package calls
	// it for exactly one move — StatusUnverified to StatusGood — and never to
	// suspend, terminate or reinstate anybody.
	UpdateUserAccountStatus(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		userID string,
		status identity.AccountStatus,
		explanation string,
	) error
}

// PasswordAttachment is somebody claiming an account that holds no password,
// answered with the link that was mailed to them.
type PasswordAttachment struct {
	_ struct{} `json:"-"`

	// Token is the secret the verification link carries, which is the whole of
	// this request's authority — see [Service.AttachPassword].
	Token string `json:"-"`

	// NewPassword is the password they chose. Whether it is long enough,
	// unusual enough, or unlike anything else is the consumer's rule, applied
	// before this call: this package holds no password policy, and the one rule
	// it applies is that it is not empty.
	NewPassword string `json:"-"`
}

// Verification is somebody being promoted out of
// [github.com/primandproper/platform-go/v14/identity.StatusUnverified], and
// what was proven to get them there.
//
// It is one value for both doors because a consumer recording this wants one
// event with a reason on it rather than two shapes to handle — and because the
// two doors differ in exactly one fact, which is the field below.
type Verification struct {
	_ struct{} `json:"-"`

	// User is who was promoted, redacted, as the row stands after the writes.
	User *identity.User `json:"user"`

	// EmailAddressProven reports which door this came through: true for
	// [Service.VerifyEmailAddress], where somebody answered a mailed link, and
	// false for [Service.CompleteVerification], where the consumer proved
	// whatever their own registration asked instead.
	//
	// It is worth recording rather than inferring, because the second case says
	// nothing about the address: no link was answered, so the address on the
	// user below is as unproven after this as it was before.
	EmailAddressProven bool `json:"emailAddressProven"`

	// Promoted reports whether this actually moved the user's status. It is
	// false where the row was already past StatusUnverified, which is the
	// second click on a link and is not an error.
	Promoted bool `json:"promoted"`
}

// AttachPassword gives a password to somebody who holds none, answered with the
// verification link that was mailed to them.
//
// # Why it is not UpdatePassword
//
// [Service.UpdatePassword] demands the current password through reauthenticate
// and must keep demanding it: relaxing that check is precisely how a
// password-change endpoint becomes a password-reset endpoint. Somebody who has
// never held a password cannot answer it, so this is a second method rather
// than a relaxed precondition on the first.
//
// # What authorizes it
//
// The token, and it has to be the token. The subject cannot be signed in — they
// hold no password, and a registrant's status admits no sign-in until they are
// verified — so the two proofs every other credential write here rests on are
// both unavailable. What is left is the secret that went to the address the
// account was registered with, which is the only thing that reaches the actual
// person. That is the same authority a consumer's "claim your account" mail has
// always carried.
//
// It is refused for a user who already holds a password with
// [ErrPasswordAlreadySet], and that refusal is what keeps the capability
// narrow: an outstanding link can furnish an account that has no password, once,
// and can do nothing to an account that has one. Somebody who has a password and
// has forgotten it goes through
// [github.com/primandproper/platform-go/v14/authentication/passwordreset],
// which is the flow with an expiry, a redemption stamp and a revocation.
//
// # What it does not do
//
// It does not spend the token and it does not verify the address. The link is
// still live afterwards, which is what lets the same one go on to
// [Service.VerifyEmailAddress] — attaching a credential and proving an address
// are two facts, and a consumer whose flow does both from one click does them in
// that order. It does not move the user's status either, so a registrant who
// attaches a password and stops there still cannot sign in.
//
// It requires [WithVerifications] and refuses with
// [ErrVerificationsNotConfigured] until it has one.
func (s *Service) AttachPassword(
	ctx context.Context,
	scope tenancy.Scope,
	attachment *PasswordAttachment,
) (err error) {
	ctx, op, done := s.begin(ctx, opAttachPassword,
		observability.WithValue(scopeKey, scope.String()),
	)
	defer func() { done(err) }()

	if attachment == nil {
		return op.Error(ErrNilPasswordAttachment, "attaching a password")
	}

	if attachment.Token == "" {
		return op.Error(ErrEmptyVerificationToken, "attaching a password")
	}

	if attachment.NewPassword == "" {
		return op.Error(ErrEmptyPassword, "attaching a password")
	}

	user, err := s.userByVerificationToken(ctx, op, scope, attachment.Token, "attaching a password")
	if err != nil {
		return err
	}

	// The refusal that keeps the mailed link from being a password reset. It is
	// the specific answer rather than the collapsed one because the caller is
	// holding a secret that was mailed to this account's own address: they are
	// the subject, and "you already have a password" tells them nothing about
	// somebody else.
	if user.HasPassword() {
		return op.Error(ErrPasswordAlreadySet, "attaching a password")
	}

	hashed, err := s.authenticator.HashPassword(ctx, attachment.NewPassword)
	if err != nil {
		return op.Error(err, "hashing an attached password")
	}

	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		// The precondition is asked again, on the transaction that is about to
		// write. The read that resolved the token ran on the reader and outside
		// any transaction, with a password hash computed in between, so "this
		// account holds no password" was established at a moment that has since
		// passed — and this write is the one that must not happen if it has
		// stopped being true. A write that does not repeat the test its own
		// select made is a write that acts on a row state nobody is still
		// asserting.
		current, txErr := s.directory.GetUser(ctx, tx, scope, user.ID)
		if txErr != nil {
			return txErr
		}

		if current.HasPassword() {
			return ErrPasswordAlreadySet
		}

		if txErr = s.directory.UpdateUserPassword(ctx, tx, scope, user.ID, hashed); txErr != nil {
			return txErr
		}

		return s.hooks.AfterAttachPassword(ctx, tx, scope, current.Redacted())
	}); err != nil {
		return op.Error(err, "writing an attached password")
	}

	return nil
}

// VerifyEmailAddress answers a verification link: it proves the address, spends
// the link, and promotes the user out of
// [github.com/primandproper/platform-go/v14/identity.StatusUnverified] so they
// can sign in.
//
// The promotion is the half that was missing. identity's own
// MarkUserEmailAddressVerified stamps the address and touches no status, and
// the only status mover it ships is the operator's write behind an operator's
// permission — which no registration flow can hold. So a registrant proved
// their address and stayed exactly as unable to sign in as before. Both writes
// happen on one transaction here, with the hook, because a proven address and
// the standing it confers are one fact.
//
// It promotes only from StatusUnverified. A suspended or terminated user who
// answers an outstanding link has their address stamped and their standing left
// alone: the link proves an address, and an operator's decision is not
// something an email can overturn. [Verification.Promoted] reports which
// happened.
//
// A token that names nobody is [ErrInvalidVerificationToken], which wraps
// [ErrInvalidCredentials] and so reads on both transports exactly as a wrong
// password does. Expired, already spent, never issued and simply wrong are one
// answer on purpose: the caller's remedy is the same in every case, and telling
// them apart tells whoever is guessing which guesses are getting warm. The
// second click on one link lands there too, because verifying clears the
// column the first click matched.
//
// It requires [WithVerifications] and refuses with
// [ErrVerificationsNotConfigured] until it has one.
func (s *Service) VerifyEmailAddress(
	ctx context.Context,
	scope tenancy.Scope,
	token string,
) (err error) {
	ctx, op, done := s.begin(ctx, opVerifyEmailAddress,
		observability.WithValue(scopeKey, scope.String()),
	)
	defer func() { done(err) }()

	if token == "" {
		return op.Error(ErrEmptyVerificationToken, "verifying an email address")
	}

	user, err := s.userByVerificationToken(ctx, op, scope, token, "verifying an email address")
	if err != nil {
		return err
	}

	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		// The token is compared again inside the store's own predicate rather
		// than trusted from the read above, which is what makes two clicks on
		// one link write once: the second finds the column already cleared and
		// matches nothing.
		if txErr := s.verifications.MarkUserEmailAddressVerified(ctx, tx, scope, user.ID, token); txErr != nil {
			return txErr
		}

		return s.promote(ctx, tx, scope, user, true)
	}); err != nil {
		return op.Error(err, "recording a verified email address")
	}

	return nil
}

// CompleteVerification promotes somebody whose consumer proved whatever their
// own registration asked — a phone number, a payment, an operator's nod, a
// document — rather than an email address.
//
// It exists because the registration this package ships is not the only kind.
// identity.StatusUnverified means "has not proven whatever registration asked
// of them", and what was asked is the consumer's; a module that only ever
// promoted on a mailed link would leave every other application reaching for
// the operator's status write, which is a permission no registration flow can
// hold and a hook that records the wrong event.
//
// It stamps no address, because none was proven: [Verification] carries
// EmailAddressProven false, and whatever the row said about the address before
// this call it still says afterwards. An outstanding verification link is left
// alone and stays answerable.
//
// # Who may call it
//
// The consumer, having decided. There is no RPC for it and there will not be:
// a transport door taking a user ID and promoting them is an unauthenticated
// account-standing write, and the proof it would have to check is one only the
// consumer holds. This is the same posture identity takes on its reset path —
// the caller has already established that whoever is asking may, and a check
// this layer could make would be one the flow with no email to answer cannot
// pass.
//
// Promoting somebody who is already past StatusUnverified is not an error and
// writes nothing: [Verification.Promoted] is false, and the hook still runs, so
// a consumer's record shows the call happened and changed nothing. A suspended
// or terminated user is left where an operator put them, for the reason
// [Service.VerifyEmailAddress] leaves them there.
//
// It requires [WithVerifications] and refuses with
// [ErrVerificationsNotConfigured] until it has one.
func (s *Service) CompleteVerification(
	ctx context.Context,
	scope tenancy.Scope,
	userID string,
) (err error) {
	ctx, op, done := s.begin(ctx, opCompleteVerification,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(userIDKey, userID),
	)
	defer func() { done(err) }()

	if userID == "" {
		return op.Error(ErrEmptyUserID, "completing a verification")
	}

	if s.verifications == nil {
		return op.Error(ErrVerificationsNotConfigured, "completing a verification")
	}

	user, err := s.directory.GetUser(ctx, s.client.Reader(), scope, userID)
	if err != nil {
		return op.Error(err, "reading the user being verified")
	}

	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		return s.promote(ctx, tx, scope, user, false)
	}); err != nil {
		return op.Error(err, "completing a verification")
	}

	return nil
}

// promote is the write both verification doors share: the status move, the
// read-back, and the hook.
//
// It is one function rather than two copies because the condition is the part
// that can be got wrong twice — a copy that promoted from any status would
// reinstate a banned user, and which of the two doors grew that copy would be
// whichever one was written second.
func (s *Service) promote(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	user *identity.User,
	emailAddressProven bool,
) error {
	promoted := user.AccountStatus == identity.StatusUnverified

	if promoted {
		// No explanation. The column is prose meant for a user who is being
		// refused something, and there is nothing to explain about somebody
		// having done what was asked of them.
		if err := s.verifications.UpdateUserAccountStatus(
			ctx, tx, scope, user.ID, identity.StatusGood, "",
		); err != nil {
			return err
		}
	}

	// Read on the transaction that made the writes, so what the hook is handed
	// carries this operation's own stamps rather than the copy read before it.
	after, err := s.directory.GetUser(ctx, tx, scope, user.ID)
	if err != nil {
		return err
	}

	return s.hooks.AfterVerify(ctx, tx, scope, &Verification{
		User:               after.Redacted(),
		EmailAddressProven: emailAddressProven,
		Promoted:           promoted,
	})
}

// userByVerificationToken resolves a mailed link to the user it names, and is
// the one read the two token-answered doors share.
//
// Every way of failing to resolve one is ErrInvalidVerificationToken. What the
// store distinguishes — no such digest, a row that is gone — is not something
// this package passes on, for the reason the sign-in doors collapse their four
// refusals into one: told apart, they are an oracle for whoever is guessing.
func (s *Service) userByVerificationToken(
	ctx context.Context,
	op observability.Operation,
	scope tenancy.Scope,
	token, describing string,
) (*identity.User, error) {
	if s.verifications == nil {
		return nil, op.Error(ErrVerificationsNotConfigured, "%s", describing)
	}

	user, err := s.verifications.GetUserByEmailVerificationToken(ctx, s.client.Reader(), scope, token)
	if err != nil {
		op.SpanOnly(reasonKey, err.Error())

		return nil, op.Error(ErrInvalidVerificationToken, "%s", describing)
	}

	op.Set(userIDKey, user.ID)

	return user, nil
}
