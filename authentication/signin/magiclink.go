package signin

import (
	"context"
	"time"

	"github.com/primandproper/platform-go/v14/identity"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/identifiers"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// MagicLinkStore is where a sign-in link's tokens live, and it is optional: a
// service built without [WithMagicLinkStore] refuses both doors with
// [ErrMagicLinksNotConfigured].
//
// This module ships a SQL implementation,
// [github.com/primandproper/platform-go/v14/authentication/signin/magiclinks],
// together with the DDL it needs, so adopting a passwordless door does not mean
// writing this.
//
// What an implementation owes its callers is not "these three methods". It is
// the properties they exist to hold, none of which the signatures can state:
//
// The secret is never stored. Issue mints it, returns it once, and persists
// something a reader cannot reverse into it.
//
// A link is spendable exactly once, and the store decides which caller spends
// it. Two concurrent Redeem calls for one token must produce one success and one
// refusal, with no cooperation from the caller. That single guarantee is the
// whole of single use, and it is why the method exists rather than the service
// reading a row and writing it back.
//
// Expiry is refused rather than reclaimed. A link past its deadline is dead to
// Redeem whether or not anything has swept it.
//
// Every method takes a tenancy.Scope and none of them offers an unscoped
// variant: an implementation filters on it rather than treating it as a hint. A
// token presented in the wrong scope matches nothing, which is what it is from
// there.
//
// # The transaction is the caller's, and that is the decision
//
// All three take a database.Tx, which is this module's store convention — but
// this is a seam where the convention had a live alternative and lost on the
// merits. links.Store, the other single-use link mechanism in this module,
// deliberately takes no executor: its records are minted by one process and
// redeemed by another, so there is no caller transaction to join, and its
// Resolve has to commit by itself or a transition sitting inside somebody's
// request is a link the next caller still finds active.
//
// That reasoning does not reach here, because a redemption is not the end of
// this flow. It is the beginning of a sign-in: the spend, the promotion it may
// perform, the refresh token it mints and the two hooks a consumer commits
// beside it are one fact, and [Service.login] already commits that fact in one
// transaction for the password door. A spend that committed by itself would put
// the hooks outside it, so a consumer's failure to record the sign-in would
// leave the link already burned and the person asking for another mail.
//
// What it costs is that single use is now the implementation's to buy inside the
// caller's transaction rather than outside it, and an implementation that cannot
// say where its own atomicity comes from does not have any. magiclinks buys it
// with the affected row count of a guarded UPDATE whose predicate repeats every
// row-state test the answer depends on.
type MagicLinkStore interface {
	// Issue mints a link token for a subject, stores its digest, and returns the
	// secret exactly once.
	//
	// It does not check that the subject exists — this store reads no user table
	// — and it does not send anything. Both are the service's. The address on the
	// request is written down and never read by the store: it is the record of
	// where the mail went, which is what Redeem's caller compares against.
	//
	// Issuing again does not withdraw what is outstanding. Somebody who asks for
	// a link twice and then opens the first message has a link that works, which
	// is the behavior the alternative quietly breaks; passwordreset.Store.Issue
	// takes the same reading of the same situation.
	//
	// The row lands when tx commits, and the secret is returned before it does.
	// Send the mail after the commit, not from inside the callback: a link
	// mailed for a transaction that then rolled back is a sign-in nobody can
	// complete, and the store has no way to take it back.
	Issue(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		request *MagicLinkRequest,
	) (*MagicLinkIssuance, error)

	// Redeem spends a secret, atomically, and answers with the link it spent,
	// carrying the subject the sign-in is for and the address it was mailed to.
	//
	// A nil error is a decision rather than an observation: it means this caller,
	// and no other, holds the right to sign that subject in. Do it in the same
	// transaction — that is what the Tx is for.
	//
	// Every refusal is [ErrInvalidMagicLink]: an unknown token, one already
	// followed, one withdrawn and one past its deadline are one answer, for the
	// reason the password door collapses its four. Told apart they are an oracle
	// for whoever is presenting guesses, and the remedy is the same in every
	// case — ask for another mail.
	Redeem(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		secret string,
	) (*MagicLink, error)

	// RevokeForSubject withdraws every outstanding link one person holds and
	// reports how many it withdrew.
	//
	// It is what an account being disabled wants, what somebody saying "that
	// wasn't me" needs, and what an erasure is built on;
	// [Service.RevokeMagicLinksForSubject] is the door those callers reach it
	// through. It is deliberately not what a successful redemption calls: a link
	// that is still outstanding is exposed exactly as much after somebody signs
	// in as it was before, and burning it would cost a person the second mail
	// they asked for without closing anything. The flow that does withdraw on
	// completion is passwordreset's, and it withdraws because the password it
	// was mailed for has changed underneath it.
	//
	// Zero is not an error: somebody who never asked for a link holds none.
	RevokeForSubject(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		subjectID string,
	) (int64, error)
}

type (
	// MagicLink is the record of one issued sign-in link.
	//
	// The token itself is not on it, and there is no field it could go in: what
	// is stored is a digest, and the secret exists once, in the MagicLinkIssuance
	// that produced it. A MagicLink read back from the store carries nothing that
	// redeems anything — what it does carry is the address the mail went to, so
	// it is a record to keep rather than a line to log.
	MagicLink struct {
		_ struct{} `json:"-"`

		// IssuedAt is when the link was minted.
		IssuedAt time.Time `json:"issuedAt"`

		// ExpiresAt is the deadline past which the link is refused. It is
		// compared against the store's clock rather than swept into truth, so a
		// row the sweeper has not reached is already dead.
		ExpiresAt time.Time `json:"expiresAt"`

		// PurgeAfter is when the row may be collected, which is past ExpiresAt by
		// the store's retention window.
		PurgeAfter time.Time `json:"purgeAfter"`

		// RedeemedAt is when the link was followed, or nil while it is unspent.
		RedeemedAt *time.Time `json:"redeemedAt,omitempty"`

		// RevokedAt is when the link was withdrawn, or nil while it stands.
		RevokedAt *time.Time `json:"revokedAt,omitempty"`

		// SubjectID is the person the link signs in. It is opaque to the store —
		// that package reads no user table — so an application whose users live
		// outside identity uses it unchanged.
		SubjectID string `json:"belongsToUser"`

		// EmailAddress is where the mail went, folded the way the directory folds
		// a handle.
		//
		// It is what makes the proof a redemption writes a proof of something:
		// this link demonstrates control of this inbox and of no other, so
		// [Service.RedeemMagicLink] compares it against the address the subject
		// holds at redemption and refuses a link they have since moved away
		// from. Without it a redemption would stamp whichever address the
		// directory row happens to carry by then, which is not the one anybody
		// reached.
		//
		// It is in plain rather than digested, for the reason the store's own
		// column documentation gives: a digest is worth something against the
		// token's thirty-two bytes and nothing against an address somebody can
		// enumerate.
		EmailAddress string `json:"emailAddress"`

		// Scope is whose directory the link was minted in.
		Scope tenancy.Scope `json:"scope"`
	}

	// MagicLinkRequest is what a mint needs: who the link is for, and how long it
	// lives.
	MagicLinkRequest struct {
		_ struct{} `json:"-"`

		// SubjectID is the person the link will sign in.
		SubjectID string `json:"belongsToUser"`

		// EmailAddress is where the mail is going, and the store writes it down
		// exactly as it is given. Hand it the same form the redemption will
		// compare against — for this service, the directory's folded handle —
		// because a second normalization is one free to disagree with the first.
		EmailAddress string `json:"emailAddress"`

		// TTL is how long the link stays redeemable. It arrives on the request
		// rather than being a store default for the reason the refresh token's
		// does: how long a credential works is the service's policy, and a
		// second copy of it beside the table would be the value nobody passed
		// competing with the value somebody chose. It is [WithMagicLinkTTL].
		TTL time.Duration `json:"ttl"`
	}

	// MagicLinkIssuance is what Issue returns: the secret to put in the mail, and
	// the row that was written for it.
	//
	// The two are separate fields rather than a link with a Secret on it because
	// they have different lifetimes. Secret is in memory for as long as it takes
	// to render a URL and hand it to a mailer; Link is the durable half, and it
	// is the one that is safe to keep.
	MagicLinkIssuance struct {
		_ struct{} `json:"-"`

		// Link is what was stored.
		Link *MagicLink `json:"link"`

		// Secret is the raw token, and this is the only place it will ever exist.
		// The store holds a digest of it and cannot reverse one, so a secret that
		// is not sent is a sign-in nobody can complete.
		//
		// It carries no json tag other than this one because it is serializable
		// at all only for a caller that has chosen to move it — do not log it, do
		// not store it, and do not put it in a response body that is not the one
		// mail.
		Secret string `json:"-"`
	}

	// MagicLinkCredentials is somebody answering a sign-in link.
	MagicLinkCredentials struct {
		_ struct{} `json:"-"`

		// Token is the secret the link carries, which is the whole of this
		// request's authority.
		Token string `json:"-"`

		// TOTPCode is the second-factor code, required from a user who holds a
		// proven secret and ignored from one who does not. It is not optional
		// for an enrolled user and cannot be made optional: a door that let a
		// mailed link stand in for an enrolled second factor would be a way
		// around it reachable by whoever controls the inbox. See
		// [Service.RedeemMagicLink].
		TOTPCode string `json:"-"`

		// ActiveAccountID is which account the minted token is for, resolved by
		// the directory, which refuses an account the subject is not a live
		// member of. Empty takes their default, which is what a link followed out
		// of an inbox almost always wants: the request that asked for the mail
		// named an address and nothing else.
		ActiveAccountID string `json:"activeAccountID,omitempty"`
	}

	// MagicLinkMail is what a MagicLinkMailer is handed once a link has been
	// issued and committed.
	MagicLinkMail struct {
		_ struct{} `json:"-"`

		// User is who the link is for, redacted.
		User *identity.User `json:"user"`

		// Issuance carries the secret to render into the URL, and the row it
		// belongs to. A Mailer that drops it has produced a sign-in nobody can
		// complete.
		Issuance *MagicLinkIssuance `json:"issuance"`
	}
)

// MagicLinkMailer delivers the one message this flow sends.
//
// It is a seam rather than a dependency on an email package because what a
// consumer sends is theirs: the address it comes from, the template, the
// wording, the URL the token is rendered into, and whether it goes out through
// their own mailer or a queue. What this package decides is when.
//
// An error from a Mailer fails [Service.RequestMagicLink], and by then the row
// is committed — so the link exists and nobody has it. That is the honest
// reading of a send that failed, and it is what lets a caller retry: asking for
// another mail mints another link and leaves the first one standing.
type MagicLinkMailer interface {
	SendMagicLink(ctx context.Context, mail *MagicLinkMail) error
}

// MagicLinkMailerFunc adapts a function to MagicLinkMailer.
type MagicLinkMailerFunc func(ctx context.Context, mail *MagicLinkMail) error

// SendMagicLink implements MagicLinkMailer.
func (f MagicLinkMailerFunc) SendMagicLink(ctx context.Context, mail *MagicLinkMail) error {
	return f(ctx, mail)
}

// RequestMagicLink mails somebody a link that signs them in.
//
// It is the half of passwordless that makes passwordless a product decision
// rather than an unfinished account. A registration naming [NoPassword] produces
// a person whose only other routes are attaching a password through
// [Service.AttachPassword] or enrolling a passkey; this is "type your email,
// click the link, you are in".
//
// # It answers the same way for everybody
//
// Whatever it finds, it returns nil. An address nobody holds, an address whose
// owner is banned, an address whose owner is terminated: all of them are a nil
// error and no mail, and they take the same time as the address that gets one —
// see [WithMagicLinkRequestFloor]. A door that answered differently would be an
// account enumerator built out of a feature meant to protect accounts, and the
// consumer's own response must not undo that by saying more than this did.
//
// Every path is held to the floor, the ones that report an error included. What
// distinguishes those is what comes back rather than when: a store that will not
// write and a mailer that will not send are this service's failures rather than
// facts about the address, so they are reported as themselves — and padded all
// the same, because an error returned early is as good a signal as a nil one.
//
// # Who gets one
//
// Somebody whose standing admits a sign-in, and somebody still in
// [github.com/primandproper/platform-go/v14/identity.StatusUnverified] — which
// is the case this door exists for, and the one place it parts company with
// [Service.LoginForToken]. A registrant has not proven their address yet, and
// the link this mails is how they do it: following it proves the address and
// promotes them in the same transaction it signs them in. See
// [Service.RedeemMagicLink].
//
// A banned or terminated user is mailed nothing, and the caller is told nothing
// about that, which is the same posture the password door takes one step later.
//
// # Rate limiting is the consumer's
//
// In front of this call, and it is not optional. This door mails on every
// request for an address somebody holds, so a deployment without a limit in
// front of it is a way to send mail through their own domain at somebody else's
// direction. primitives-go's ratelimiting is the piece; the sentence
// [Service.LoginForToken] carries about a rate limit, a lockout and a captcha
// being the consumer's applies here with more force, because the cost of an
// unthrottled attempt is an email rather than a hash.
//
// It requires [WithMagicLinkStore] and [WithMagicLinkMailer], and refuses with
// [ErrMagicLinksNotConfigured] until it has both.
func (s *Service) RequestMagicLink(
	ctx context.Context,
	scope tenancy.Scope,
	emailAddress string,
) (err error) {
	ctx, op, done := s.begin(ctx, opRequestMagicLink,
		observability.WithValue(scopeKey, scope.String()),
	)
	defer func() { done(err) }()

	// Held open for the floor whatever happens below, so that what this call did
	// — or did not do — is not readable off a stopwatch. It is deferred before
	// the first refusal so that every path through this function is padded,
	// including the ones that return early.
	defer s.padTo(ctx, op, s.clk.Now().Add(s.magicLinkFloor))

	if s.magicLinks == nil || s.magicLinkMailer == nil {
		return op.Error(ErrMagicLinksNotConfigured, "requesting a sign-in link")
	}

	if emailAddress == "" {
		return op.Error(ErrEmptyHandle, "requesting a sign-in link")
	}

	// Folded the way the directory folds it, so this reads the row the password
	// door would have read. A second copy of a normalization is a copy that can
	// disagree with the rows.
	handle := identity.FoldHandle(emailAddress)

	user, err := s.directory.GetUserByEmailAddress(ctx, s.client.Reader(), scope, handle)
	if err != nil {
		if platformerrors.Is(err, identity.ErrUserNotFound) {
			// The whole of the enumeration defense, and the reason it is a nil
			// return rather than a sentinel the caller could branch on. Nothing
			// was minted and nothing was sent; the answer is the answer somebody
			// with an account gets.
			op.SpanOnly(reasonKey, identity.ErrUserNotFound.Error())

			return nil
		}

		// Anything else is the directory failing rather than a fact about this
		// address — a connection that is gone, a scope that will not validate —
		// and collapsing it into the silent answer would hide an outage behind a
		// feature that looks like it worked.
		return op.Error(err, "reading the user a sign-in link was asked for")
	}

	op.Set(userIDKey, user.ID)

	if !admitsMagicLink(user.AccountStatus) {
		// Told to the span and to nobody else. An operator reading their own
		// logs can see that a banned user asked for a link; the person asking
		// learns exactly what somebody with no account learns.
		op.SpanOnly(reasonKey, statusRefusal(user).Error())

		return nil
	}

	var issuance *MagicLinkIssuance

	// The mint is in a transaction of its own, and the mail is after it. A link
	// handed to a mailer from inside the callback is one that may be sent for a
	// transaction that then rolled back — a URL in somebody's inbox that will
	// never redeem — and the store has no way to take it back.
	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		issuance, err = s.magicLinks.Issue(ctx, tx, scope, &MagicLinkRequest{
			SubjectID: user.ID,
			// The row's address comes off the user rather than off the argument,
			// so that what is recorded is where the mail is actually going: the
			// mailer below is handed this user, and a caller who typed an
			// equivalent-but-differently-spelled address would otherwise leave a
			// row the redemption's comparison could not match.
			EmailAddress: identity.FoldHandle(user.EmailAddress),
			TTL:          s.magicLinkTTL,
		})

		return err
	}); err != nil {
		return op.Error(err, "issuing a sign-in link")
	}

	if err = s.magicLinkMailer.SendMagicLink(ctx, &MagicLinkMail{
		User:     user.Redacted(),
		Issuance: issuance,
	}); err != nil {
		return op.Error(err, "mailing a sign-in link")
	}

	return nil
}

