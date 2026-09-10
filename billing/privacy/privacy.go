/*
Package privacy is the billing tables' contribution to a subject access request:
a dataprivacy.Collector that returns what somebody was sold and what they paid.

# There is deliberately no Eraser

Every other privacy subpackage in this module ships both halves. This one ships
one, and the omission is the ruling rather than an unfinished job.

A subscription, a purchase and a ledger row are financial records. Every
jurisdiction that grants a right to erasure also requires those records kept —
seven years is the common figure — and every one of them resolves the conflict
the same way: the retention obligation wins, and the controller says so in its
response. An Eraser here would be a seam whose only correct implementation is one
that erases nothing, and shipping it would invite a deployment to register it and
believe its billing history had been deleted.

What a deployment genuinely owes the subject is a statement of what is kept and
why, which is a sentence in a privacy notice rather than a method on a store. A
deployment whose counsel says otherwise deletes the rows through the store's
archive and its own migration; it is a decision somebody has made, and this
package declines to make it for them.

The [Collector] is not affected by any of that. The right of access is not the
right of erasure, and what the subject is entitled to see is exactly what these
tables hold about them.

# Subjects are people, and these tables are keyed on accounts

dataprivacy.Subject names whoever made the request, and every row here belongs to
an account. The mapping between the two is a question about a consumer's tenancy
model — one person may hold several accounts, an account may have several members
— so it is an [AccountResolver] the caller supplies rather than something this
package guesses at.

It is a constructor argument rather than an option with a default, for the reason
comments/privacy gives about its own resolver: a default that answered "the
account whose id equals the subject id" would export nothing for most deployments
and report success doing it.

# Where the executor comes from

Every read in billing runs on an executor its caller supplies, and
dataprivacy.Collector.Collect is handed nothing — an export is a read, and the
seam that asks for it hands over a subject and nothing else. So [NewCollector]
takes the executor once, at construction, and every collection runs on it. It is
the shape comments/privacy took first.

That is a database.SQLQueryExecutor rather than a database.Client: a collector
reads and does nothing else, and the narrower type is also the one that lets a
consumer hand it a Tx where an export genuinely has to see a transaction's own
writes. There is no matching argument for an eraser because there is no eraser —
see above.

# Why this is a package rather than methods on the store

billing would otherwise import dataprivacy, which imports operations, which
imports the queue and the scheduler — so a service that sells something and runs
no privacy pipeline would compile all of it. The seam goes here for the same
reason dataprivacy/auditerasure exists, and it costs one constructor argument.

# Observability

The collector instruments nothing of its own. Everything it does is a call into
the store, which spans and logs every read it makes, and a second span around a
loop of those would name the same work twice.
*/
package privacy

import (
	"context"
	"encoding/json"

	"github.com/primandproper/platform-go/v14/billing"
	"github.com/primandproper/platform-go/v14/dataprivacy"

	"github.com/primandproper/primitives-go/database"
	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/filtering"
	"github.com/primandproper/primitives-go/tenancy"
)

// DefaultKey is the registry key this collector is normally registered under. It
// names the section an export's artifact carries it in.
const DefaultKey = "billing"

// pageSize is how many rows of each kind one page of the walk reads.
const pageSize = 100

// The sentinels this package returns.
var (
	// ErrNilStore indicates a nil billing.Store.
	ErrNilStore = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil billing store")

	// ErrNilAccountResolver indicates a nil AccountResolver. It is required —
	// see the package documentation for why there is no default.
	ErrNilAccountResolver = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil account resolver")

	// ErrNilExecutor indicates a nil executor. The collector's is supplied at
	// construction, because billing keeps no connection of its own to fall back
	// to and Collect has nowhere to take one.
	ErrNilExecutor = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil billing privacy query executor")
)

// Account is one account a subject's billing may be under: which scope it is in,
// and its id within that scope.
//
// Both halves are needed and neither is inferable from the other, which is why
// this is a struct rather than a pair of resolvers.
type Account struct {
	// ID is the account id those rows carry in belongs_to_account.
	ID string
	// Scope is the tenancy scope the account's rows are in.
	Scope tenancy.Scope
}

// AccountResolver answers which accounts a subject's billing is under.
//
// Returning none is an answer rather than a failure: a subject who has never
// bought anything has no billing, and the collector reports an empty export for
// them.
//
// requestScope is the confinement the privacy request named, handed over beside
// the subject. The zero Scope is the request that named none — a plain "give me
// my data" — and a resolver that has accounts in several scopes reads it as the
// instruction to return all of them, or refuses, as its deployment requires.
type AccountResolver func(
	ctx context.Context,
	requestScope tenancy.Scope,
	subject dataprivacy.Subject,
) ([]Account, error)

// FixedAccounts is an AccountResolver for the deployment whose subject id is its
// account id, in one scope — a single-tenant application where a person and
// their account are the same row.
//
// It is spelled out here rather than made the default, so that taking it is a
// deployment saying its model is that simple.
func FixedAccounts(scope tenancy.Scope) AccountResolver {
	return func(_ context.Context, _ tenancy.Scope, subject dataprivacy.Subject) ([]Account, error) {
		return []Account{{Scope: scope, ID: subject.ID}}, nil
	}
}

