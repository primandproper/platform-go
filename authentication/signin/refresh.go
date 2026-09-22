package signin

import (
	"context"
	"time"

	"github.com/primandproper/platform-go/v14/identity"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/idempotency"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// RefreshToken is one stored refresh token, as a RefreshTokenStore hands it
// back.
//
// It never carries the token. What a store keeps is a digest, and what it
// answers with is everything about the row except the thing that could be
// presented — so a caller holding one of these holds a description of a
// credential rather than the credential. The secret exists in exactly one place
// and for exactly one moment: RefreshTokenIssuance.Secret, on the way out of a
// mint.
type RefreshToken struct {
	_ struct{} `json:"-"`

	// IssuedAt is when this token was minted.
	IssuedAt time.Time `json:"issuedAt"`

	// ExpiresAt is when it stops being exchangeable. A family that reaches this
	// without being exchanged is a sign-in that has ended.
	ExpiresAt time.Time `json:"expiresAt"`

	// PurgeAfter is when the row may be collected, which is past ExpiresAt by
	// the store's retention window. It is later than the deadline deliberately:
	// a row collected at its own expiry could no longer tell "already spent"
	// from "no such token", which is the distinction reuse detection rests on.
	PurgeAfter time.Time `json:"purgeAfter"`

	// RedeemedAt is when this token was exchanged, and nil until it is.
	// Presenting a token that carries one is what ErrRefreshTokenReused reports.
	RedeemedAt *time.Time `json:"redeemedAt,omitempty"`

	// RevokedAt is when this token was withdrawn, and nil until it is. A
	// detected reuse writes it across a whole family; a sign-out and an erasure
	// write it across a subject.
	RevokedAt *time.Time `json:"revokedAt,omitempty"`

	// FamilyID is which continuous login this token belongs to: minted when the
	// password was proven, inherited by every successor a rotation issues, and
	// ended as a unit when a spent token is presented again.
	//
	// It is deliberately not called a session.
	// [github.com/primandproper/platform-go/v14/sessions] is a different package
	// with its own store and its own revocation, and one mechanism answering to
	// one name in two places is the drift a shared vocabulary exists to prevent.
	FamilyID string `json:"familyID"`

	// SubjectID is which person signed in — identity.Principal's user ID.
	SubjectID string `json:"subjectID"`

	// ActiveAccountID is which account inside the directory the access tokens
	// this family mints are for.
	//
	// It is stored rather than re-resolved, and that is the column that makes an
	// exchange honest: re-resolving would hand back a token for whatever the
	// user's default account has since become rather than for the account that
	// was proven at sign-in.
	ActiveAccountID string `json:"activeAccountID"`

	// Scope is the directory the sign-in was made in.
	Scope tenancy.Scope `json:"scope"`

	// Administrative reports whether this login came through
	// [Service.AdminLoginForToken].
	//
	// It is stored for the same reason ActiveAccountID is: an exchange that
	// could not read it back would hand an administrative session an ordinary
	// token on DefaultTokenTTL and an ordinary successor on
	// DefaultRefreshTokenTTL — the shorter administrative lifetimes undone at
	// the first refresh, silently, and in the direction that lengthens them.
	Administrative bool `json:"administrative"`
}

// Live reports whether a token could still be exchanged at the given instant.
//
// It is what a store's own guard decides in SQL, spelled once in Go so that an
// administrative view, a test and a non-SQL implementation answer it the same
// way. A store must not read it in place of its guard: the guard is what makes
// two concurrent exchanges resolve to one winner, and a Go comparison a round
// trip earlier cannot.
func (t *RefreshToken) Live(at time.Time) bool {
	if t == nil {
		return false
	}

	return t.RedeemedAt == nil && t.RevokedAt == nil && t.ExpiresAt.After(at)
}

// RefreshTokenRequest is one mint: which family the token joins, who it is for,
// and how long it lives.
//
// It is a struct rather than four arguments so that a fact added here later is
// additive for an implementer.
type RefreshTokenRequest struct {
	_ struct{} `json:"-"`

	// FamilyID is the login this token belongs to. A sign-in mints a fresh one;
	// an exchange passes the spent token's, which is what makes the successor a
	// successor rather than a second login.
	FamilyID string `json:"familyID"`

	// SubjectID is which person the token is for.
	SubjectID string `json:"subjectID"`

	// ActiveAccountID is which account the access tokens minted from this family
	// are for. It may be empty, for a user who is a member of no account at all
	// — who signs in and gets a token against nothing rather than being refused.
	ActiveAccountID string `json:"activeAccountID"`

	// Administrative records which door this login came through, so that an
	// exchange can mint on the same lifetimes the sign-in did.
	Administrative bool `json:"administrative"`

	// TTL is how long the minted token may be exchanged for. It is the service's
	// WithRefreshTokenTTL, resolved before the call, rather than something a
	// store decides: how long a sign-in lasts is policy, and a store that held a
	// default for it would be a second place that policy lived.
	TTL time.Duration `json:"ttl"`
}

// RefreshTokenIssuance is a minted refresh token: the row it wrote, and the
// secret, once.
//
// The secret is returned before the transaction commits and cannot be recovered
// afterwards — the store keeps a digest. A caller that hands it to somebody
// before the commit has handed out a credential for a sign-in that may yet roll
// back, which is why [Service.LoginForToken] returns it rather than delivering
// it.
type RefreshTokenIssuance struct {
	_ struct{} `json:"-"`

	// Token is the row that was written, secret excluded.
	Token *RefreshToken `json:"token"`

	// Secret is the credential itself, and the only moment it exists outside the
	// holder's hands. It is not redacted anywhere, because a mint that hides it
	// has accomplished nothing — which is the reason it must not be logged,
	// recorded by a hook, or put in an error message.
	Secret string `json:"-"`
}

// RefreshTokenStore is where a sign-in's refresh tokens live.
//
// This module ships a SQL implementation together with the DDL it needs —
// [github.com/primandproper/platform-go/v14/authentication/signin/refreshtokens]
// — so adopting rotation does not mean writing this. The interface exists
// because the flow and its storage are genuinely separable, and because a
// service that names none of it mints no refresh tokens at all: rotation is
// [WithRefreshTokenStore] rather than a fifth constructor parameter, so a
// consumer using [Service.Authenticate] as a credential check owes no table.
//
// What an implementation owes its callers is not "these four methods". It is
// the four properties the methods exist to hold, none of which the signatures
// can state:
//
// The secret is never stored. Issue mints it, returns it once, and persists
// something a reader cannot reverse into it.
//
// A token is exchangeable exactly once, and the store decides which caller
// exchanges it. Two concurrent Redeem calls for one token, in two transactions,
// must produce one success and one refusal, with no cooperation from the caller.
//
// A token presented after it was spent is [ErrRefreshTokenReused], and reporting
// it ends the family in the same transaction. That is the whole of what rotation
// buys: without it a stolen token is a shared session nobody can see, and with it
// the theft is a detected event that signs both parties out.
//
// Expiry and revocation are refused rather than reclaimed. A token past its
// deadline, or one whose family was revoked, is dead to Redeem whether or not
// anything has deleted the row.
//
// Every method takes a tenancy.Scope and none of them offers an unscoped
// variant: an implementation filters on it rather than treating it as a hint. A
// token presented in the wrong directory matches nothing and is refused as an
// unknown one, which is what it is from there.
//
// # The transaction is the caller's
//
// All four are writes and all four take a database.Tx, which is the module's
// store convention — and here the reason is a correctness property rather than a
// bookkeeping one. An exchange spends one token and mints its successor, and a
// window in which one of those has landed and the other has not is a window
// where a sign-in has either two live refresh tokens or none. One transaction is
// what removes it, and the transaction is the caller's so that the hooks a
// consumer commits alongside a sign-in land in it too.
//
// No carve-out is needed and none is taken: CLAUDE.md's enumerated six stays
// closed. The mint runs inside the login transaction the service already opens.
//
// # What it deliberately cannot answer
//
// Nothing here can tell a client's own retry of an exchange from somebody else's
// replay of it, and after an ambiguous failure — a timeout, a dropped
// connection, a backgrounded app — that is a dropped packet ending a login. A
// store that can tell them apart implements [IdempotentRefreshTokenStore] as
// well; one that does not is complete, and behaves exactly as it does today.
type RefreshTokenStore interface {
	// Issue mints a token, stores its digest, and returns the secret exactly
	// once.
	//
	// It does not check that the subject exists — this store reads no user table
	// — and it does not decide which family the token joins. Both are the
	// service's.
	//
	// The row lands when tx commits, and the secret is returned before it does.
	// Hand it to whoever is signing in after the commit, not from inside the
	// callback: a credential returned for a transaction that then rolled back is
	// one nothing will ever accept, and the store has no way to take it back.
	Issue(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		request *RefreshTokenRequest,
	) (*RefreshTokenIssuance, error)

	// Redeem spends a secret, atomically, and answers with the token it spent —
	// RedeemedAt set, and carrying the family, subject and account the successor
	// inherits.
	//
	// A nil error is a decision rather than an observation: it means this caller,
	// and no other, holds the right to mint the successor. Mint it in the same
	// transaction — that is what the Tx is for.
	//
	// A secret whose row says it was already spent is ErrRefreshTokenReused, and
	// the implementation revokes the whole family before returning it, in tx. The
	// two are one act: a reuse reported without the revocation is a theft detected
	// and then allowed to continue.
	//
	// That puts one obligation on the caller, and it is the only place in this
	// module where an error arrives with work attached. The revocation is written
	// into tx and is undone by rolling tx back, so a caller that returns this
	// sentinel out of its transaction callback detects the theft and then erases
	// its own response to it. Commit the transaction and report the refusal
	// afterwards — Service.ExchangeRefreshToken is the worked example, and it is
	// why that method captures this one sentinel rather than returning it.
	//
	// Every other refusal — an unknown digest, a wrong directory, a revoked
	// token, an expired one — is ErrInvalidCredentials bare. They are collapsed
	// for the reason the password door collapses its four: told apart, they are
	// an oracle for whoever is presenting guesses. ErrRefreshTokenReused wraps
	// ErrInvalidCredentials rather than standing beside it, so the collapse is
	// what a caller matching the ordinary refusal actually sees, and so the two
	// carry one set of words onto the wire; what the reuse adds is a node
	// errors.Is can still find, for the log and the alarm.
	Redeem(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		secret string,
	) (*RefreshToken, error)

	// RevokeFamily ends one login and reports how many tokens it withdrew.
	//
	// It is what a detected reuse triggers and what a sign-out calls. Already
	// revoked rows are left alone, so the record still says when the family
	// actually stopped working and a second call reports zero rather than moving
	// the stamp.
	RevokeFamily(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		familyID string,
	) (int64, error)

	// RevokeForSubject ends every login one person holds and reports how many
	// tokens it withdrew.
	//
	// It is "disable this account", "sign out everywhere", and the erasure a
	// dataprivacy run performs. It is a method of its own rather than a loop over
	// RevokeFamily, and it has to be: a caller holding a subject identifier
	// cannot enumerate that person's families, and a loop would leave live
	// whatever was issued while it ran.
	RevokeForSubject(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		subjectID string,
	) (int64, error)
}

// IdempotentRefreshTokenStore is a [RefreshTokenStore] that can tell a client's
// own retry of an exchange from somebody else's replay of it.
//
// It exists because rotation's central rule is unfollowable after an *ambiguous*
// failure. A spent refresh token is single-use with reuse detection, so a client
// that retries an exchange must retry it with the successor it was given — and a
// client whose request timed out, whose connection dropped, or that was
// backgrounded mid-call has no successor, because none arrived. Re-sending the
// token it still holds revokes a live login; giving up loses the session. A
// dropped packet signs the user out, and there is no third answer without this.
//
// The third answer is a key the client mints once per logical exchange, outside
// its retry loop, and sends on every attempt — the convention
// [github.com/primandproper/primitives-go/v2/idempotency] already defines, and
// the metadata entry this package's own gRPC client stamps on this one RPC. A store
// implementing this interface records that key with the spend, so a later
// presentation of the same token bearing the same key is recognizable as the
// retry it is.
//
// What it does *not* do is replay a recorded response. The retry is answered
// with a freshly minted successor, and the one the first attempt minted is
// revoked. Two reasons, and the second is the stronger: this module keeps no
// refresh token secret at rest to replay, and a replay would hand an attacker
// who captured the request the very token the legitimate client holds — two
// parties silently sharing one credential, which is the thing reuse detection
// exists to prevent. Under a re-mint the same capture is loud: the client's next
// call fails, it signs in again, and the theft surfaces.
//
// # Why it is a second interface
//
// [RefreshTokenStore] is exported and injected through [WithRefreshTokenStore],
// so it is meant to be implemented outside this module, and a method added to it
// would stop every such implementation compiling. This one embeds it and is
// type-asserted for at runtime: a store that does not implement it, or a request
// that carries no key, takes exactly the path it takes today.
//
// # The obligation the two methods share
//
// They are two halves of one exchange and belong in one transaction — the
// caller's, as every write on the embedded interface is. RedeemIdempotently
// without RecordSuccessor leaves a row that is spent and cannot say what it
// minted, which is the state the retry branch refuses; and a caller that ran one
// and not the other has a login whose next dropped response ends it.
// [Service.ExchangeRefreshToken] is the worked example.
type IdempotentRefreshTokenStore interface {
	RefreshTokenStore

	// RedeemIdempotently spends a secret the way Redeem does, recording the key
	// that spent it, and honors a retry presented with that same key.
	//
	// A first exchange answers exactly as Redeem would. A presentation of a
	// token this key already spent answers the same way *again* — the spent
	// token, nil error — when the exchange it is retrying has not been overtaken:
	// the successor it minted is still unspent and unrevoked, no more than the
	// implementation's grace period has passed, and the key has not already been
	// honored once. The implementation revokes that superseded successor in tx
	// and the caller mints a replacement, as it would for any successful
	// redemption.
	//
	// Everything else is Redeem's answer unchanged, family revocation and
	// ErrRefreshTokenReused included — no key, a different key, a successor the
	// client has already spent, a window that has closed, or a key already spent
	// on one retry. The caller's obligation is therefore Redeem's obligation, and
	// for the same reason: the revocation is written into tx before the sentinel
	// is returned, so returning it out of the transaction callback undoes the
	// response to the theft it just reported.
	//
	// An empty or malformed key is refused rather than ignored, through
	// idempotency.ValidateKey's sentinels. A caller with no key calls Redeem.
	RedeemIdempotently(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		secret string,
		key string,
	) (*RefreshToken, error)

	// RecordSuccessor tells the store which token an exchange minted, naming
	// both by the secrets the caller is holding rather than by any digest the
	// store renders from them.
	//
	// It runs in the same transaction as the spend and the mint. A retry can only
	// be honored by revoking the successor the first attempt produced, and "the
	// successor" is a row nothing else in the schema can name — so a spend
	// committed without this is a login whose next ambiguous failure ends it,
	// which is precisely the behavior this interface was added to remove.
	//
	// It is called after a mint rather than before, because the secret it records
	// does not exist until Issue has returned it.
	RecordSuccessor(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		predecessor string,
		successor string,
	) error
}

// ExchangeRefreshToken spends a refresh token and answers with a fresh access
// token and its successor, in one transaction.
//
// It mints on the lifetimes the login was opened with, so an administrative
// session stays administrative and stays short: the door is a column on the row
// rather than something this call could be told.
//
// This is what makes a sign-in outlive its access token without asking for a
// password again. The presented token is marked spent and a successor is minted
// into the same family; both happen in one transaction, so a sign-in never holds
// two live refresh tokens and never holds none.
//
// Presenting a token that was already spent is [ErrRefreshTokenReused], and it
// ends the family: every token that login ever issued is revoked, including the
// successor whoever holds the other copy is carrying. That is the standard
// answer and the only one that turns a stolen token into a detected theft rather
// than a shared session — and it is why the two copies are not told apart here.
// From this side a thief's replay and a victim's retry are the same request, so
// ending both is the answer that does not leave the thief signed in.
//
// Every other refusal is [ErrInvalidCredentials]: an unknown token, one from
// another directory, one whose family was revoked, one past its deadline. A
// replay answers as one of those to anybody reading the response — the sentinel
// wraps it, and both transports send its words — so the branch a client takes is
// the same one, and only a log records which it was.
//
// The principal is re-resolved against the directory rather than taken off the
// row, so a user banned, terminated or removed from the account since they
// signed in is refused here rather than carried by a family for as long as it
// lives. On this module's directory that refusal is
// [github.com/primandproper/platform-go/v14/identity.ErrSignInNotAdmitted], which
// identity.Store.GetPrincipal returns instead of a Principal; [ErrUserBanned],
// [ErrUserTerminated] and [ErrUserUnverified] are what a [Directory] that does not
// enforce status of its own produces here. Either way the exchange refuses and
// the presented token is left unspent, since the refusal rolls the transaction
// back. What is *not* re-resolved is which account the token is for — that is
// the row's, for the reason [RefreshToken.ActiveAccountID] gives.
//
// [Hooks.AfterIssueToken] runs in the same transaction, with the new token's
// family on it. [Hooks.AfterAuthenticate] does not: nobody proved a credential
// here, and an access log that recorded a refresh as an authentication would
// report a password that was never typed.
//
// A service built without [WithRefreshTokenStore] mints no refresh tokens, so
// every call here is [ErrRefreshTokensNotConfigured].
//
// # A retry that is not a replay
//
// Everything above describes an exchange that either happened or did not. The
// case it does not cover is the one a client cannot act on: a request whose
// answer never arrived. There is no successor to retry with, and re-sending the
// token reads from here as the theft it is indistinguishable from.
//
// A caller that puts an idempotency key on ctx —
// [github.com/primandproper/primitives-go/v2/idempotency.WithKey], which this
// module's gRPC surface fills from the conventional `idempotency-key` metadata —
// gets the other answer, provided its store implements
// [IdempotentRefreshTokenStore]. The retry is answered with a *fresh* successor
// and the one the lost response carried is revoked, so the client ends up with
// exactly one live refresh token either way. The key buys one such retry; a
// second presentation of it is a reuse like any other.
//
// Neither half is conditional on the other being configured. No key on ctx, or a
// store without that interface, takes the path above unchanged — which is what
// makes this a minor rather than a break.
func (s *Service) ExchangeRefreshToken(
	ctx context.Context,
	scope tenancy.Scope,
	refreshToken string,
) (signIn *SignIn, err error) {
	ctx, op, done := s.begin(ctx, opExchangeRefreshToken,
		observability.WithValue(scopeKey, scope.String()),
	)
	defer func() { done(err) }()

	if s.refreshTokens == nil {
		return nil, op.Error(ErrRefreshTokensNotConfigured, "exchanging a refresh token")
	}

	if err = scope.Validate(); err != nil {
		return nil, op.Error(err, "checking the scope a refresh token was presented in")
	}

	if refreshToken == "" {
		return nil, op.Error(ErrEmptyRefreshToken, "reading a refresh token")
	}

	// The one refusal that is not returned out of the transaction below, held
	// here instead. A detected reuse revokes the family inside tx, so returning
	// the sentinel would roll that revocation back — the theft would be noticed
	// and then un-responded to. The transaction commits the revocation and the
	// caller is told afterwards. See RefreshTokenStore.Redeem.
	var reuse error

	// The store and the key, resolved once outside the transaction because
	// neither can change inside it and because both being absent is the ordinary
	// case rather than a fallback.
	idempotent, key := s.idempotentExchange(ctx)

	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		spent, txErr := s.spend(ctx, tx, scope, refreshToken, idempotent, key)
		if txErr != nil {
			if platformerrors.Is(txErr, ErrRefreshTokenReused) {
				reuse = txErr

				return nil
			}

			return txErr
		}

		op.SetValues(map[string]any{userIDKey: spent.SubjectID, familyKey: spent.FamilyID})

		// Re-read rather than trusted. The row says who signed in; whether they
		// may still sign in is a question only the directory answers, and a
		// family that outlived a ban would be a suspension that takes effect
		// whenever the access token happens to expire.
		//
		// It reads on tx rather than on the reader, so the transaction that just
		// spent the token sees a directory consistent with it.
		//
		// identity.Store answers a suspended user with
		// identity.ErrSignInNotAdmitted here rather than with a Principal, so that
		// is the refusal a consumer on this module's directory sees. The check
		// below is kept for the Directory that is not it — the seam is an
		// interface, and a consumer's own directory that returns a Principal for a
		// banned user must not mint them a token because this package assumed
		// somebody else refused.
		principal, txErr := s.directory.GetPrincipal(ctx, tx, scope, spent.SubjectID, spent.ActiveAccountID)
		if txErr != nil {
			return txErr
		}

		if !principal.User.AccountStatus.AdmitsSignIn() {
			return statusRefusal(principal.User)
		}

		op.Set(accountIDKey, principal.ActiveAccountID)

		// The access token first, because a claims builder or an issuer that
		// refuses must not leave a spent token behind a successor nobody
		// received. Both are inside the transaction, so either way nothing
		// commits.
		// The door the login came through rather than this request's, which is
		// what keeps the administrative pair of lifetimes attached to an
		// administrative session across every refresh it makes.
		if signIn, txErr = s.mintToken(ctx, principal, spent.FamilyID, spent.Administrative); txErr != nil {
			return txErr
		}

		if txErr = s.mintRefreshToken(ctx, tx, scope, signIn, spent.FamilyID); txErr != nil {
			return txErr
		}

		// The second half of an idempotent exchange, and it lands in this
		// transaction rather than after it for the reason the first half does: a
		// spend committed without the successor it minted is a login whose next
		// dropped response ends it, which is the failure this whole path exists
		// to remove.
		if idempotent != nil {
			if txErr = idempotent.RecordSuccessor(ctx, tx, scope, refreshToken, signIn.RefreshToken); txErr != nil {
				return txErr
			}
		}

		return s.hooks.AfterIssueToken(ctx, tx, scope, signIn)
	}); err != nil {
		if isRefreshRefusal(err) {
			return nil, op.Error(err, "exchanging a refresh token")
		}

		return nil, op.Error(err, "rotating a refresh token")
	}

	if reuse != nil {
		return nil, op.Error(reuse, "exchanging a refresh token")
	}

	return signIn, nil
}

