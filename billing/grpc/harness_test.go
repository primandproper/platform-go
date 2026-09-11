package grpc_test

import (
	"context"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/billing"
	"github.com/primandproper/platform-go/v14/billing/billingpb"
	billinggrpc "github.com/primandproper/platform-go/v14/billing/grpc"
	"github.com/primandproper/platform-go/v14/billing/migrations"
	"github.com/primandproper/platform-go/v14/errormappers"
	identitygrpc "github.com/primandproper/platform-go/v14/identity/grpc"

	"github.com/primandproper/primitives-go/v2/capitalism"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
	"github.com/primandproper/primitives-go/v2/filtering/filteringpb"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test/must"
)

// The suite runs the server's methods in process, against a real SQLite
// database and a real billing.SQLStore.
//
// In process rather than over a bufconn, as authentication/oauth2clients/grpc's
// suite is and for the same reason: what these tests are about is which rows a
// caller reaches, and a connection would add a consumer's interceptor to the
// things under test without adding anything to the decision. The database is
// real for the opposite reason — the refusals here are the store's, and a mocked
// store answers that question itself.

// TestMain registers the domain tier's error mappers once for the binary.
//
// Without it this suite would assert the codes each handler passes as its
// *default* rather than the ones a client reads. Every handler here hands
// PrepareAndLogGRPCStatus codes.Internal for a store failure on purpose — the
// registered mapper is what turns a missing product into NotFound and a
// three-letter currency into InvalidArgument over the preserved chain — so a
// suite that skipped the registration would pin Internal as the answer to "no
// such product" and pass.
//
// It is also exactly the call a consumer owes at their composition root.
func TestMain(m *testing.M) {
	errormappers.Register()
	m.Run()
}

// The scopes these tests work in, and the accounts in the first.
//
// otherScope exists because the scope on this surface comes off the principal
// rather than off the request, so the only way to show a read is keyed on it is
// to ask for the same row from a caller the extractor puts somewhere else.
var (
	testScope  = tenancy.Of("tenant_1")
	otherScope = tenancy.Of("tenant_2")
)

const (
	testUser     = "user_1"
	otherUser    = "user_2"
	testAccount  = "account_1"
	otherAccount = "account_2"
)

// testClientConfig is the minimal database.ClientConfig these tests dial with.
type testClientConfig struct {
	connectionString string
}

var _ database.ClientConfig = (*testClientConfig)(nil)

func (c *testClientConfig) GetReadConnectionString() string   { return c.connectionString }
func (c *testClientConfig) GetWriteConnectionString() string  { return c.connectionString }
func (c *testClientConfig) GetMaxPingAttempts() uint64        { return 1 }
func (c *testClientConfig) GetPingWaitPeriod() time.Duration  { return time.Millisecond }
func (c *testClientConfig) GetMaxIdleConns() int              { return 2 }
func (c *testClientConfig) GetMaxOpenConns() int              { return 1 }
func (c *testClientConfig) GetConnMaxLifetime() time.Duration { return time.Minute }

// prefixCounter names a fresh set of tables per subtest, since the external id
// columns carry unique indexes and subtests share one database.
var prefixCounter atomic.Uint64

// testPrincipal is the consumer's half of the principal seam, as small as the
// interface allows.
type testPrincipal struct {
	userID          string
	activeAccountID string
	scope           tenancy.Scope
}

var _ identitygrpc.Principal = (*testPrincipal)(nil)

func (p *testPrincipal) UserID() string          { return p.userID }
func (p *testPrincipal) Scope() tenancy.Scope    { return p.scope }
func (p *testPrincipal) ActiveAccountID() string { return p.activeAccountID }

// principalKey is where the suite's stand-in for an authentication interceptor
// puts the principal.
type principalKey struct{}

// withPrincipal is what a consumer's interceptor does, with the credential
// reading step removed. A context carrying none reaches the server as an
// anonymous request.
func withPrincipal(ctx context.Context, p identitygrpc.Principal) context.Context {
	if p == nil {
		return ctx
	}

	return context.WithValue(ctx, principalKey{}, p)
}

// extractPrincipal is the PrincipalExtractor the server is built with.
func extractPrincipal(ctx context.Context) (identitygrpc.Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(identitygrpc.Principal)

	return p, ok
}

