package fcm

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/errors"
	"github.com/primandproper/platform-go/observability/logging"
	loggingnoop "github.com/primandproper/platform-go/observability/logging/noop"
	"github.com/primandproper/platform-go/observability/metrics"
	mockmetrics "github.com/primandproper/platform-go/observability/metrics/mock"
	"github.com/primandproper/platform-go/observability/tracing"
	tracingnoop "github.com/primandproper/platform-go/observability/tracing/noop"

	firebase "firebase.google.com/go/v4"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
	"google.golang.org/api/option"
)

// fakeServiceAccountJSON is a syntactically-valid Firebase service-account JSON.
const fakeServiceAccountJSON = `{"type":"service_account","project_id":"fake","private_key_id":"id","private_key":"-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQC7VJTUt9Us8cKj\nMzEfYyjiWA4R4/M2bS1GB4t7NXp98C3SC6dVMvDuictGeurT8jNbvJZHtCSuYEvu\nNMoSfm76oqFvAp8Gy0iz5sxjZmSnXyCdPEovGhLa0VzMaQ8s+CLOyS56YyCFGeJZ\nqgtzJ6GR3eqoYSW9b9UMvkBpZODSctWSNGj3P7jRFDO5VoTwCQAWbFnOjDfH5Ulg\np2PKSQnSJP3AJLQNFNe7br1XbrhV//eO+t51mIpGSDCUv3E0DDFcWDTH9cXDTTlR\nZVEiR2BwpZOOkE/Z0/BVnhZYL71oZV34bKfWjQIt6V/isSMahdsAASACp4ZTGtwi\nVuNd9tybAgMBAAECggEBAKTmjaS6tkK8BlPXClTQ2vpz/N6uxDeS35mXpqasqskV\nlaAidgg/sWqpjXDbXr93otIMLlWsM+X0CqMDgSXKejLS2jx4GDjI1ZTXg++0AMJ8\nsJ74pWzVDOfmCEQ/7wXs3+cbnXhKriO8Z036q92Qc1+N87SI38nkGa0ABH9CN83H\nmQqt4fB7UdHzuIRe/me2PGhIq5ZBzj6h3BpoPGzEP+x3l9YmK8t/1cN0pqI+dQwY\ndgfGjackLu/2qH80MCF7IyQaseZUOJyKrCLtSD/Iixv/hzDEUPfOCjFDgTpzf3cw\nta8+oE4wHCo1iI1/4TlPkwmXx4qSXtmw4aQPz7IDQvECgYEA8KNThCO2gsC2I9PQ\nDM/8Cw0O983WCDY+oi+7JPiNAJwv5DYBqEZB1QYdj06YD16XlC/HAZMsMku1na2T\nN0driwenQQWzoev3g2S7gRDoS/FCJSI3jJ+kjgtaA7Qmzlgk1TxODN+G1H91HW7t\n0l7VnL27IWyYo2qRRK3jzxqUiPUCgYEAx0oQs2reBQGMVZnApD1jeq7n4MvNLcPv\nt8b/eU9iUv6Y4Mj0Suo/AU8lYZXm8ubbqAlwz2VSVunD2tOplHyMUrtCtObAfVDU\nAhCndKaA9gApgfb3xw1IKbuQ1u4IF1FJl3VtumfQn//LiH1B3rXhcdyo3/vIttEk\n48RakUKClU8CgYEAzV7W3COOlDDcQd935DdtKBFRAPRPAlspQUnzMi5eSHMD/ISL\nDY5IiQHbIH83D4bvXq0X7qQoSBSNP7Dvv3HYuqMhf0DaegrlBuJllFVVq9qPVRnK\nxt1Il2HgxOBvbhOT+9in1BzA+YJ99UzC85O0Qz06A+CmtHEy4aZ2kj5hHjECgYEA\nmNS4+A8Fkss8Js1RieK2LniBxMgmYml3pfVLKGnzmng7H2+cwPLhPIzIuwytXywh\n2bzbsYEfYx3EoEVgMEpPhoarQnYPukrJO4gwE2o5Te6T5mJSZGlQJQj9q4ZB2Dfz\net6INsK0oG8XVGXSpQvQh3RUYekCZQkBBFcpqWpbIEsCgYAnM3DQf3FJoSnXaMhr\nVBIovic5l0xFkEHskAjFTevO86Fsz1C2aSeRKSqGFoOQ0tmJzBEs1R6KqnHInicD\nTQrKhArgLXX4v3CddjfTRJkFWDbE/CkvKZNOrcf1nhaGCPspRJj2KUkj1Fhl9Cnc\ndn/RsYEONbwQSjIfMPkvxF+8HQ==\n-----END PRIVATE KEY-----\n","client_email":"test@fake.iam.gserviceaccount.com","client_id":"1","auth_uri":"https://accounts.google.com/o/oauth2/auth","token_uri":"https://oauth2.googleapis.com/token","auth_provider_x509_cert_url":"https://www.googleapis.com/oauth2/v1/certs","client_x509_cert_url":"https://www.googleapis.com/robot/v1/metadata/x509/test%40fake.iam.gserviceaccount.com"}`

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func createTestFCMSenderWithTransport(t *testing.T, fn roundTripFunc) *Sender {
	t.Helper()

	ctx := t.Context()
	httpClient := &http.Client{Transport: fn}

	app, err := firebase.NewApp(ctx, &firebase.Config{ProjectID: "test-project"}, option.WithHTTPClient(httpClient))
	must.NoError(t, err)

	client, err := app.Messaging(ctx)
	must.NoError(t, err)

	mp := metrics.EnsureMetricsProvider(nil)
	sendCounter, err := mp.NewInt64Counter(o11yName + "_sends")
	must.NoError(t, err)
	errorCounter, err := mp.NewInt64Counter(o11yName + "_errors")
	must.NoError(t, err)

	return &Sender{
		client:       client,
		tracer:       tracing.NewNamedTracer(tracingnoop.NewTracerProvider(), o11yName),
		logger:       logging.NewNamedLogger(loggingnoop.NewLogger(), o11yName),
		sendCounter:  sendCounter,
		errorCounter: errorCounter,
	}
}

