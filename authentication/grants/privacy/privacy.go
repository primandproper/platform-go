/*
Package privacy is the third-party grant table's contribution to a subject
access request: a dataprivacy.Collector that returns the accounts somebody
connected, and a dataprivacy.Eraser that destroys the grants.

# Why this is a package rather than two methods on the store

grants would otherwise import dataprivacy, which imports operations, the queue
and the scheduler — so a service that connects a calendar and runs no privacy
pipeline would compile all of it. The seam goes here for the reason every other
<pkg>/privacy in this module exists.

# What the export is, and what it is not

A grant is personal data twice over: it names an account at somebody else's
service, and it belongs to a subject who is a person or something a person owns.
The export says so — which provider, which account there, which scopes were
granted, when the connection was made, last refreshed, and revoked, and by whom.
That is [Export], and it is a shape of its own rather than grants.Grant.

It carries no token and cannot. The store's read beneath it never selects either
token column, and [Export] has no field one could be copied into. A subject
access request is owed the fact of a connection; the credential inside it is a
standing key to an account, and an export is a file that gets emailed.

# The export includes revoked grants

It reads [grants.Store.ListAllForSubject], which returns revoked grants beside
live ones. A connection somebody made and later ended is still something this
deployment recorded about them.

# Why the erasure deletes, and what it does not do

[grants.Store.Revoke] keeps the row so "this account was connected and on this
date it stopped being" stays answerable — which is exactly the sentence a
forgotten subject has asked nobody to be able to write. So this calls
[grants.Store.DeleteForSubject], revoked rows included, inside the erasure's
transaction, and nothing is retained.

It tells no provider. A grant deleted here is one this deployment can no longer
use, but the provider still honors the refresh token until it is revoked there.
A consumer that wants the grants revoked at the source registers a BeforeErase
step through privacyadapters that reads each grant and calls the provider's
revocation endpoint — before this eraser deletes the tokens it would need.

# Scopes

Every read and write in grants is scoped, and a subject access request may arrive
naming no scope, so both halves take a [ScopeResolver]. It is a constructor
argument with no default, because there is no default that is right twice.

# Executors

[Eraser] is handed the request's database.Tx and passes it straight down, so the
grants go with the rest of the subject's footprint. [NewCollector] takes a
database.SQLQueryExecutor once, at construction, because
dataprivacy.Collector.Collect has nowhere to put one.
*/
package privacy