// RedeemMagicLink answers a sign-in link: it spends the link, proves the address
// it was mailed to, promotes a registrant who was waiting on exactly that, and
// issues a token.
//
// # What it proves, and what follows from it
//
// Control of the inbox the link was mailed to, which is the same fact
// [Service.VerifyEmailAddress] exists to establish. So this door establishes it:
// a redemption stamps that address proven, where it was not already, and
// promotes a user out of
// [github.com/primandproper/platform-go/v14/identity.StatusUnverified], through
// the same [Service.promote] the verification door uses and in the transaction
// that signs them in.
//
// "The inbox the link was mailed to" is the whole of what it proves, so the
// redemption checks that it is still the subject's address and refuses the link
// otherwise. A subject who changed address in the minutes a link is live is a
// subject whose outstanding links proved something about an inbox that is no
// longer theirs, and a redemption that skipped the check would stamp the new
// address proven on the strength of a mail sent to the old one — the one
// direction a verification flow must not fail in. [MagicLink.EmailAddress] is
// the recorded half of that comparison.
//
// An address already proven is not stamped again. The column says when it was
// proven, and a write on every redemption would quietly turn it into when
// somebody last followed a link.
//
// The alternative was two mails proving one thing. A passwordless registrant
// would have had to answer the registration link and then ask for a sign-in
// link, on the arrival path that exists to be the simple one, and the second
// mail would have proven nothing the first had not.
//
// Promoting burns the registration link, because identity holds the proof and
// the outstanding token as one state — see identity.Store.MarkUserEmailAddressProven.
// That link has nothing left to prove by then.
//
// A suspended, banned or terminated user is left exactly where an operator put
// them: [Service.promote] moves nobody but an unverified user, and the status
// check below refuses the rest before it is reached. An email is not something
// that overturns an operator's decision.
//
// # What it keeps from the password door
//
// The second factor, unchanged. A user who holds a proven TOTP secret must send
// a code, and [MagicLinkCredentials.TOTPCode] is where it goes — one of their
// recovery codes included, on a service built with [WithRecoveryCodeStore],
// spent on the transaction that spends the link. A door that
// skipped it would be a way around somebody's second factor reachable by
// whoever controls their inbox — which is precisely the thing a second factor is
// enrolled against.
//
// The refusals, collapsed. Every way of failing to spend a link is
// [ErrInvalidMagicLink] — the store's four, and the address that moved — and
// a wrong second-factor code is [ErrInvalidCredentials], exactly as it is at
// the password door. What is told apart is on the span.
//
// The minting, whole. The same family is minted, the same refresh token where a
// store is configured, the same [Hooks.AfterAuthenticate] and
// [Hooks.AfterIssueToken] in that order, in one transaction with the spend.
//
// # There is no administrative door here
//
// [Service.AdminLoginForToken] rules that a service role able to ban a user or
// terminate an account is the one credential a password alone does not answer
// for. A mailed link is strictly weaker than a password, so there is no
// AdminRedeemMagicLink and there will not be one; an administrator signs in
// through the door that demands both.
//
// # One consequence worth stating
//
// A failed second factor rolls the spend back, so the link still works and the
// person can try the code again. That matches the password door, where a wrong
// code does not invalidate the password — and it is the reason the rate limit
// this package keeps asking the consumer for is not optional, because a link
// that survives a wrong code is a link somebody holding it can present codes
// against. Burning it instead would mean a mistyped digit costs a fresh mail,
// and would hand anybody who intercepted a link a way to spend every link the
// person is sent.
//
// It requires [WithMagicLinkStore] and [WithVerifications], and refuses with
// [ErrMagicLinksNotConfigured] or [ErrVerificationsNotConfigured] until it has
// both.
func (s *Service) RedeemMagicLink(
	ctx context.Context,
	scope tenancy.Scope,
	credentials *MagicLinkCredentials,
) (signIn *SignIn, err error) {
	ctx, op, done := s.begin(ctx, opRedeemMagicLink,
		observability.WithValue(scopeKey, scope.String()),
	)
	defer func() { done(err) }()

	if credentials == nil {
		return nil, op.Error(ErrNilCredentials, "redeeming a sign-in link")
	}

	if credentials.Token == "" {
		return nil, op.Error(ErrEmptyMagicLinkToken, "redeeming a sign-in link")
	}

	if s.magicLinks == nil {
		return nil, op.Error(ErrMagicLinksNotConfigured, "redeeming a sign-in link")
	}

	if s.verifications == nil {
		return nil, op.Error(ErrVerificationsNotConfigured, "redeeming a sign-in link")
	}

	// The login this sign-in begins, minted here rather than by a store, for
	// Service.login's reason: it names a sign-in rather than a row, so a service
	// that stores no refresh tokens still has a login for a claim and a hook to
	// name.
	familyID := identifiers.New()
	op.Set(familyKey, familyID)

	attempt := &FailedSignIn{}

	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		signIn, err = s.redeem(ctx, op, tx, scope, credentials, familyID, attempt)

		return err
	}); err != nil {
		if attempt.Reason != nil {
			// Recorded out here, after the transaction that carried the refusal
			// has unwound. The hook writes on a transaction of its own — see
			// Service.refuse — and one opened inside the callback would commit
			// or roll back with the spend it is describing.
			return nil, s.recordFailure(ctx, op, scope, attempt, err)
		}

		return nil, op.Error(err, "redeeming a sign-in link")
	}

	return signIn, nil
}

