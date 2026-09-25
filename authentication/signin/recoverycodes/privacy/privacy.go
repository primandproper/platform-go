/*
Package privacy is the recovery code table's contribution to a subject access
request: a dataprivacy.Collector that returns when somebody was issued a set of
recovery codes and which of them they spent, and a dataprivacy.Eraser that
destroys the set.

# Why this ships at all

magiclinks, beside this package's parent, ships no adapter: its one personal
column is a copy of an address identity holds, and a sweeper deletes it at a
link's lifetime plus a retention window whether anybody asks or not. The
recovery code table holds less than that — a user id and digests — but it has no
sweeper, because a recovery code does not lapse. A row outlives its owner until
something deletes it, so an erasure that reached identity and not here would
leave the user's id with their codes under it for as long as the table exists.
This is that something. See the recoverycodes package documentation.

# What the export is, and what it cannot be

A row here is three facts about a person: that they held a recovery code, when
the set it belonged to was issued, and whether and when they spent it. That is
what [recoverycodes.RecoveryCode] carries and what the fragment holds.

It is not a credential. A code exists once, on its way to the person it was
issued to; the table holds a digest of it, and the read this collector calls
does not project that column — so there is no field on RecoveryCode for one to
arrive in. That matters more here than for a reset token, because sixty random
bits behind a fast digest is a digest somebody could reverse, and an export is a
file that leaves the building.

# Why the erasure deletes

There is nothing else it could do. A recovery code has no revoked state to move
it to — a replacement deletes the set it replaces — and a spent row kept behind
is the record "this person lost their authenticator on this date", which is
exactly the sentence a forgotten subject asked nobody to be able to write.
Nothing is retained, and so nothing is reported as retained.

# Scopes

Every read and write in recoverycodes is scoped, and a subject access request
may arrive without a scope. So both halves take a [ScopeResolver], for the
reason passwordreset/privacy gives: which scopes a person's codes may be in is a
question about the consumer's tenancy model rather than about this table, and
there is no default that is right twice.

# Subjects are users

recoverycodes keys on a user id — a string there, because the package never
reads a user table — so a dataprivacy.Subject's id is the user and there is no
vocabulary to bridge. A subject whose type is not a person is resolved by the
same rule: whatever id the request carries is what a code would have been filed
under, and one that matches nothing collects and erases nothing.

# The collector's read is not a page

[recoverycodes.Store.ListForUser] answers with a slice, so this collector does
not go through dataprivacy.CollectAll. The bound is the replacement: it deletes
the set it replaces, so what one person holds is one set — eight rows by default
— rather than their history.

# Executors

dataprivacy.Eraser.Erase is handed the request's database.Tx, so [Eraser] passes
that straight down and the codes go with the rest of the subject's footprint.
dataprivacy.Collector.Collect is handed nothing, so [NewCollector] takes the
executor once, at construction.

# Observability

Neither half instruments anything of its own. Everything they do is a call into
the store, which spans every read and write it makes.
*/
package privacy