// SignOut ends the login a refresh token belongs to, named by the token itself.
//
// It is the deliberate half of what [Service.RevokeRefreshTokenFamily] does, and
// it exists because a client cannot call that one: a family identifier is not a
// secret — it is on every IssuedToken and it is minted by identifiers.New, whose
// values carry a timestamp and a counter — so an RPC that took one would let a
// caller end a login by guessing at one. The presented token is high-entropy and
// is the only thing a client holds that names its own family and nobody else's.
//
// It spends the token and then revokes the family, in one transaction, which is
// the order that matters: the token is being retired either way, and a
// revocation that landed without the spend would leave a row a later reuse check
// reads as theft rather than as a sign-out.
//
// **Every refusal a presented token can draw is success here.** A token nobody
// holds, one already spent, one already revoked and one past its deadline all
// mean the same thing about the login the caller asked to end — it is over, or it
// never began — and answering ErrInvalidCredentials to somebody pressing sign out
// would be an error message for an act that has already happened. It is also the
// same anti-enumeration property the exchange has, arrived at from the other
// side: a sign-out that refused an unknown token would say which tokens are real.
//
// The already-spent case is the one with work attached, and it is why this method
// captures the sentinel rather than returning it.
// [RefreshTokenStore.Redeem] writes the family's revocation into tx before
// answering ErrRefreshTokenReused, so returning it out of the callback would roll
// back the very thing the caller wanted done. The transaction commits and the
// caller is told nothing, because nothing is what they need to be told.
//
// What it does not do is stop an access token already in somebody's hands — see
// [Service.RevokeRefreshTokenFamily], whose documentation applies here
// unchanged. A service that mints no refresh tokens has no login to end and
// answers [ErrRefreshTokensNotConfigured].
func (s *Service) SignOut(ctx context.Context, scope tenancy.Scope, refreshToken string) (err error) {
	ctx, op, done := s.begin(ctx, opSignOut, observability.WithValue(scopeKey, scope.String()))
	defer func() { done(err) }()

	if s.refreshTokens == nil {
		return op.Error(ErrRefreshTokensNotConfigured, "signing out")
	}

	if err = scope.Validate(); err != nil {
		return op.Error(err, "checking the scope a sign-out was made in")
	}

	if refreshToken == "" {
		return op.Error(ErrEmptyRefreshToken, "reading the refresh token a sign-out presented")
	}

	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		spent, txErr := s.refreshTokens.Redeem(ctx, tx, scope, refreshToken)
		if txErr != nil {
			// Both branches commit. A reuse has already had the family revoked
			// into tx by the store, and every other refusal is a token that
			// names no live login — neither is a reason to abandon the
			// transaction, and the first is a reason not to.
			if platformerrors.Is(txErr, ErrInvalidCredentials) {
				op.SpanOnly(signOutNothingToEndKey, true)

				return nil
			}

			return txErr
		}

		op.SetValues(map[string]any{userIDKey: spent.SubjectID, familyKey: spent.FamilyID})

		_, txErr = s.refreshTokens.RevokeFamily(ctx, tx, scope, spent.FamilyID)

		return txErr
	}); err != nil {
		return op.Error(err, "signing out")
	}

	return nil
}