// redeem is everything one redemption does on one transaction: the spend, the
// standing, the second factor, the promotion, the principal and the mint.
//
// It runs inside the callback rather than beside it because every one of those
// is the same fact. A spend that committed while the hooks rolled back would
// burn somebody's link for a sign-in the consumer never recorded, and a
// promotion that committed without the spend would promote a registrant on a
// link that still works.
//
// It reports its refusals through attempt as well as returning them, so the
// caller can record a failed sign-in once the transaction has unwound.
func (s *Service) redeem(
	ctx context.Context,
	op observability.Operation,
	tx database.Tx,
	scope tenancy.Scope,
	credentials *MagicLinkCredentials,
	familyID string,
	attempt *FailedSignIn,
) (*SignIn, error) {
	link, err := s.magicLinks.Redeem(ctx, tx, scope, credentials.Token)
	if err != nil {
		// Nobody is named yet: the token matched no spendable row, so there is
		// no subject to put on the attempt. The handle stays empty for the
		// reason FailedSignIn.Handle documents — a bearer following a URL typed
		// no handle.
		return nil, refusal(op, attempt, err)
	}

	attempt.UserID = link.SubjectID
	op.Set(userIDKey, link.SubjectID)

	// Read on the transaction that just spent the link, so the standing this
	// sign-in turns on is the one that holds at the instant of the spend rather
	// than one read a round trip earlier.
	user, err := s.directory.GetUser(ctx, tx, scope, link.SubjectID)
	if err != nil {
		return nil, err
	}

	if !admitsMagicLink(user.AccountStatus) {
		return nil, refusal(op, attempt, statusRefusal(user))
	}

	// The address the link was mailed to, against the one the subject holds now.
	// A link demonstrates control of one inbox, so a subject who has changed
	// address since it was minted is somebody this link can no longer speak for
	// — and the proof below would otherwise stamp an address nobody has reached.
	//
	// Refusing the whole redemption rather than only the proof is the stronger of
	// the two readings and the one the flow can state: a person whose address
	// changed because the old inbox was lost is a person whose old links should
	// stop working, and a sign-in granted on an inbox they have walked away from
	// is the thing they walked away from it to prevent. What it costs is a mail
	// asked for and then superseded, which is one more request at the new
	// address.
	if link.EmailAddress != identity.FoldHandle(user.EmailAddress) {
		return nil, refusal(op, attempt, errMagicLinkAddressChanged)
	}

	// Checked on tx, which this door already holds — see
	// Service.checkSecondFactorCode for why nothing here reads elsewhere.
	usedRecoveryCode, err := s.verifySecondFactor(ctx, tx, scope, user, credentials.TOTPCode, false)
	if err != nil {
		return nil, refusal(op, attempt, err)
	}

	// A recovery code is spent here, on the transaction that spent the link and
	// before anything is promoted or minted, so a redemption that rolls back
	// leaves both the link and the code standing. A code somebody else spent
	// since the check above is refused as a wrong code is.
	if usedRecoveryCode {
		if err = s.spendRecoveryCode(ctx, tx, scope, user.Redacted(), credentials.TOTPCode); err != nil {
			if platformerrors.Is(err, errRecoveryCodeSpent) {
				return nil, refusal(op, attempt, err)
			}

			return nil, err
		}
	}

	// The proof and the promotion, and the order matters: the principal below is
	// resolved after them, so a registrant signs in as somebody in good standing
	// rather than as somebody the directory would refuse.
	//
	// The proof is written only where there is something to prove. An address
	// already stamped is not stamped again, because the column records when it
	// was proven rather than when somebody last followed a link, and a write on
	// every redemption would turn the one into the other for anybody who signs
	// in this way twice. It would also clear the outstanding registration token
	// of a user who by definition holds none — identity moves the stamp and the
	// digest together, so a proven address has nothing outstanding to clear.
	if !user.EmailAddressVerified() {
		if err = s.verifications.MarkUserEmailAddressProven(ctx, tx, scope, user.ID); err != nil {
			return nil, err
		}
	}

	if err = s.promote(ctx, tx, scope, user.ID, true); err != nil {
		return nil, err
	}

	principal, err := s.directory.GetPrincipal(ctx, tx, scope, user.ID, credentials.ActiveAccountID)
	if err != nil {
		return nil, err
	}

	op.Set(accountIDKey, principal.ActiveAccountID)

	// Minted inside the transaction, unlike Service.login, which mints before it
	// opens one. There the mint depends on nothing the transaction does; here it
	// depends on a principal that only exists because the spend and the
	// promotion above have happened. Issuing a token is a computation rather
	// than a write, so nothing is held open by it.
	signIn, err := s.mintToken(ctx, principal, familyID, false)
	if err != nil {
		return nil, platformerrors.Wrap(err, "issuing a token")
	}

	if err = s.mintRefreshToken(ctx, tx, scope, signIn, familyID); err != nil {
		return nil, err
	}

	auth := &Authentication{Principal: principal, Administrative: false}

	if err = s.hooks.AfterAuthenticate(ctx, tx, scope, auth); err != nil {
		return nil, err
	}

	return signIn, s.hooks.AfterIssueToken(ctx, tx, scope, signIn)
}

