package oauth2server

import (
	"net/http"
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"
)

func codes(t *testing.T, s *conformance.Session, srv *server) {
	t.Helper()

	t.Run("an authorization code is redeemed once", func(t *testing.T) {
		t.Parallel()

		f := newFlow(t, s, srv)
		code := f.authorize(t)

		f.exchange(t, code).issued(t, "the first exchange of a code")
		f.exchange(t, code).refused(t, http.StatusBadRequest, errorInvalidGrant, "a second exchange of the same code")
	})

	// RFC 6749 §4.1.2: a code presented twice means somebody other than the
	// client may hold it, so what it issued the first time is ended too. The
	// server has no switch for it.
	t.Run("a replayed code ends the tokens it issued", func(t *testing.T) {
		t.Parallel()

		f := newFlow(t, s, srv)
		code := f.authorize(t)

		issued := f.exchange(t, code).issued(t, "the first exchange of a code")
		f.exchange(t, code).refused(t, http.StatusBadRequest, errorInvalidGrant, "a second exchange of the same code")

		f.refresh(t, f.client, issued.refresh).
			refused(t, http.StatusBadRequest, errorInvalidGrant, "refreshing a pair whose code was replayed")

		// The control, for the same client and person: a pair whose code nobody
		// replayed refreshes, so the refusal above is the replay's doing rather
		// than a refresh grant that does not work here.
		untouched := f.pair(t)
		f.refresh(t, f.client, untouched.refresh).issued(t, "refreshing a pair whose code was redeemed once")
	})
}
