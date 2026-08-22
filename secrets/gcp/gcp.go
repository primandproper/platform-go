package gcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/secrets"

	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const name = "gcp_secret_source"

const (
	secretVersionLatest = "latest"
	projectsPrefix      = "projects/"

	projectIDKey = "project.id"
)

// SecretVersionAccessor abstracts AccessSecretVersion for testability.
type SecretVersionAccessor interface {
	AccessSecretVersion(ctx context.Context, req *secretmanagerpb.AccessSecretVersionRequest) (*secretmanagerpb.AccessSecretVersionResponse, error)
	Close() error
}

var _ secrets.SecretSource = (*SecretSource)(nil)

// SecretSource reads secrets from GCP Secret Manager. It is exported, and
// returned by NewSecretSource, so a caller can depend on this source rather
// than on the interface every provider shares — and so reason about what this
// one alone does: reach one project over the network, per lookup.
type SecretSource struct {
	o11y          observability.Observer
	lookupCounter metrics.Int64Counter
	errorCounter  metrics.Int64Counter
	latencyHist   metrics.Float64Histogram
	client        SecretVersionAccessor
	projectID     string
}

// NewSecretSource creates a SecretSource backed by GCP Secret Manager.
// If client is nil, a new client is created using Application Default Credentials.
func NewSecretSource(ctx context.Context, cfg *Config, client SecretVersionAccessor, opts ...Option) (*SecretSource, error) {
	if cfg == nil {
		return nil, errors.New("gcp secret source: config is required")
	}
	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "gcp secret source")
	}

	o := newOptions(opts)
	// A source is bound to one project, so the project is stated here — which
	// also puts it on the constructor-time and error log lines, not just on the
	// one operation that used to set it.
	o11y := observability.NewObserverWithValues(name, o.logger, o.tracerProvider,
		map[string]any{projectIDKey: cfg.ProjectID})
	mp := metrics.EnsureMetricsProvider(o.metricsProvider)

	lookupCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_lookups", name))
	if err != nil {
		return nil, errors.Wrap(err, "creating lookup counter")
	}

	errorCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_errors", name))
	if err != nil {
		return nil, errors.Wrap(err, "creating error counter")
	}

	latencyHist, err := mp.NewFloat64Histogram(fmt.Sprintf("%s_latency_ms", name))
	if err != nil {
		return nil, errors.Wrap(err, "creating latency histogram")
	}

	if client != nil {
		return &SecretSource{
			o11y:          o11y,
			lookupCounter: lookupCounter,
			errorCounter:  errorCounter,
			latencyHist:   latencyHist,
			client:        client,
			projectID:     cfg.ProjectID,
		}, nil
	}

	smClient, smErr := secretmanager.NewClient(ctx)
	if smErr != nil {
		return nil, errors.Wrap(smErr, "gcp secret source: creating client")
	}

	return &SecretSource{
		o11y:          o11y,
		lookupCounter: lookupCounter,
		errorCounter:  errorCounter,
		latencyHist:   latencyHist,
		client:        &secretManagerClientAdapter{Client: smClient},
		projectID:     cfg.ProjectID,
	}, nil
}

// secretManagerClientAdapter adapts *secretmanager.Client to SecretVersionAccessor.
type secretManagerClientAdapter struct {
	*secretmanager.Client
}

func (a *secretManagerClientAdapter) AccessSecretVersion(ctx context.Context, req *secretmanagerpb.AccessSecretVersionRequest) (*secretmanagerpb.AccessSecretVersionResponse, error) {
	return a.Client.AccessSecretVersion(ctx, req)
}

func (g *SecretSource) GetSecret(ctx context.Context, name string) (string, error) {
	ctx, op := g.o11y.Begin(ctx)
	defer op.End()

	defer op.Time(ctx, nil, g.latencyHist)()

	op.Set(keys.SecretNameKey, name)

	resourceName := g.resolveName(name)
	req := &secretmanagerpb.AccessSecretVersionRequest{
		Name: resourceName,
	}

	resp, err := g.client.AccessSecretVersion(ctx, req)
	if err != nil {
		g.errorCounter.Add(ctx, 1)

		// Mapped, not passed through: secrets.ErrSecretNotFound exists so a
		// caller can tell "no such secret" from "could not reach the provider"
		// without knowing which provider it got.
		if status.Code(err) == codes.NotFound {
			return "", op.Error(
				errors.Join(secrets.ErrSecretNotFound, err),
				"accessing secret %q", name,
			)
		}

		return "", op.Error(err, "accessing secret %q", name)
	}
	if resp.Payload == nil || resp.Payload.Data == nil {
		g.errorCounter.Add(ctx, 1)
		return "", op.Error(secrets.ErrSecretNotFound, "secret %q has no payload", name)
	}

	g.lookupCounter.Add(ctx, 1)

	return string(resp.Payload.Data), nil
}

func (g *SecretSource) Close() error {
	return g.client.Close()
}

func (g *SecretSource) resolveName(name string) string {
	if strings.HasPrefix(name, projectsPrefix) {
		return name
	}
	return fmt.Sprintf("projects/%s/secrets/%s/versions/%s", g.projectID, name, secretVersionLatest)
}
