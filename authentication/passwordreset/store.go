package passwordreset

import (
	"context"
	"time"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// Store is the persistence seam for password reset tokens.
//
// This package ships a SQL implementation (NewSQLStore) together with the DDL
// it needs (passwordreset/migrations), so adopting the reset flow does not mean
// writing this. The interface exists because the flow and its storage are
// genuinely separable — an application keeping short-lived credentials
// somewhere that is not a SQL database should not have to fork the package to
// keep them.
//
// What an implementation owes its callers is not "these six methods". It is
// the three properties the methods exist to hold, none of which the signatures
// can state:
//
// The secret is never stored. Issue mints it, returns it once, and persists
// something a reader cannot reverse into it.
//
// A token is spendable exactly once, and the store decides which caller spends
// it. Two concurrent Consume calls for one token, in two transactions, must
// produce one success and one ErrTokenRedeemed, with no cooperation from the
// caller.
//
// Expiry is refused rather than reclaimed. A token past its deadline is dead to
// Verify and Consume whether or not anything has deleted the row.
//
// Every method takes a tenancy.Scope and none of them offers an unscoped
// variant: an implementation filters on it rather than treating it as a hint. A
// token presented in the wrong scope is ErrTokenNotFound, which is what it is
// from there.
//
// # The transaction is the caller's
//
// The four writes take a database.Tx and the two reads take the wider
// database.SQLQueryExecutor, which is the module's store convention rather than
// anything this package invented — but this is the package where the reason for
// it is a security property rather than a bookkeeping one. Consume decides who
// may change a password, and the password write is somebody else's statement;
// two transactions leave a window in which one of them has landed and the other
// has not, and the direction that window falls in is not the caller's to choose.
// One transaction is what removes it. A caller with genuinely nothing to join
// opens one with Client.WithTransaction and passes the Tx it is handed.
//
// A Store that is not a SQL store still takes these types. That is the cost of
// the seam being one signature rather than one per backend, and it is a small
// one: an implementation with no transaction of its own ignores the executor,
// while a caller keeping users in the same database as their reset tokens — the
// case this package is written for — gets the guarantee from the type.
type Store interface {
	// Issue mints a token for a principal, stores its digest, and returns the
	// secret exactly once.
	//
	// It does not check that the user exists — this package reads no user table
	// — and it does not send anything. Both are the caller's, and the second one
	// is where the flow's one real leak lives: an application that returns a
	// different response for a known and an unknown address has built an account
	// enumeration oracle out of a feature intended to protect accounts. Issue
	// for the users you find, say the same thing either way.
	//
	// Issuing again does not invalidate what is outstanding. A user who clicks
	// "email me a link" twice and then opens the first message has a link that
	// works, which is the behavior the alternative quietly breaks.
	//
	// The row lands when tx commits, and the secret is returned before it does.
	// Send the email after the commit, not from inside the callback: a link
	// mailed for a transaction that then rolled back is a reset nobody can
	// complete, and the store has no way to take it back.
	Issue(ctx context.Context, tx database.Tx, scope tenancy.Scope, userID string, ttl time.Duration) (*Issuance, error)

	// Verify resolves a secret to its token without spending it, for the page
	// load that precedes the form.
	//
	// It returns ErrTokenNotFound, ErrTokenExpired, or ErrTokenRedeemed for a
	// token that cannot be spent. A nil error means the token was live when it
	// was read, which is a weaker statement than Consume's: nothing here holds
	// it live until the submit.
	//
	// It takes the wider executor so that both of its callers are served by one
	// method. A page load holds no transaction and passes Client.Writer(); the
	// submit that verifies and then consumes passes the Tx it is about to spend
	// in, and reads what that transaction has already written.
	Verify(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, secret string) (*Token, error)

	// Consume spends a secret, atomically, and returns the token it spent with
	// its RedeemedAt set.
	//
	// A nil error is the one thing in this package that is a decision rather
	// than an observation: it means this caller, and no other, holds the right
	// to change that user's password. Write the password in the same
	// transaction — that is what the Tx is for. Consuming in one transaction and
	// writing in another leaves either a spent token over an unchanged password,
	// which costs an email, or a changed password with a live reset link still
	// outstanding, which is a vulnerability.
	//
	// A refusal writes nothing, so a caller that swallows one and commits anyway
	// commits none of Consume's work — but it also commits a password write that
	// was never authorized. Return the error and let the transaction unwind.
	Consume(ctx context.Context, tx database.Tx, scope tenancy.Scope, secret string) (*Token, error)

	// RevokeForUser destroys every unredeemed token a principal holds and
	// reports how many it destroyed.
	//
	// It is what a completed reset calls afterwards, so the links that were
	// outstanding when the password changed stop working — in the transaction
	// that changed it, since a revocation that lands separately is a window in
	// which the old links still work against the new password. It is also what a
	// user who says "that wasn't me" needs, and what an administrator disabling
	// an account wants to run before walking away from it.
	//
	// Redeemed rows are left alone. They are already unspendable, and they are
	// what makes "this link has already been used" answerable for the life of
	// the link.
	RevokeForUser(ctx context.Context, tx database.Tx, scope tenancy.Scope, userID string) (int64, error)

	// DeleteForUser destroys every token a principal holds — redeemed rows
	// included — and reports how many it destroyed. It is what
	// authentication/passwordreset/privacy's dataprivacy.Eraser is built on.
	//
	// It is a second method rather than a flag on RevokeForUser, because the
	// two are different acts that happen to touch one table. A revocation is
	// part of the reset flow: it runs in the transaction that changed the
	// password, and it spares redeemed rows on purpose so that a spent link
	// keeps answering "this link has already been used" for the rest of its
	// life. An erasure is somebody exercising a right, and it is precisely the
	// case where that answer is not owed — the person it would be answered
	// about has asked to be forgotten. A boolean between them would make the
	// difference something a caller passes rather than something they choose.
	//
	// Reaching for the revocation instead would leave every redeemed row, which
	// is to say every reset the subject actually completed, with their
	// identifier on it until the sweeper got to it.
	//
	// Zero is not an error: a principal who never asked for a reset is a
	// principal with nothing here to erase.
	DeleteForUser(ctx context.Context, tx database.Tx, scope tenancy.Scope, userID string) (int64, error)

	// ListForUser reads every token a principal holds, oldest first, and is what
	// authentication/passwordreset/privacy's dataprivacy.Collector is built on.
	//
	// What it answers with is what a subject access request is entitled to: when
	// each reset was asked for, when it expires, and whether it was spent. The
	// digest is not projected — no statement in this package reads that column
	// back — so there is nothing here a caller could exchange for a link.
	//
	// It is unpaged, and that is a decision rather than an omission. Every row
	// in this table is deleted at its own expiry by the sweeper, so what one
	// principal holds is bounded by the token lifetime rather than by their
	// history: it is identity.Store.ListMembershipsForUser's reading, over a
	// table with a stronger version of the same guarantee. Paging a handful
	// means a caller who forgets to loop reads some of somebody's data and
	// treats the rest as absent, which for a subject access request is a
	// compliance defect that looks exactly like a correct answer.
	//
	// An empty result is an empty slice and no error.
	ListForUser(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, userID string) ([]*Token, error)
}
