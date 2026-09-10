package grpc_test

import (
	"context"
	"testing"
	"time"

	identitygrpc "github.com/primandproper/platform-go/v14/identity/grpc"
	"github.com/primandproper/platform-go/v14/identity/identitypb"

	"github.com/primandproper/primitives-go/observability"
	lognoop "github.com/primandproper/primitives-go/observability/logging/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// TestWithPillarsSuppliesAllThreeAndOrderDecides is the property every config
// subpackage's WithPillars shares: it assigns the three at once, and because
// options apply in order a later WithMetricsProvider(nil) leaves this one
// component unmetered without touching the other two.
func TestWithPillarsSuppliesAllThreeAndOrderDecides(T *testing.T) {
	T.Parallel()

	recorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	T.Cleanup(func() { must.NoError(T, tracerProvider.Shutdown(context.Background())) })

	instruments := &recordingInstruments{}

	pillars := &observability.Pillars{
		Logger:          lognoop.NewLogger(),
		TracerProvider:  tracerProvider,
		MetricsProvider: instruments.provider(),
	}

	h := newHarness(T,
		identitygrpc.WithPillars(pillars),
		identitygrpc.WithMetricsProvider(nil),
	)

	user := h.seedUser(T, testScope, "somebody")

	_, err := h.client.GetUser(h.ctx(), &identitypb.GetUserRequest{UserId: user.ID})
	must.NoError(T, err)

	test.SliceNotEmpty(T, recorder.Ended(),
		test.Sprint("the tracer provider WithPillars supplied recorded nothing"))
	test.EqOp(T, int64(0), instruments.requests.Load(),
		test.Sprint("the metrics provider a later option set to nil was used anyway"))
}

// TestWithLoggerIsAccepted covers the seam rather than the output: what a logger
// writes is logging's to test, and what matters here is that a server built with
// one still answers.
func TestWithLoggerIsAccepted(T *testing.T) {
	T.Parallel()

	h := newHarness(T, identitygrpc.WithLogger(lognoop.NewLogger()))

	user := h.seedUser(T, testScope, "somebody")

	found, err := h.client.GetUser(h.ctx(), &identitypb.GetUserRequest{UserId: user.ID})
	must.NoError(T, err)
	test.EqOp(T, "somebody", found.GetUser().GetUsername())
}

// TestWithInvitationTTLIgnoresANonPositiveDuration: a zero or negative TTL is an
// invitation that has already expired, which is the one answer worse than the
// default it would replace.
func TestWithInvitationTTLIgnoresANonPositiveDuration(T *testing.T) {
	T.Parallel()

	h := newHarness(T,
		identitygrpc.WithTokenMinter(fixedMinter(testInvitationToken)),
		identitygrpc.WithInvitationTTL(-time.Hour),
	)

	sender := h.seedAccount(T, testScope, "sender")

	before := time.Now().UTC()
	invitation := invite(T, h, sender, "invitee@example.com", "support")

	expiry := invitation.GetExpiresAt().AsTime()
	test.True(T, expiry.After(before), test.Sprintf(
		"a negative TTL was taken, and the invitation expired at %s", expiry))
	test.True(T, expiry.After(before.Add(identitygrpc.DefaultInvitationTTL).Add(-time.Minute)),
		test.Sprintf("a negative TTL displaced DefaultInvitationTTL, leaving an expiry of %s", expiry))
}

// TestTheDefaultMinterIsUnguessable is what WithTokenMinter(nil) falls back to,
// and it is not a policy choice: a guessable invitation token is an account
// takeover.
func TestTheDefaultMinterIsUnguessable(T *testing.T) {
	T.Parallel()

	h := newHarness(T, identitygrpc.WithTokenMinter(nil))

	sender := h.seedAccount(T, testScope, "sender")

	first := invite(T, h, sender, "first@example.com", "support")
	second := invite(T, h, sender, "second@example.com", "support")

	tokens := make([]string, 0, 2)

	for _, id := range []string{first.GetId(), second.GetId()} {
		stored, err := h.store.GetInvitation(T.Context(), h.db.Reader(), testScope, id)
		must.NoError(T, err)
		must.StrNotEqFold(T, "", stored.Token, must.Sprint("an invitation was written with no token"))

		// Thirty-two bytes of CSPRNG, base64-encoded. The assertion is on the
		// floor rather than the exact length so an encoding change does not read
		// as a security regression, but a token short enough to guess does.
		test.True(T, len(stored.Token) >= 32,
			test.Sprintf("an invitation token of %d characters is short enough to guess", len(stored.Token)))

		tokens = append(tokens, stored.Token)
	}

	test.NotEqOp(T, tokens[0], tokens[1],
		test.Sprint("two invitations were minted the same token"))
}

// TestNewServerRefusesADefaultTTLBeyondTheMaximum: a server whose default
// lifetime is longer than the ceiling it holds clients to would issue, on a
// request naming nothing, an invitation it would have refused the request for
// naming. It is refused at construction whichever order the two options came.
func TestNewServerRefusesADefaultTTLBeyondTheMaximum(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	_, err := identitygrpc.NewServer(h.svc, h.store, h.db, extractPrincipal,
		identitygrpc.WithMaxInvitationTTL(time.Hour),
		identitygrpc.WithInvitationTTL(2*time.Hour),
	)
	test.ErrorIs(T, err, identitygrpc.ErrInvitationTTLExceedsMaximum)

	_, err = identitygrpc.NewServer(h.svc, h.store, h.db, extractPrincipal,
		identitygrpc.WithInvitationTTL(2*time.Hour),
		identitygrpc.WithMaxInvitationTTL(time.Hour),
	)
	test.ErrorIs(T, err, identitygrpc.ErrInvitationTTLExceedsMaximum)

	// A non-positive maximum is ignored the way a non-positive default is.
	srv, err := identitygrpc.NewServer(h.svc, h.store, h.db, extractPrincipal,
		identitygrpc.WithMaxInvitationTTL(-time.Hour),
	)
	must.NoError(T, err)
	test.NotNil(T, srv)
}