// errMagicLinkAddressChanged is why a redemption is refused when the link was
// mailed to an address its subject no longer holds.
//
// It is unexported and wraps [ErrInvalidMagicLink] because it is the fifth way
// of failing to spend a link and the caller is told what the other four are
// told: a refusal spelled apart here would say, to whoever is presenting
// guesses, that a token was real and its owner has moved. What tells it apart is
// the span, which is where the other four are told apart too.
var errMagicLinkAddressChanged = platformerrors.Wrap(ErrInvalidMagicLink,
	"sign-in link was mailed to an address its subject no longer holds")

// refusal records why a redemption was refused and returns what the caller is
// told.
//
// The reason reaches the span and the attempt; the sentinel is what comes back.
// It is Service.refuse split in half, because the hook half cannot run here —
// see Service.RedeemMagicLink, where the other half is called once the
// transaction has unwound.
func refusal(op observability.Operation, attempt *FailedSignIn, reason error) error {
	attempt.Reason = reason

	op.SpanOnly(reasonKey, reason.Error())

	return reason
}

// recordFailure runs the failed-sign-in hook for a refused redemption and
// returns what the caller is told.
//
// A hook that fails does not rescue the sign-in — it was already refused — so
// its error is joined to the refusal rather than replacing it, which keeps
// errors.Is against the sentinel matching and keeps the consumer's failure from
// being swallowed. It is Service.refuse's reading, and the two would be one
// function if the transaction shapes agreed.
func (s *Service) recordFailure(
	ctx context.Context,
	op observability.Operation,
	scope tenancy.Scope,
	attempt *FailedSignIn,
	refused error,
) error {
	err := op.Error(refused, "redeeming a sign-in link")

	if hookErr := s.client.WithTransaction(ctx, func(tx database.Tx) error {
		return s.hooks.AfterFailedSignIn(ctx, tx, scope, attempt)
	}); hookErr != nil {
		op.Acknowledge(hookErr, "recording a failed sign-in link redemption")

		return platformerrors.Join(err, hookErr)
	}

	return err
}

