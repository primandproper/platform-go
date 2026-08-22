package requestsigning

import (
	"testing"
	"time"

	clockmock "github.com/primandproper/platform-go/v13/clock/mock"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestFreshness_Now(T *testing.T) {
	T.Parallel()

	pinned := time.Date(2026, time.July, 27, 12, 0, 0, 0, time.UTC)

	T.Run("a pinned instant wins over everything", func(t *testing.T) {
		t.Parallel()

		f := Freshness{At: pinned, Clock: &clockmock.ClockMock{NowFunc: time.Now}}

		test.EqOp(t, pinned, f.Now())
	})

	T.Run("an injected clock is read when nothing is pinned", func(t *testing.T) {
		t.Parallel()

		f := Freshness{Clock: &clockmock.ClockMock{NowFunc: func() time.Time { return pinned }}}

		test.EqOp(t, pinned, f.Now())
	})

	// The zero value has to resolve to something rather than the Unix epoch:
	// pinning verification to 1970 would reject every request there is.
	T.Run("the wall clock stands in when neither was supplied", func(t *testing.T) {
		t.Parallel()

		var f Freshness

		must.True(t, time.Since(f.Now()) < time.Minute)
	})
}

func TestFreshness_Check(T *testing.T) {
	T.Parallel()

	at := time.Date(2026, time.July, 27, 12, 0, 0, 0, time.UTC)
	f := Freshness{At: at, Tolerance: time.Minute}

	T.Run("accepts a timestamp inside the window", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, f.Check(at))
		test.NoError(t, f.Check(at.Add(-30*time.Second)))
		test.NoError(t, f.Check(at.Add(30*time.Second)))
	})

	// The window is symmetric: a timestamp from the future is either clock skew
	// or a sender minting signatures that outlive the window, and neither is
	// something to accept.
	T.Run("rejects a timestamp on either side of the window", func(t *testing.T) {
		t.Parallel()

		test.ErrorIs(t, f.Check(at.Add(-2*time.Minute)), ErrStaleSignature)
		test.ErrorIs(t, f.Check(at.Add(2*time.Minute)), ErrStaleSignature)
	})

	T.Run("compares in UTC whatever zone the timestamp carries", func(t *testing.T) {
		t.Parallel()

		elsewhere := time.FixedZone("UTC+9", 9*60*60)

		test.NoError(t, f.Check(at.In(elsewhere)))
	})

	// A zero Tolerance is not "do not check" — it is the tightest window there
	// is, which is what keeps an unset field from disabling the freshness bound
	// the signed timestamp exists to provide.
	T.Run("a zero tolerance admits only the exact instant", func(t *testing.T) {
		t.Parallel()

		tight := Freshness{At: at}

		test.NoError(t, tight.Check(at))
		test.ErrorIs(t, tight.Check(at.Add(time.Nanosecond)), ErrStaleSignature)
	})
}
