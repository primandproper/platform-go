package signin

import (
	"context"
	"time"

	"github.com/primandproper/platform-go/v15/identity"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// Verifications is what the link-answered and standing-changing
// operations need from identity, and it is optional: a service built without
// [WithVerifications] refuses each of them with
// [ErrVerificationsNotConfigured].
//
// It is separate from [Directory] rather than more methods on it, and
// that is deliberate. Directory is the interface the component holding
// everybody's passwords depends on, kept to what a sign-in needs so that it
// cannot be made to do more; these are a different job, wanted by a
// different set of consumers, and adding them would break every implementer of
// an interface a consumer is expected to satisfy themselves.
//
// It is store-level where [Registrar] is service-level, for the reason the
// operations differ: identity's Service opens a transaction per method, and
// marking an address proven and moving the standing it confers are one fact
// that must commit once.
//
// [github.com/primandproper/platform-go/v15/identity.Store] satisfies it.
type Verifications interface {
	// GetUserByEmailVerificationToken reads the live user a verification link
	// names, by the digest of the token rather than by the token. A link that
	// has already been answered matches nobody, because verifying clears the
	// column, and one whose deadline has passed is refused rather than resolved.
	//
	// The deadline is the store's to enforce and not this package's, which is
	// why nothing here reads a clock: the column is stamped by whatever minted
	// the link, and the implementation compares it against the clock that
	// stamped it. A check repeated here would be a second boundary free to
	// disagree with that one about the second a link dies in.
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

	// MarkUserEmailAddressProven stamps the address as proven for a caller
	// holding no verification token, and clears any outstanding one.
	//
	// It is what [Service.RedeemMagicLink] calls. A sign-in link answered out of
	// the registrant's own inbox proves reachability exactly as a verification
	// link does, and cannot satisfy the method above: that statement compares
	// the digest the row carries, and this caller holds a token from another
	// table. The single-use property the guard buys is held there instead, by
	// the sign-in link store's own guarded spend.
	MarkUserEmailAddressProven(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		userID string,
	) error

	// SetUserEmailAddressVerificationToken stores the digest of the token a new
	// verification link carries, and the deadline it dies at, replacing any
	// outstanding one — so the link mailed before stops working. It refuses an
	// address that is already proven with
	// identity.ErrEmailAddressAlreadyVerified rather than withdrawing the proof
	// to make room, which is what keeps [Service.RequestVerificationEmail] from
	// un-verifying anybody even when the read ahead of it has gone stale.
	SetUserEmailAddressVerificationToken(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		userID, token string,
		expiresAt time.Time,
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
// [github.com/primandproper/platform-go/v15/identity.StatusUnverified], and
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
// and can do nothing to an account that has one. It is narrow in time as well:
// the link carries a deadline, stamped beside its digest when it was minted, and
// this door is closed once that has passed. Somebody who has a password and has
// forgotten it goes through
// [github.com/primandproper/platform-go/v15/authentication/passwordreset],
// which is still the flow with a redemption stamp and a revocation of its own.
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
// A service built with [WithPasswordPolicy] applies it to the password being
// attached, and one built with [WithAccountPasswordPolicy] applies that after
// it; a refusal from either is [ErrPasswordRefused] with the link still live.
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

	// After the token is resolved and before anything is hashed, so a refusal
	// leaves the link exactly as live as it was.
	if err = s.checkPassword(ctx, attachment.NewPassword); err != nil {
		return op.Error(err, "attaching a password")
	}

	if err = s.checkAccountPassword(ctx, user, attachment.NewPassword); err != nil {
		return op.Error(err, "attaching a password")
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
// [github.com/primandproper/platform-go/v15/identity.StatusUnverified] so they
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
// All four are reachable. Expired is the store's refusal —
// identity.ErrEmailVerificationLinkExpired, raised against the deadline stamped
// beside the digest — collapsed here with the rest, and the specific reason is
// recorded on the operation's span so that an operator can tell a dead link
// from a wrong one without the caller being told. How long that window is is
// the registration's to choose: see [DefaultVerificationLinkTTL].
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

		return s.promote(ctx, tx, scope, user.ID, true)
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

	// No read out here. The standing this promotion turns on is read by promote
	// on the transaction that writes it, and a copy read before that one opened
	// would be a second answer to the same question with a gap in between.
	// Resolving a user ID nobody holds is that read's refusal, unchanged.
	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		return s.promote(ctx, tx, scope, userID, false)
	}); err != nil {
		return op.Error(err, "completing a verification")
	}

	return nil
}

// VerificationMail is what a [VerificationMailer] is handed once a new
// verification link has been minted and committed.
type VerificationMail struct {
	_ struct{} `json:"-"`

	// ExpiresAt is when the link stops being answerable, for a mail that says
	// so. It is the deadline stored beside the digest, from the same clock read.
	ExpiresAt time.Time `json:"expiresAt"`

	// User is who the link is for, redacted, as the row stood when the link was
	// minted. The address to mail is on it.
	User *identity.User `json:"user"`

	// Token is the secret to render into the link's URL, and this is the only
	// place it will ever exist: the column holds its digest. A mailer that drops
	// it has sent a link nobody can answer.
	//
	// It is excluded from JSON for the reason MagicLinkIssuance.Secret is: do
	// not log it, do not store it, and do not put it anywhere but the one mail.
	Token string `json:"-"`
}

// VerificationMailer delivers the verification links this service mints: the
// first, which [Service.Register] mints with the registrant, and every one
// [Service.RequestVerificationEmail] and its anonymous sibling mint after it.
//
// It is a seam rather than a dependency on an email package for the reason
// [MagicLinkMailer] is: the sender, the template, the wording and the URL the
// token is rendered into are the consumer's, and what this package decides is
// when. It is a seam of its own rather than that one because a consumer
// implements only the mail for the doors they mount.
//
// An error from a mailer fails RequestVerificationEmail, and by then the link is
// committed — so the new link exists, the previous one is retired, and nobody
// has either. That is the honest reading of a send that failed, and asking again
// mints another. It fails Register the same way, with the registrant committed
// and handed back beside the error.
type VerificationMailer interface {
	SendVerification(ctx context.Context, mail *VerificationMail) error
}

// VerificationMailerFunc adapts a function to VerificationMailer.
type VerificationMailerFunc func(ctx context.Context, mail *VerificationMail) error

// SendVerification implements VerificationMailer.
func (f VerificationMailerFunc) SendVerification(ctx context.Context, mail *VerificationMail) error {
	return f(ctx, mail)
}

// RequestVerificationEmail mails somebody a fresh link that proves their
// address, and retires the one they were sent before.
//
// It is the signed-in resend: for somebody whose address stopped being proven
// because it changed, or an unproven user an operator admitted anyway. A
// registrant cannot use it — an unproven registration is refused at the
// password door with [ErrUserUnverified], so they are never signed in to ask —
// and [Service.RequestVerificationEmailByAddress] is their door. This one is
// about the caller and nobody else — userID is the signed-in person's own,
// taken off their principal by a transport — so there is no enumeration to
// defend against here, and the answers are specific.
//
// # A proven address is refused
//
// With [ErrEmailAddressAlreadyVerified], and the proof is left exactly as it
// was. A link and a proof may not stand on one row together, and asking for a
// link is not a statement that a proven address has stopped being the
// caller's, so this is not a way to un-verify anybody. The refusal is made on
// the transaction that writes, and identity's store makes it again in the
// write's own predicate, so a proof that lands in between is refused rather than
// withdrawn.
//
// An address that has stopped being proven — because it changed — is the case
// this door serves beside a registrant's: the change withdraws the proof, and
// a flow changing an address calls this afterwards to mail the new one a link.
//
// # What it mints
//
// A token from the same source and with the same deadline the registration's
// link has, [WithVerificationLinkTTL]. The digest replaces whatever link was
// outstanding, so only the newest link verifies. The token reaches the
// [VerificationMailer] and nothing else: it is not returned, not logged and not
// handed to [Hooks.AfterRequestVerificationEmail], which runs in the
// transaction that stored the digest and is told only who asked.
//
// The link is committed before the mail is sent, and the mail is sent after the
// commit rather than from inside it, for the reason [Service.RequestMagicLink]
// gives: a link mailed for a transaction that then rolled back is a URL in
// somebody's inbox that will never answer.
//
// # Rate limiting is the deployment's
//
// In front of this call. It mails on every request, so a deployment without a
// limit in front of it lets a signed-in person send an unbounded stream of mail
// through its domain, and each one retires the last.
//
// It requires [WithVerifications] and [WithVerificationMailer], and refuses
// with [ErrVerificationsNotConfigured] or [ErrVerificationMailerNotConfigured]
// until it has both.
func (s *Service) RequestVerificationEmail(
	ctx context.Context,
	scope tenancy.Scope,
	userID string,
) (err error) {
	ctx, op, done := s.begin(ctx, opRequestVerificationEmail,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(userIDKey, userID),
	)
	defer func() { done(err) }()

	if userID == "" {
		return op.Error(ErrEmptyUserID, "requesting a verification link")
	}

	if err = s.resendConfigured(); err != nil {
		return op.Error(err, "requesting a verification link")
	}

	if err = s.resendVerification(ctx, scope, userID); err != nil {
		return op.Error(err, "requesting a verification link")
	}

	return nil
}

// RequestVerificationEmailByAddress mails a fresh verification link to an
// address whose owner has not proven it, and answers the same way whatever it
// found.
//
// It is the resend for somebody who cannot sign in to ask for one, which is
// every registrant: a user whose registration is unproven is refused at the
// password door with [ErrUserUnverified], and that refusal is where a client
// sends them to a "we mailed you a link — send another" page. The refusal is
// told only to somebody who proved the password, but this door is anonymous
// and takes the address alone, so a registrant who signed up without a
// password — a passkey, a federated identity — reaches it too.
//
// # The answer is the same either way
//
// It returns nil whether the address belongs to nobody, to somebody whose
// address is already proven, to somebody whose standing admits no mail, or to
// somebody it mailed, and it is held to [WithMagicLinkRequestFloor]'s floor
// on every path for [Service.RequestMagicLink]'s reason: an anonymous door that
// answered or timed those apart would be a directory enumerator, and one that
// told a proven address from an unproven one would be a way to learn who has
// finished registering. What it did is on the span and nowhere else. The floor
// is that door's rather than one of its own because the two doors do the same
// work — a read, a mint, a commit and a send — and one floor raised for a slow
// mail server is one that covers both.
//
// A banned or terminated user is mailed nothing. Everybody else whose address
// is unproven — a registrant, or somebody whose address changed — is mailed a
// link exactly as [Service.RequestVerificationEmail] mails one, retiring the
// last.
//
// # Rate limiting is the deployment's
//
// In front of this call, and with more force than in front of the signed-in
// door: anybody can reach this one, and it mails on every request for an
// unproven address. A deployment without a limit in front of it lets a
// stranger fill a registrant's inbox from its domain.
//
// It requires [WithVerifications] and [WithVerificationMailer], and refuses
// with [ErrVerificationsNotConfigured] or [ErrVerificationMailerNotConfigured]
// until it has both — the one answer that is not uniform, since it is a wiring
// failure rather than a fact about any address.
func (s *Service) RequestVerificationEmailByAddress(
	ctx context.Context,
	scope tenancy.Scope,
	emailAddress string,
) (err error) {
	ctx, op, done := s.begin(ctx, opRequestVerificationEmailByAddress,
		observability.WithValue(scopeKey, scope.String()),
	)
	defer func() { done(err) }()

	// Deferred before the first refusal, as RequestMagicLink's is, so every
	// path through this function is padded.
	defer s.padTo(ctx, op, s.clk.Now().Add(s.magicLinkFloor))

	if err = s.resendConfigured(); err != nil {
		return op.Error(err, "requesting a verification link by address")
	}

	if emailAddress == "" {
		return op.Error(ErrEmptyHandle, "requesting a verification link by address")
	}

	user, err := s.directory.GetUserByEmailAddress(ctx, s.client.Reader(), scope, identity.FoldHandle(emailAddress))
	if err != nil {
		if platformerrors.Is(err, identity.ErrUserNotFound) {
			op.SpanOnly(reasonKey, identity.ErrUserNotFound.Error())

			return nil
		}

		// The directory failing rather than a fact about the address, and
		// collapsing it into the silent answer would hide an outage.
		return op.Error(err, "reading the user a verification link was asked for")
	}

	op.Set(userIDKey, user.ID)

	if !admitsMagicLink(user.AccountStatus) {
		op.SpanOnly(reasonKey, statusRefusal(user).Error())

		return nil
	}

	if err = s.resendVerification(ctx, scope, user.ID); err != nil {
		// A proven address is the one refusal the signed-in door names and
		// this one must not: told to a stranger, it says the address's owner
		// finished registering.
		if platformerrors.Is(err, ErrEmailAddressAlreadyVerified) {
			op.SpanOnly(reasonKey, ErrEmailAddressAlreadyVerified.Error())

			return nil
		}

		return op.Error(err, "requesting a verification link by address")
	}

	return nil
}

// resendConfigured is what both resend doors require before they do anything.
func (s *Service) resendConfigured() error {
	if s.verifications == nil {
		return ErrVerificationsNotConfigured
	}

	if s.verificationMailer == nil {
		return ErrVerificationMailerNotConfigured
	}

	return nil
}

// resendVerification is the body both resend doors share: mint a link for
// userID, store its digest in place of the outstanding one, run the hook, and
// mail it once that has committed. A proven address is
// [ErrEmailAddressAlreadyVerified], and keeps its proof.
func (s *Service) resendVerification(ctx context.Context, scope tenancy.Scope, userID string) error {
	// Minted before the transaction opens and outside it, which is the shape
	// Register's mint has: nothing about drawing a secret needs a row locked.
	token, err := s.generateSecret(ctx)
	if err != nil {
		return platformerrors.Wrap(err, "generating an email verification token")
	}

	expiresAt := s.clk.Now().UTC().Add(s.verificationLinkTTL)

	var user *identity.User

	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		current, txErr := s.directory.GetUser(ctx, tx, scope, userID)
		if txErr != nil {
			return txErr
		}

		if current.EmailAddressVerified() {
			return ErrEmailAddressAlreadyVerified
		}

		// The store repeats the test in its own predicate, so a proof landing
		// between the read above and this write is refused rather than
		// withdrawn. Its refusal is identity's sentinel, and it is answered as
		// this package's so the caller sees one refusal whichever check made it.
		if txErr = s.verifications.SetUserEmailAddressVerificationToken(
			ctx, tx, scope, userID, token, expiresAt,
		); txErr != nil {
			if platformerrors.Is(txErr, identity.ErrEmailAddressAlreadyVerified) {
				return ErrEmailAddressAlreadyVerified
			}

			return txErr
		}

		user = current

		return s.hooks.AfterRequestVerificationEmail(ctx, tx, scope, current.Redacted())
	}); err != nil {
		return platformerrors.Wrap(err, "minting a verification link")
	}

	if err = s.verificationMailer.SendVerification(ctx, &VerificationMail{
		User:      user.Redacted(),
		Token:     token,
		ExpiresAt: expiresAt,
	}); err != nil {
		return platformerrors.Wrap(err, "mailing a verification link")
	}

	return nil
}