// RevokeRefreshTokenFamily ends one login: every refresh token that sign-in ever
// issued stops being exchangeable, and it reports how many it withdrew.
//
// It is sign-out. What it does not do is stop the access token already in
// somebody's hands — nothing here can, because an access token is checked by the
// consumer's interceptor against the issuer's signature rather than against this
// table. What it does is stop that access token being replaced, so a sign-out
// takes effect within one access-token lifetime, which is what
// [DefaultTokenTTL]'s hour is chosen against.
//
// A family nobody holds tokens for is zero and no error.
func (s *Service) RevokeRefreshTokenFamily(
	ctx context.Context,
	scope tenancy.Scope,
	familyID string,
) (revoked int64, err error) {
	ctx, op, done := s.begin(ctx, opRevokeRefreshFamily,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(familyKey, familyID),
	)
	defer func() { done(err) }()

	if s.refreshTokens == nil {
		return 0, op.Error(ErrRefreshTokensNotConfigured, "revoking a refresh token family")
	}

	if err = scope.Validate(); err != nil {
		return 0, op.Error(err, "checking the scope a refresh token family was revoked in")
	}

	if familyID == "" {
		return 0, op.Error(ErrEmptyFamilyID, "reading a refresh token family")
	}

	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		var txErr error

		revoked, txErr = s.refreshTokens.RevokeFamily(ctx, tx, scope, familyID)

		return txErr
	}); err != nil {
		return 0, op.Error(err, "revoking a refresh token family")
	}

	return revoked, nil
}

