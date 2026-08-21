package dataprivacy_test

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/primandproper/platform-go/v12/database"
	"github.com/primandproper/platform-go/v12/dataprivacy"
	dataprivacymock "github.com/primandproper/platform-go/v12/dataprivacy/mock"
	"github.com/primandproper/platform-go/v12/filtering"
	"github.com/primandproper/platform-go/v12/operations"
	uploadsnoop "github.com/primandproper/platform-go/v12/uploads/noop"
)

// A Collector returns one domain's view of a subject as already-encoded JSON.
// The library never looks inside it, which is what lets a domain be added by
// registration rather than by editing a shared type.
func ExampleCollector() {
	identity := dataprivacy.CollectorFunc(func(_ context.Context, subject dataprivacy.Subject) (json.RawMessage, error) {
		// In a real collector this is a query against the domain's own tables.
		return json.Marshal(map[string]string{
			"id":    subject.ID,
			"email": "someone@example.com",
		})
	})

	fragment, err := identity.Collect(context.Background(), dataprivacy.Subject{ID: "user-1"})
	if err != nil {
		panic(err)
	}

	fmt.Println(string(fragment))
	// Output: {"email":"someone@example.com","id":"user-1"}
}

// CollectorFor covers the collector that is one paged read. Walking the cursor
// to its end and saying "nothing held" as an omitted section are this package's
// semantics, so a domain supplies only its own read.
func ExampleCollectorFor() {
	// In a real collector this is the domain's repository method.
	list := func(_ context.Context, subject dataprivacy.Subject, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[exampleWebhook], error) {
		return filtering.NewQueryFilteredResult(
			[]*exampleWebhook{{ID: "webhook-1", Owner: subject.ID}},
			1, 1,
			func(w *exampleWebhook) string { return w.ID },
			filter,
		), nil
	}

	collector := dataprivacy.CollectorFor(list)

	fragment, err := collector.Collect(context.Background(), dataprivacy.Subject{ID: "user-1"})
	if err != nil {
		panic(err)
	}

	fmt.Println(string(fragment))
	// Output: [{"id":"webhook-1","owner":"user-1"}]
}

// exampleWebhook stands in for whatever a domain's list read returns.
type exampleWebhook struct {
	ID    string `json:"id"`
	Owner string `json:"owner"`
}

// Fragment is how a collector that is not a plain list read says "nothing about
// this subject": nil, so the section is omitted from the artifact rather than
// written as null.
func ExampleFragment() {
	settings, held := struct {
		Locale string `json:"locale"`
	}{Locale: "en-GB"}, true

	fragment, err := dataprivacy.Fragment(held, settings)
	if err != nil {
		panic(err)
	}

	empty, err := dataprivacy.Fragment(false, settings)
	if err != nil {
		panic(err)
	}

	fmt.Println(string(fragment))
	fmt.Println(empty == nil)
	// Output:
	// {"locale":"en-GB"}
	// true
}

// An Eraser reports what it destroyed, what it anonymized, and what it kept.
// Erasure is not the inverse of export: only the domain knows which of its
// tables must be retained and on what basis.
func ExampleEraser() {
	billing := dataprivacy.EraserFunc(func(
		_ context.Context,
		_ database.SQLQueryExecutor,
		_ dataprivacy.Subject,
	) (dataprivacy.ErasureOutcome, error) {
		return dataprivacy.ErasureOutcome{
			Deleted:    12,
			Anonymized: 3,
			Retained: map[string]string{
				"invoices": "financial records, retained 7 years under tax law",
			},
		}, nil
	})

	outcome, err := billing.Erase(context.Background(), nil, dataprivacy.Subject{ID: "user-1"})
	if err != nil {
		panic(err)
	}

	fmt.Println(outcome.Deleted, outcome.Anonymized)
	fmt.Println(outcome.Retained["invoices"])
	// Output:
	// 12 3
	// financial records, retained 7 years under tax law
}

// Adding a domain is a registration, not an edit to a central type that imports
// every domain package.
func ExampleRegistry() {
	registry := dataprivacy.NewRegistry()

	for _, key := range []string{"identity", "billing", "webhooks"} {
		if err := registry.RegisterCollector(key, dataprivacy.CollectorFunc(
			func(context.Context, dataprivacy.Subject) (json.RawMessage, error) {
				return json.RawMessage(`{}`), nil
			},
		)); err != nil {
			panic(err)
		}
	}

	// Sorted, so two exports of the same subject list their sections
	// identically whatever order the wiring ran in.
	fmt.Println(registry.CollectorKeys())
	// Output: [billing identity webhooks]
}

// A partial export is delivered with a manifest naming what is missing, rather
// than failing outright or silently omitting the gap.
func ExampleManifest() {
	doc := &dataprivacy.Document{
		Data: map[string]json.RawMessage{
			"identity": json.RawMessage(`{"email":"someone@example.com"}`),
		},
		Manifest: dataprivacy.Manifest{
			Format:    dataprivacy.DocumentFormat,
			RequestID: "req-1",
			Sections:  []string{"identity"},
			Failures:  map[string]string{"billing": "context deadline exceeded"},
		},
	}

	fmt.Println(doc.Complete())
	fmt.Println(doc.Manifest.Failures["billing"])
	// Output:
	// false
	// context deadline exceeded
}

// Both halves of the package run as operations. The Fulfiller supplies the two
// runners and registers them; an operations Worker over the same registry is
// what claims, leases, retries, and reports on them.
func ExampleFulfiller_Register() {
	domains := dataprivacy.NewRegistry()

	if err := domains.RegisterCollector("identity", dataprivacy.CollectorFunc(
		func(context.Context, dataprivacy.Subject) (json.RawMessage, error) {
			return json.RawMessage(`{}`), nil
		},
	)); err != nil {
		panic(err)
	}

	if err := domains.RegisterEraser("identity", dataprivacy.EraserFunc(
		func(context.Context, database.SQLQueryExecutor, dataprivacy.Subject) (dataprivacy.ErasureOutcome, error) {
			return dataprivacy.ErasureOutcome{}, nil
		},
	)); err != nil {
		panic(err)
	}

	// In a real assembly the store is a dataprivacy.NewSQLStore and the uploader
	// is real storage; the registration below is the whole of the wiring this
	// example is about.
	fulfiller, err := dataprivacy.NewFulfiller(
		context.Background(), &dataprivacy.FulfillerConfig{}, &dataprivacymock.StoreMock{}, domains,
		dataprivacy.WithFulfillerUploadManager(uploadsnoop.NewUploadManager()),
	)
	if err != nil {
		panic(err)
	}

	kinds := operations.NewRegistry()
	if err = fulfiller.Register(kinds); err != nil {
		panic(err)
	}

	// A process that only submits registers these too: operations resolves a
	// kind at Start, so an unrunnable operation is refused there rather than
	// discovered in a worker an hour later.
	fmt.Println(kinds.Kinds())
	// Output: [dataprivacy.erasure dataprivacy.export]
}
