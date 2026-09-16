/*
Package privacy is the reset token table's contribution to a subject access
request: a dataprivacy.Collector that returns when somebody asked to reset their
password, and a dataprivacy.Eraser that destroys the record.

# Why this is a package rather than two methods on the store

passwordreset would otherwise import dataprivacy, which imports operations, which
imports the queue and the scheduler — so a service with a "forgot your password"
link and no privacy pipeline would compile all of it. The seam goes here for the
same reason dataprivacy/auditerasure exists, and it costs one constructor
argument.

# What the export is, and what it cannot be

A row here is four facts about a person: that they asked for a reset, when, when
the link stopped working, and whether they used it. That is what
[passwordreset.Token] carries and what the fragment holds.

It is not a credential. The secret exists once, in memory, on its way to an
email; the table holds a digest of it that nothing reverses, and no statement in
the package projects that column — so there is no field on Token for it to arrive
in. An export here cannot leak a link even in principle, which is worth saying
because a package named after password resets is one somebody will reasonably
check twice.

# Why the erasure deletes, and why the revocation is not what it calls

[passwordreset.Store.RevokeForUser] already destroys a principal's tokens, and it
is deliberately not the write this uses. A revocation spares redeemed rows,
because a spent link keeps answering "this link has already been used" for as
long as it would have kept working — which is the most common reason a reset
fails and the one worth telling somebody apart from "no such link".

An erasure is the case where that answer is not owed. The person it would be
answered about has asked to be forgotten, and a redeemed row is the record of a
reset they actually completed, sitting under their identifier until the sweeper
reaches it. So the store grew [passwordreset.Store.DeleteForUser] and this calls
that.

Waiting for the sweeper is not an answer either. It removes rows at their own
expiry, which for a live token is hours away — and an erasure that quietly meant
"in a little while" would be one whose completion date is a TTL nobody told the
subject about.

Nothing is retained, and so nothing is reported as retained. A deployment whose
jurisdiction requires a record that a reset was requested registers no eraser.

# Scopes

Every consumer read and write in passwordreset is scoped, and a subject access
request may arrive without a scope — the request's confinement names nobody for
the plain "give me my data". So both halves take a [ScopeResolver]: the mapping
from a subject to the scopes their tokens may be in, which is a question about
the consumer's tenancy model rather than about this table.

It is a constructor argument rather than an option with a default, because there
is no default that is right twice. A deployment with one tenant wants
[FixedScopes] over tenancy.Global(); one whose requests always name their scope
wants [RequestScope]; one that resolves a person to their accounts has to ask its
own directory. A resolver that silently answered "the global scope" for the third
of those would export nothing and erase nothing, and report success for both.

# Subjects are principals

passwordreset keys on a principal — a user id in every deployment this module
has, but a string there, because the package never reads a user table — so a
dataprivacy.Subject's id is the principal and there is no vocabulary to bridge. A
subject whose type is not a person is resolved by the same rule: whatever id the
request carries is what a token would have been filed under, and one that matches
nothing collects and erases nothing.

# The collector's read is not a page

[passwordreset.Store.ListForUser] answers with a slice rather than a
filtering.QueryFilteredResult, so this is the one collector in the module that
does not go through dataprivacy.CollectAll. The bound is the sweeper: every row
is deleted at its own expiry, so what one principal holds is bounded by the token
lifetime rather than by their history. identity/privacy takes the same shape over
identity.Store.ListMembershipsForUser for the same kind of reason.

What CollectAll is for is still respected — a collector that reads one page and
stops returns a truncated subject access request — and the way it is respected
here is that there are no pages to stop after.

# Executors

Every read and write in passwordreset runs on an executor its caller supplies,
and the two halves get theirs from different places. dataprivacy.Eraser.Erase is
handed the request's database.Tx, so [Eraser] passes that straight down and the
tokens go with the rest of the subject's footprint.
dataprivacy.Collector.Collect is handed nothing, because an export is a read and
there is no transaction for it to be part of — so [NewCollector] takes the
executor once, at construction, and every collection runs on it.

That is a database.SQLQueryExecutor rather than a database.Client: a collector
reads and does nothing else, and the narrower type is also the one that lets a
consumer hand it a Tx where an export genuinely has to see a transaction's own
writes. It is worth noting which executor a consumer passes here, for the reason
[passwordreset.NewSQLStore] gives about Verify: these rows are written and read
seconds apart, and a replica is where a just-issued token is not.

# Observability

Neither half instruments anything of its own. Everything they do is a call into
the store, which spans and logs every read and write it makes, and a second span
around a loop of those would name the same work twice.
*/
package privacy

