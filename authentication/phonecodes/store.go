package phonecodes

import (
	"context"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// Store is the texted-code table: one code per phone number per scope, stored
// as a digest, spent once, and dead after too many wrong guesses.
//
// Every write takes a database.Tx and every read takes the wider
// database.SQLQueryExecutor, which is the module's store convention. A caller
// with nothing to join opens one with Client.WithTransaction.
//
// Every method is scoped and there is no unscoped variant. An application with
// a single tenant passes tenancy.Global() everywhere.
//
// # What this store does not do
//
// It sends nothing. Store.Issue returns the plaintext once; the consumer texts
// it through primitives-go's sms after the transaction commits, the way
// passwordreset mails after its commit rather than inside it — a code texted
// for a transaction that then rolled back is a code that will never redeem.
//
// It rate limits nothing but guesses. How often a number may be sent a code is
// the consumer's to decide, with Issuance.Previous to decide it from, and how
// often a caller may try is primitives-go's ratelimiting in front of the door.
//
// It reads no directory. The subject is whatever the consumer says it is.
type Store interface {
	// Issue mints a code for a phone number, stores its digest, and returns the
	// plaintext exactly once.
	//
	// It replaces whatever code the number already held in the scope, spent or
	// not, so a person who asks twice and enters the second code succeeds and
	// one who enters the first fails. The replaced code comes back as
	// Issuance.Previous.
	//
	// # Two issues racing for one number may deadlock on MySQL
	//
	// The replacement is an upsert onto (scope, phone_number), and InnoDB
	// breaks two of those racing for a number that holds no row yet by killing
	// one transaction with 1213. Postgres and SQLite serialize them instead.
	// The store cannot start over itself, because the transaction is the
	// caller's, so a caller whose number can be asked for twice at once runs
	// the issue under database.WithTransaction with database.RetryOnConflict.
	// Starting over is safe as long as the callback acts only through its Tx
	// and texts nothing, since the code is texted after the commit:
	//
	//	err := database.WithTransaction(ctx, client, func(tx database.Tx) error {
	//		issuance, err = store.Issue(ctx, tx, scope, request)
	//		return err
	//	}, database.RetryOnConflict(3))
	Issue(ctx context.Context, tx database.Tx, scope tenancy.Scope, request *IssueRequest) (*Issuance, error)

	// Redeem spends the code a phone number holds, if code is it, and answers
	// with the code it spent and true. A code it will not spend is false, with
	// a nil Code and a nil error.
	//
	// Every refusal is the same false: no code for the number, a spent one, a
	// withdrawn one, an expired one, one at its attempt limit, and the wrong
	// code. Told apart, they would tell a guesser which half of a guess was
	// right; the difference is recorded on the span instead. ErrCodeInvalid is
	// the sentinel a caller reports it with.
	//
	// A refusal is a result rather than an error because a wrong code against
	// a live one is counted, in tx, and the count is the attempt limit — the
	// whole of what stops a guesser working through a million codes. As an
	// error, the natural `return err` out of a WithTransaction callback would
	// roll the count back. As a result, that same callback commits it:
	//
	//	var redeemed bool
	//	err := client.WithTransaction(ctx, func(tx database.Tx) error {
	//		spent, ok, err := store.Redeem(ctx, tx, scope, phone, entered)
	//		if err != nil || !ok {
	//			return err // nil for a refusal, so the count commits
	//		}
	//		redeemed = true
	//		// ... the rest of the sign-in, on tx, with spent ...
	//	})
	//	if err == nil && !redeemed {
	//		err = phonecodes.ErrCodeInvalid
	//	}
	//
	// ErrCodeInvalid is reported after the transaction, never from inside it.
	//
	// An error is a failure to answer — an argument refused, or the database —
	// and nothing was counted that the caller must keep.
	Redeem(ctx context.Context, tx database.Tx, scope tenancy.Scope, phoneNumber, code string) (*Code, bool, error)

	// RevokeForSubject withdraws every unspent code one person holds in the
	// scope and reports how many it withdrew. A second revocation withdraws
	// nothing and is not an error.
	RevokeForSubject(ctx context.Context, tx database.Tx, scope tenancy.Scope, subjectID string) (int64, error)

	// ListForSubject reads every code one person holds in the scope, in any
	// state, oldest first. It is the privacy export's read —
	// authentication/phonecodes/privacy — and unpaged because a person holds
	// one row per number.
	ListForSubject(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, subjectID string) ([]*Code, error)

	// DeleteForSubject destroys every code one person holds in the scope and
	// reports how many rows went. It is the erasure. A person who holds none
	// deletes nothing and is not an error.
	DeleteForSubject(ctx context.Context, tx database.Tx, scope tenancy.Scope, subjectID string) (int64, error)
}
