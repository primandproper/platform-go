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
// transaction, and the second sees that transaction's own uncommitted writes.
//
// # Every write answers with the row it moved
//
// All three of them, and none of them touches the [Client] it was handed. A
// write that reported only an error left its caller describing the registration
// as it stood a statement earlier: an audit entry for a revision named fields
// the statement may not have kept, and one for a withdrawal written from the id
// alone recorded that *something* was withdrawn. The row each returns is read
// back on the caller's own transaction, immediately after the write, so what
// comes back is what the statement left rather than what a second connection
// can see.
//
// The read-back is a second statement rather than RETURNING. MySQL has none,
// and the corpus is one text per dialect rendered from one description, so
// there is no per-dialect fork to hide it in; the guarded write holds the row
// until commit, so the two statements have no gap between them. The cost is one
// round trip per write, and the create's is the one it was already paying.
//
// A refused write answers with a nil row. The sentinels are unchanged — a
// guarded write that matched nothing is still ErrClientNotFound, a minted
// identifier already in use is still ErrClientIDTaken — and the row comes back
// only alongside a nil error.
//
// # The one method that takes no scope
//
// [Store.ResolveClientID], and its documentation says why. Every other method
// here takes a tenancy.Scope, and there is deliberately no unscoped variant of
// any of them: the caller who reaches for one is the caller who has not thought
// about tenancy.
type Store interface {
	// CreateClient records a registration and answers with the row it wrote.
	//
	// The scope is an argument even though the Client carries one, and a Client
	// whose own scope disagrees is refused with ErrScopeMismatch rather than
	// corrected — a scope read off a struct the caller assembled somewhere else
	// is exactly the derivation the column rule exists to rule out. A Client
	// naming no scope adopts the argument's, on the row that comes back and not
	// on the argument: the Client handed in is left exactly as it was.
	//
	// What comes back carries Client.SecretHash and no plaintext secret. It is a
	// read of the row, and the row has never held one — the plaintext is
	// returned once, by [Service.CreateClient], on an [IssuedClient], and there
	// is no read in this package that recovers it.
	//
	// A client_id already in use is ErrClientIDTaken rather than an overwrite.
	CreateClient(ctx context.Context, tx database.Tx, scope tenancy.Scope, client *Client) (*Client, error)

	// UpdateClient revises the four descriptive fields of one live
	// registration, stamps last_updated_at, and answers with the revised row.
	//
	// It is the method here with nothing to read a result off at all — the
	// caller holds an id and a patch — so it is the clearest case in the package
	// for returning the row, and last_updated_at is a value only the database
	// could have named.
	//
	// It cannot touch the owner, the client_id or the digest: those are
	// immutable in the schema as well as in [UpdateInput], because a row that
	// could reassign its own owner would make the owner check a formality and a
	// credential rotated by an UPDATE is one nobody was handed a new secret for.
	//
	// A registration that is absent, archived, or in another registry is
	// ErrClientNotFound.
	UpdateClient(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		id string,
		input *UpdateInput,
	) (*Client, error)

	// ArchiveClient withdraws one registration and answers with the row it
	// withdrew, ArchivedAt set.
	//
	// It is the one write here whose result no other read of this registry can
	// reach: every consumer read but [Store.ResolveClientID] filters the
	// withdrawn rows out, and that one needs a client_id rather than the row id
	// this call was given. So the name, the owner and the redirect URIs a
	// withdrawal entry has to record are available here and nowhere afterwards.
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
	ArchiveClient(ctx context.Context, tx database.Tx, scope tenancy.Scope, id string) (*Client, error)

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

	// DeleteClientsForOwner destroys every registration one person owns within
	// the scope, withdrawn ones included, and reports how many it destroyed. It
	// is what authentication/oauth2clients/privacy's dataprivacy.Eraser is built
	// on, and it takes the caller's transaction for the reason every other write
	// here does: a subject's credentials going with the rest of their footprint,
	// or not at all, is the whole of what an erasure means.
	//
	// Zero is not an error. An erasure runs against whatever the subject
	// actually left behind, and a person who registered nothing is a person with
	// nothing here to reach.
	//
	// It is the one hard delete in this store, and [Store.ArchiveClient] is what
	// it is not. Withdrawing keeps the row so the authorization server can refuse
	// the tokens a live client_id names — and it keeps belongs_to_user, Name and
	// Description with it, which between them are the whole of what this table
	// says about a person. An erasure that archived would be an erasure that
	// erased nothing, so this reaches past the write that was already there.
	//
	// Deleting does not weaken the refusal it takes away. A token naming a
	// client_id no row resolves is refused by the absence, which is stricter than
	// the withdrawn row's refusal rather than looser; what is lost is the ability
	// to tell a withdrawn client from one that never existed, and for a subject
	// who asked to be forgotten "never existed" is the answer they asked for.
	//
	// It does not reach a registration nobody owns. belongs_to_user is the empty
	// string on one the deployment or the tenant administers on its own behalf,
	// so an empty userID is ErrEmptyUserID here for the reason it is on
	// [Store.ListClientsForOwner], and a sharper one: an erasure that quietly
	// answered with the rows nobody owns would destroy the deployment's own
	// credentials on behalf of a subject who never had any.
	DeleteClientsForOwner(ctx context.Context, tx database.Tx, scope tenancy.Scope, userID string) (int64, error)
}