import (
	"context"
	"encoding/json"

	"github.com/primandproper/platform-go/v14/authentication/passwordreset"
	"github.com/primandproper/platform-go/v14/dataprivacy"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// DefaultKey is the registry key these are normally registered under. It names
// the section an export's artifact carries them in, and the prefix of any
// entries an outcome reports.
const DefaultKey = "password_reset"

// The sentinels this package returns.
var (
	// ErrNilStore indicates a nil passwordreset.Store.
	ErrNilStore = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil password reset token store")

	// ErrNilScopeResolver indicates a nil ScopeResolver. It is required, and
	// refusing it here is what stops an export that quietly covers no scope from
	// being discovered by the subject.
	ErrNilScopeResolver = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil password reset privacy scope resolver")

	// ErrNilExecutor indicates a nil executor. Both halves run on one somebody
	// else supplies — the collector's at construction, the eraser's per request —
	// because this package keeps no connection of its own to fall back to.
	ErrNilExecutor = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil password reset privacy query executor")

	// ErrUnscopedRequest indicates a request that names no scope, handed to
	// [RequestScope], which has nowhere else to get one.
	ErrUnscopedRequest = platformerrors.New("password reset request names no scope")
)

// ScopeResolver names the scopes a subject's reset tokens may be in.
//
// Returning no scopes is legitimate and means the subject has nothing here: the
// collector reports the domain as holding nothing, and the eraser destroys
// nothing. Returning too many is how one subject's erasure reaches another
// tenant's rows, so it is worth being exact.
//
// requestScope is the confinement the privacy request named, which the fulfiller
// hands over beside the subject. The zero Scope is the request that named none —
// a plain "give me my data" — and what a resolver makes of that is the whole of
// the decision this seam exists for.
type ScopeResolver func(
	ctx context.Context,
	requestScope tenancy.Scope,
	subject dataprivacy.Subject,
) ([]tenancy.Scope, error)

// RequestScope resolves the scope the request itself names, for a deployment
// where a privacy request always arrives scoped.
//
// A request that names none is ErrUnscopedRequest rather than the global scope.
// The confinement arrives as a tenancy.Scope, so "confined to nobody" and "the
// global scope" are already distinct values here and nothing has to reconstruct
// the difference; what a resolver still cannot do is invent the scope a request
// declined to name. The difference is not recoverable later: an export that
// quietly covered only the global scope would be well-formed, would have a
// section, and would be missing every reset the subject ever asked for.
func RequestScope(
	_ context.Context,
	requestScope tenancy.Scope,
	subject dataprivacy.Subject,
) ([]tenancy.Scope, error) {
	if requestScope.Validate() != nil {
		return nil, platformerrors.Wrapf(ErrUnscopedRequest, "subject %q", subject.ID)
	}

	return []tenancy.Scope{requestScope}, nil
}

// FixedScopes resolves every subject to the same scopes, for a deployment whose
// tenancy is fixed — most often the single-tenant one, as
// FixedScopes(tenancy.Global()).
func FixedScopes(scopes ...tenancy.Scope) ScopeResolver {
	fixed := make([]tenancy.Scope, len(scopes))
	copy(fixed, scopes)

	return func(context.Context, tenancy.Scope, dataprivacy.Subject) ([]tenancy.Scope, error) {
		return fixed, nil
	}
}

// Collector returns the resets a subject asked for.
type Collector struct {
	store   passwordreset.Store
	reader  database.SQLQueryExecutor
	resolve ScopeResolver
}

var _ dataprivacy.Collector = (*Collector)(nil)

// NewCollector builds the collector over a store, the executor its reads run on,
// and a scope resolver. All three are required.
//
// The executor is a constructor argument because dataprivacy.Collector.Collect
// has nowhere to put one: an export is a read, and the seam that asks for it
// hands over a subject and nothing else.
func NewCollector(
	store passwordreset.Store,
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
// It reads each scope's tokens in one call, because the store's read is unpaged
// — see the package documentation for the bound that makes that the right shape
// here rather than a shortcut past dataprivacy.CollectAll.
//
// Redeemed and expired rows come back with the rest. An export says what the
// table holds rather than what is still usable, and a reset the subject
// completed is exactly the kind of thing the right of access is about.
func (c *Collector) Collect(
	ctx context.Context,
	requestScope tenancy.Scope,
	subject dataprivacy.Subject,
) (json.RawMessage, error) {
	scopes, err := c.resolve(ctx, requestScope, subject)
	if err != nil {
		return nil, platformerrors.Wrap(err, "resolving password reset scopes for subject")
	}

	var asked []passwordreset.Token

	for _, scope := range scopes {
		held, collectErr := c.store.ListForUser(ctx, c.reader, scope, subject.ID)
		if collectErr != nil {
			return nil, platformerrors.Wrapf(collectErr, "collecting password reset tokens in scope %q", scope)
		}

		for _, token := range held {
			if token == nil {
				continue
			}

			asked = append(asked, *token)
		}
	}

	return dataprivacy.Fragment(len(asked) > 0, asked)
}

// Eraser destroys the record of the resets a subject asked for.
type Eraser struct {
	store   passwordreset.Store
	resolve ScopeResolver
}

var _ dataprivacy.Eraser = (*Eraser)(nil)

// NewEraser builds the eraser over a store and a scope resolver. Both are
// required.
func NewEraser(store passwordreset.Store, resolve ScopeResolver) (*Eraser, error) {
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
// tokens and the rest of the subject's footprint commit or roll back together. A
// subject who never asked for a reset erases nothing and is not an error.
//
// It calls [passwordreset.Store.DeleteForUser] and not
// [passwordreset.Store.RevokeForUser]; the package documentation says why.
func (e *Eraser) Erase(
	ctx context.Context,
	tx database.Tx,
	requestScope tenancy.Scope,
	subject dataprivacy.Subject,
) (dataprivacy.ErasureOutcome, error) {
	if tx == nil {
		return dataprivacy.ErasureOutcome{}, ErrNilExecutor
	}

	scopes, err := e.resolve(ctx, requestScope, subject)
	if err != nil {
		return dataprivacy.ErasureOutcome{},
			platformerrors.Wrap(err, "resolving password reset scopes for subject")
	}

	var outcome dataprivacy.ErasureOutcome

	for _, scope := range scopes {
		deleted, deleteErr := e.store.DeleteForUser(ctx, tx, scope, subject.ID)
		if deleteErr != nil {
			return dataprivacy.ErasureOutcome{},
				platformerrors.Wrapf(deleteErr, "erasing password reset tokens in scope %q", scope)
		}

		outcome.Deleted += deleted
	}

	return outcome, nil
}
