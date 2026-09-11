package sessions

import (
	"context"
	"time"

	"github.com/primandproper/primitives-go/v2/tenancy"
)

const (
	// DefaultAbsoluteTimeout is how long a session may live from the moment it
	// was established, regardless of activity. It is the ceiling a stolen
	// cookie cannot outlive, so it is the one timeout that has to be set even
	// on a store nobody idles out.
	DefaultAbsoluteTimeout = 24 * time.Hour

	// DefaultIdleTimeout is how long a session survives without being read.
	DefaultIdleTimeout = 30 * time.Minute

	// DefaultTouchInterval is how much of the idle window has to elapse before
	// a read refreshes the session's idle deadline. See Policy for why this is
	// not zero.
	DefaultTouchInterval = time.Minute

	// DefaultRetentionGrace is how long an expired record is kept before the
	// backing store may reclaim it, so that a user who comes back can be told
	// why they were signed out rather than merely that they were. See
	// Policy.Grace for why a backend's own expiry is the wrong thing to end a
	// session with.
	DefaultRetentionGrace = time.Hour

	// DefaultIDByteLength is how many random bytes a session identifier is
	// minted from — 256 bits, which is what makes the cache backend's
	// collision-free Create safe to assume.
	DefaultIDByteLength = 32

	// serviceName names the loggers, spans, and metrics this package emits.
	serviceName = "sessions"

	// recordVersion stamps every record written. A deploy that changes the
	// shape of T (or of Record itself) bumps it, and records written by the
	// previous shape read as absent rather than being misread — a stale
	// session is a re-login, where a misread one is a user holding somebody
	// else's payload.
	//
	// It is 2 because Record grew a Holder and a Metadata. A record written by
	// the previous shape carries neither, so it would decode into a session
	// held by nobody: reachable by its identifier, absent from its owner's
	// list, and unreachable by the revocation they would use to end it. That is
	// worse than the re-login this discards it for, and it is invisible —
	// nothing about such a session looks wrong until somebody tries to sign out
	// of it.
	recordVersion = 2

	// timeResolution is what every stamped time is truncated to.
	//
	// It is here rather than in the database backend because both backends
	// have to agree: Postgres and MySQL keep microseconds, so a nanosecond
	// stamp comes back changed, and a Session read from the database would
	// then differ from the one New just returned for the same session. The
	// cache backend round-trips whatever it is given, so truncating at the
	// stamping site is what makes the two interchangeable.
	timeResolution = time.Microsecond
)