import (
	"context"
	"encoding/json"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/grants"
	"github.com/primandproper/platform-go/v14/dataprivacy"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// DefaultKey is the registry key these are normally registered under. It names
// the section an export's artifact carries them in.
const DefaultKey = "oauth2_grants"

// heldNoun names what this domain holds, for the message dataprivacy's fan-out
// helper wraps a failed write in.
const heldNoun = "third-party grants"

// The sentinels this package returns.
var (
	// ErrNilStore indicates a nil grants.Store.
	ErrNilStore = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil grant store for the privacy adapter")

	// ErrNilScopeResolver indicates a nil ScopeResolver.
	ErrNilScopeResolver = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil grant privacy scope resolver")

	// ErrNilExecutor indicates a nil executor.
	ErrNilExecutor = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil grant privacy query executor")

	// ErrUnscopedRequest indicates a request that names no scope, handed to
	// [RequestScope], which has nowhere else to get one.
	ErrUnscopedRequest = platformerrors.New("grant privacy request names no scope")
)

// ScopeResolver names the scopes a subject's grants may be in. It is
// [dataprivacy.ScopeResolver] under this package's name, aliased so one
// resolver function serves every adapter without a conversion.
type ScopeResolver = dataprivacy.ScopeResolver

// RequestScope resolves the scope the request itself names. A request that
// names none is [ErrUnscopedRequest] rather than the global scope.
var RequestScope = dataprivacy.RequestScopeOr(ErrUnscopedRequest)

// FixedScopes resolves every subject to the same scopes — most often
// FixedScopes(tenancy.Global()).
var FixedScopes = dataprivacy.FixedScopes

// Export is one grant as a subject access request carries it: every fact about
// the connection, and neither token.
type Export struct {
	CreatedAt            time.Time               `json:"createdAt"`
	LastUpdatedAt        *time.Time              `json:"lastUpdatedAt,omitempty"`
	AccessTokenExpiresAt *time.Time              `json:"accessTokenExpiresAt,omitempty"`
	RevokedAt            *time.Time              `json:"revokedAt,omitempty"`
	ID                   string                  `json:"id"`
	Scope                tenancy.Scope           `json:"scope"`
	Provider             string                  `json:"provider"`
	ProviderAccountID    string                  `json:"providerAccountID,omitempty"`
	RevocationReason     grants.RevocationReason `json:"revocationReason,omitempty"`
	GrantedScopes        []string                `json:"grantedScopes,omitempty"`
}

// exportOf copies a grant's metadata, field by field, into the export shape.
// It is a copy rather than a conversion so that a token field added to
// grants.Grant later has nowhere to land here.
func exportOf(g *grants.Grant) Export {
	return Export{
		CreatedAt:            g.CreatedAt,
		LastUpdatedAt:        g.LastUpdatedAt,
		AccessTokenExpiresAt: g.AccessTokenExpiresAt,
		RevokedAt:            g.RevokedAt,
		ID:                   g.ID,
		Scope:                g.Scope,
		Provider:             g.Provider,
		ProviderAccountID:    g.ProviderAccountID,
		RevocationReason:     g.RevocationReason,
		GrantedScopes:        g.GrantedScopes,
	}
}

// Collector returns the grants a subject holds.
type Collector struct {
	store   grants.Store
	reader  database.SQLQueryExecutor
	resolve ScopeResolver
}

var _ dataprivacy.Collector = (*Collector)(nil)

// NewCollector builds the collector over a store, the executor its reads run on,
// and a scope resolver. All three are required.
func NewCollector(store grants.Store, reader database.SQLQueryExecutor, resolve ScopeResolver) (*Collector, error) {
	if store == nil {
		return nil, ErrNilStore
	}

	if reader == nil {
		return nil, ErrNilExecutor
	}

	if resolve == nil {
		return nil, ErrNilScopeResolver
	}

	return &Collector{store: store, reader: reader, resolve: resolve}, nil
}

// Collect implements dataprivacy.Collector.
//
// The store's read is unpaged — a subject holds at most one grant per provider —
// so this walks the resolver's scopes rather than a cursor.
func (c *Collector) Collect(
	ctx context.Context,
	requestScope tenancy.Scope,
	subject dataprivacy.Subject,
) (json.RawMessage, error) {
	var exported []Export

	err := dataprivacy.ForEachOwner(ctx, c.resolve, requestScope, subject,
		func(ctx context.Context, scope tenancy.Scope) error {
			held, collectErr := c.store.ListAllForSubject(ctx, c.reader, scope, subject.ID)
			if collectErr != nil {
				return platformerrors.Wrapf(collectErr, "collecting grants in scope %q", scope)
			}

			for _, grant := range held {
				if grant == nil {
					continue
				}

				exported = append(exported, exportOf(grant))
			}

			return nil
		})
	if err != nil {
		return nil, err
	}

	return dataprivacy.Fragment(len(exported) > 0, exported)
}

// Eraser destroys the grants a subject holds.
type Eraser struct {
	store   grants.Store
	resolve ScopeResolver
}

var _ dataprivacy.Eraser = (*Eraser)(nil)

// NewEraser builds the eraser over a store and a scope resolver. Both are
// required.
func NewEraser(store grants.Store, resolve ScopeResolver) (*Eraser, error) {
	if store == nil {
		return nil, ErrNilStore
	}

	if resolve == nil {
		return nil, ErrNilScopeResolver
	}

	return &Eraser{store: store, resolve: resolve}, nil
}

// Erase implements dataprivacy.Eraser, in the request's transaction. A subject
// who holds no grants erases nothing and is not an error.
func (e *Eraser) Erase(
	ctx context.Context,
	tx database.Tx,
	requestScope tenancy.Scope,
	subject dataprivacy.Subject,
) (dataprivacy.ErasureOutcome, error) {
	if tx == nil {
		return dataprivacy.ErasureOutcome{}, ErrNilExecutor
	}

	return dataprivacy.EraseByScope(ctx, tx, e.resolve, requestScope, subject, heldNoun,
		func(ctx context.Context, tx database.Tx, scope tenancy.Scope) (dataprivacy.ErasureOutcome, error) {
			deleted, deleteErr := e.store.DeleteForSubject(ctx, tx, scope, subject.ID)
			if deleteErr != nil {
				return dataprivacy.ErasureOutcome{}, deleteErr
			}

			return dataprivacy.ErasureOutcome{Deleted: deleted}, nil
		})
}
