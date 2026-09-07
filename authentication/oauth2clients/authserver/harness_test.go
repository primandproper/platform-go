package authserver_test

import (
	"context"
	"net/http"

	"github.com/primandproper/platform-go/v14/authentication/oauth2clients"
	"github.com/primandproper/platform-go/v14/authentication/oauth2server"
	"github.com/primandproper/platform-go/v14/database"
	"github.com/primandproper/platform-go/v14/filtering"
	"github.com/primandproper/platform-go/v14/tenancy"
)

// The two registries these tests work in, and the two people in them.
var (
	tenantA = tenancy.Of("tenant-a")
	tenantB = tenancy.Of("tenant-b")
)

const (
	userA = "user-a"
	userB = "user-b"
)

// fakeRegistry is an oauth2clients.Store that answers one lookup.
//
// It is hand-written rather than generated because these tests exercise one
// method: what the seams do with what ResolveClientID hands back. The other six
// are declared to satisfy the interface and panic if anything reaches them,
// which is the assertion that these seams read nothing else.
type fakeRegistry struct {
	client *oauth2clients.Client
	err    error
}

var _ oauth2clients.Store = (*fakeRegistry)(nil)

func (f *fakeRegistry) ResolveClientID(
	context.Context, database.SQLQueryExecutor, string,
) (*oauth2clients.Client, error) {
	return f.client, f.err
}

func (f *fakeRegistry) CreateClient(
	context.Context, database.Tx, tenancy.Scope, *oauth2clients.Client,
) error {
	panic("the authorization server seams do not write registrations")
}

func (f *fakeRegistry) UpdateClient(
	context.Context, database.Tx, tenancy.Scope, string, *oauth2clients.UpdateInput,
) error {
	panic("the authorization server seams do not write registrations")
}

func (f *fakeRegistry) ArchiveClient(context.Context, database.Tx, tenancy.Scope, string) error {
	panic("the authorization server seams do not write registrations")
}

func (f *fakeRegistry) GetClient(
	context.Context, database.SQLQueryExecutor, tenancy.Scope, string,
) (*oauth2clients.Client, error) {
	panic("the authorization server seams resolve by client_id, never by row id")
}

func (f *fakeRegistry) ListClients(
	context.Context, database.SQLQueryExecutor, tenancy.Scope, *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[oauth2clients.Client], error) {
	panic("the authorization server seams do not page registrations")
}

func (f *fakeRegistry) ListClientsForOwner(
	context.Context, database.SQLQueryExecutor, tenancy.Scope, string, *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[oauth2clients.Client], error) {
	panic("the authorization server seams do not page registrations")
}

// fakeProtocolStore is an oauth2server.Store whose client half must never be
// reached: the decorator overrides all three of those methods, and anything
// arriving here is the embedding having stopped working.
type fakeProtocolStore struct {
	oauth2server.Store
}

// resolverFunc adapts a function to oauth2server.SubjectResolver, so a test can
// state the inner resolver's answer inline.
type resolverFunc func(ctx context.Context, req *http.Request) (*oauth2server.Subject, error)

func (f resolverFunc) ResolveSubject(
	ctx context.Context,
	req *http.Request,
) (*oauth2server.Subject, error) {
	return f(ctx, req)
}

// registration builds a fixture in one registry, optionally owned.
func registration(scope tenancy.Scope, owner string) *oauth2clients.Client {
	return &oauth2clients.Client{
		Scope:         scope,
		BelongsToUser: owner,
		ID:            "row-1",
		ClientID:      "cid-1",
		SecretHash:    oauth2server.Hash("s3cret"),
		Name:          "test client",
		RedirectURIs:  []string{"https://example.test/callback"},
		Scopes:        []string{"recipes:read"},
	}
}
