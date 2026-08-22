package links

import (
	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

// Sentinels. errors/http and errors/grpc map these onto status codes, so those
// packages import this one. That direction is load-bearing: nothing here may
// import errors/http or errors/grpc, or the cycle closes.
//
// The four redemption failures are separate sentinels rather than one, which is
// the opposite of what sessions does with its own. The reason is entropy. An
// unusable session cookie is distinguishable from a forged one only by a check
// an attacker can run millions of times, so telling the two apart is an oracle;
// a link token is 256 random bits, and nobody is ever holding one they did not
// receive. Separating them costs nothing and buys the difference between "this
// link has already been used" and "this link has expired" — two sentences with
// two different next steps for the person reading them.
var (
	// ErrLinkNotFound indicates no link exists for the token. Either it was
	// never minted, or it resolved long enough ago that retention has dropped
	// the record.
	ErrLinkNotFound = platformerrors.New("action link not found")
	// ErrLinkAlreadyRedeemed indicates the link was already consumed. It is the
	// single-use guarantee reporting itself, and the answer a mail scanner's
	// prefetch produces when the user's own click arrives second — see the
	// package documentation on prefetching.
	ErrLinkAlreadyRedeemed = platformerrors.New("action link already redeemed")
	// ErrLinkExpired indicates the link's lifetime elapsed before it was used.
	ErrLinkExpired = platformerrors.New("action link expired")
	// ErrLinkRevoked indicates the link was withdrawn before it was used.
	ErrLinkRevoked = platformerrors.New("action link revoked")

	// ErrInvalidToken indicates a token that is empty or longer than this
	// package will hash. It is a malformed request rather than a redemption
	// outcome: no link was looked for.
	ErrInvalidToken = platformerrors.New("invalid action link token")
	// ErrInvalidID indicates an empty ID was passed to Revoke.
	ErrInvalidID = platformerrors.New("invalid action link ID")

	// ErrUnknownAction indicates Mint was asked for an action no policy was
	// registered for.
	//
	// Registration is what makes an action mintable, which makes the registry an
	// allowlist as well as a configuration: a typo produces this rather than a
	// working-looking link to a page that does not exist, and a metric labeled
	// by action cannot be given unbounded cardinality by a caller.
	ErrUnknownAction = platformerrors.New("unknown action link action")
	// ErrEmptySubject indicates Mint was called without a subject. A link bound
	// to nobody would redeem into a claim the caller cannot act on, so it is
	// rejected rather than minted.
	ErrEmptySubject = platformerrors.New("empty action link subject")
	// ErrNoActions indicates NewMinter was called with no action registered.
	// A Minter that can mint nothing is a wiring mistake, and one that reports
	// itself at construction is cheaper than one that reports itself at the
	// first password reset.
	ErrNoActions = platformerrors.New("no action link actions registered")

	// ErrInvalidActionURL indicates an action's URL template is unusable: empty,
	// unparseable, missing TokenPlaceholder, carrying more than one of them, or
	// naming a scheme that would put the token on the wire in the clear.
	ErrInvalidActionURL = platformerrors.New("invalid action link URL template")
	// ErrInsecureActionURL indicates an action's URL template is http against a
	// host that is not loopback. The token is a bearer credential and the URL is
	// the only place it exists, so cleartext delivery hands it to every hop.
	// WithInsecureURLs exists for the environments where that is knowingly
	// acceptable.
	ErrInsecureActionURL = platformerrors.Wrap(ErrInvalidActionURL, "action link URL is not https")
	// ErrInvalidTTL indicates a non-positive lifetime.
	//
	// There is no default. A magic-login link and an unsubscribe link differ by
	// four orders of magnitude in how long they should live, and any value this
	// package picked would be wrong for one of them in the dangerous direction.
	ErrInvalidTTL = platformerrors.New("invalid action link TTL")

	// ErrStoreUnavailable indicates the record store could not be read or
	// written. Redemption fails closed on it without exception: a link this
	// package cannot prove is unused, and cannot mark as used, must not be
	// honored — there is no availability argument on the other side of an
	// account.
	ErrStoreUnavailable = platformerrors.New("action link store unavailable")

	// ErrNilStore indicates NewMinter was called without a record store. It
	// wraps errors.ErrNilInputParameter, so a caller may check either.
	ErrNilStore = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil action link store")
	// ErrNilLocker indicates NewMinter was called without a locker. It has no
	// default: an implicit noop would let two concurrent redemptions of one
	// token both succeed, which is the single failure this package exists to
	// prevent. It wraps errors.ErrNilInputParameter, so a caller may check
	// either.
	ErrNilLocker = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil action link locker")
)