// promote is the write both verification doors share: the standing read, the
// status move, and the hook.
//
// It is one function rather than two copies because the condition is the part
// that can be got wrong twice — a copy that promoted from any status would
// reinstate a banned user, and which of the two doors grew that copy would be
// whichever one was written second.
//
// It takes a user ID rather than a user, because the standing it turns on is
// the one thing it may not accept from its caller: both doors resolved their
// user before this transaction existed, and this is the read that has to be
// inside it.
func (s *Service) promote(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	userID string,
	emailAddressProven bool,
) error {
	// The standing is read on the transaction that is about to write, not taken
	// from the copy the caller resolved. That copy came off the reader, before
	// this transaction opened — so "this user is still unverified" was
	// established at a moment that has since passed, and an operator's
	// suspension landing in the gap is exactly the decision this test exists to
	// leave standing. A write that does not repeat the row-state test its own
	// select made is a write acting on a state nobody is still asserting.
	current, err := s.directory.GetUser(ctx, tx, scope, userID)
	if err != nil {
		return err
	}

	promoted := current.AccountStatus == identity.StatusUnverified
	after := current

	if promoted {
		// No explanation. The column is prose meant for a user who is being
		// refused something, and there is nothing to explain about somebody
		// having done what was asked of them.
		if err = s.verifications.UpdateUserAccountStatus(
			ctx, tx, scope, userID, identity.StatusGood, "",
		); err != nil {
			return err
		}

		// Read again, after the write this one made, so the hook is handed the
		// standing this operation produced rather than the one it found. The
		// promotion is the only write here that moves a column a redacted user
		// carries, which is why the other path hands on what it already read.
		if after, err = s.directory.GetUser(ctx, tx, scope, userID); err != nil {
			return err
		}
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
