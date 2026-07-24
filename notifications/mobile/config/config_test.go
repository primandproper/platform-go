package config

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"github.com/primandproper/platform-go/v6/notifications/mobile"
	loggingnoop "github.com/primandproper/platform-go/v6/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/v6/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

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

// fakeServiceAccountJSON is a syntactically-valid Firebase service-account JSON
// with a fake private key. firebase.NewApp accepts it for client construction
// without making network calls; it never actually authenticates anything.
const fakeServiceAccountJSON = `{"type":"service_account","project_id":"fake","private_key_id":"id","private_key":"-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQC7VJTUt9Us8cKj\nMzEfYyjiWA4R4/M2bS1GB4t7NXp98C3SC6dVMvDuictGeurT8jNbvJZHtCSuYEvu\nNMoSfm76oqFvAp8Gy0iz5sxjZmSnXyCdPEovGhLa0VzMaQ8s+CLOyS56YyCFGeJZ\nqgtzJ6GR3eqoYSW9b9UMvkBpZODSctWSNGj3P7jRFDO5VoTwCQAWbFnOjDfH5Ulg\np2PKSQnSJP3AJLQNFNe7br1XbrhV//eO+t51mIpGSDCUv3E0DDFcWDTH9cXDTTlR\nZVEiR2BwpZOOkE/Z0/BVnhZYL71oZV34bKfWjQIt6V/isSMahdsAASACp4ZTGtwi\nVuNd9tybAgMBAAECggEBAKTmjaS6tkK8BlPXClTQ2vpz/N6uxDeS35mXpqasqskV\nlaAidgg/sWqpjXDbXr93otIMLlWsM+X0CqMDgSXKejLS2jx4GDjI1ZTXg++0AMJ8\nsJ74pWzVDOfmCEQ/7wXs3+cbnXhKriO8Z036q92Qc1+N87SI38nkGa0ABH9CN83H\nmQqt4fB7UdHzuIRe/me2PGhIq5ZBzj6h3BpoPGzEP+x3l9YmK8t/1cN0pqI+dQwY\ndgfGjackLu/2qH80MCF7IyQaseZUOJyKrCLtSD/Iixv/hzDEUPfOCjFDgTpzf3cw\nta8+oE4wHCo1iI1/4TlPkwmXx4qSXtmw4aQPz7IDQvECgYEA8KNThCO2gsC2I9PQ\nDM/8Cw0O983WCDY+oi+7JPiNAJwv5DYBqEZB1QYdj06YD16XlC/HAZMsMku1na2T\nN0driwenQQWzoev3g2S7gRDoS/FCJSI3jJ+kjgtaA7Qmzlgk1TxODN+G1H91HW7t\n0l7VnL27IWyYo2qRRK3jzxqUiPUCgYEAx0oQs2reBQGMVZnApD1jeq7n4MvNLcPv\nt8b/eU9iUv6Y4Mj0Suo/AU8lYZXm8ubbqAlwz2VSVunD2tOplHyMUrtCtObAfVDU\nAhCndKaA9gApgfb3xw1IKbuQ1u4IF1FJl3VtumfQn//LiH1B3rXhcdyo3/vIttEk\n48RakUKClU8CgYEAzV7W3COOlDDcQd935DdtKBFRAPRPAlspQUnzMi5eSHMD/ISL\nDY5IiQHbIH83D4bvXq0X7qQoSBSNP7Dvv3HYuqMhf0DaegrlBuJllFVVq9qPVRnK\nxt1Il2HgxOBvbhOT+9in1BzA+YJ99UzC85O0Qz06A+CmtHEy4aZ2kj5hHjECgYEA\nmNS4+A8Fkss8Js1RieK2LniBxMgmYml3pfVLKGnzmng7H2+cwPLhPIzIuwytXywh\n2bzbsYEfYx3EoEVgMEpPhoarQnYPukrJO4gwE2o5Te6T5mJSZGlQJQj9q4ZB2Dfz\net6INsK0oG8XVGXSpQvQh3RUYekCZQkBBFcpqWpbIEsCgYAnM3DQf3FJoSnXaMhr\nVBIovic5l0xFkEHskAjFTevO86Fsz1C2aSeRKSqGFoOQ0tmJzBEs1R6KqnHInicD\nTQrKhArgLXX4v3CddjfTRJkFWDbE/CkvKZNOrcf1nhaGCPspRJj2KUkj1Fhl9Cnc\ndn/RsYEONbwQSjIfMPkvxF+8HQ==\n-----END PRIVATE KEY-----\n","client_email":"test@fake.iam.gserviceaccount.com","client_id":"1","auth_uri":"https://accounts.google.com/o/oauth2/auth","token_uri":"https://oauth2.googleapis.com/token","auth_provider_x509_cert_url":"https://www.googleapis.com/oauth2/v1/certs","client_x509_cert_url":"https://www.googleapis.com/robot/v1/metadata/x509/test%40fake.iam.gserviceaccount.com"}`