// admitsMagicLink reports whether a standing admits a sign-in through the link
// door.
//
// It is AccountStatus.AdmitsSignIn widened by exactly one status, and the widening
// is this door's whole reason for existing: an unverified registrant is who the
// mail was sent to, and refusing them here would mean the link that proves their
// address cannot be the link that signs them in.
//
// It is a function here rather than a method on identity.AccountStatus because
// it is this package's policy rather than the directory's fact. identity's
// reading — only good standing admits a sign-in — is right for every caller that
// is not holding a proof of the address the account was registered with.
func admitsMagicLink(status identity.AccountStatus) bool {
	return status.AdmitsSignIn() || status == identity.StatusUnverified
}

// padTo holds the answer until deadline, so that what a request did — or did not
// do — is not readable off how long it took.
//
// It sleeps on the service's clock rather than on time.Sleep, so a test can
// assert the deadline both paths are held to without waiting for it, and so a
// synctest bubble advances through it.
//
// A context that ends first ends the wait, because there is nobody left to be
// told anything in constant time — and the span says so rather than the wait
// being silently short. A caller whose clients time out below the floor is a
// caller whose floor is not covering anything, which is a fact about their
// configuration and not about this request.
//
// It is a second copy of authentication/passwordreset's, and the duplication is
// accepted rather than lifted. The two are the only timing floors in this
// module, they pad different flows against different work, and a shared home for
// nine lines would be a primitives-go release standing between this door and the
// one other caller — see the package documentation, where what a drift between
// them would cost is stated.
func (s *Service) padTo(ctx context.Context, op observability.Operation, deadline time.Time) {
	remaining := deadline.Sub(s.clk.Now())
	if remaining <= 0 {
		return
	}

	if err := s.clk.Sleep(ctx, remaining); err != nil {
		op.SpanOnly(padKey, false)
	}
}