func TestNewSender(T *testing.T) {
	T.Parallel()

	ctx := T.Context()
	logger := loggingnoop.NewLogger()
	tracingProvider := tracingnoop.NewTracerProvider()

	T.Run("with nil config", func(t *testing.T) {
		t.Parallel()

		sender, err := NewSender(ctx, nil, tracingProvider, logger, nil)
		test.Nil(t, sender)
		test.Error(t, err)
		test.StrContains(t, err.Error(), "config is required")
	})

	T.Run("with non-existent credentials path", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			CredentialsPath: filepath.Join(t.TempDir(), "nonexistent-firebase-credentials.json"),
		}
		sender, err := NewSender(ctx, cfg, tracingProvider, logger, nil)
		test.Nil(t, sender)
		test.Error(t, err)
		test.StrContains(t, err.Error(), "credentials file not found")
	})

	T.Run("with empty credentials path uses ADC", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{CredentialsPath: ""}
		sender, err := NewSender(ctx, cfg, tracingProvider, logger, nil)
		// ADC typically fails without GCP credentials in test env
		if err != nil {
			test.Nil(t, sender)
			test.Error(t, err)
			test.StrContains(t, err.Error(), "fcm:")
			return
		}
		must.NotNil(t, sender)
	})

	T.Run("with invalid JSON credentials file", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		path := filepath.Join(dir, "creds.json")
		must.NoError(t, os.WriteFile(path, []byte("not valid json"), 0o600))

		cfg := &Config{CredentialsPath: path}
		sender, err := NewSender(ctx, cfg, tracingProvider, logger, nil)
		test.Nil(t, sender)
		test.Error(t, err)
		test.StrContains(t, err.Error(), "fcm:")
	})

	T.Run("with valid credentials file", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		path := filepath.Join(dir, "creds.json")
		must.NoError(t, os.WriteFile(path, []byte(fakeServiceAccountJSON), 0o600))

		cfg := &Config{CredentialsPath: path}
		sender, err := NewSender(ctx, cfg, tracingProvider, logger, nil)
		must.NoError(t, err)
		must.NotNil(t, sender)
	})

	T.Run("with send counter creation error", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		path := filepath.Join(dir, "creds.json")
		must.NoError(t, os.WriteFile(path, []byte(fakeServiceAccountJSON), 0o600))

		mp := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(counterName string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				test.EqOp(t, o11yName+"_sends", counterName)
				return (*metrics.Int64CounterImpl)(nil), errors.New("counter error")
			},
		}

		cfg := &Config{CredentialsPath: path}
		sender, err := NewSender(ctx, cfg, tracingProvider, logger, mp)
		test.Nil(t, sender)
		must.Error(t, err)
		test.StrContains(t, err.Error(), "creating send counter")

		test.SliceLen(t, 1, mp.NewInt64CounterCalls())
	})

	T.Run("with error counter creation error", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		path := filepath.Join(dir, "creds.json")
		must.NoError(t, os.WriteFile(path, []byte(fakeServiceAccountJSON), 0o600))

		mp := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(counterName string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				switch counterName {
				case o11yName + "_sends":
					return (*metrics.Int64CounterImpl)(nil), nil
				case o11yName + "_errors":
					return (*metrics.Int64CounterImpl)(nil), errors.New("counter error")
				}
				t.Fatalf("unexpected NewInt64Counter call: %q", counterName)
				return nil, nil
			},
		}

		cfg := &Config{CredentialsPath: path}
		sender, err := NewSender(ctx, cfg, tracingProvider, logger, mp)
		test.Nil(t, sender)
		must.Error(t, err)
		test.StrContains(t, err.Error(), "creating error counter")

		test.SliceLen(t, 2, mp.NewInt64CounterCalls())
	})
}

func TestSender_Send(T *testing.T) {
	T.Parallel()

	ctx := T.Context()

	T.Run("successful send", func(t *testing.T) {
		t.Parallel()

		sender := createTestFCMSenderWithTransport(t, func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"name":"projects/test-project/messages/12345"}`)),
			}, nil
		})

		err := sender.Send(ctx, "device-token-abc", "Test Title", "Test Body")
		test.NoError(t, err)
	})

	T.Run("send returns error", func(t *testing.T) {
		t.Parallel()

		sender := createTestFCMSenderWithTransport(t, func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusUnauthorized,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"error":{"code":401,"message":"unauthorized","status":"UNAUTHENTICATED"}}`)),
			}, nil
		})

		err := sender.Send(ctx, "device-token-abc", "Test Title", "Test Body")
		must.Error(t, err)
	})
}