// RevokeRefreshTokensForSubject ends every login one person holds, and reports
// how many refresh tokens it withdrew.
//
// It is "sign out everywhere", the thing an operator runs when disabling an
// account, and what a data erasure calls. It is a door of its own rather than a
// loop over [Service.RevokeRefreshTokenFamily] for the reason
// [RefreshTokenStore.RevokeForSubject] gives: a caller holding a user ID cannot
// enumerate that person's families, and a loop would leave live whatever was
// issued while it ran.
//
// A user who has never signed in is zero and no error.
func (s *Service) RevokeRefreshTokensForSubject(
	ctx context.Context,
	scope tenancy.Scope,
	userID string,
) (revoked int64, err error) {
	ctx, op, done := s.begin(ctx, opRevokeRefreshSubject,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(userIDKey, userID),
	)
	defer func() { done(err) }()

	if s.refreshTokens == nil {
		return 0, op.Error(ErrRefreshTokensNotConfigured, "revoking a subject's refresh tokens")
	}

	if err = scope.Validate(); err != nil {
		return 0, op.Error(err, "checking the scope a subject's refresh tokens were revoked in")
	}

	if userID == "" {
		return 0, op.Error(ErrEmptyUserID, "reading the subject whose refresh tokens are revoked")
	}

	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		var txErr error

		revoked, txErr = s.refreshTokens.RevokeForSubject(ctx, tx, scope, userID)

		return txErr
	}); err != nil {
		return 0, op.Error(err, "revoking a subject's refresh tokens")
	}

	return revoked, nil
}

