package oauth2server

import (
	"net/http"
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"

	"github.com/shoenig/test"
)

func revocation(t *testing.T, s *conformance.Session, srv *server) {
	t.Helper()

	// Both halves in one subtest, because each is the other's control. Another
	// client is told 200 — RFC 7009 gives it no way to learn whose token it
	// presented — and the token survives, so the survival is the assertion; the
	// owning client's revocation then ends it, which is what proves revocation
	// reached the store at all.
	t.Run("a revocation ends a token for its own client and for no other", func(t *testing.T) {
		t.Parallel()

		f := newFlow(t, s, srv)
		issued := f.pair(t)
		stranger := f.register(t)

		answered := f.revoke(t, stranger, issued.refresh)
		test.EqOp(t, http.StatusOK, answered.status,
			test.Sprintf("another client revoking a token answered %d rather than the 200 that discloses nothing: %s", answered.status, answered.body))

		rotated := f.refresh(t, f.client, issued.refresh).issued(t, "refreshing a token another client tried to revoke")

		answered = f.revoke(t, f.client, rotated.refresh)
		test.EqOp(t, http.StatusOK, answered.status,
			test.Sprintf("a client revoking its own token answered %d: %s", answered.status, answered.body))
		test.SliceEmpty(t, answered.body, test.Sprint("a revocation answered with a body; RFC 7009 answers an empty 200"))

		f.refresh(t, f.client, rotated.refresh).
			refused(t, http.StatusBadRequest, errorInvalidGrant, "refreshing a token its own client revoked")
	})

	t.Run("a token nobody issued is acknowledged, and no token at all is refused", func(t *testing.T) {
		t.Parallel()

		f := newFlow(t, s, srv)

		// RFC 7009 §2.2: an endpoint that answered an unknown token differently
		// would let anybody enumerate which tokens exist.
		answered := f.revoke(t, f.client, "conformance-never-issued")
		test.EqOp(t, http.StatusOK, answered.status,
			test.Sprintf("revoking a token nobody issued answered %d: %s", answered.status, answered.body))

		f.revoke(t, f.client, "").refused(t, http.StatusBadRequest, errorInvalidRequest, "a revocation naming no token")
	})
}
