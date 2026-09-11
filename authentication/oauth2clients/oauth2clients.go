package oauth2clients

import (
	"slices"
	"time"

	"github.com/primandproper/primitives-go/v2/tenancy"
)

// serviceName scopes this package's spans, loggers and instruments.
const serviceName = "oauth2clients"

// Client is one registered OAuth2 client.
//
// It is a different noun from oauth2server.Client, which is what that package's
// /register endpoint writes: an anonymous RFC 7591 registration, bounded by an
// expiry, never listed and answerable to nobody. This one is administered — it
// has a name somebody chose, a listing surface, a permission in front of every
// write, and an archival lifecycle. The two live in two tables for that reason.
//
// # Who it belongs to
//
// Two fields, and between them they express both arrangements this package
// supports without a mode flag anywhere.
//
// Scope is which registry the row is in. tenancy.Global() is the registry the
// deployment itself keeps, which is what a single-tenant application uses for
// everything and what a multi-tenant one uses for the clients that are nobody's
// in particular.
//
// BelongsToUser is the person who owns the credential, and the empty string is
// a registration administered on behalf of no person.
//
// So an infrastructural client — an operator minted it, and it governs how an
// application speaks to the service for any user — is Global with no owner. A
// personal API credential is a scope and an owner. A tenant's own service
// integration is a scope and no owner. Each field that is filled in adds a
// predicate to [Client.Admits] and none of them removes one, which is why there
// is no setting here that turns a check off.
type Client struct {
	// CreatedAt is when the registration was accepted, assigned by the database
	// so that it cannot disagree with the id the cursor walk orders by.
	CreatedAt time.Time

	// LastUpdatedAt is when the registration was last revised, and nil for one
	// nobody has touched since it was created.
	LastUpdatedAt *time.Time

	// ArchivedAt is when the registration was withdrawn, and nil while it is
	// live. Withdrawn rather than deleted: a client_id names tokens that may
	// still be live, and the row is what the authorization server reads to
	// refuse them.
	ArchivedAt *time.Time

	// Scope is which registry this row is in. See the type documentation.
	Scope tenancy.Scope

	// BelongsToUser is the person who owns this credential, or the empty string
	// for one nobody owns.
	BelongsToUser string

	// ID is the row. It is what audit entries, outbox rows and console URLs
	// name, and it is not what the client sends — see ClientID.
	ID string

	// ClientID is the identifier the client sends at /authorize and /token.
	//
	// It is a second identifier rather than the primary key so that rotating it
	// later does not orphan every reference to the row. It is minted here from
	// crypto/rand and is globally unique.
	ClientID string

	// SecretHash is the SHA-256 digest of the client secret, hex-encoded,
	// produced by oauth2server.Hash — the function the authorization server
	// compares against, so there is one home for the encoding rather than two
	// that can drift.
	//
	// The plaintext is returned to its creator exactly once, by
	// [Service.CreateClient], and is never stored. Nothing reads it back,
	// because there is nothing to read.
	SecretHash string

	// Name is shown on the consent form, and Description is for whoever
	// administers the registry. Both are free text somebody typed — render
	// them, never trust them.
	Name        string
	Description string

	// RedirectURIs are the exact addresses this client may receive an
	// authorization code at, matched byte for byte as OAuth 2.1 requires.
	RedirectURIs []string

	// Scopes are the scopes this client may request. The authorization server
	// rejects a request for anything outside this set rather than silently
	// narrowing it, because narrowing hands back a token that looks like the one
	// that was asked for and is not.
	Scopes []string
}

// Administered reports whether this registration belongs to no person.
//
// It exists so that callers ask the question rather than comparing
// BelongsToUser to the empty string, which is a comparison that reads as a
// missing-value check and is not one: the empty string is a value here, and it
// names the arrangement with the wider reach.
func (c *Client) Administered() bool { return c.BelongsToUser == "" }

// Archived reports whether this registration has been withdrawn.
func (c *Client) Archived() bool { return c.ArchivedAt != nil }

// Clone returns a deep copy, so a caller handed one from a hook cannot mutate
// the value the store is still holding.
func (c *Client) Clone() *Client {
	if c == nil {
		return nil
	}

	clone := *c
	clone.RedirectURIs = slices.Clone(c.RedirectURIs)
	clone.Scopes = slices.Clone(c.Scopes)

	if c.LastUpdatedAt != nil {
		updated := *c.LastUpdatedAt
		clone.LastUpdatedAt = &updated
	}

	if c.ArchivedAt != nil {
		archived := *c.ArchivedAt
		clone.ArchivedAt = &archived
	}

	return &clone
}

// IssuedClient is a registration and the secret it was issued with.
//
// It is a separate type, producible only by [Service.CreateClient], rather than
// a Secret field on [Client] that is populated once and empty on every other
// read. A field like that is the field that ends up in a log line, in a JSON
// response, or in a hook's audit entry, because nothing about its declaration
// says it is usually absent. A distinct type says it: the secret exists on the
// one value the creation path returns, and every other path in this package
// deals in *Client and could not carry it if it wanted to.
type IssuedClient struct {
	// Client is the registration as stored.
	Client *Client

	// Secret is the plaintext client secret, in the only place it ever appears.
	// It is not recoverable: what the row holds is Client.SecretHash, and there
	// is no read that reverses it.
	Secret string
}

// CreationInput is what a caller supplies to register a client. Everything else
// about the row — the two identifiers, the digest, the timestamps, the scope and
// the owner — is decided by the service or the database.
type CreationInput struct {
	// Name is shown on the consent form. Required.
	Name string

	// Description is for whoever administers the registry. Optional.
	Description string

	// RedirectURIs are the addresses this client may receive a code at. At
	// least one is required; see ErrNoRedirectURIs for why.
	RedirectURIs []string

	// Scopes are the scopes this client may request. Optional — a client that
	// names none may request none, which is the correct default for a
	// registration whose author had no scope in mind.
	Scopes []string
}

// UpdateInput is what a caller may revise about a registration.
//
// It is the four descriptive fields and nothing else. The owner, the client_id
// and the digest are immutable in the schema as well as here: a row that could
// reassign its own owner would make the owner check a formality, and a
// credential rotated by an UPDATE is one nobody was handed a new secret for.
type UpdateInput struct {
	Name         string
	Description  string
	RedirectURIs []string
	Scopes       []string
}
