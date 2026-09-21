package grpc

import (
	"context"

	"github.com/primandproper/platform-go/v14/callers"
)

// ContactResolver decides where a Join's contact address comes from, for a
// deployment that does not want it stated on the wire.
//
// # The two deployments this surface serves, and why only one is safe by default
//
// Join is in [PublicMethods] because a pre-launch waitlist is joined by people
// who have no account yet, and an anonymous visitor has no session to derive an
// address from — so the address is in the request, and anybody may name any
// address. That is what a signup form is, and double opt-in rather than this
// interface is what makes it honest.
//
// A deployment that instead mounts Join behind a grant of its own has a signed-in
// caller and a different problem: with the address still read off the wire, any
// authenticated caller may sign any address up for somebody else. Until this
// interface there was no way to say "the address is the caller's", and a consumer
// who noticed wrote a decorator that overwrote the field before the handler saw
// it. That decorator is this.
//
// # What it does not defend
//
// It does not make the refusals louder, because they are quiet on purpose.
// ErrAlreadySignedUp and ErrContactWithdrawn are swallowed by every Join and
// answered as success — see quietJoinOutcome — so that a caller who can name an
// address cannot use the reply to learn whether that address is already on a
// list or has asked to be left alone. A resolver narrows who may name an
// address; the quiet outcomes are what keep naming one uninformative, and they
// hold with or without one.
//
// # Absent means the wire
//
// A server built without one reads the contact from the request, which is the
// behavior every consumer had before this existed and the right one for the
// public form. Supplying one is the deployment saying its Join is not that.
type ContactResolver interface {
	// ResolveContact answers with the contact address a join should record.
	//
	// caller is nil for an anonymous request. stated is what the request
	// carried, so an implementation may fall back to it, refuse when it
	// disagrees with the caller's own address, or ignore it entirely — which is
	// the ordinary implementation and the reason this exists.
	//
	// A nil error and an empty address is a join with no contact, which the
	// store refuses as it refuses any other. An error refuses the join and
	// reaches the client through the same door a failed authorization does.
	ResolveContact(ctx context.Context, caller callers.Principal, stated string) (string, error)
}

// ContactResolverFunc adapts a function to [ContactResolver], for a consumer
// whose rule is one closure over the session they already hold.
//
//	waitlistsgrpc.WithContactResolver(
//		waitlistsgrpc.ContactResolverFunc(func(_ context.Context, caller callers.Principal, _ string) (string, error) {
//			return emailOf(caller), nil
//		}))
type ContactResolverFunc func(ctx context.Context, caller callers.Principal, stated string) (string, error)

var _ ContactResolver = ContactResolverFunc(nil)

// ResolveContact satisfies [ContactResolver].
func (f ContactResolverFunc) ResolveContact(
	ctx context.Context,
	caller callers.Principal,
	stated string,
) (string, error) {
	return f(ctx, caller, stated)
}

// resolveContact applies the configured resolver, or leaves the stated address
// alone when there is none.
func (s *Server) resolveContact(
	ctx context.Context,
	caller callers.Principal,
	stated string,
) (string, error) {
	if s.contacts == nil {
		return stated, nil
	}

	return s.contacts.ResolveContact(ctx, caller, stated)
}
