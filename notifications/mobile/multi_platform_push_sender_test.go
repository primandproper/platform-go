package mobile

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/primandproper/platform-go/v2/notifications/mobile/apns"
	"github.com/primandproper/platform-go/v2/notifications/mobile/fcm"
	"github.com/primandproper/platform-go/v2/observability"
	loggingnoop "github.com/primandproper/platform-go/v2/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/v2/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// newRecordingSender builds a MultiPlatformPushSender with a RecordingObserver
// swapped in, so a test can both drive SendPush and assert which fields it observed.
func newRecordingSender(t *testing.T, apnsSender *apns.Sender, fcmSender *fcm.Sender) (*MultiPlatformPushSender, *observability.RecordingObserver) {
	t.Helper()

	s := NewMultiPlatformPushSender(apnsSender, fcmSender, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider())
	must.NotNil(t, s)

	obs := observability.NewRecordingObserver()
	s.o11y = obs

	return s, obs
}

// fakeServiceAccountJSON is a syntactically-valid Firebase service-account JSON.
const fakeServiceAccountJSON = `{"type":"service_account","project_id":"fake","private_key_id":"id","private_key":"-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQC7VJTUt9Us8cKj\nMzEfYyjiWA4R4/M2bS1GB4t7NXp98C3SC6dVMvDuictGeurT8jNbvJZHtCSuYEvu\nNMoSfm76oqFvAp8Gy0iz5sxjZmSnXyCdPEovGhLa0VzMaQ8s+CLOyS56YyCFGeJZ\nqgtzJ6GR3eqoYSW9b9UMvkBpZODSctWSNGj3P7jRFDO5VoTwCQAWbFnOjDfH5Ulg\np2PKSQnSJP3AJLQNFNe7br1XbrhV//eO+t51mIpGSDCUv3E0DDFcWDTH9cXDTTlR\nZVEiR2BwpZOOkE/Z0/BVnhZYL71oZV34bKfWjQIt6V/isSMahdsAASACp4ZTGtwi\nVuNd9tybAgMBAAECggEBAKTmjaS6tkK8BlPXClTQ2vpz/N6uxDeS35mXpqasqskV\nlaAidgg/sWqpjXDbXr93otIMLlWsM+X0CqMDgSXKejLS2jx4GDjI1ZTXg++0AMJ8\nsJ74pWzVDOfmCEQ/7wXs3+cbnXhKriO8Z036q92Qc1+N87SI38nkGa0ABH9CN83H\nmQqt4fB7UdHzuIRe/me2PGhIq5ZBzj6h3BpoPGzEP+x3l9YmK8t/1cN0pqI+dQwY\ndgfGjackLu/2qH80MCF7IyQaseZUOJyKrCLtSD/Iixv/hzDEUPfOCjFDgTpzf3cw\nta8+oE4wHCo1iI1/4TlPkwmXx4qSXtmw4aQPz7IDQvECgYEA8KNThCO2gsC2I9PQ\nDM/8Cw0O983WCDY+oi+7JPiNAJwv5DYBqEZB1QYdj06YD16XlC/HAZMsMku1na2T\nN0driwenQQWzoev3g2S7gRDoS/FCJSI3jJ+kjgtaA7Qmzlgk1TxODN+G1H91HW7t\n0l7VnL27IWyYo2qRRK3jzxqUiPUCgYEAx0oQs2reBQGMVZnApD1jeq7n4MvNLcPv\nt8b/eU9iUv6Y4Mj0Suo/AU8lYZXm8ubbqAlwz2VSVunD2tOplHyMUrtCtObAfVDU\nAhCndKaA9gApgfb3xw1IKbuQ1u4IF1FJl3VtumfQn//LiH1B3rXhcdyo3/vIttEk\n48RakUKClU8CgYEAzV7W3COOlDDcQd935DdtKBFRAPRPAlspQUnzMi5eSHMD/ISL\nDY5IiQHbIH83D4bvXq0X7qQoSBSNP7Dvv3HYuqMhf0DaegrlBuJllFVVq9qPVRnK\nxt1Il2HgxOBvbhOT+9in1BzA+YJ99UzC85O0Qz06A+CmtHEy4aZ2kj5hHjECgYEA\nmNS4+A8Fkss8Js1RieK2LniBxMgmYml3pfVLKGnzmng7H2+cwPLhPIzIuwytXywh\n2bzbsYEfYx3EoEVgMEpPhoarQnYPukrJO4gwE2o5Te6T5mJSZGlQJQj9q4ZB2Dfz\net6INsK0oG8XVGXSpQvQh3RUYekCZQkBBFcpqWpbIEsCgYAnM3DQf3FJoSnXaMhr\nVBIovic5l0xFkEHskAjFTevO86Fsz1C2aSeRKSqGFoOQ0tmJzBEs1R6KqnHInicD\nTQrKhArgLXX4v3CddjfTRJkFWDbE/CkvKZNOrcf1nhaGCPspRJj2KUkj1Fhl9Cnc\ndn/RsYEONbwQSjIfMPkvxF+8HQ==\n-----END PRIVATE KEY-----\n","client_email":"test@fake.iam.gserviceaccount.com","client_id":"1","auth_uri":"https://accounts.google.com/o/oauth2/auth","token_uri":"https://oauth2.googleapis.com/token","auth_provider_x509_cert_url":"https://www.googleapis.com/oauth2/v1/certs","client_x509_cert_url":"https://www.googleapis.com/robot/v1/metadata/x509/test%40fake.iam.gserviceaccount.com"}`

