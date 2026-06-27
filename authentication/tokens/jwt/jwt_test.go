package jwt

import (
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/primandproper/platform-go/authentication/tokens"
	"github.com/primandproper/platform-go/observability"
	loggingnoop "github.com/primandproper/platform-go/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/observability/tracing/noop"

	"github.com/golang-jwt/jwt/v5"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// newRecordingSigner builds a signer with a RecordingObserver swapped in, so a
// test can both drive the signer and assert what it observed.
func newRecordingSigner(t *testing.T) (*signer, *observability.RecordingObserver) {
	t.Helper()

	issuer, err := NewJWTSigner(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), "platform-test", t.Name(), []byte(exampleSigningKey))
	must.NoError(t, err)

	s := issuer.(*signer)

	obs := observability.NewRecordingObserver()
	s.o11y = obs

	return s, obs
}

const (
	exampleSigningKey = "HEREISA32CHARSECRETWHICHISMADEUP"
	ed25519SigningKey = "HEREISA64CHARSECRETWHICHISMADEUPHEREISA64CHARSECRETWHICHISMADEUP"
	exampleToken      = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJhdWQiOiJUZXN0X2p3dFNpZ25lcl9Jc3N1ZUpXVC9zdGFuZGFyZCIsImV4cCI6MTcyNzU3MDU0OCwiaWF0IjoxNzI3NTY5OTQ4LCJpc3MiOiJkaW5uZXJkb25lYmV0dGVyIiwianRpIjoiY3JzYTA3NnRnM3FkdG1jY3E5MTAiLCJuYmYiOjE3Mjc1Njk4ODgsInN1YiI6ImNyc2EwNzZ0ZzNxZHRtY2NxOTBnIn0.tMASrQBoYAq4n1iwOElLqUQsYOARX5T1qxo8RKhvaAg"
	exampleExpiry     = time.Minute * 10
	exampleSubject    = "user_id"
)

func Test_signer_IssueJWT(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		s, err := NewJWTSigner(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), "platform-test", t.Name(), []byte(exampleSigningKey))
		must.NoError(t, err)

		ctx := t.Context()

		actual, _, err := s.IssueToken(ctx, exampleSubject, exampleExpiry, nil)
		test.NoError(t, err)

		claims, err := s.ParseToken(ctx, actual)
		test.NoError(t, err)
		test.EqOp(t, exampleSubject, claims.Subject())
	})

	T.Run("with extra claims", func(t *testing.T) {
		t.Parallel()

		s, err := NewJWTSigner(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), "platform-test", t.Name(), []byte(exampleSigningKey))
		must.NoError(t, err)

		ctx := t.Context()

		accountID := "account_123"
		sessionID := "session_456"

		tokenStr, jti, err := s.IssueToken(ctx, exampleSubject, exampleExpiry, map[string]any{
			"account_id": accountID,
			"sid":        sessionID,
		})
		test.NoError(t, err)
		test.NotEq(t, "", tokenStr)
		test.NotEq(t, "", jti)

		claims, err := s.ParseToken(ctx, tokenStr)
		must.NoError(t, err)

		test.EqOp(t, exampleSubject, claims.Subject())
		test.EqOp(t, jti, claims.JTI())
		test.False(t, claims.ExpiresAt().IsZero())

		gotAccount, ok := claims.GetString("account_id")
		test.True(t, ok)
		test.EqOp(t, accountID, gotAccount)

		gotSession, ok := claims.GetString("sid")
		test.True(t, ok)
		test.EqOp(t, sessionID, gotSession)

		raw, ok := claims.Get("account_id")
		test.True(t, ok)
		test.Eq(t, any(accountID), raw)
	})

	T.Run("rejects reserved claim key", func(t *testing.T) {
		t.Parallel()

		s, err := NewJWTSigner(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), "platform-test", t.Name(), []byte(exampleSigningKey))
		must.NoError(t, err)

		_, _, err = s.IssueToken(t.Context(), exampleSubject, exampleExpiry, map[string]any{
			"sub": "attacker_id",
		})
		test.ErrorIs(t, err, tokens.ErrReservedClaim)
	})
}

func Test_signer_ParseToken(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		s, err := NewJWTSigner(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), "platform-test", t.Name(), []byte(exampleSigningKey))
		must.NoError(t, err)

		ctx := t.Context()

		issuedToken, _, err := s.IssueToken(ctx, exampleSubject, exampleExpiry, nil)
		test.NoError(t, err)

		claims, err := s.ParseToken(ctx, issuedToken)
		test.NoError(t, err)
		test.EqOp(t, exampleSubject, claims.Subject())
	})

	T.Run("with invalid algo", func(t *testing.T) {
		t.Parallel()

		token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.MapClaims{})

		cryptoSigner := ed25519.PrivateKey(ed25519SigningKey)
		tokenString, err := token.SignedString(cryptoSigner)
		must.NoError(t, err)

		ctx := t.Context()

		s, obs := newRecordingSigner(t)

		claims, err := s.ParseToken(ctx, tokenString)
		test.Error(t, err)
		test.Nil(t, claims)

		op := obs.ObservedOperationWithKeys(t)
		must.SliceLen(t, 1, op.Errors)
	})

	T.Run("with invalid key", func(t *testing.T) {
		t.Parallel()

		s, obs := newRecordingSigner(t)

		s.signingKey = nil

		ctx := t.Context()

		claims, err := s.ParseToken(ctx, exampleToken)
		test.Error(t, err)
		test.Nil(t, claims)

		op := obs.ObservedOperationWithKeys(t)
		must.SliceLen(t, 1, op.Errors)
	})

	T.Run("missing optional claim returns empty string", func(t *testing.T) {
		t.Parallel()

		s, err := NewJWTSigner(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), "platform-test", t.Name(), []byte(exampleSigningKey))
		must.NoError(t, err)

		ctx := t.Context()

		tokenStr, _, err := s.IssueToken(ctx, exampleSubject, exampleExpiry, nil)
		must.NoError(t, err)

		claims, err := s.ParseToken(ctx, tokenStr)
		must.NoError(t, err)

		gotSession, ok := claims.GetString("sid")
		test.False(t, ok)
		test.EqOp(t, "", gotSession)

		raw, ok := claims.Get("sid")
		test.False(t, ok)
		test.Nil(t, raw)
	})
}