// idempotentExchange reports which of the two exchange paths this request takes:
// the store to use for it, and the key it was given.
//
// Both halves have to be present, and the conjunction is the whole rule. A key
// with a store that cannot record it would be protection a client was told it
// had; a store that can record one, given no key, has nothing to record. Either
// missing is today's exchange, which is the correct answer rather than a
// degraded one — a client that sends no key has not asked for this.
//
// It reads the key off ctx rather than taking it as an argument, because that is
// where the convention puts it: idempotency.WithKey is what the transports fill,
// and a parameter here would be a second way to say the same thing that a
// consumer's own handler would have to remember to wire.
func (s *Service) idempotentExchange(ctx context.Context) (store IdempotentRefreshTokenStore, key string) {
	store, ok := s.refreshTokens.(IdempotentRefreshTokenStore)
	if !ok {
		return nil, ""
	}

	minted, ok := idempotency.KeyFromContext(ctx)
	if !ok {
		return nil, ""
	}

	return store, string(minted)
}

// spend redeems the presented token through whichever of the two doors this
// request qualified for.
//
// It is a branch rather than a store method with a sometimes-empty key
// parameter, so that a store which never learned about idempotency is never
// handed one — and so that the ordinary exchange's call is the same line it has
// always been.
func (s *Service) spend(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	refreshToken string,
	idempotent IdempotentRefreshTokenStore,
	key string,
) (*RefreshToken, error) {
	if idempotent != nil {
		return idempotent.RedeemIdempotently(ctx, tx, scope, refreshToken, key)
	}

	return s.refreshTokens.Redeem(ctx, tx, scope, refreshToken)
}