type (
	// Holder names whose sessions a call is about: the tenancy scope, and the
	// principal inside it.
	//
	// It is one value rather than two arguments because neither half is a key
	// on its own, and both failures are the kind nobody notices in review. A
	// revocation keyed on the principal alone reaches into every tenant that
	// happens to spell an identifier the same way; one keyed on the scope alone
	// signs out everybody in it. Passed together, the pair is what a statement
	// binds and what a caller has to have decided.
	//
	// Principal is an opaque identifier, deliberately not a type from this
	// module's identity package: a session store that could not be used without
	// an identity store would be a session store nobody could use with their
	// own. What the string means is the consumer's business — a user ID, a
	// service account, an API client.
	//
	// The empty principal is a session attributed to nobody, which is what
	// Store.New establishes. It is not enumerable: a list of every anonymous
	// session in a scope answers nobody's question, and would be a way to reach
	// sessions that have not yet been claimed.
	Holder struct {
		// Principal is who holds the session within that scope.
		Principal string
		// Scope is the tenant whose data the session is. It must name
		// something — see tenancy.Scope, whose zero value names nobody and
		// which no query here accepts.
		Scope tenancy.Scope
	}

	// Metadata describes the client a session was established from, for the
	// security page that lists a principal's sessions back to them.
	//
	// Every field is the client's own account of itself, so none of it is
	// evidence and none of it is ever compared against anything. It is there so
	// that a person scanning their own sessions can recognize the ones that are
	// theirs and notice one that is not — which is a judgment only they can
	// make, and only if they are shown enough to make it.
	//
	// It is stamped once, when the session is established, and no later write
	// moves it. A session whose recorded device changed under a user would be
	// worse than one that recorded nothing.
	Metadata struct {
		// DeviceName is what the client called itself, in whatever vocabulary
		// the consumer chose.
		DeviceName string
		// IPAddress is the address the session was established from. It is
		// rendered, never trusted: deriving it from a forwarded-for header is
		// the caller's decision, and so is whether to believe it.
		IPAddress string
		// UserAgent is the client's self-description at establishment.
		UserAgent string
		// LoginMethod is how the principal proved themselves — a password, a
		// passkey, an OAuth provider's name. The vocabulary is the consumer's.
		LoginMethod string
	}

	// Identified is one stored record together with the identifier it is stored
	// under.
	//
	// It exists for the one read that answers with identifiers rather than
	// taking them: a Record does not carry its own, because everywhere else the
	// identifier is the key the record was fetched by. An enumeration is the
	// caller asking which identifiers those are, and the answer is unusable
	// without them — they are what a revocation is then aimed at.
	Identified[T any] struct {
		// Record is the stored record.
		Record *Record[T]
		// ID is the identifier it is stored under.
		ID string
	}

	// Record is what a Backend holds for a session identifier. It carries the
	// payload and the two anchors expiry is measured from, and nothing else —
	// the identifier is the key it is stored under, and the deadlines are
	// derived from the Policy rather than frozen into the record.
	//
	// T must round-trip through whichever backend stores it. The cache backend
	// serializes with its provider's codec (CBOR by default), the database
	// backend with an encoding.Codec; both want a concrete struct with
	// exported fields.
	Record[T any] struct {
		// CreatedAt is when the session was established. It survives Renew, so
		// rotating an identifier does not extend the absolute deadline — which
		// is the whole reason rotation is safe to do on every privilege change.
		CreatedAt time.Time
		// LastSeenAt is when the session was last read or written. It is the
		// idle deadline's anchor, and it is refreshed no more often than the
		// Policy's touch interval — see Policy.
		LastSeenAt time.Time
		// Data is the payload. It may be nil: a session that only needs to
		// exist is a legitimate session.
		Data *T
		// Metadata is what the session was established from. Like Holder it
		// survives Renew, and unlike the two anchors above it is never
		// reassigned: it describes an establishment rather than a state.
		Metadata Metadata
		// Holder is whose session this is. It survives Renew, so rotating an
		// identifier does not hand the session to somebody else — and it is
		// what makes a principal's sessions enumerable at all.
		Holder Holder
		// Version is the record shape this was written with.
		Version int
	}

	// Session is a live session as a Store hands it back.
	//
	// It is a snapshot, not a handle: mutating it changes nothing server-side.
	// Store.Save is how a payload is written back.
	Session[T any] struct {
		// CreatedAt is when the session was established, unchanged by Renew.
		CreatedAt time.Time
		// LastSeenAt is the idle deadline's anchor as of this read.
		LastSeenAt time.Time
		// ExpiresAt is the earlier of the absolute and idle deadlines: the
		// instant this session stops being usable if nothing touches it again.
		// It is what a cookie's MaxAge should be derived from, so the browser
		// and the store agree on when the session ended.
		ExpiresAt time.Time
		// Data is the payload, as stored.
		Data *T
		// Metadata is what this session was established from.
		Metadata Metadata
		// ID is the identifier this session was read under. It is the value the
		// cookie carries, and the only part of a session that ever leaves the
		// server.
		ID string
		// Holder is whose session this is.
		Holder Holder
		// IsCurrent reports whether this is the session the caller asked with.
		//
		// It is derived at read time from the identifier passed to Store.List
		// rather than stored, because "current" is a fact about a request and
		// not about a session: the same row is current to one browser and not
		// to the other four. A stored flag would have to be moved on every
		// read, and would be wrong for everybody but the last writer.
		//
		// It is always false outside List — the by-identifier reads answer
		// about the session the caller already named.
		IsCurrent bool
	}

	// Store is the server-side session store: identifiers in, payloads out.
	//
	// Every method takes or returns an identifier rather than a cookie. What
	// the identifier travels in is the caller's business — sessions/http binds
	// it to a signed cookie, which is what nearly everyone wants.
	//
	// A Store enforces the expiry Policy and mints identifiers; where the
	// records physically live is the Backend's business. Absence and expiry are
	// reported as ErrNotFound and ErrExpired, and ErrExpired wraps ErrNotFound,
	// so a caller that does not care about the difference checks one thing.
	Store[T any] interface {
		// New establishes a session around data and returns it, identifier
		// included. data may be nil.
		//
		// The session it establishes is attributed to nobody: the global scope
		// and the empty principal. It is therefore reachable only by its
		// identifier and appears in no List — which is what an anonymous
		// session is. Call NewFor once somebody has signed in.
		New(ctx context.Context, data *T) (*Session[T], error)
		// NewFor establishes a session held by somebody, with the metadata a
		// security page will render beside it.
		//
		// This is the call a sign-in makes. What separates it from New is that
		// the resulting session is enumerable and revocable as one of that
		// principal's — so a holder with no scope, or no principal, is refused
		// rather than quietly establishing a session nobody can find: see
		// tenancy.ErrNoScope and ErrPrincipalRequired.
		//
		// The holder and the metadata are stamped once. Renew carries both
		// across, and no other write moves either.
		NewFor(ctx context.Context, holder Holder, metadata Metadata, data *T) (*Session[T], error)
		// Get reads a session, refreshing its idle deadline when the Policy's
		// touch interval has elapsed.
		//
		// A session past either deadline is reported as ErrExpired and removed;
		// one that was never there, or whose record was written by another
		// shape of this package, is reported as ErrNotFound.
		Get(ctx context.Context, id string) (*Session[T], error)
		// Save replaces a session's payload. It refreshes the idle deadline and
		// leaves the absolute one alone.
		Save(ctx context.Context, id string, data *T) error
		// Renew rotates a session's identifier, carrying the payload and the
		// original CreatedAt across, and returns the new identifier.
		//
		// Call it on every privilege change — sign-in above all. Without it, an
		// identifier an attacker planted before sign-in is still valid after
		// it, which is session fixation. Because CreatedAt survives, rotating
		// on every privilege change cannot be used to extend a session forever.
		//
		// The old identifier stops working the moment this returns nil. If it
		// returns an error, assume it still works and refuse the privilege
		// change.
		Renew(ctx context.Context, oldID string) (newID string, err error)
		// Delete ends a session. An identifier that was already gone is not an
		// error: sign-out is not the place to surface a race.
		Delete(ctx context.Context, id string) error
		// List enumerates the live sessions holder holds, newest first.
		//
		// currentID is the identifier the caller is asking with, and decides
		// which of the returned sessions has IsCurrent set; the empty string
		// marks none of them. It is not a filter — the session it names is
		// still listed, since a security page that hid the reader's own session
		// would be listing the wrong set.
		//
		// Expired sessions are left out, decided by the same Policy the
		// by-identifier read decides with, so a session this omits is one Get
		// would refuse. Nothing is written: an enumeration does not touch idle
		// deadlines, or reading a security page would keep every session on it
		// alive.
		//
		// A holder with an empty principal is ErrPrincipalRequired. A backend
		// that keeps no principal index is ErrNoPrincipalIndex — see Backend.
		List(ctx context.Context, holder Holder, currentID string) ([]*Session[T], error)
		// Revoke ends one of a holder's sessions.
		//
		// The holder is part of the question rather than checked beforehand: the
		// session ends only if it is the named principal's within the named
		// scope, decided where the row is removed. A caller naming a session
		// that is not theirs is answered ErrNotFound, not a refusal, so the
		// answer does not confirm that the identifier names anything.
		//
		// Unlike Delete this is not idempotent: revoking a session that is
		// already gone is ErrNotFound. Delete ends the caller's own session,
		// where a race is a sign-out that already happened; this ends a session
		// somebody is looking at a list of, where "there was nothing there" is
		// the answer they need to see.
		Revoke(ctx context.Context, holder Holder, id string) error
		// RevokeAll ends every session a holder holds, including the caller's
		// own, and reports how many ended.
		//
		// It is the "sign out everywhere" a password change should trigger.
		RevokeAll(ctx context.Context, holder Holder) (int, error)
		// RevokeAllExcept ends every session a holder holds but one, and
		// reports how many ended.
		//
		// keepID is normally the caller's current session, which is what makes
		// this "sign out my other devices". An empty keepID spares nothing and
		// is exactly RevokeAll; an identifier the holder does not hold spares
		// nothing either, because sparing is by the same key everything else
		// here is.
		RevokeAllExcept(ctx context.Context, holder Holder, keepID string) (int, error)
		// Policy reports the expiry rule this store enforces.
		//
		// It is on the interface because the cookie has to agree with it:
		// sessions/http derives how long a browser should keep a session
		// cookie from the absolute timeout rather than from a second setting
		// that could drift from this one.
		Policy() Policy
		// Close releases what the store holds — the backend's connection pool,
		// a background sweep — and is safe to call more than once.
		Close() error
	}

	// Backend is where a Store's records physically live. sessions/cache and
	// sessions/database implement it; a Store adds identifiers, expiry, and
	// observability on top.
	//
	// The split exists because the parts worth getting exactly right — what
	// Renew preserves, when a session is expired, whether a touch may
	// resurrect a signed-out session — are the parts that must not differ
	// between backends. Written once in Store, they cannot.
	//
	// Every method's ttl is how long the record should remain retrievable, and
	// is always positive: a Store never asks a backend to store something
	// already expired.
	Backend[T any] interface {
		// Load reads the record stored under id, reporting ErrNotFound when
		// there is none. It does not evaluate expiry — the Store does, from the
		// record's own anchors, so both backends answer the same question the
		// same way.
		Load(ctx context.Context, id string) (*Record[T], error)
		// Create stores a record under an identifier that must not already
		// exist, reporting ErrIDConflict if it does.
		Create(ctx context.Context, id string, record *Record[T], ttl time.Duration) error
		// Update overwrites the record stored under an existing identifier,
		// reporting ErrNotFound when there is none.
		//
		// The existence requirement is not bookkeeping. Without it a request
		// that read a session just before it was signed out would write it back
		// afterwards, and the sign-out would not have happened.
		Update(ctx context.Context, id string, record *Record[T], ttl time.Duration) error
		// Rename moves a record from oldID to newID, reporting ErrNotFound when
		// oldID holds nothing. On a nil return, oldID no longer resolves.
		Rename(ctx context.Context, oldID, newID string, record *Record[T], ttl time.Duration) error
		// Delete removes the record stored under id. An identifier that was
		// already absent is not an error.
		Delete(ctx context.Context, id string) error
		// ListHeld returns every record the holder holds, newest first, each
		// with the identifier it is stored under. It applies no expiry — the
		// Store filters, from the same anchors it refuses a single record by.
		//
		// A backend that keeps no index on the holder reports
		// ErrNoPrincipalIndex, and sessions/cache is one: a key-value store
		// answers "what is under this key" and cannot answer "which keys belong
		// to this person" without a second structure to maintain, which is a
		// second source of truth about which sessions are live.
		//
		// That is why these three are on the interface rather than living only
		// on the backend that has them. Sweep is not here because a caller who
		// chose a cache never needed one; this is here because a caller who
		// chose a cache and does need it has to be told so, in the one place
		// they would otherwise assume it worked.
		ListHeld(ctx context.Context, holder Holder) ([]*Identified[T], error)
		// DeleteHeld removes the record stored under id if — and only if — the
		// holder holds it, reporting how many rows went: one, or none.
		//
		// The holder is part of the statement rather than a check the caller
		// makes first. A revocation authorized in one round trip and executed
		// in another is one that can be got out of step; here the server
		// decides "this session, and it is theirs" at the instant it goes.
		DeleteHeld(ctx context.Context, holder Holder, id string) (int, error)
		// DeleteAllHeld removes every record the holder holds, sparing the one
		// stored under keepID when that is not empty, and reports how many
		// went.
		DeleteAllHeld(ctx context.Context, holder Holder, keepID string) (int, error)
		// Close releases the backend's resources and is safe to call more than
		// once.
		Close() error
	}
)

// session renders a stored record as the snapshot callers see.
func (s *BackendStore[T]) session(id string, record *Record[T]) *Session[T] {
	return &Session[T]{
		CreatedAt:  record.CreatedAt,
		LastSeenAt: record.LastSeenAt,
		ExpiresAt:  s.policy.Deadline(record.CreatedAt, record.LastSeenAt),
		Data:       record.Data,
		Holder:     record.Holder,
		Metadata:   record.Metadata,
		ID:         id,
	}
}
