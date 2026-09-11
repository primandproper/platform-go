package links

import (
	"maps"
	"time"
)

const (
	// DefaultTokenBytes is how many random bytes a token carries before
	// encoding. Thirty-two is 256 bits, which is not guessable and is short
	// enough to survive a mail client's line wrapping once base64url-encoded.
	DefaultTokenBytes = 32

	// DefaultRetention is how long a resolved link — redeemed, revoked, or
	// expired — stays in the store after it stops working.
	//
	// It buys one thing: the difference between "that link was already used"
	// and "no such link". Both are dead ends for the bearer, but only one of
	// them is a sentence a person can act on, and after retention has elapsed
	// the store cannot tell them apart any more.
	DefaultRetention = 24 * time.Hour

	// DefaultMaxTokenLength bounds what Redeem will hash. A token this package
	// minted is 43 bytes at the default size; the limit is generous enough to
	// survive a larger WithTokenBytes and small enough that an endpoint reachable
	// without authentication cannot be made to hash a megabyte per request.
	DefaultMaxTokenLength = 512

	// TokenPlaceholder is what an action's URL template must contain, exactly
	// once, to say where the token goes.
	TokenPlaceholder = "{token}"

	// serviceName names the loggers, spans, and metrics this package emits.
	serviceName = "links"

	// RecordVersion stamps every record written. A deploy that changes the shape
	// of Record bumps it, and records written by the previous shape then read as
	// absent rather than being misread — a link minted by the old binary stops
	// working, which is the safe direction for a credential.
	//
	// It is exported because a Store is what compares it. The check belongs
	// there rather than above it: links/database keeps it in a column and
	// reads it inside the transaction that resolves the row, and nothing wants
	// a Minter deciding after the fact that the record it just resolved was
	// written by something else.
	RecordVersion = 2
)

type (
	// Action names what a link does: "magic_login", "verify_email",
	// "password_reset", "unsubscribe".
	//
	// It is half of what a token is bound to, and the half that stops one flow's
	// link from working in another's. A verification link that redeems as a login
	// is an account takeover, and without the action on the record nothing in the
	// redemption path can tell the two apart.
	Action string

	// Subject names who or what the link is for — conventionally a user ID.
	// It is returned by redemption and is what the caller acts on.
	Subject string

	// Token is the secret in the URL. It is the whole credential: whoever holds
	// it can redeem the link.
	//
	// It is never persisted and never logged. The server stores a digest of it
	// (see ID), so a dump of the store yields nothing that can be redeemed, and
	// nothing in this package writes a Token to a span, a log line, or a metric
	// attribute. A caller that renders one into anything durable undoes both.
	Token string

	// ID is the non-secret handle for a link: the hex digest of its token.
	//
	// It exists so that minting and redeeming can be recorded, correlated, and
	// acted on without the token being written down anywhere. Because it is
	// derived rather than stored, mint and redeem agree on it with no extra
	// state, and a preimage of it is exactly as hard to find as the token
	// itself — which is what makes it safe to put in an audit entry.
	//
	// It is also the handle Revoke takes, which is what lets an application kill
	// an outstanding link months later from its own audit log, holding nothing
	// secret in the meantime.
	ID string
)

// State is what has happened to a link.
type State uint8

const (
	// StateActive marks a link that has not been used and has not been revoked.
	// Whether it is still within its lifetime is a separate question — see
	// Record.ExpiresAt.
	StateActive State = iota + 1
	// StateRedeemed marks a link that has been consumed. The record is kept
	// past redemption for DefaultRetention so a second attempt can be told what
	// happened rather than told nothing.
	StateRedeemed
	// StateRevoked marks a link withdrawn before it was used.
	StateRevoked
)