func createTestP8File(t *testing.T) string {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	must.NoError(t, err)

	keyBytes, err := x509.MarshalPKCS8PrivateKey(key)
	must.NoError(t, err)

	block := &pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: keyBytes,
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "AuthKey.p8")
	must.NoError(t, os.WriteFile(path, pem.EncodeToMemory(block), 0o600))
	return path
}

func createTestFCMCredsFile(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "fcm-creds.json")
	must.NoError(t, os.WriteFile(path, []byte(fakeServiceAccountJSON), 0o600))
	return path
}

func TestMultiPlatformPushSender_SendPush(T *testing.T) {
	T.Parallel()

	ctx := T.Context()
	logger := loggingnoop.NewLogger()
	tracer := tracingnoop.NewTracerProvider()

	T.Run("ios returns ErrPlatformNotSupported when apnsSender nil", func(t *testing.T) {
		t.Parallel()

		sender, obs := newRecordingSender(t, nil, nil)
		err := sender.SendPush(ctx, "ios", "token", PushMessage{Title: "title", Body: "body"})
		test.Error(t, err)
		test.ErrorIs(t, err, ErrPlatformNotSupported)

		op := obs.ObservedOperationWithData(t, map[string]any{"platform": "ios"})
		must.SliceLen(t, 1, op.Errors)
	})

	T.Run("android returns ErrPlatformNotSupported when fcmSender nil", func(t *testing.T) {
		t.Parallel()

		sender, obs := newRecordingSender(t, nil, nil)
		err := sender.SendPush(ctx, "android", "token", PushMessage{Title: "title", Body: "body"})
		test.Error(t, err)
		test.ErrorIs(t, err, ErrPlatformNotSupported)

		op := obs.ObservedOperationWithData(t, map[string]any{"platform": "android"})
		must.SliceLen(t, 1, op.Errors)
	})

	T.Run("unknown platform returns error", func(t *testing.T) {
		t.Parallel()

		sender, obs := newRecordingSender(t, nil, nil)
		err := sender.SendPush(ctx, "unknown", "token", PushMessage{Title: "title", Body: "body"})
		test.Error(t, err)
		test.StrContains(t, err.Error(), "unknown platform")

		op := obs.ObservedOperationWithData(t, map[string]any{"platform": "unknown"})
		must.SliceLen(t, 1, op.Errors)
	})

	T.Run("ios delegates to apns sender", func(t *testing.T) {
		t.Parallel()

		p8Path := createTestP8File(t)
		apnsCfg := &apns.Config{
			AuthKeyPath: p8Path,
			KeyID:       "KEY123",
			TeamID:      "TEAM123",
			BundleID:    "com.example.app",
		}
		apnsSender, err := apns.NewSender(apnsCfg, tracer, logger, nil)
		must.NoError(t, err)

		sender := NewMultiPlatformPushSender(apnsSender, nil, logger, tracer)
		err = sender.SendPush(ctx, "ios", "not-a-valid-token", PushMessage{Title: "title", Body: "body"})
		// The apns sender will reject the token format, but the delegation code path is covered.
		test.Error(t, err)
		test.True(t, !errors.Is(err, ErrPlatformNotSupported))
	})

	T.Run("android delegates to fcm sender", func(t *testing.T) {
		t.Parallel()

		credsPath := createTestFCMCredsFile(t)
		fcmCfg := &fcm.Config{CredentialsPath: credsPath}
		fcmSender, err := fcm.NewSender(ctx, fcmCfg, tracer, logger, nil)
		must.NoError(t, err)

		sender := NewMultiPlatformPushSender(nil, fcmSender, logger, tracer)
		err = sender.SendPush(ctx, "android", "device-token-abc", PushMessage{Title: "title", Body: "body"})
		// The fcm sender will fail at the HTTP level, but the delegation code path is covered.
		test.Error(t, err)
		test.True(t, !errors.Is(err, ErrPlatformNotSupported))
	})
}
