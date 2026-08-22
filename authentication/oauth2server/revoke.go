package oauth2server

import (
	"context"
	stderrors "errors"
	"net/http"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// RevokeHandler serves POST /revoke: RFC 7009 token revocation.
//
// The endpoint answers 200 for a token it revoked, a token it has never seen,
// and a token that was already dead. That is RFC 7009 §2.2, and the reason is
// worth stating because the alternative looks more helpful: an endpoint that
// answered 404 for an unknown token would let anybody enumerate which tokens
// exist by sending guesses at it.
//
// The client is authenticated first, and a token belonging to another client is
// treated as unknown. Without that, any registered client could revoke any
// other client's tokens by presenting them — an endpoint whose entire purpose
// is destructive and whose success is unverifiable from outside.
func (s *Server) RevokeHandler() http.Handler {
	return http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		ctx, op := s.o11y.BeginCustom(req.Context(), operationName(endpointRevoke))
		defer s.end(ctx, op, endpointRevoke, s.clock.Now())

		s.ops.Attempt(ctx, metric.WithAttributes(attribute.String(endpointKey, endpointRevoke)))

		if err := req.ParseForm(); err != nil {
			writeProtocolError(res, s.fail(ctx, op, endpointRevoke,
				newProtocolError(http.StatusBadRequest, ErrorCodeInvalidRequest, "malformed request", err)))

			return
		}

		client, perr := s.authenticateClient(ctx, req)
		if perr != nil {
			writeProtocolError(res, s.fail(ctx, op, endpointRevoke, perr))

			return
		}

		op.Set(clientIDKey, client.ID)

		value := req.Form.Get(paramToken)
		if value == "" {
			// The one thing that is a request error rather than a silent 200:
			// there is nothing here to revoke, and answering 200 would tell a
			// client its sign-out worked when it never asked for one.
			writeProtocolError(res, s.fail(ctx, op, endpointRevoke,
				newProtocolError(http.StatusBadRequest, ErrorCodeInvalidRequest, "token is required", ErrEmptyIdentifier)))

			return
		}

		s.revoke(ctx, op, client, Hash(value), req.Form.Get(paramTokenTypeHint))

		// No body. RFC 7009 §2.2 specifies an empty 200, and there is nothing
		// to say: the client cannot be told whether the token existed.
		res.Header().Set("Cache-Control", "no-store")
		res.WriteHeader(http.StatusOK)
	})
}

// revoke ends whatever the presented digest names, if it belongs to client.
//
// The hint is an optimization and nothing more, exactly as RFC 7009 §2.1 says:
// a wrong hint costs a second lookup rather than a failed revocation, because a
// client that mislabels its own token still meant to revoke it.
//
// Revoking a refresh token takes its whole family with it, which is the
// behavior a sign-out needs: the access token minted alongside it would
// otherwise keep working for the rest of its lifetime, and a user who signs out
// is asking for the session to be over now.
func (s *Server) revoke(ctx context.Context, op observability.Operation, client *Client, digest, hint string) {
	order := []string{hintAccessToken, hintRefreshToken}
	if hint == hintRefreshToken {
		order = []string{hintRefreshToken, hintAccessToken}
	}

	for _, kind := range order {
		if s.revokeOne(ctx, op, client, digest, kind) {
			return
		}
	}
}

// revokeOne revokes one kind of token, reporting whether it found one.
func (s *Server) revokeOne(ctx context.Context, op observability.Operation, client *Client, digest, kind string) bool {
	if kind == hintRefreshToken {
		// Read rather than consumed. Consuming would mark the token redeemed,
		// so an honest rotation racing this sign-out would then look like a
		// replay — the endpoint that ends a session must not manufacture
		// evidence of an attack on the way past.
		token, err := s.store.GetRefreshToken(ctx, digest)
		if err != nil {
			s.acknowledgeRevokeFailure(op, err, "reading refresh token for revocation")

			return false
		}

		if token.ClientID != client.ID {
			return false
		}

		// Reported to the observer once for the family rather than once per
		// record, and only when something was actually removed: a family the
		// store could not write is a sign-out the deployment did not get.
		if s.revokeFamily(ctx, op, token.FamilyID, "token revoked by its client") > 0 {
			s.observeRevocation(ctx, op, token.Subject, token.FamilyID)
		}

		return true
	}

	token, err := s.store.GetAccessToken(ctx, digest)
	if err != nil {
		s.acknowledgeRevokeFailure(op, err, "reading access token for revocation")

		return false
	}

	if token.ClientID != client.ID {
		return false
	}

	if err = s.store.RevokeAccessToken(ctx, digest); err != nil {
		op.Acknowledge(err, "revoking access token")

		return true
	}

	s.revocations.Add(ctx, 1)
	op.Set(familyIDKey, token.FamilyID)
	s.observeRevocation(ctx, op, token.Subject, token.FamilyID)

	return true
}

// observeRevocation hands a completed revocation to the deployment's observer,
// if it declared one.
//
// The panic is recovered for the same reason metering recovers around a
// provider SDK: this is consumer code, it runs after the records are already
// gone, and a failing analytics callback turning a successful sign-out into a
// dropped connection would have the client retry a revocation that worked.
func (s *Server) observeRevocation(ctx context.Context, op observability.Operation, subject Subject, familyID string) {
	if s.revocationObserver == nil {
		return
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			op.Acknowledge(platformerrors.Newf("%v", recovered), "revocation observer panicked")
		}
	}()

	s.revocationObserver(ctx, subject, familyID)
}

// acknowledgeRevokeFailure records a store failure during revocation without
// turning it into a response.
//
// An absent token is not a failure — it is most of what this endpoint is sent —
// so only a genuinely broken store is recorded. The client is answered 200
// either way, because RFC 7009 gives it no other answer, which makes the
// operation record the only place the failure can show up at all.
func (s *Server) acknowledgeRevokeFailure(op observability.Operation, err error, description string) {
	if stderrors.Is(err, ErrNotFound) {
		return
	}

	op.Acknowledge(err, "%s", description)
}
