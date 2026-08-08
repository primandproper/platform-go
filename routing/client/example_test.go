package client_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"

	"github.com/primandproper/platform-go/v10/encoding"
	"github.com/primandproper/platform-go/v10/routing"
	"github.com/primandproper/platform-go/v10/routing/backends/chi"
	"github.com/primandproper/platform-go/v10/routing/client"
)

// The contract a service publishes: the In and Out types, and the endpoints that
// speak them. A Go consumer imports this and needs nothing else.
type (
	fetchBook struct {
		ISBN   string `path:"isbn"`
		Locale string `query:"locale"`
	}

	book struct {
		ISBN   string `json:"isbn"`
		Title  string `json:"title"`
		Locale string `json:"locale"`
	}
)

var getBook = routing.Endpoint[fetchBook, book]{
	Method:  http.MethodGet,
	Pattern: "/books/{isbn}",
}

// Example shows the same descriptor serving a route and calling it: registered
// with routing.Register on one side, invoked with client.Call on the other. The
// path and every field name are stated once.
func Example() {
	// --- The service ------------------------------------------------------
	router := routing.New(
		chi.NewBackend(&chi.Config{ServiceName: "library"}),
		encoding.NewServerEncoderDecoder(encoding.ContentTypeJSON),
	)

	routing.Register(router, getBook, func(_ context.Context, in fetchBook) (book, error) {
		if in.ISBN != "9780262033848" {
			return book{}, sql.ErrNoRows
		}

		return book{ISBN: in.ISBN, Title: "Introduction to Algorithms", Locale: in.Locale}, nil
	})

	if err := router.Err(); err != nil {
		panic(err)
	}

	srv := httptest.NewServer(router.Handler())
	defer srv.Close()

	// --- The consumer, in another repository ------------------------------
	c, err := client.New(srv.URL, client.WithHTTPClient(srv.Client()))
	if err != nil {
		panic(err)
	}

	found, err := client.Call(context.Background(), c, getBook, fetchBook{
		ISBN:   "9780262033848",
		Locale: "en-US",
	})
	if err != nil {
		panic(err)
	}

	fmt.Printf("%s (%s)\n", found.Title, found.Locale)

	// A refusal comes back as the sentinel the handler returned, so the caller
	// branches on it exactly as it would have in-process.
	_, err = client.Call(context.Background(), c, getBook, fetchBook{ISBN: "0000000000000"})
	fmt.Println("missing book is sql.ErrNoRows:", errors.Is(err, sql.ErrNoRows))

	// Output:
	// Introduction to Algorithms (en-US)
	// missing book is sql.ErrNoRows: true
}
