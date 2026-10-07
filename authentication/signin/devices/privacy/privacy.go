/*
Package privacy is the sign-in device table's contribution to a subject access
request: a dataprivacy.Collector that returns every address, user agent and
device name recorded against a subject's logins, and a dataprivacy.Eraser that
deletes them.

# Why both halves

The table holds where somebody signed in from, which is personal data by any
reading, so the access half is owed. The erasure half is owed because nothing
else reaches these rows in time: the user column carries no REFERENCES — the
devices package is usable by an application whose directory is not identity's —
so an erasure that deleted the user would cascade nothing here, and the sweep
deletes a row only once its login could no longer be alive, which for a live
refresh token is its whole lifetime away. This is the recoverycodes/privacy
shape.

# Why the erasure deletes

There is nothing else it could do. A device row has no state to move it to that
would still be worth keeping, and a row kept behind is the record "this person
signed in from this address", which is exactly the sentence a forgotten subject
asked nobody to be able to write. Nothing is retained, and so nothing is
reported as retained.

Erasing the rows does not end the logins. That is signin's — a deployment that
erases a person ends their logins through it, and the next refresh of a login
that survived would record a fresh row.

# Scopes

Every read and write in devices is scoped, and a subject access request may
arrive without a scope. So both halves take a [ScopeResolver], for the reason
passwordreset/privacy gives: which scopes a person's logins may be in is a
question about the consumer's tenancy model rather than about this table, and
there is no default that is right twice.

# Subjects are users

devices keys on a user id — a string there, because the package never reads a
user table — so a dataprivacy.Subject's id is the user and there is no
vocabulary to bridge.

# The collector's read is not a page

[devices.Store.ListForUser] answers with a slice, so this collector does not go
through dataprivacy.CollectAll. The bound is the sweep: a row is one login, and
it is deleted once the login can no longer be alive.

# Executors

dataprivacy.Eraser.Erase is handed the request's database.Tx, so [Eraser] passes
that straight down and the rows go with the rest of the subject's footprint.
dataprivacy.Collector.Collect is handed nothing, so [NewCollector] takes the
executor once, at construction.
*/
package privacy

import (
	"context"
	"encoding/json"

	"github.com/primandproper/platform-go/v15/authentication/signin/devices"
	"github.com/primandproper/platform-go/v15/dataprivacy"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// DefaultKey is the registry key these are normally registered under. It names
// the section an export's artifact carries them in, and the prefix of any
// entries an outcome reports.
const DefaultKey = "signin_devices"

// heldNoun names what this domain holds, for the message dataprivacy's fan-out
// helper wraps a failed write in.
const heldNoun = "sign-in devices"

// The sentinels this package returns.
var (
	// ErrNilStore indicates a nil devices.Store.
	ErrNilStore = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil sign-in device store for the privacy adapter")

	// ErrNilScopeResolver indicates a nil ScopeResolver. It is required, and
	// refusing it here is what stops an export that quietly covers no scope from
	// being discovered by the subject.
	ErrNilScopeResolver = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil sign-in device privacy scope resolver")

	// ErrNilExecutor indicates a nil executor. Both halves run on one somebody
	// else supplies — the collector's at construction, the eraser's per request —
	// because this package keeps no connection of its own to fall back to.
	ErrNilExecutor = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil sign-in device privacy query executor")

	// ErrUnscopedRequest indicates a request that names no scope, handed to
	// [RequestScope], which has nowhere else to get one.
	ErrUnscopedRequest = platformerrors.New("sign-in device privacy request names no scope")
)

// ScopeResolver names the scopes a subject's logins may be in.
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

// Collector returns the devices recorded against a subject's logins.
type Collector struct {
	store   devices.Store
	reader  database.SQLQueryExecutor
	resolve ScopeResolver
}

var _ dataprivacy.Collector = (*Collector)(nil)

// NewCollector builds the collector over a store, the executor its reads run on,
// and a scope resolver. All three are required.
func NewCollector(
	store devices.Store,
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
func (c *Collector) Collect(
	ctx context.Context,
	requestScope tenancy.Scope,
	subject dataprivacy.Subject,
) (json.RawMessage, error) {
	var held []devices.Device

	err := dataprivacy.ForEachOwner(ctx, c.resolve, requestScope, subject,
		func(ctx context.Context, scope tenancy.Scope) error {
			recorded, collectErr := c.store.ListForUser(ctx, c.reader, scope, subject.ID)
			if collectErr != nil {
				return platformerrors.Wrapf(collectErr, "collecting sign-in devices in scope %q", scope)
			}

			for _, device := range recorded {
				if device == nil {
					continue
				}

				held = append(held, *device)
			}

			return nil
		})
	if err != nil {
		return nil, err
	}

	return dataprivacy.Fragment(len(held) > 0, held)
}

// Eraser deletes the devices recorded against a subject's logins.
type Eraser struct {
	store   devices.Store
	resolve ScopeResolver
}

var _ dataprivacy.Eraser = (*Eraser)(nil)

// NewEraser builds the eraser over a store and a scope resolver. Both are
// required.
func NewEraser(store devices.Store, resolve ScopeResolver) (*Eraser, error) {
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
// rows and the rest of the subject's footprint commit or roll back together. A
// subject who never signed in erases nothing and is not an error.
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
