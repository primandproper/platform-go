package passkeys

import (
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
)

// The sentinels this package returns. They live together because a caller
// deciding what to do next is choosing between them, and a set spread across the
// files that happen to return each one cannot be read as the set it is.
var (
	// ErrNilDatabaseClient indicates a nil database.Client handed to
	// NewSQLStore. It wraps errors.ErrNilInputParameter, so a caller may check
	// either.
	ErrNilDatabaseClient = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil passkey database client")

	// ErrNilExecutor indicates a nil executor. Every method on the Store runs on
	// one the caller supplies — a database.Tx for a write, an executor for a
	// read — so there is no method that can fall back to a connection of the
	// store's own.
	ErrNilExecutor = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil passkey query executor")

	// ErrNilCredential indicates a nil Credential handed to a write.
	ErrNilCredential = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil passkey credential")

	// ErrNilStore indicates a nil Store handed to NewUserSource.
	ErrNilStore = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil passkey store")

	// ErrNilResolver indicates a nil handle resolver handed to NewUserSource.
	// It is the one thing that constructor cannot supply for itself — see
	// NewUserSource.
	ErrNilResolver = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil passkey user handle resolver")

	// ErrEmptyUserID indicates a write or read that named no user.
	//
	// It is refused rather than treated as a wildcard. A read keyed on the empty
	// string would answer with whatever rows a consumer happened to write with
	// one, and an archive keyed on it would be a revocation whose owner
	// predicate matches nobody — the shape of a bug that looks like a working
	// call.
	ErrEmptyUserID = platformerrors.New("passkey user id is required")

	// ErrEmptyCredentialID indicates a read or write that named no credential.
	ErrEmptyCredentialID = platformerrors.New("passkey credential id is required")

	// ErrEmptyPublicKey indicates a registration with no public key. A
	// credential whose key is absent verifies nothing, and storing one would
	// make every later assertion against it fail inside the protocol engine
	// rather than here.
	ErrEmptyPublicKey = platformerrors.New("passkey public key is required")

	// ErrEmptyHandle indicates a user handle of no bytes handed to a UserSource.
	ErrEmptyHandle = platformerrors.New("webauthn user handle is required")

	// ErrCredentialNotFound indicates a passkey that does not exist in the scope
	// that asked. A credential in another scope reads as absent, which is what
	// it is from here — and is the answer that does not turn the read into an
	// oracle for which credentials exist in other tenants. An archived one reads
	// as absent too: a revoked passkey verifies nothing.
	ErrCredentialNotFound = platformerrors.New("passkey credential not found")

	// ErrCredentialRegistered indicates a credential ID already enrolled and
	// live in this scope.
	//
	// It is a distinct error rather than a raw constraint violation because the
	// difference between "this authenticator is already enrolled" and "the
	// database is unwell" decides what a registration flow tells the person in
	// front of it. The unique index is what guarantees uniqueness; the check
	// that raises this is what turns the ordinary case into a sentinel, so two
	// registrations racing for one credential still reach the index and the
	// loser gets the driver's error.
	ErrCredentialRegistered = platformerrors.New("passkey credential is already registered")

	// ErrScopeMismatch indicates a Credential whose Scope disagrees with the
	// scope the call named.
	//
	// The argument is the scope, always. A write that corrected the entity
	// instead would let the field decide which tenant a passkey lands in, which
	// is the derivation the tenancy convention exists to rule out. A credential
	// naming no scope adopts the argument; one naming a different scope is
	// refused rather than rewritten, which is the reading comments.Store already
	// takes of the same disagreement.
	ErrScopeMismatch = platformerrors.New("passkey credential scope disagrees with the scope named by the call")

	// ErrSignCountOutOfRange indicates a sign_count that does not fit the
	// authenticator counter it stands for.
	//
	// The column is a 64-bit integer because that is what the three dialects
	// share, and a WebAuthn signature counter is 32 bits unsigned. A row outside
	// that range was not written by this package, and reporting it is the honest
	// answer: silently clamping would hand clone detection a counter that never
	// moves, which is the exact failure this table exists to prevent.
	ErrSignCountOutOfRange = platformerrors.New("stored passkey sign count is outside the range of a WebAuthn signature counter")
)