// mintRefreshToken writes the refresh token a completed sign-in or a completed
// exchange hands back, onto the SignIn the caller is about to receive.
//
// It is a no-op for a service built without a store, which is what makes
// rotation optional without a branch at every call site. What it is not is a
// place where a failure is tolerated: a sign-in that was asked for a refresh
// token and could not mint one is a sign-in that would silently end an hour
// later, so the error travels and the transaction unwinds.
func (s *Service) mintRefreshToken(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	signIn *SignIn,
	familyID string,
) error {
	if s.refreshTokens == nil {
		return nil
	}

	ttl := s.refreshTokenTTL
	if signIn.Administrative {
		ttl = s.adminRefreshTokenTTL
	}

	issuance, err := s.refreshTokens.Issue(ctx, tx, scope, &RefreshTokenRequest{
		TTL:             ttl,
		FamilyID:        familyID,
		SubjectID:       signIn.Principal.User.ID,
		ActiveAccountID: signIn.Principal.ActiveAccountID,
		Administrative:  signIn.Administrative,
	})
	if err != nil {
		return platformerrors.Wrap(err, "minting a refresh token")
	}

	signIn.RefreshToken = issuance.Secret
	signIn.RefreshTokenExpiresAt = issuance.Token.ExpiresAt

	return nil
}

// isRefreshRefusal reports whether err is the exchange answering about a token
// rather than failing to answer.
//
// The status refusals are among them because a banned user presenting a valid
// refresh token is the flow working, and the directory's absence sentinel is not:
// a refresh token naming a user the directory has since archived is a fact
// somebody should look at.
//
// identity.ErrSignInNotAdmitted is the fourth status refusal and is the one a
// consumer will actually see, because identity.Store.GetPrincipal refuses a
// banned user before it has a Principal to hand back — so the re-read below
// answers with the directory's sentinel and this package's three are reached only
// by a Directory that does not enforce status of its own. It is listed for the
// same reason the other three are: a suspension arriving here is the suspension
// working.
func isRefreshRefusal(err error) bool {
	return platformerrors.Is(err, ErrRefreshTokenReused) ||
		platformerrors.Is(err, ErrInvalidCredentials) ||
		platformerrors.Is(err, identity.ErrSignInNotAdmitted) ||
		platformerrors.Is(err, ErrUserBanned) ||
		platformerrors.Is(err, ErrUserTerminated) ||
		platformerrors.Is(err, ErrUserUnverified)
}
