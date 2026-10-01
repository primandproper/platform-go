package oauth2server

import (
	"net/http"
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"
)

func refresh(t *testing.T, s *conformance.Session, srv *server) {
	t.Helper()

	// A refresh token rotates on every use, so one presented after it rotated
	// is either a client that lost track or somebody holding a copy — and the
	// server cannot tell which, so it ends the family: every token descended
	// from the same authorization, the current one included.
	t.Run("a replayed refresh token ends its whole family", func(t *testing.T) {
		t.Parallel()

		f := newFlow(t, s, srv)
		first := f.pair(t)

		rotated := f.refresh(t, f.client, first.refresh).issued(t, "the first refresh")

		f.refresh(t, f.client, first.refresh).
			refused(t, http.StatusBadRequest, errorInvalidGrant, "refreshing with a token that has already rotated")
		f.refresh(t, f.client, rotated.refresh).
			refused(t, http.StatusBadRequest, errorInvalidGrant, "refreshing with the current token of a family whose ancestor was replayed")

		// The control: a rotated token of a family nobody replayed refreshes,
		// which is the step that fails above only because of the replay.
		other := f.pair(t)
		otherRotated := f.refresh(t, f.client, other.refresh).issued(t, "the first refresh of an untouched family")
		f.refresh(t, f.client, otherRotated.refresh).issued(t, "refreshing with the current token of an untouched family")
	})
}
