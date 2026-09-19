/*
Package privacy is the registered-passkey table's contribution to a subject
access request: a dataprivacy.Collector that returns the passkeys somebody
enrolled, and a dataprivacy.Eraser that destroys them.

# Why this is a package rather than two methods on the store

passkeys would otherwise import dataprivacy, which imports operations, which
imports the queue and the scheduler — so a service with a passkey login and no
privacy pipeline would compile all of it. The seam goes here for the same reason
authentication/passwordreset/privacy and dataprivacy/auditerasure exist, and it
costs one constructor argument.

# What the export is

A row here is what a deployment knows about one of somebody's authenticators:
that they enrolled it, what they called it, how it connects, when it was
registered, when it last signed them in, and whether it has been revoked. That
is what [passkeys.Credential] carries and what the fragment holds.

The private key is not in it and never was. A passkey's secret half is generated
inside the authenticator and never leaves it, which is the whole point of the
ceremony — what this table stores is the public key an assertion is checked
against, and the credential ID every login sends in the clear. So this export
cannot leak a credential even in principle, which is worth saying plainly about
a package whose subject is somebody's login.

# The export includes the passkeys they revoked

It reads [passkeys.Store.ListAllCredentialsForUser] rather than the read a
ceremony uses, and the difference is the archived rows. A revoked passkey is
still a thing this deployment recorded about the person — they enrolled an
authenticator, gave it a name, and took it off their account on a date — and
nothing in that package ever removes the row, so an export built on the live read
would be an export whose completeness depended on what the subject had got around
to revoking.

# Why the erasure deletes, and why it is not a revocation

[passkeys.Store.ArchiveCredentialForUser] already takes a passkey off somebody's
account, and it is deliberately not the write this uses. A revocation keeps the
row, because the row is what answers "this person had an authenticator here and
removed it on this date" — a question a security review asks, and one the archive
exists to leave answerable.

An erasure is the case where that answer is not owed, because the person it would
be answered about has asked not to be described. So this calls
[passkeys.Store.DeleteCredentialsForUser], which takes the revoked rows as well
as the live ones. Reaching for the archive here would leave the name somebody
gave their security key sitting under their identifier for the life of the
deployment: nothing in passkeys expires a credential, so there is no sweeper to
wait for and no retention this could be measured against.

Nothing is retained, and so nothing is reported as retained. A deployment whose
jurisdiction requires a record that an authenticator was enrolled registers no
eraser.

# It takes a login away

An erasure that runs while the subject can still sign in removes the way they
sign in. That is the intended reading rather than a hazard to guard against — a
subject who has been forgotten has no account to log in to — but it is worth
stating, because a deployment that reaches for the privacy pipeline to implement
"remove my passkeys" has reached for the wrong thing and wants the archive.

# Scopes

Every read and write in passkeys is scoped, and a subject access request may
arrive without a scope — the request's confinement names nobody for the plain
"give me my data". So both halves take a [ScopeResolver]: the mapping from a
subject to the scopes their passkeys may be in, which is a question about the
consumer's tenancy model rather than about this table.

It is a constructor argument rather than an option with a default, because there
is no default that is right twice. A deployment with one tenant wants
[FixedScopes] over tenancy.Global(); one whose requests always name their scope
wants [RequestScope]; one that resolves a person to their accounts has to ask its
own directory.

The scope matters more here than the fan-out usually implies: one authenticator
is commonly enrolled in several tenants, since a person carries one security key
and belongs to whatever they belong to. A resolver that named one scope would
export and erase that tenant's row and leave the others, which is the same
authenticator described somewhere the subject was told it no longer is.

# Subjects are the consumer's users, not WebAuthn handles

A [dataprivacy.Subject]'s id is what passkeys stores in
[passkeys.Credential.BelongsToUser] — the consumer's own user id. It is
deliberately not the WebAuthn user handle, which is the opaque value an
authenticator returns at login: resolving a handle to a user is the consumer's
directory's job, the reason passkeys takes a resolver at all, and a privacy
request arrives naming a person rather than a ceremony. A subject whose id
matches no row exports nothing and erases nothing, which is the ordinary answer
rather than an error.

# The collector's read is not a page

[passkeys.Store.ListAllCredentialsForUser] answers with a slice rather than a
filtering.QueryFilteredResult, so this is a collector that does not go through
dataprivacy.CollectAll, as authentication/passwordreset/privacy and
identity/privacy already are. The bound is the platform rather than a cursor: an
authenticator is a physical thing, and the set is every one a person has ever
enrolled — the handful they carry, plus the ones they have replaced.

What CollectAll is for is still respected. A collector that reads one page and
stops returns a truncated subject access request, and the way that is respected
here is that there are no pages to stop after.

# Executors

dataprivacy.Eraser.Erase is handed the request's database.Tx, so [Eraser] passes
that straight down and the passkeys go with the rest of the subject's footprint —
in particular with the user row, so there is no committed state in which the
account is gone and the credentials that opened it are not.

dataprivacy.Collector.Collect is handed nothing, because an export is a read and
there is no transaction for it to be part of. So [NewCollector] takes the
executor once, at construction, and every collection runs on it. That is a
database.SQLQueryExecutor rather than a database.Client: a collector reads and
does nothing else, and the narrower type is also the one that lets a consumer
hand it a Tx where an export genuinely has to see a transaction's own writes.

# Observability

Neither half instruments anything of its own. Everything they do is a call into
the store, which spans and logs every read and write it makes, and a second span
around a loop of those would name the same work twice.
*/
package privacy