import (
	"context"
	"encoding/json"

	"github.com/primandproper/platform-go/v14/authentication/signin/recoverycodes"
	"github.com/primandproper/platform-go/v14/dataprivacy"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// DefaultKey is the registry key these are normally registered under. It names
// the section an export's artifact carries them in, and the prefix of any
// entries an outcome reports.
const DefaultKey = "recovery_codes"

// heldNoun names what this domain holds, for the message dataprivacy's fan-out
// helper wraps a failed write in.
const heldNoun = "recovery codes"

// The sentinels this package returns.
var (
	// ErrNilStore indicates a nil recoverycodes.Store.
	ErrNilStore = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil recovery code store for the privacy adapter")

	// ErrNilScopeResolver indicates a nil ScopeResolver. It is required, and
	// refusing it here is what stops an export that quietly covers no scope from
	// being discovered by the subject.
	ErrNilScopeResolver = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil recovery code privacy scope resolver")

	// ErrNilExecutor indicates a nil executor. Both halves run on one somebody
	// else supplies — the collector's at construction, the eraser's per request —
	// because this package keeps no connection of its own to fall back to.
	ErrNilExecutor = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil recovery code privacy query executor")

	// ErrUnscopedRequest indicates a request that names no scope, handed to
	// [RequestScope], which has nowhere else to get one.
	ErrUnscopedRequest = platformerrors.New("recovery code privacy request names no scope")
)

// ScopeResolver names the scopes a subject's recovery codes may be in.
//
// It is [dataprivacy.ScopeResolver] under this package's name, and the = is
// load-bearing: a defined type of its own would be assignable to the sibling
// adapters' only through a conversion.
type ScopeResolver = dataprivacy.ScopeResolver

// RequestScope resolves the scope the request itself names, for a deployment
// where a privacy request always arrives scoped. A request that names none is
// [ErrUnscopedRequest] rather than the global scope.
var RequestScope = dataprivacy.RequestScopeOr(ErrUnscopedRequest)

// FixedScopes resolves every subject to the same scopes, for a deployment whose
// tenancy is fixed — most often the single-tenant one, as
// FixedScopes(tenancy.Global()).
var FixedScopes = dataprivacy.FixedScopes

// Collector returns the recovery codes a subject holds, as records.
type Collector struct {
	store   recoverycodes.Store
	reader  database.SQLQueryExecutor
	resolve ScopeResolver
}

var _ dataprivacy.Collector = (*Collector)(nil)

// NewCollector builds the collector over a store, the executor its reads run on,
// and a scope resolver. All three are required.
func NewCollector(
	store recoverycodes.Store,
	reader database.SQLQueryExecutor,
	resolve ScopeResolver,
) (*Collector, error) {
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
// Spent codes come back with the rest. An export says what the table holds
// rather than what is still usable, and "you used a recovery code on this date"
// is exactly the kind of thing the right of access is about.
func (c *Collector) Collect(
	ctx context.Context,
	requestScope tenancy.Scope,
	subject dataprivacy.Subject,
) (json.RawMessage, error) {
	var held []recoverycodes.RecoveryCode

	err := dataprivacy.ForEachOwner(ctx, c.resolve, requestScope, subject,
		func(ctx context.Context, scope tenancy.Scope) error {
			codes, collectErr := c.store.ListForUser(ctx, c.reader, scope, subject.ID)
			if collectErr != nil {
				return platformerrors.Wrapf(collectErr, "collecting recovery codes in scope %q", scope)
			}

			for _, code := range codes {
				if code == nil {
					continue
				}

				held = append(held, *code)
			}

			return nil
		})
	if err != nil {
		return nil, err
	}

	return dataprivacy.Fragment(len(held) > 0, held)
}

// Eraser destroys the recovery codes a subject holds.
type Eraser struct {
	store   recoverycodes.Store
	resolve ScopeResolver
}

var _ dataprivacy.Eraser = (*Eraser)(nil)

// NewEraser builds the eraser over a store and a scope resolver. Both are
// required.
func NewEraser(store recoverycodes.Store, resolve ScopeResolver) (*Eraser, error) {
	if store == nil {
		return nil, ErrNilStore
	}

	if resolve == nil {
		return nil, ErrNilScopeResolver
	}

	return &Eraser{store: store, resolve: resolve}, nil
}

// Erase implements dataprivacy.Eraser.
//
// It runs in the request's transaction and uses the executor it is given, so the
// codes and the rest of the subject's footprint commit or roll back together. A
// subject who never held a set erases nothing and is not an error.
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
			deleted, deleteErr := e.store.DeleteForUser(ctx, tx, scope, subject.ID)
			if deleteErr != nil {
				return dataprivacy.ErasureOutcome{}, deleteErr
			}

			return dataprivacy.ErasureOutcome{Deleted: deleted}, nil
		})
}