// Export is what one account's billing looks like in a subject access artifact.
//
// It is the store's own types rather than a flattened rendering of them, so that
// what a subject receives and what the application reads are the same shape — and
// so that a column added to a table appears in the export without anybody
// remembering to add it here.
type Export struct {
	// Account is which account these rows belong to.
	Account string `json:"account"`
	// Scope is the tenancy scope it is in. It renders as the identifier the
	// column holds — the empty string for the global scope — which is what
	// tenancy.Scope's own JSON is, so an export says the same thing the rows
	// behind it do.
	Scope tenancy.Scope `json:"scope"`
	// Subscriptions is every recurring agreement, current and lapsed.
	Subscriptions []*billing.Subscription `json:"subscriptions"`
	// Purchases is every one-time sale.
	Purchases []*billing.Purchase `json:"purchases"`
	// Transactions is every payment attempt, whatever became of it.
	Transactions []*billing.Transaction `json:"transactions"`
}

var _ dataprivacy.Collector = (*Collector)(nil)

// Collector exports what an account was sold and what it paid.
//
// It is exported, and returned by NewCollector, so a caller can depend on what it
// built rather than on the dataprivacy.Collector seam.
type Collector struct {
	store   billing.Store
	reader  database.SQLQueryExecutor
	resolve AccountResolver
}

// NewCollector builds the collector over a store, the executor its reads run on,
// and an account resolver. All three are required.
//
// The executor is a constructor argument because dataprivacy.Collector.Collect
// has nowhere to put one. Client.Reader() is what a consumer ordinarily passes;
// a Tx is what it passes when the export has to see writes that transaction has
// not committed.
func NewCollector(
	store billing.Store,
	reader database.SQLQueryExecutor,
	resolve AccountResolver,
) (*Collector, error) {
	if store == nil {
		return nil, ErrNilStore
	}

	if reader == nil {
		return nil, ErrNilExecutor
	}

	if resolve == nil {
		return nil, ErrNilAccountResolver
	}

	return &Collector{store: store, reader: reader, resolve: resolve}, nil
}

// Collect implements dataprivacy.Collector.
//
// It reads archived rows as well as live ones. An archived subscription is still
// something the subject was sold, and an export that showed only what an operator
// had not yet tidied away would be an export that answered a different question
// than the one the right of access asks.
func (c *Collector) Collect(
	ctx context.Context,
	requestScope tenancy.Scope,
	subject dataprivacy.Subject,
) (json.RawMessage, error) {
	accounts, err := c.resolve(ctx, requestScope, subject)
	if err != nil {
		return nil, platformerrors.Wrapf(err, "resolving billing accounts for subject %q", subject.ID)
	}

	exports := make([]Export, 0, len(accounts))

	for i := range accounts {
		export, collectErr := c.collectAccount(ctx, &accounts[i])
		if collectErr != nil {
			return nil, collectErr
		}

		exports = append(exports, export)
	}

	body, err := json.Marshal(exports)
	if err != nil {
		return nil, platformerrors.Wrap(err, "encoding the billing export")
	}

	return body, nil
}

// collectAccount walks the three tables for one account.
func (c *Collector) collectAccount(ctx context.Context, account *Account) (Export, error) {
	export := Export{Account: account.ID, Scope: account.Scope}

	subscriptions, err := drain(func(filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[billing.Subscription], error) {
		return c.store.ListSubscriptionsForAccount(ctx, c.reader, account.Scope, account.ID, filter)
	})
	if err != nil {
		return export, platformerrors.Wrapf(err, "collecting subscriptions for account %q", account.ID)
	}

	purchases, err := drain(func(filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[billing.Purchase], error) {
		return c.store.ListPurchasesForAccount(ctx, c.reader, account.Scope, account.ID, filter)
	})
	if err != nil {
		return export, platformerrors.Wrapf(err, "collecting purchases for account %q", account.ID)
	}

	transactions, err := drain(func(filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[billing.Transaction], error) {
		return c.store.ListTransactionsForAccount(ctx, c.reader, account.Scope, account.ID, filter)
	})
	if err != nil {
		return export, platformerrors.Wrapf(err, "collecting transactions for account %q", account.ID)
	}

	export.Subscriptions, export.Purchases, export.Transactions = subscriptions, purchases, transactions

	return export, nil
}

// drain walks every page of one read and returns the rows.
//
// The walk is by cursor rather than by offset, which is what makes it stable
// while the tables are being written to — an export is not a snapshot, and a
// deployment recording a payment halfway through one should not lose a row to a
// shifted page boundary.
func drain[T any](
	page func(*filtering.QueryFilter) (*filtering.QueryFilteredResult[T], error),
) ([]*T, error) {
	limit := uint16(pageSize)
	archived := true

	filter := &filtering.QueryFilter{MaxResponseSize: &limit, IncludeArchived: &archived}

	var collected []*T

	for {
		result, err := page(filter)
		if err != nil {
			return nil, err
		}

		collected = append(collected, result.Data...)

		if result.Cursor == "" || len(result.Data) == 0 {
			return collected, nil
		}

		filter.Cursor = &result.Cursor
	}
}