type (
	// Record is what the store holds for a link, keyed by the digest of its
	// token. It holds no secret: everything in it is already known to whoever
	// minted the link, and none of it can be turned back into a token.
	//
	// It is exported because a Store is what holds it, and the shipped
	// implementation lives in a package of its own. Its fields are read by
	// this package and by that one; Claims is what a redemption hands back.
	Record struct {
		// CreatedAt is when the link was minted.
		CreatedAt time.Time
		// ExpiresAt is when the link stops being redeemable.
		//
		// The store's own purge deadline is set past it deliberately, so this
		// field rather than the storage is what decides. A store that forgets
		// a record late must not be able to resurrect a credential.
		ExpiresAt time.Time
		// ResolvedAt is when the link was redeemed or revoked, and is nil while
		// the link is active.
		//
		// It is a pointer because "not yet resolved" is an absence rather than
		// an instant, and it is the absence the store already spells: the
		// column is NULL for exactly the active rows, and resolved_at IS NULL
		// is the guard that makes a resolution single-use. A zero time.Time
		// would put a second spelling of that absence opposite the first, one
		// that reads as an instant everywhere it is rendered — a JSON
		// round-trip hands back 0001-01-01T00:00:00Z rather than null — and
		// that no comparison against a clock can tell from a stamp.
		ResolvedAt *time.Time
		// PurgeAfter is when the store may forget this record, which is past
		// ExpiresAt by the Minter's retention window.
		//
		// It is what buys the difference between "that link was already used"
		// and "no such link" — two dead ends for the bearer, one of them a
		// sentence a person can act on. It is stamped by the Minter rather than
		// computed by the store, so a second store added later inherits the
		// deadline rather than recomputing it from a retention window of its
		// own and disagreeing about how long a spent link keeps answering.
		PurgeAfter time.Time
		// Metadata is what the minter attached, returned verbatim on
		// redemption.
		Metadata map[string]string
		// Action is what this link does.
		Action Action
		// Subject is who it is for.
		Subject Subject
		// Version is the record shape this was written with.
		Version int
		// State is what has happened to the link.
		State State
	}

	// Claims is what a successful redemption yields: everything the link was
	// bound to at mint time, and nothing the bearer supplied.
	//
	// The name is borrowed from JWT and the resemblance stops there. These
	// claims were never in the URL, were never readable by the bearer, and were
	// never signed — they were looked up. There is no algorithm field, no key
	// to confuse, and no way for a holder to assert anything the minter did not.
	Claims struct {
		// IssuedAt is when the link was minted.
		IssuedAt time.Time
		// ExpiresAt is when the link would have stopped being redeemable.
		ExpiresAt time.Time
		// Metadata is what the minter attached. It is a copy, so a caller may
		// keep or mutate it.
		Metadata map[string]string
		// Action is what the bearer is entitled to do — and the field that must
		// be checked before doing it, if one handler serves more than one
		// action.
		Action Action
		// Subject is who the link was for.
		Subject Subject
		// ID is the link's non-secret handle, for the audit entry recording
		// what was just granted.
		ID ID
	}

	// Link is a freshly minted link: the URL to deliver, and the handles for
	// talking about it afterwards.
	//
	// URL and Token are the same secret in two forms. Deliver one of them and
	// record ID; putting URL or Token into a log, an analytics event, or an
	// error message hands out the credential.
	Link struct {
		// ExpiresAt is when the link stops being redeemable.
		ExpiresAt time.Time
		// URL is the address to deliver, with the token already in place.
		URL string
		// Token is the bare secret, for a caller that delivers it some other
		// way than as this URL — a deep link, or a QR code carrying only the
		// token.
		Token Token
		// ID is the non-secret handle, for the audit entry recording the mint.
		ID ID
		// Action is what the link does.
		Action Action
		// Subject is who it is for.
		Subject Subject
	}
)

// Usable reports why a record cannot be acted on at now, or nil when it can.
//
// It is the one place the answer is decided, which is what keeps a store from
// having an opinion about it. links/database asks it inside the transaction
// that resolves the row, having read that row in the same transaction. A store
// that decided expiry for itself would be a second copy of this comparison,
// free to disagree with Inspect about the last second of a link's life.
//
// Expiry is decided here against the caller's clock rather than left to the
// store's own reclamation. A table reclaims nothing until something sweeps it,
// and PurgeAfter is set past ExpiresAt on purpose, so a store that forgets a
// record late must not be able to keep a credential alive past the moment it
// was supposed to die.
func (r *Record) Usable(now time.Time) error {
	switch r.State {
	case StateRedeemed:
		return ErrLinkAlreadyRedeemed
	case StateRevoked:
		return ErrLinkRevoked
	case StateActive:
		if !now.UTC().Before(r.ExpiresAt) {
			return ErrLinkExpired
		}

		return nil
	default:
		// A state this binary does not know is treated the same as a shape it
		// cannot read: refuse. The alternative is honoring a link whose meaning
		// was written by something else.
		return ErrLinkNotFound
	}
}

// Current reports whether the record was written by this shape of the package.
//
// A store answers ErrStaleRecord for one that was not, rather than decoding it:
// a record whose fields meant something else is a credential read with the
// wrong meanings, and invalidating it is the safe direction.
func (r *Record) Current() bool {
	return r.Version == RecordVersion
}

// claims renders a stored record as the answer to a redemption.
//
// The metadata map is copied rather than shared. A store is free to hand back
// the live pointer it holds — an in-memory one written for tests does exactly
// that — so a caller that mutated this map would be editing a record still
// sitting in the store.
func (r *Record) claims(id ID) *Claims {
	return &Claims{
		ID:        id,
		Action:    r.Action,
		Subject:   r.Subject,
		Metadata:  maps.Clone(r.Metadata),
		IssuedAt:  r.CreatedAt,
		ExpiresAt: r.ExpiresAt,
	}
}
