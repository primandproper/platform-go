/*
Package privacy is the texted-code table's contribution to a subject access
request: a dataprivacy.Collector that returns the numbers a person was texted a
code at, and a dataprivacy.Eraser that destroys the rows.

# Why this is a package rather than two methods on the store

phonecodes would otherwise import dataprivacy, which imports operations, the
queue and the scheduler — so a service that texts codes and runs no privacy
pipeline would compile all of it. The seam goes here for the reason every other
<pkg>/privacy in this module exists.

# What the export is, and what it is not

A row is personal data because of the phone number on it. The export says which
number, when a code was sent to it, until when it was good, whether and when it
was used or withdrawn, and how many wrong guesses were made against it. That is
[Export], a shape of its own rather than phonecodes.Code.

It carries no code and cannot. No read the store makes projects the digest, and
the plaintext was never stored.

# Why the erasure deletes

A spent or withdrawn row is kept until the sweeper's retention window passes,
so that an operator can still read what happened to it. That is exactly the
record a forgotten subject asked nobody to keep, so this calls
phonecodes.Store.DeleteForSubject, inside the erasure's transaction, and
nothing is retained.

# Scopes

Every read and write in phonecodes is scoped, and a subject access request may
arrive naming no scope, so both halves take a [ScopeResolver]. It is a
constructor argument with no default, because there is no default that is right
twice.

# Executors

[Eraser] is handed the request's database.Tx and passes it straight down, so the
rows go with the rest of the subject's footprint. [NewCollector] takes a
database.SQLQueryExecutor once, at construction, because
dataprivacy.Collector.Collect has nowhere to put one. The store's read is
unpaged, and the bound is structural: one row per number a person was texted
at.
*/
package privacy

import (
	"context"
	"encoding/json"
	"time"

	"github.com/primandproper/platform-go/v15/authentication/phonecodes"
	"github.com/primandproper/platform-go/v15/dataprivacy"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// DefaultKey is the registry key these are normally registered under. It names
// the section an export's artifact carries them in.
const DefaultKey = "phone_codes"

// heldNoun names what this domain holds, for the message dataprivacy's fan-out
// helper wraps a failed write in.
const heldNoun = "texted codes"

// The sentinels this package returns.
var (
	// ErrNilStore indicates a nil phonecodes.Store.
	ErrNilStore = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil phone code store for the privacy adapter")

	// ErrNilScopeResolver indicates a nil ScopeResolver.
	ErrNilScopeResolver = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil phone code privacy scope resolver")

	// ErrNilExecutor indicates a nil executor.
	ErrNilExecutor = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil phone code privacy query executor")

	// ErrUnscopedRequest indicates a request that names no scope, handed to
	// [RequestScope], which has nowhere else to get one.
	ErrUnscopedRequest = platformerrors.New("phone code privacy request names no scope")
)

// ScopeResolver names the scopes a subject's codes may be in. It is
// [dataprivacy.ScopeResolver] under this package's name, aliased so one
// resolver function serves every adapter without a conversion.
type ScopeResolver = dataprivacy.ScopeResolver

// RequestScope resolves the scope the request itself names. A request that
// names none is [ErrUnscopedRequest] rather than the global scope.
var RequestScope = dataprivacy.RequestScopeOr(ErrUnscopedRequest)

// FixedScopes resolves every subject to the same scopes — most often
// FixedScopes(tenancy.Global()).
var FixedScopes = dataprivacy.FixedScopes

// Export is one texted code as a subject access request carries it: every fact
// about it, and not the code.
type Export struct {
	IssuedAt    time.Time     `json:"issuedAt"`
	ExpiresAt   time.Time     `json:"expiresAt"`
	RedeemedAt  *time.Time    `json:"redeemedAt,omitempty"`
	RevokedAt   *time.Time    `json:"revokedAt,omitempty"`
	ID          string        `json:"id"`
	PhoneNumber string        `json:"phoneNumber"`
	Scope       tenancy.Scope `json:"scope"`
	Attempts    int           `json:"attempts"`
	MaxAttempts int           `json:"maxAttempts"`
}

// exportOf copies a code's facts, field by field, into the export shape. It is
// a copy rather than a conversion so that a field added to phonecodes.Code
// later has to be chosen to land here.
func exportOf(c *phonecodes.Code) Export {
	return Export{
		IssuedAt:    c.IssuedAt,
		ExpiresAt:   c.ExpiresAt,
		RedeemedAt:  c.RedeemedAt,
		RevokedAt:   c.RevokedAt,
		ID:          c.ID,
		Scope:       c.Scope,
		PhoneNumber: c.PhoneNumber,
		Attempts:    c.Attempts,
		MaxAttempts: c.MaxAttempts,
	}
}

// Collector returns the codes a subject was texted.
type Collector struct {
	store   phonecodes.Store
	reader  database.SQLQueryExecutor
	resolve ScopeResolver
}

var _ dataprivacy.Collector = (*Collector)(nil)

// NewCollector builds the collector over a store, the executor its reads run on,
// and a scope resolver. All three are required.
func NewCollector(store phonecodes.Store, reader database.SQLQueryExecutor, resolve ScopeResolver) (*Collector, error) {
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
	var exported []Export

	err := dataprivacy.ForEachOwner(ctx, c.resolve, requestScope, subject,
		func(ctx context.Context, scope tenancy.Scope) error {
			held, collectErr := c.store.ListForSubject(ctx, c.reader, scope, subject.ID)
			if collectErr != nil {
				return platformerrors.Wrapf(collectErr, "collecting texted codes in scope %q", scope)
			}

			for _, code := range held {
				if code == nil {
					continue
				}

				exported = append(exported, exportOf(code))
			}

			return nil
		})
	if err != nil {
		return nil, err
	}

	return dataprivacy.Fragment(len(exported) > 0, exported)
}

// Eraser destroys the codes a subject was texted.
type Eraser struct {
	store   phonecodes.Store
	resolve ScopeResolver
}

var _ dataprivacy.Eraser = (*Eraser)(nil)

// NewEraser builds the eraser over a store and a scope resolver. Both are
// required.
func NewEraser(store phonecodes.Store, resolve ScopeResolver) (*Eraser, error) {
	if store == nil {
		return nil, ErrNilStore
	}

	if resolve == nil {
		return nil, ErrNilScopeResolver
	}

	return &Eraser{store: store, resolve: resolve}, nil
}

// Erase implements dataprivacy.Eraser, in the request's transaction. A subject
// who holds no codes erases nothing and is not an error.
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