// ownAccountAuthorizer is the suite's AccountAuthorizer: a caller may act on the
// account their principal names as active, and on nothing else.
//
// It is deliberately not what a deployment would ship — a real one reads a
// membership, which is what identity/grpc's MembershipAuthorizer does — but it
// is a rule with two sides, which is all these tests need in order to show that
// the seam is consulted and that a refusal is answered the two ways this surface
// answers one.
func ownAccountAuthorizer() billinggrpc.AccountAuthorizer {
	return billinggrpc.AccountAuthorizerFunc(
		func(_ context.Context, caller billinggrpc.Principal, accountID string) error {
			if caller != nil && accountID != "" && caller.ActiveAccountID() == accountID {
				return nil
			}

			return billinggrpc.ErrTargetNotPermitted
		})
}

// brokenAuthorizer is an authorizer that cannot decide, which is a different
// answer from a refusal and reaches a client as codes.Internal.
func brokenAuthorizer(err error) billinggrpc.AccountAuthorizer {
	return billinggrpc.AccountAuthorizerFunc(
		func(context.Context, billinggrpc.Principal, string) error { return err })
}

// harness is one database, one store and one server over them.
type harness struct {
	db     database.Client
	store  *billing.SQLStore
	server *billinggrpc.Server
}

// newHarness migrates a uniquely prefixed set of tables and builds the surface
// over them, with the suite's own account rule.
func newHarness(tb testing.TB) *harness {
	tb.Helper()

	return newHarnessWithAuthorizer(tb, ownAccountAuthorizer())
}

// newHarnessWithAuthorizer is newHarness with the seam supplied, for the tests
// that are about what this surface does with each of the three answers an
// AccountAuthorizer may give.
func newHarnessWithAuthorizer(tb testing.TB, targets billinggrpc.AccountAuthorizer) *harness {
	tb.Helper()

	db, err := sqlite.NewDatabaseClient(tb.Context(),
		&testClientConfig{connectionString: filepath.Join(tb.TempDir(), "billing.db")})
	must.NoError(tb, err)
	tb.Cleanup(func() { _ = db.Close() })

	prefix := fmt.Sprintf("bg_%d", prefixCounter.Add(1))

	stmts, err := migrations.Statements(dialect.SQLite, prefix)
	must.NoError(tb, err)
	must.SliceNotEmpty(tb, stmts)

	for _, stmt := range stmts {
		_, execErr := db.Writer().ExecContext(tb.Context(), stmt)
		must.NoError(tb, execErr, must.Sprintf("executing %q", stmt))
	}

	store, err := billing.NewSQLStore(db, billing.WithTablePrefix(prefix))
	must.NoError(tb, err)

	server, err := billinggrpc.NewServer(store, db, extractPrincipal, targets)
	must.NoError(tb, err)

	return &harness{db: db, store: store, server: server}
}

// ctx is a request context carrying a caller in testScope, active on the named
// account.
func (h *harness) ctx(tb testing.TB, userID, accountID string) context.Context {
	tb.Helper()

	return withPrincipal(tb.Context(),
		&testPrincipal{userID: userID, activeAccountID: accountID, scope: testScope})
}

// otherScopeCtx is a caller the extractor puts in a different tenant entirely.
func (h *harness) otherScopeCtx(tb testing.TB) context.Context {
	tb.Helper()

	return withPrincipal(tb.Context(),
		&testPrincipal{userID: otherUser, activeAccountID: testAccount, scope: otherScope})
}

// seedProduct writes one product directly through the store, so a test asserting
// what a caller can reach does not reach it through the surface under test.
func (h *harness) seedProduct(tb testing.TB, scope tenancy.Scope) *billing.Product {
	tb.Helper()

	var product *billing.Product

	must.NoError(tb, h.db.WithTransaction(tb.Context(), func(tx database.Tx) error {
		var err error
		product, err = h.store.CreateProduct(tb.Context(), tx, scope, &billing.Product{
			Name:        "a thing",
			Description: "a thing that is sold",
			Kind:        billing.KindOneTime,
			Currency:    "USD",
			AmountCents: 500,
		})

		return err
	}))
	must.NotNil(tb, product)

	return product
}