func createTestFCMCredsFile(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "fcm-creds.json")
	must.NoError(t, os.WriteFile(path, []byte(fakeServiceAccountJSON), 0o600))
	return path
}

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	ctx := T.Context()

	T.Run("with noop provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderNoop}
		test.NoError(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("with empty provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ""}
		test.NoError(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("with apns_fcm provider and both nil", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderAPNsFCM,
			APNs:     nil,
			FCM:      nil,
		}
		test.Error(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("with apns_fcm provider and nil APNs but FCM present", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderAPNsFCM,
			APNs:     nil,
			FCM:      &FCMConfig{},
		}
		test.NoError(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("with apns_fcm provider and nil FCM but APNs present", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderAPNsFCM,
			APNs:     &APNsConfig{AuthKeyPath: "x", KeyID: "x", TeamID: "x", BundleID: "x"},
			FCM:      nil,
		}
		test.NoError(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("with apns_fcm provider and both configs", func(t *testing.T) {
		t.Parallel()

		p8Path := createTestP8File(t)
		cfg := &Config{
			Provider: ProviderAPNsFCM,
			APNs:     &APNsConfig{AuthKeyPath: p8Path, KeyID: "x", TeamID: "x", BundleID: "x"},
			FCM:      &FCMConfig{},
		}
		test.NoError(t, cfg.ValidateWithContext(ctx))
	})
}

func TestConfig_NewPushSender(T *testing.T) {
	T.Parallel()

	ctx := T.Context()
	logger := loggingnoop.NewLogger()
	tracer := tracingnoop.NewTracerProvider()

	T.Run("with empty provider returns noop", func(t *testing.T) {
		t.Parallel()

		cfg := Config{Provider: ""}
		sender, err := cfg.NewPushSender(ctx, logger, tracer, nil)
		must.NoError(t, err)
		must.NotNil(t, sender)
		// Noop returns nil on SendPush
		test.NoError(t, sender.SendPush(ctx, "ios", "token", mobile.PushMessage{Title: "title", Body: "body"}))
	})

	T.Run("with noop provider returns noop", func(t *testing.T) {
		t.Parallel()

		cfg := Config{Provider: ProviderNoop}
		sender, err := cfg.NewPushSender(ctx, logger, tracer, nil)
		must.NoError(t, err)
		must.NotNil(t, sender)
		test.NoError(t, sender.SendPush(ctx, "android", "token", mobile.PushMessage{Title: "title", Body: "body"}))
	})

	T.Run("with apns_fcm provider and nil APNs returns FCM-only sender", func(t *testing.T) {
		t.Parallel()

		credsPath := createTestFCMCredsFile(t)
		cfg := Config{
			Provider: ProviderAPNsFCM,
			APNs:     nil,
			FCM:      &FCMConfig{CredentialsPath: credsPath},
		}
		sender, err := cfg.NewPushSender(ctx, logger, tracer, nil)
		must.NoError(t, err)
		must.NotNil(t, sender)
		// iOS is not configured, so it should report as unsupported.
		err = sender.SendPush(ctx, "ios", "token", mobile.PushMessage{Title: "title", Body: "body"})
		test.Error(t, err)
		test.ErrorIs(t, err, mobile.ErrPlatformNotSupported)
	})

	T.Run("with apns_fcm provider and nil FCM returns iOS-only sender", func(t *testing.T) {
		t.Parallel()

		p8Path := createTestP8File(t)
		cfg := Config{
			Provider: ProviderAPNsFCM,
			APNs:     &APNsConfig{AuthKeyPath: p8Path, KeyID: "x", TeamID: "x", BundleID: "x"},
			FCM:      nil,
		}
		sender, err := cfg.NewPushSender(ctx, logger, tracer, nil)
		must.NoError(t, err)
		must.NotNil(t, sender)
		// Android not configured, should return ErrPlatformNotSupported
		err = sender.SendPush(ctx, "android", "token", mobile.PushMessage{Title: "title", Body: "body"})
		test.Error(t, err)
		test.ErrorIs(t, err, mobile.ErrPlatformNotSupported)
	})

	T.Run("with apns_fcm provider and invalid APNs path fails loudly", func(t *testing.T) {
		t.Parallel()

		cfg := Config{
			Provider: ProviderAPNsFCM,
			APNs:     &APNsConfig{AuthKeyPath: filepath.Join(t.TempDir(), "nonexistent.p8"), KeyID: "x", TeamID: "x", BundleID: "x"},
			FCM:      &FCMConfig{},
		}
		sender, err := cfg.NewPushSender(ctx, logger, tracer, nil)
		test.Error(t, err)
		test.Nil(t, sender)
	})

	T.Run("with apns_fcm provider and invalid FCM path fails loudly", func(t *testing.T) {
		t.Parallel()

		p8Path := createTestP8File(t)
		cfg := Config{
			Provider: ProviderAPNsFCM,
			APNs:     &APNsConfig{AuthKeyPath: p8Path, KeyID: "x", TeamID: "x", BundleID: "x"},
			FCM:      &FCMConfig{CredentialsPath: filepath.Join(t.TempDir(), "nonexistent.json")},
		}
		sender, err := cfg.NewPushSender(ctx, logger, tracer, nil)
		test.Error(t, err)
		test.Nil(t, sender)
	})

	T.Run("with apns_fcm provider and neither platform configured fails loudly", func(t *testing.T) {
		t.Parallel()

		cfg := Config{
			Provider: ProviderAPNsFCM,
			APNs:     nil,
			FCM:      nil,
		}
		sender, err := cfg.NewPushSender(ctx, logger, tracer, nil)
		test.Error(t, err)
		test.Nil(t, sender)
	})

	T.Run("with unknown provider returns noop", func(t *testing.T) {
		t.Parallel()

		cfg := Config{Provider: "unknown"}
		sender, err := cfg.NewPushSender(ctx, logger, tracer, nil)
		must.NoError(t, err)
		must.NotNil(t, sender)
		test.NoError(t, sender.SendPush(ctx, "ios", "token", mobile.PushMessage{Title: "title", Body: "body"}))
	})

	T.Run("with apns_fcm provider and valid FCM creds returns multi-platform sender", func(t *testing.T) {
		t.Parallel()

		credsPath := createTestFCMCredsFile(t)
		cfg := Config{
			Provider: ProviderAPNsFCM,
			APNs:     nil,
			FCM:      &FCMConfig{CredentialsPath: credsPath},
		}
		sender, err := cfg.NewPushSender(ctx, logger, tracer, nil)
		must.NoError(t, err)
		must.NotNil(t, sender)
	})

	T.Run("with apns_fcm provider and both valid configs returns multi-platform sender", func(t *testing.T) {
		t.Parallel()

		p8Path := createTestP8File(t)
		credsPath := createTestFCMCredsFile(t)
		cfg := Config{
			Provider: ProviderAPNsFCM,
			APNs:     &APNsConfig{AuthKeyPath: p8Path, KeyID: "x", TeamID: "x", BundleID: "x"},
			FCM:      &FCMConfig{CredentialsPath: credsPath},
		}
		sender, err := cfg.NewPushSender(ctx, logger, tracer, nil)
		must.NoError(t, err)
		must.NotNil(t, sender)
	})
}
