package oauth2clients

import (
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/oauth2clients/oauth2clientspb"
	"github.com/primandproper/platform-go/v14/conformance"

	"github.com/primandproper/primitives-go/v2/identifiers"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func reach(t *testing.T, s *conformance.Session) {
	t.Helper()

	// The reach these RPCs are behind a permission for. They are not keyed on
	// the caller: an administrator reads and withdraws a registration somebody
	// else minted, and the only thing that narrows them is the registry.
	t.Run("a colleague's registration is readable and can be withdrawn", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)
		other := colleague(t, s, caller)
		theirs := register(t, other).GetClient().GetId()
		ctx := caller.Context(t.Context())

		read, err := caller.Surfaces.OAuth2Clients.GetOAuth2Client(ctx,
			&oauth2clientspb.GetOAuth2ClientRequest{Oauth2ClientId: theirs})
		must.NoError(t, err, must.Sprint("a registration in the caller's own registry was unreachable"))
		test.EqOp(t, theirs, read.GetResult().GetId())

		_, err = caller.Surfaces.OAuth2Clients.ArchiveOAuth2Client(ctx,
			&oauth2clientspb.ArchiveOAuth2ClientRequest{Oauth2ClientId: theirs})
		must.NoError(t, err)

		// Withdrawn means nothing on this surface answers with it again, for
		// anybody in the registry — including the colleague who minted it.
		_, err = other.Surfaces.OAuth2Clients.GetOAuth2Client(other.Context(t.Context()),
			&oauth2clientspb.GetOAuth2ClientRequest{Oauth2ClientId: theirs})
		must.Error(t, err, must.Sprint("a withdrawn registration was still readable"))
		test.EqOp(t, codes.NotFound, status.Code(err))

		test.SliceNotContains(t, registry(t, caller), theirs,
			test.Sprint("a withdrawn registration was still listed"))
	})

	t.Run("a listing pages the caller's whole registry and nobody else's", func(t *testing.T) {
		t.Parallel()

		mine, theirs := twoRegistries(t, s)
		other := colleague(t, s, mine)

		own := register(t, mine).GetClient().GetId()
		shared := register(t, other).GetClient().GetId()
		foreign := register(t, theirs).GetClient().GetId()

		// Presence and absence of three named registrations, never a count.
		ids := registry(t, mine)
		test.SliceContains(t, ids, own, test.Sprint("the caller's own registration was missing"))
		test.SliceContains(t, ids, shared,
			test.Sprint("a colleague's registration was missing; the listing is the registry's, not the caller's"))
		test.SliceNotContains(t, ids, foreign,
			test.Sprint("a neighboring registry's registration reached this listing"))
	})

	t.Run("a registration in another registry is absent to a caller naming it", func(t *testing.T) {
		t.Parallel()

		mine, theirs := twoRegistries(t, s)
		own := register(t, mine).GetClient().GetId()

		// The positive control: the minter reaches it.
		read, err := mine.Surfaces.OAuth2Clients.GetOAuth2Client(mine.Context(t.Context()),
			&oauth2clientspb.GetOAuth2ClientRequest{Oauth2ClientId: own})
		must.NoError(t, err, must.Sprint("the caller cannot read its own registration; the absence below proves nothing"))
		test.EqOp(t, own, read.GetResult().GetId())

		ctx := theirs.Context(t.Context())

		_, err = theirs.Surfaces.OAuth2Clients.GetOAuth2Client(ctx,
			&oauth2clientspb.GetOAuth2ClientRequest{Oauth2ClientId: own})
		must.Error(t, err, must.Sprint("a neighboring registry's registration was readable"))
		test.EqOp(t, codes.NotFound, status.Code(err))

		_, err = theirs.Surfaces.OAuth2Clients.ArchiveOAuth2Client(ctx,
			&oauth2clientspb.ArchiveOAuth2ClientRequest{Oauth2ClientId: own})
		must.Error(t, err, must.Sprint("a neighboring registry withdrew this caller's registration"))
		test.EqOp(t, codes.NotFound, status.Code(err))

		// And the refused withdrawal changed nothing.
		test.SliceContains(t, registry(t, mine), own,
			test.Sprint("a refused withdrawal withdrew the registration anyway"))
	})

	// A registration in another registry and one that was never minted are the
	// same answer, so a caller holding the permission in their own registry
	// cannot use it to learn which identifiers exist in anybody else's.
	t.Run("another registry's registration is answered exactly as one never minted", func(t *testing.T) {
		t.Parallel()

		mine, theirs := twoRegistries(t, s)
		own := register(t, mine).GetClient().GetId()
		ctx := theirs.Context(t.Context())

		// The positive control. Two refusals being alike proves nothing about a
		// deployment that refuses everything alike.
		_, err := mine.Surfaces.OAuth2Clients.GetOAuth2Client(mine.Context(t.Context()),
			&oauth2clientspb.GetOAuth2ClientRequest{Oauth2ClientId: own})
		must.NoError(t, err, must.Sprint("the caller cannot read its own registration; the comparison below proves nothing"))

		_, crossRegistry := theirs.Surfaces.OAuth2Clients.GetOAuth2Client(ctx,
			&oauth2clientspb.GetOAuth2ClientRequest{Oauth2ClientId: own})
		must.Error(t, crossRegistry)

		_, neverMinted := theirs.Surfaces.OAuth2Clients.GetOAuth2Client(ctx,
			&oauth2clientspb.GetOAuth2ClientRequest{Oauth2ClientId: identifiers.New()})
		must.Error(t, neverMinted)

		test.EqOp(t, status.Code(neverMinted), status.Code(crossRegistry),
			test.Sprint("a registration elsewhere was answered differently from one that does not exist"))
	})
}
