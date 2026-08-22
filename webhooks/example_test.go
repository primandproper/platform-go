package webhooks_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/primandproper/platform-go/v13/cryptography/requestsigning"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/database/sqlite"
	"github.com/primandproper/platform-go/v13/tenancy"
	"github.com/primandproper/platform-go/v13/webhooks"
	"github.com/primandproper/platform-go/v13/webhooks/migrations"
)

// An application's event types, declared as webhooks.EventType constants. That
// is the form this package asks for, and the reason is the catalog below: it has
// to list every event type the application publishes, a missing entry fails the
// dispatch gate, and a list maintained by hand beside the constants it mirrors
// drifts. Declared this way the two can be derived from one another, because
// "which constants are event types" is a question the type checker can answer.
const (
	OrderCreated webhooks.EventType = "order.created"
	OrderUpdated webhooks.EventType = "order.updated"
)

// Dispatch writes deliveries through the caller's transaction, so an event
// cannot survive a rolled-back state change — nor be lost by a commit that
// succeeded while the publish failed.
func ExampleDispatcher_Dispatch() {
	ctx := context.Background()

	// In a real service these come from your DI container.
	client, _, dispatcher := exampleWiring()

	order := struct {
		ID        string `json:"id"`
		AccountID string `json:"accountID"`
	}{ID: "order-7", AccountID: "acct_01HZY0000000000000"}

	body, err := json.Marshal(order)
	if err != nil {
		panic(err)
	}

	err = client.WithTransaction(ctx, func(q database.SQLQueryExecutor) error {
		// ... the state change that produced the event ...

		return dispatcher.Dispatch(ctx, q, &webhooks.Delivery{
			// Whose event this is. The fan-out is bounded by it, so only this
			// account's endpoints are resolved. An application whose events are
			// global says tenancy.Global().
			Scope:     tenancy.Of(order.AccountID),
			EventType: OrderUpdated,
			// Deliveries sharing an ordering key reach a given subscriber in
			// dispatch order, so order.updated cannot overtake order.created.
			OrderingKey: order.ID,
			Payload:     body,
		})
	})

	fmt.Println(err)
	// Output: <nil>
}

// An endpoint belongs to somebody, and fan-out is bounded by whose event it is:
// registering in one account's scope means never receiving another account's copy
// of the same event type.
func ExampleDispatcher_Register() {
	ctx := context.Background()

	client, store, dispatcher := exampleWiring()

	secret := webhooks.Secret{Current: []byte("the shared signing key")}

	subscribers := map[string]tenancy.Scope{
		"endpoint-for-acct-1": tenancy.Of("acct_1"),
		"endpoint-for-acct-2": tenancy.Of("acct_2"),
		// A scope like any other, and the one an application whose events are
		// global uses for everything.
		"endpoint-for-nobody": tenancy.Global(),
	}

	for id, scope := range subscribers {
		if err := dispatcher.Register(ctx, &webhooks.Endpoint{
			ID:     id,
			Scope:  scope,
			URL:    "https://93.184.216.34/hooks/" + id,
			Secret: secret,
			Events: []webhooks.EventType{OrderUpdated},
		}); err != nil {
			panic(err)
		}
	}

	// Who order.updated reaches, per scope. Nothing here can return the other
	// account's endpoint: the scope is a predicate on the query.
	err := client.WithTransaction(ctx, func(q database.SQLQueryExecutor) error {
		for _, scope := range []tenancy.Scope{tenancy.Of("acct_1"), tenancy.Global()} {
			endpoints, resolveErr := store.EndpointsForEvent(ctx, q, scope, OrderUpdated)
			if resolveErr != nil {
				return resolveErr
			}

			for _, endpoint := range endpoints {
				fmt.Println(scope, endpoint.ID)
			}
		}

		return nil
	})
	if err != nil {
		panic(err)
	}

	// Output:
	// acct_1 endpoint-for-acct-1
	// <global> endpoint-for-nobody
}