// seedSubscription writes one live agreement for the named account.
func (h *harness) seedSubscription(tb testing.TB, scope tenancy.Scope, accountID string) *billing.Subscription {
	tb.Helper()

	var (
		product      = h.seedRecurringProduct(tb, scope)
		subscription *billing.Subscription
		now          = time.Now().UTC()
	)

	must.NoError(tb, h.db.WithTransaction(tb.Context(), func(tx database.Tx) error {
		var err error
		subscription, err = h.store.CreateSubscription(tb.Context(), tx, scope, &billing.Subscription{
			BelongsToAccount:   accountID,
			ProductID:          product.ID,
			Status:             capitalism.SubscriptionStatusActive,
			CurrentPeriodStart: now.Add(-24 * time.Hour),
			CurrentPeriodEnd:   now.Add(24 * time.Hour),
		})

		return err
	}))
	must.NotNil(tb, subscription)

	return subscription
}

// seedRecurringProduct is the product a subscription can be opened against.
func (h *harness) seedRecurringProduct(tb testing.TB, scope tenancy.Scope) *billing.Product {
	tb.Helper()

	var product *billing.Product

	must.NoError(tb, h.db.WithTransaction(tb.Context(), func(tx database.Tx) error {
		var err error
		product, err = h.store.CreateProduct(tb.Context(), tx, scope, &billing.Product{
			Name:                  "a plan",
			Kind:                  billing.KindRecurring,
			Currency:              "USD",
			AmountCents:           1000,
			BillingIntervalMonths: 1,
		})

		return err
	}))
	must.NotNil(tb, product)

	return product
}

// seedPurchase writes one outstanding sale for the named account.
func (h *harness) seedPurchase(tb testing.TB, scope tenancy.Scope, accountID string) *billing.Purchase {
	tb.Helper()

	var (
		product  = h.seedProduct(tb, scope)
		purchase *billing.Purchase
	)

	must.NoError(tb, h.db.WithTransaction(tb.Context(), func(tx database.Tx) error {
		var err error
		purchase, err = h.store.CreatePurchase(tb.Context(), tx, scope, &billing.Purchase{
			BelongsToAccount: accountID,
			ProductID:        product.ID,
			Currency:         "USD",
			AmountCents:      500,
		})

		return err
	}))
	must.NotNil(tb, purchase)

	return purchase
}

// seedTransaction writes one ledger row for the named account.
func (h *harness) seedTransaction(tb testing.TB, scope tenancy.Scope, accountID string) *billing.Transaction {
	tb.Helper()

	var transaction *billing.Transaction

	must.NoError(tb, h.db.WithTransaction(tb.Context(), func(tx database.Tx) error {
		var err error
		transaction, err = h.store.RecordTransaction(tb.Context(), tx, scope, &billing.Transaction{
			BelongsToAccount: accountID,
			Status:           billing.TransactionSucceeded,
			Currency:         "USD",
			AmountCents:      500,
		})

		return err
	}))
	must.NotNil(tb, transaction)

	return transaction
}

// creationInput is the smallest product the store accepts.
func creationInput() *billingpb.ProductCreationInput {
	return &billingpb.ProductCreationInput{
		Name:        "a thing",
		Description: "a thing that is sold",
		Kind:        billingpb.ProductKind_PRODUCT_KIND_ONE_TIME,
		Currency:    "USD",
		AmountCents: 500,
	}
}

// updateInput is a revision of it, restating all seven revisable fields.
func updateInput() *billingpb.ProductUpdateInput {
	return &billingpb.ProductUpdateInput{
		Name:        "a renamed thing",
		Description: "still sold",
		Kind:        billingpb.ProductKind_PRODUCT_KIND_ONE_TIME,
		Currency:    "USD",
		AmountCents: 750,
	}
}

// badFilter is a page request no converter can read, for the tests that assert a
// malformed request is answered as malformed before anything is gated or read.
//
// The sort direction is the field with a closed set of spellings, so an
// unrecognized one is refused by filtering/grpc rather than by this package.
func badFilter() *filteringpb.QueryFilter {
	sortBy := "sideways"

	return &filteringpb.QueryFilter{SortBy: &sortBy}
}
