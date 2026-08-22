package requestsigning

import (
	"time"

	"github.com/primandproper/platform-go/v13/clock"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

// Freshness is how a verifier decides whether a signed timestamp is recent
// enough: what "now" is, and how far from it a timestamp may sit.
//
// It is exported because it is not this scheme's. Every signature scheme that
// binds a timestamp into the signed material — this package's v1, Stripe's
// t=…,v1=…, the next vendor's — needs the same three-source resolution of
// "now" and the same symmetric window around it, and the copies of that in this
// module had already begun to differ in whether a skew was rounded and which
// direction the comparison read. There is one of each here so a verifier for
// somebody else's scheme gets the resolution and the sentinel rather than a
// paragraph of prose telling it to reimplement them.
//
// The zero value resolves to the wall clock with no tolerance at all, which
// rejects everything; a constructor building one is expected to start from
// DefaultTolerance.
type Freshness struct {
	// Clock is the source of time. Absent means the wall clock.
	Clock clock.Clock

	// At pins the instant verification compares against, winning over Clock. It
	// exists for tests and for replaying a captured request against a known
	// instant. The zero time means "not pinned" rather than the Unix epoch, so
	// an unset field cannot silently reject everything.
	At time.Time

	// Tolerance is how far a signed timestamp may sit from Now, in either
	// direction.
	//
	// There is deliberately no value meaning "do not check". A signature with
	// no freshness bound is replayable forever, which is the property a signed
	// timestamp exists to remove; a caller that wants a long window names a long
	// duration and can be seen to have done so.
	Tolerance time.Duration
}

// Now resolves the instant a verification compares a signed timestamp against:
// the pinned one if a caller named it, the injected clock's otherwise, and the
// wall clock when neither was supplied.
func (f Freshness) Now() time.Time {
	if !f.At.IsZero() {
		return f.At
	}

	if f.Clock != nil {
		return f.Clock.Now()
	}

	return time.Now()
}

// Check reports whether signedAt sits within Tolerance of Now, returning a
// wrapped ErrStaleSignature carrying the drift when it does not.
//
// The window is symmetric. A timestamp from the future is as suspect as one
// from the past — it is either clock skew, which is the benign case this error
// exists to name, or a sender minting signatures that stay valid longer than
// the window allows.
//
// Callers run this before computing any MAC. A stale request is then rejected
// without spending work proportional to its body, which is what keeps a replay
// flood from costing a receiver anything. The timestamp is unauthenticated at
// that point, and that is fine: forging it either moves the request out of the
// window or leaves it signed under a payload whose MAC will not match, so
// nothing is decided on an unverified value.
func (f Freshness) Check(signedAt time.Time) error {
	drift := f.Now().UTC().Sub(signedAt.UTC())
	if drift > f.Tolerance || drift < -f.Tolerance {
		return platformerrors.Wrapf(ErrStaleSignature, "signed %s from now", drift.Round(time.Second))
	}

	return nil
}