import (
	"context"
	"encoding/json"

	"github.com/primandproper/platform-go/v14/authentication/passkeys"
	"github.com/primandproper/platform-go/v14/dataprivacy"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// DefaultKey is the registry key these are normally registered under. It names
// the section an export's artifact carries them in, and the prefix of any
// entries an outcome reports.
const DefaultKey = "passkeys"

// heldNoun names what this domain holds, for the message dataprivacy's fan-out
// helper wraps a failed write in. The registry key names the section; this names
// the table, which is what somebody reading the log wants.
const heldNoun = "passkeys"

// The sentinels this package returns.
var (
	// ErrNilStore indicates a nil passkeys.Store.
	ErrNilStore = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil passkey store for the privacy adapter")

	// ErrNilScopeResolver indicates a nil ScopeResolver. It is required, and
	// refusing it here is what stops an export that quietly covers no scope from
	// being discovered by the subject.
	ErrNilScopeResolver = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil passkey privacy scope resolver")

	// ErrNilExecutor indicates a nil executor. Both halves run on one somebody
	// else supplies — the collector's at construction, the eraser's per request
	// — because this package keeps no connection of its own to fall back to.
	ErrNilExecutor = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil passkey privacy query executor")

	// ErrUnscopedRequest indicates a request that names no scope, handed to
	// [RequestScope], which has nowhere else to get one.
	ErrUnscopedRequest = platformerrors.New("passkey privacy request names no scope")
)

// ScopeResolver names the scopes a subject's passkeys may be in.
//
// It is [dataprivacy.ScopeResolver] under this package's name, and the = is
// load-bearing rather than cosmetic: a defined type of its own would be
// assignable to the identical defined type in the sibling adapters only through
// a conversion, so a deployment with one resolver function would write one
// conversion per domain. See dataprivacy.ScopeResolver for what a resolver
// answers, what returning none means, and why it has no default.
type ScopeResolver = dataprivacy.ScopeResolver

// RequestScope resolves the scope the request itself names, for a deployment
// where a privacy request always arrives scoped.
//
// A request that names none is [ErrUnscopedRequest] rather than the global
// scope — see dataprivacy.RequestScopeOr, which this is built from, for why the
// difference is not recoverable later.
var RequestScope = dataprivacy.RequestScopeOr(ErrUnscopedRequest)

// FixedScopes resolves every subject to the same scopes, for a deployment whose
// tenancy is fixed — most often the single-tenant one, as
// FixedScopes(tenancy.Global()).
var FixedScopes = dataprivacy.FixedScopes

// Collector returns the passkeys a subject registered.
type Collector struct {
	store   passkeys.Store
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
	store passkeys.Store,
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
// It reads each scope's passkeys in one call, because the store's read is
// unpaged — see the package documentation for the bound that makes that the
// right shape here rather than a shortcut past dataprivacy.CollectAll.
//
// Revoked passkeys come back with the rest, through
// [passkeys.Store.ListAllCredentialsForUser]. An export says what the table
// holds rather than what still works, and an authenticator somebody enrolled and
// later removed is exactly the kind of thing the right of access is about.
func (c *Collector) Collect(
	ctx context.Context,
	requestScope tenancy.Scope,
	subject dataprivacy.Subject,
) (json.RawMessage, error) {
	var enrolled []passkeys.Credential

	// dataprivacy.ForEachOwner rather than CollectByScope, because this read is
	// not paged: it returns the whole set on the bound the package documentation
	// states, so there is no cursor for the paging helper to walk.
	err := dataprivacy.ForEachOwner(ctx, c.resolve, requestScope, subject,
		func(ctx context.Context, scope tenancy.Scope) error {
			held, collectErr := c.store.ListAllCredentialsForUser(ctx, c.reader, scope, subject.ID)
			if collectErr != nil {
				return platformerrors.Wrapf(collectErr, "collecting passkeys in scope %q", scope)
			}

			for _, credential := range held {
				if credential == nil {
					continue
				}

				enrolled = append(enrolled, *credential)
			}

			return nil
		})
	if err != nil {
		return nil, err
	}

	return dataprivacy.Fragment(len(enrolled) > 0, enrolled)
}

// Eraser destroys the passkeys a subject registered.
type Eraser struct {
	store   passkeys.Store
	resolve ScopeResolver
}

var _ dataprivacy.Eraser = (*Eraser)(nil)

// NewEraser builds the eraser over a store and a scope resolver. Both are
// required.
func NewEraser(store passkeys.Store, resolve ScopeResolver) (*Eraser, error) {
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
// passkeys and the rest of the subject's footprint commit or roll back together.
// A subject who registered none erases nothing and is not an error.
//
// It calls [passkeys.Store.DeleteCredentialsForUser] and not
// [passkeys.Store.ArchiveCredentialForUser]; the package documentation says why,
// and says what it means that this takes away a way to sign in.
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
			deleted, deleteErr := e.store.DeleteCredentialsForUser(ctx, tx, scope, subject.ID)
			if deleteErr != nil {
				return dataprivacy.ErasureOutcome{}, deleteErr
			}

			return dataprivacy.ErasureOutcome{Deleted: deleted}, nil
		})
}
