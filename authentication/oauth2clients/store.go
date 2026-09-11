package oauth2clients

import (
	"context"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// Store is where registered clients live.
//
// # The shapes, and why they differ
//
// Every write takes a database.Tx and every consumer read takes a
// database.SQLQueryExecutor, which is this module's convention and is load
// bearing here in the ordinary way: a registration almost never travels alone.
// The audit entry recording who minted it and the outbox event announcing it are
// the same fact as the row, and a store that opened its own transaction would
// leave them in a second one — so a refused audit entry would commit a
// credential with no provenance. The Tx is producible only by
// database.RunInTransaction, so the signature is the caller's proof they are
// already inside one.
//
// The reads take the wider type deliberately. A Tx satisfies SQLQueryExecutor,
// so one method serves a caller holding Client.Reader() and a caller inside a
// transaction, and the second sees that transaction's own uncommitted writes —
// which is what [Service.CreateClient] needs to read back the creation time the
// database assigned a row it has just inserted.
//
// # The one method that takes no scope
//
// [Store.ResolveClientID], and its documentation says why. Every other method
// here takes a tenancy.Scope, and there is deliberately no unscoped variant of
// any of them: the caller who reaches for one is the caller who has not thought
// about tenancy.
type Store interface {
	// CreateClient records a registration.
	//
	// The scope is an argument even though the Client carries one, and a Client
	// whose own scope disagrees is refused with ErrScopeMismatch rather than
	// corrected — a scope read off a struct the caller assembled somewhere else
	// is exactly the derivation the column rule exists to rule out. A Client
	// naming no scope adopts the argument's.
	//
	// A client_id already in use is ErrClientIDTaken rather than an overwrite.
	CreateClient(ctx context.Context, tx database.Tx, scope tenancy.Scope, client *Client) error

	// UpdateClient revises the four descriptive fields of one live
	// registration, and stamps last_updated_at.
	//
	// It cannot touch the owner, the client_id or the digest: those are
	// immutable in the schema as well as in [UpdateInput], because a row that
	// could reassign its own owner would make the owner check a formality and a
	// credential rotated by an UPDATE is one nobody was handed a new secret for.
	//
	// A registration that is absent, archived, or in another registry is
	// ErrClientNotFound.
	UpdateClient(ctx context.Context, tx database.Tx, scope tenancy.Scope, id string, input *UpdateInput) error

	// ArchiveClient withdraws one registration.
	//
	// Withdrawn rather than deleted, and that is not merely the module's row
	// convention: a client_id names access and refresh tokens that may still be
	// live, and the row is what [Store.ResolveClientID] reads to refuse them.
	// A deleted row would make a withdrawn client indistinguishable from one
	// that never existed, which is the state in which a client_id could be
	// re-issued to somebody else.
	//
	// A registration that is absent, already archived, or in another registry is
	// ErrClientNotFound.
	ArchiveClient(ctx context.Context, tx database.Tx, scope tenancy.Scope, id string) error

	// GetClient reads one of the registry's live registrations by row id.
	//
	// By the row's id, not by its client_id — see [Store.ResolveClientID] for
	// the other one. This is the read a console and the transport make, and it
	// is scoped, so a registration in another registry is ErrClientNotFound
	// rather than a row somebody else's administrator can see.
	GetClient(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, id string) (*Client, error)

	// ListClients pages one registry's registrations — the administered read,
	// which is every row in the registry whoever owns it.
	ListClients(
		ctx context.Context,
		q database.SQLQueryExecutor,
		scope tenancy.Scope,
		filter *filtering.QueryFilter,
	) (*filtering.QueryFilteredResult[Client], error)

	// ListClientsForOwner pages the registrations one person owns, in one
	// registry — the self-service read.
	//
	// It is a second statement rather than a filter on the first, because the
	// two are different questions and only one of them needs a permission in
	// front of it. An empty userID is ErrEmptyUserID and not "the administered
	// ones": a self-service listing that quietly answered with the rows nobody
	// owns would hand a caller with no session the deployment's own credentials.
	ListClientsForOwner(
		ctx context.Context,
		q database.SQLQueryExecutor,
		scope tenancy.Scope,
		userID string,
		filter *filtering.QueryFilter,
	) (*filtering.QueryFilteredResult[Client], error)

	// ResolveClientID reads the registration an authorization request names,
	// across every registry, by the identifier the client sent.
	//
	// It is the one read here that takes no tenancy.Scope, and it takes none
	// because it *resolves* one rather than omitting one. client_id is minted
	// from crypto/rand and is globally unique by the index the schema declares,
	// and the row it finds is the only thing in the system that knows which
	// registry the client is in. There is no scope the caller could have passed:
	// /authorize names a client and nothing else, before anybody has signed in.
	//
	// That makes it the machinery carve-out the tenancy convention names for a
	// component servicing itself — the same one webhooks' delivery worker gets —
	// rather than the unscoped consumer read it forbids. A caller who already
	// holds a scope wants [Store.GetClient].
	//
	// It grants nobody anything. Resolving a registration is not authorizing
	// one, and it cannot be, because it has no subject: [Client.Admits] is what
	// turns a resolved registration into a permitted one, and it runs in the
	// seams that have just authenticated somebody.
	//
	// An archived registration is returned rather than hidden, with ArchivedAt
	// set. The refusal belongs to the caller that has a subject and a reason to
	// name, not to a predicate in a statement — and hiding it here would make a
	// withdrawn client indistinguishable from an unknown one at the one layer
	// that can tell the difference.
	ResolveClientID(ctx context.Context, q database.SQLQueryExecutor, clientID string) (*Client, error)
}
