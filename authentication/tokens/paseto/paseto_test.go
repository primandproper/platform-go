package paseto

import (
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v2/authentication/tokens"
	"github.com/primandproper/platform-go/v2/observability"
	loggingnoop "github.com/primandproper/platform-go/v2/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/v2/observability/tracing/noop"

	"github.com/golang-jwt/jwt/v5"
	"github.com/o1egl/paseto/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// craftToken encrypts an arbitrary claim set with the signer's key, bypassing
// IssueToken's claim ownership so a test can forge expired / wrong-audience
// tokens that still authenticate.
func craftToken(t *testing.T, s *signer, claims map[string]any) string {
	t.Helper()

	tokenStr, err := paseto.NewV2().Encrypt(s.signingKey, claims, "footer")
	must.NoError(t, err)

	return tokenStr
}

// validClaims returns a fully-valid claim set for s that individual tests
// mutate one field at a time to isolate each validation rule.
func validClaims(s *signer) map[string]any {
	now := time.Now().UTC()
	return map[string]any{
		"aud": s.audience,
		"iss": s.issuer,
		"sub": exampleSubject,
		"jti": "jti_123",
		"iat": now,
		"nbf": now.Add(-time.Minute),
		"exp": now.Add(exampleExpiry),
	}
}

const (
	exampleSigningKey = "HEREISA32CHARSECRETWHICHISMADEUP"
	ed25519SigningKey = "HEREISA64CHARSECRETWHICHISMADEUPHEREISA64CHARSECRETWHICHISMADEUP"
	exampleToken      = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJhdWQiOiJUZXN0X2p3dFNpZ25lcl9Jc3N1ZUpXVC9zdGFuZGFyZCIsImV4cCI6MTcyNzU3MDU0OCwiaWF0IjoxNzI3NTY5OTQ4LCJpc3MiOiJkaW5uZXJkb25lYmV0dGVyIiwianRpIjoiY3JzYTA3NnRnM3FkdG1jY3E5MTAiLCJuYmYiOjE3Mjc1Njk4ODgsInN1YiI6ImNyc2EwNzZ0ZzNxZHRtY2NxOTBnIn0.tMASrQBoYAq4n1iwOElLqUQsYOARX5T1qxo8RKhvaAg"
	exampleExpiry     = time.Minute * 10
	exampleSubject    = "user_id"
)

// newRecordingSigner builds a signer with a RecordingObserver swapped in, so a
// test can drive its methods and assert what it observed.
func newRecordingSigner(t *testing.T, signingKey []byte) (*signer, *observability.RecordingObserver) {
	t.Helper()

	issuer, err := NewPASETOSigner(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), "platform-test", t.Name(), signingKey)
	must.NoError(t, err)

	s, ok := issuer.(*signer)
	must.True(t, ok)

	obs := observability.NewRecordingObserver()
	s.o11y = obs

	return s, obs
}

func Test_signer_IssueToken(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		s, err := NewPASETOSigner(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), "platform-test", t.Name(), []byte(exampleSigningKey))
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

		s, err := NewPASETOSigner(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), "platform-test", t.Name(), []byte(exampleSigningKey))
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

		s, err := NewPASETOSigner(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), "platform-test", t.Name(), []byte(exampleSigningKey))
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

		s, err := NewPASETOSigner(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), "platform-test", t.Name(), []byte(exampleSigningKey))
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

		s, obs := newRecordingSigner(t, []byte(exampleSigningKey))

		claims, err := s.ParseToken(ctx, tokenString)
		test.Error(t, err)
		test.Nil(t, claims)

		// The decryption failure must have been recorded on the operation.
		op := obs.ObservedOperationWithKeys(t)
		must.SliceLen(t, 1, op.Errors)
	})

	T.Run("with invalid key", func(t *testing.T) {
		t.Parallel()

		s, obs := newRecordingSigner(t, nil)

		ctx := t.Context()

		claims, err := s.ParseToken(ctx, exampleToken)
		test.Error(t, err)
		test.Nil(t, claims)

		op := obs.ObservedOperationWithKeys(t)
		must.SliceLen(t, 1, op.Errors)
	})

	T.Run("missing optional claim returns empty string", func(t *testing.T) {
		t.Parallel()

		s, err := NewPASETOSigner(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), "platform-test", t.Name(), []byte(exampleSigningKey))
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

	// Regression: decrypting into a map performs authenticated decryption only,
	// so an expired token would otherwise parse cleanly with no error.
	T.Run("rejects expired token", func(t *testing.T) {
		t.Parallel()

		s, _ := newRecordingSigner(t, []byte(exampleSigningKey))

		claims := validClaims(s)
		claims["exp"] = time.Now().Add(-time.Hour).UTC()

		parsed, err := s.ParseToken(t.Context(), craftToken(t, s, claims))
		test.ErrorIs(t, err, tokens.ErrTokenExpired)
		test.Nil(t, parsed)
	})

	T.Run("rejects not-yet-valid token", func(t *testing.T) {
		t.Parallel()

		s, _ := newRecordingSigner(t, []byte(exampleSigningKey))

		claims := validClaims(s)
		claims["nbf"] = time.Now().Add(time.Hour).UTC()

		parsed, err := s.ParseToken(t.Context(), craftToken(t, s, claims))
		test.ErrorIs(t, err, tokens.ErrTokenNotYetValid)
		test.Nil(t, parsed)
	})

	T.Run("rejects mismatched audience", func(t *testing.T) {
		t.Parallel()

		s, _ := newRecordingSigner(t, []byte(exampleSigningKey))

		claims := validClaims(s)
		claims["aud"] = "some-other-service"

		parsed, err := s.ParseToken(t.Context(), craftToken(t, s, claims))
		test.ErrorIs(t, err, tokens.ErrInvalidAudience)
		test.Nil(t, parsed)
	})

	T.Run("rejects mismatched issuer", func(t *testing.T) {
		t.Parallel()

		s, _ := newRecordingSigner(t, []byte(exampleSigningKey))

		claims := validClaims(s)
		claims["iss"] = "some-other-issuer"

		parsed, err := s.ParseToken(t.Context(), craftToken(t, s, claims))
		test.ErrorIs(t, err, tokens.ErrInvalidIssuer)
		test.Nil(t, parsed)
	})

	// A freshly crafted, fully-valid token must still pass, proving the
	// validation above does not reject legitimate tokens.
	T.Run("accepts a valid crafted token", func(t *testing.T) {
		t.Parallel()

		s, _ := newRecordingSigner(t, []byte(exampleSigningKey))

		parsed, err := s.ParseToken(t.Context(), craftToken(t, s, validClaims(s)))
		test.NoError(t, err)
		must.NotNil(t, parsed)
		test.EqOp(t, exampleSubject, parsed.Subject())
	})
}
