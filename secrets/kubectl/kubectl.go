package kubectl

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/primandproper/platform-go/v5/errors"
	"github.com/primandproper/platform-go/v5/observability"
	"github.com/primandproper/platform-go/v5/observability/logging"
	"github.com/primandproper/platform-go/v5/observability/metrics"
	"github.com/primandproper/platform-go/v5/observability/tracing"
	"github.com/primandproper/platform-go/v5/secrets"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const name = "kubectl_secret_source"

// SecretGetter abstracts the Kubernetes Secrets API for testability.
type SecretGetter interface {
	Get(ctx context.Context, name string, opts metav1.GetOptions) (*corev1.Secret, error)
}

type kubectlSecretSource struct {
	o11y          observability.Observer
	lookupCounter metrics.Int64Counter
	errorCounter  metrics.Int64Counter
	latencyHist   metrics.Float64Histogram
	client        SecretGetter
}

// NewKubectlSecretSource creates a SecretSource backed by Kubernetes secrets.
// If client is nil, a new client is created using the kubeconfig path or in-cluster config.
func NewKubectlSecretSource(ctx context.Context, cfg *Config, client SecretGetter, logger logging.Logger, tracerProvider tracing.TracerProvider, metricsProvider metrics.Provider) (secrets.SecretSource, error) {
	if cfg == nil {
		return nil, errors.New("kubectl secret source: config is required")
	}
	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "kubectl secret source")
	}

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
		return &kubectlSecretSource{
			o11y:          observability.NewObserver(name, logger, tracerProvider),
			lookupCounter: lookupCounter,
			errorCounter:  errorCounter,
			latencyHist:   latencyHist,
			client:        client,
		}, nil
	}

	var restCfg *rest.Config
	if cfg.Kubeconfig != "" {
		restCfg, err = clientcmd.BuildConfigFromFlags("", cfg.Kubeconfig)
	} else {
		restCfg, err = rest.InClusterConfig()
	}
	if err != nil {
		return nil, errors.Wrap(err, "kubectl secret source: building kubernetes config")
	}

	clientset, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, errors.Wrap(err, "kubectl secret source: creating kubernetes client")
	}

	return &kubectlSecretSource{
		o11y:          observability.NewObserver(name, logger, tracerProvider),
		lookupCounter: lookupCounter,
		errorCounter:  errorCounter,
		latencyHist:   latencyHist,
		client:        clientset.CoreV1().Secrets(cfg.Namespace),
	}, nil
}

func (k *kubectlSecretSource) GetSecret(ctx context.Context, name string) (string, error) {
	ctx, op := k.o11y.Begin(ctx)
	defer op.End()

	startTime := time.Now()
	defer func() {
		k.latencyHist.Record(ctx, float64(time.Since(startTime).Milliseconds()))
	}()

	secretName, key, err := resolveName(name)
	if err != nil {
		k.errorCounter.Add(ctx, 1)
		return "", err
	}

	// Only the identifiers are observed here; secret values are never attached.
	op.Set("secret.name", secretName).Set("secret.key", key)

	secret, err := k.client.Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		k.errorCounter.Add(ctx, 1)
		return "", op.Error(err, "getting kubernetes secret %q", secretName)
	}

	data, ok := secret.Data[key]
	if !ok {
		k.errorCounter.Add(ctx, 1)
		return "", errors.Newf("key %q not found in kubernetes secret %q", key, secretName)
	}

	k.lookupCounter.Add(ctx, 1)

	return string(data), nil
}

func (k *kubectlSecretSource) Close() error {
	return nil
}

// resolveName splits a name in the form "secret-name/key" into its components.
func resolveName(input string) (secretName, key string, err error) {
	before, after, ok := strings.Cut(input, "/")
	if !ok {
		return "", "", errors.Newf("invalid secret name %q: expected format \"secret-name/key\"", input)
	}
	return before, after, nil
}

// Ensure kubectlSecretSource implements secrets.SecretSource.
var _ secrets.SecretSource = (*kubectlSecretSource)(nil)