// What a subscriber does on receipt. The scheme lives in
// cryptography/requestsigning, which this package signs through; a subscriber
// verifies through the same package, so the two halves cannot drift.
func Example_verifyingADelivery() {
	secret := webhooks.Secret{Current: []byte("the shared signing key")}

	subscriber := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		// The exact bytes received, read before any decoding. Decoding and
		// re-encoding changes key order and whitespace, and the signature covers
		// bytes rather than meaning.
		body, err := io.ReadAll(req.Body)
		if err != nil {
			res.WriteHeader(http.StatusBadRequest)

			return
		}

		if err = requestsigning.Verify(secret, body, req.Header.Get(requestsigning.SignatureHeader)); err != nil {
			res.WriteHeader(http.StatusUnauthorized)

			return
		}

		// The delivery ID is stable across every retry and replay of one
		// delivery, so it is the key to deduplicate on.
		_ = req.Header.Get(webhooks.DeliveryIDHeader)

		res.WriteHeader(http.StatusNoContent)
	}))
	defer subscriber.Close()

	payload := []byte(`{"id":"order-7"}`)

	signature, err := requestsigning.Sign(secret, payload, time.Now())
	if err != nil {
		panic(err)
	}

	req, err := http.NewRequestWithContext(context.Background(),
		http.MethodPost, subscriber.URL, strings.NewReader(string(payload)))
	if err != nil {
		panic(err)
	}

	req.Header.Set(requestsigning.SignatureHeader, signature)

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		panic(err)
	}
	defer func() { _ = res.Body.Close() }()

	fmt.Println(res.StatusCode)

	// A tampered body no longer verifies.
	fmt.Println(requestsigning.Verify(secret, []byte(`{"id":"order-8"}`), signature))

	// Output:
	// 204
	// invalid request signature
}

// exampleWiring stands up a throwaway SQLite-backed dispatcher so the Dispatch
// example is executable rather than illustrative. A real service builds these
// once at startup, through webhooks/config.
func exampleWiring() (database.Client, webhooks.Store, webhooks.Dispatcher) {
	ctx := context.Background()

	dir, err := os.MkdirTemp("", "webhooks-example")
	if err != nil {
		panic(err)
	}

	client, err := sqlite.NewDatabaseClient(ctx, &exampleClientConfig{
		connectionString: filepath.Join(dir, "webhooks.db"),
	})
	if err != nil {
		panic(err)
	}

	stmts, err := migrations.Statements(dialect.SQLite, webhooks.DefaultTablePrefix)
	if err != nil {
		panic(err)
	}

	for _, stmt := range stmts {
		if _, err = client.Writer().ExecContext(ctx, stmt); err != nil {
			panic(err)
		}
	}

	store, err := webhooks.NewSQLStore(client)
	if err != nil {
		panic(err)
	}

	// The catalog is the application's: what its events mean is not the
	// library's opinion, and an event outside it is rejected at both ends.
	dispatcher, err := webhooks.NewDispatcher(store, webhooks.WithCatalog(webhooks.Catalog{
		OrderCreated: {Description: "an order was created"},
		OrderUpdated: {Description: "an order was updated"},
	}))
	if err != nil {
		panic(err)
	}

	return client, store, dispatcher
}

type exampleClientConfig struct {
	connectionString string
}

func (c *exampleClientConfig) GetReadConnectionString() string   { return c.connectionString }
func (c *exampleClientConfig) GetWriteConnectionString() string  { return c.connectionString }
func (c *exampleClientConfig) GetMaxPingAttempts() uint64        { return 1 }
func (c *exampleClientConfig) GetPingWaitPeriod() time.Duration  { return time.Millisecond }
func (c *exampleClientConfig) GetMaxIdleConns() int              { return 2 }
func (c *exampleClientConfig) GetMaxOpenConns() int              { return 1 }
func (c *exampleClientConfig) GetConnMaxLifetime() time.Duration { return time.Minute }
