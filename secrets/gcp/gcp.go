package gcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/primandproper/platform-go/v6/errors"
	"github.com/primandproper/platform-go/v6/observability"
	"github.com/primandproper/platform-go/v6/observability/keys"
	"github.com/primandproper/platform-go/v6/observability/logging"
	"github.com/primandproper/platform-go/v6/observability/metrics"
	"github.com/primandproper/platform-go/v6/observability/tracing"
	"github.com/primandproper/platform-go/v6/secrets"

	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
)

const name = "gcp_secret_source"

const (
	secretVersionLatest = "latest"
	projectsPrefix      = "projects/"
)

// SecretVersionAccessor abstracts AccessSecretVersion for testability.
type SecretVersionAccessor interface {
	AccessSecretVersion(ctx context.Context, req *secretmanagerpb.AccessSecretVersionRequest) (*secretmanagerpb.AccessSecretVersionResponse, error)
	Close() error
}

type gcpSecretSource struct {
	o11y          observability.Observer
	lookupCounter metrics.Int64Counter
	errorCounter  metrics.Int64Counter
	latencyHist   metrics.Float64Histogram
	client        SecretVersionAccessor
	projectID     string
}

// NewGCPSecretSource creates a SecretSource backed by GCP Secret Manager.
// If client is nil, a new client is created using Application Default Credentials.
func NewGCPSecretSource(ctx context.Context, cfg *Config, client SecretVersionAccessor, logger logging.Logger, tracerProvider tracing.TracerProvider, metricsProvider metrics.Provider) (secrets.SecretSource, error) {
	if cfg == nil {
		return nil, errors.New("gcp secret source: config is required")
	}
	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "gcp secret source")
	}

	o11y := observability.NewObserver(name, logger, tracerProvider)
	mp := metrics.EnsureMetricsProvider(metricsProvider)

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
		return &gcpSecretSource{
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

	return &gcpSecretSource{
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

func (g *gcpSecretSource) GetSecret(ctx context.Context, name string) (string, error) {
	ctx, op := g.o11y.Begin(ctx)
	defer op.End()

	startTime := time.Now()
	defer func() {
		g.latencyHist.Record(ctx, float64(time.Since(startTime).Milliseconds()))
	}()

	op.Set(keys.NameKey, name).Set("project.id", g.projectID)

	resourceName := g.resolveName(name)
	req := &secretmanagerpb.AccessSecretVersionRequest{
		Name: resourceName,
	}

	resp, err := g.client.AccessSecretVersion(ctx, req)
	if err != nil {
		g.errorCounter.Add(ctx, 1)
		return "", op.Error(err, "accessing secret %q", name)
	}
	if resp.Payload == nil || resp.Payload.Data == nil {
		g.errorCounter.Add(ctx, 1)
		return "", op.Error(secrets.ErrSecretNotFound, "secret %q has no payload", name)
	}

	g.lookupCounter.Add(ctx, 1)

	return string(resp.Payload.Data), nil
}

func (g *gcpSecretSource) Close() error {
	return g.client.Close()
}

func (g *gcpSecretSource) resolveName(name string) string {
	if strings.HasPrefix(name, projectsPrefix) {
		return name
	}
	return fmt.Sprintf("projects/%s/secrets/%s/versions/%s", g.projectID, name, secretVersionLatest)
}