// RevokeMagicLinksForSubject withdraws every outstanding sign-in link one person
// holds, and reports how many it withdrew.
//
// It is what an operator disabling an account runs and what a data erasure
// calls, which is the pair [Service.RevokeRefreshTokensForSubject] serves for
// the other credential a sign-in leaves behind. The two are separate doors
// because they withdraw different things and a consumer may hold a store for one
// and not the other; a deployment doing both calls both.
//
// It is deliberately not called by a successful redemption. A link still in an
// inbox is exposed exactly as much after somebody signs in as it was before, and
// burning the rest would cost them the second mail they asked for while closing
// nothing — see [MagicLinkStore.RevokeForSubject], where that reading is argued
// against passwordreset's opposite one.
//
// What it does not do is end the sign-ins those links already produced. A link
// that has been followed is spent, and the session it minted is a refresh token
// family — so "stop this person signing in with what was mailed to them" and
// "sign this person out" are two calls, in that order, and an operator who makes
// only this one has closed the door without emptying the room.
//
// A person who never asked for a link is zero and no error. It requires
// [WithMagicLinkStore] and refuses with [ErrMagicLinksNotConfigured] until it has
// one — a mailer is not needed, because withdrawing sends nothing.
func (s *Service) RevokeMagicLinksForSubject(
	ctx context.Context,
	scope tenancy.Scope,
	userID string,
) (revoked int64, err error) {
	ctx, op, done := s.begin(ctx, opRevokeMagicLinksSubject,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(userIDKey, userID),
	)
	defer func() { done(err) }()

	if s.magicLinks == nil {
		return 0, op.Error(ErrMagicLinksNotConfigured, "revoking a subject's sign-in links")
	}

	if err = scope.Validate(); err != nil {
		return 0, op.Error(err, "checking the scope a subject's sign-in links were revoked in")
	}

	if userID == "" {
		return 0, op.Error(ErrEmptyUserID, "reading the subject whose sign-in links are revoked")
	}

	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		var txErr error

		revoked, txErr = s.magicLinks.RevokeForSubject(ctx, tx, scope, userID)

		return txErr
	}); err != nil {
		return 0, op.Error(err, "revoking a subject's sign-in links")
	}

	return revoked, nil
}
