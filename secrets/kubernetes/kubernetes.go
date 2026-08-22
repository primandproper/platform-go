/*
Package kubernetes sources secrets from the Kubernetes Secrets API.

It talks to the API server in-process through client-go. It does not shell out
to the kubectl binary, and does not require one to be installed — which is what
the package was called until v9, and why it was renamed.
*/
package kubernetes

import (
	"context"
	"fmt"
	"strings"

	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/secrets"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sclient "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const name = "kubernetes_secret_source"

// SecretGetter abstracts the Kubernetes Secrets API for testability.
type SecretGetter interface {
	Get(ctx context.Context, name string, opts metav1.GetOptions) (*corev1.Secret, error)
}

// SecretSource reads secrets from the Kubernetes Secrets API. It is exported,
// and returned by NewSecretSource, so a caller can depend on this source rather
// than on the interface every provider shares — and so reason about what this
// one alone does: reach the API server for a namespaced Secret, per lookup.
type SecretSource struct {
	o11y          observability.Observer
	lookupCounter metrics.Int64Counter
	errorCounter  metrics.Int64Counter
	latencyHist   metrics.Float64Histogram
	client        SecretGetter
}

// NewSecretSource creates a SecretSource backed by Kubernetes secrets.
// If client is nil, a new client is created using the kubeconfig path or in-cluster config.
func NewSecretSource(ctx context.Context, cfg *Config, client SecretGetter, opts ...Option) (*SecretSource, error) {
	if cfg == nil {
		return nil, errors.New("kubernetes secret source: config is required")
	}
	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "kubernetes secret source")
	}

	o := newOptions(opts)
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
			o11y:          observability.NewObserver(name, o.logger, o.tracerProvider),
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
		return nil, errors.Wrap(err, "kubernetes secret source: building kubernetes config")
	}

	clientset, err := k8sclient.NewForConfig(restCfg)
	if err != nil {
		return nil, errors.Wrap(err, "kubernetes secret source: creating kubernetes client")
	}

	return &SecretSource{
		o11y:          observability.NewObserver(name, o.logger, o.tracerProvider),
		lookupCounter: lookupCounter,
		errorCounter:  errorCounter,
		latencyHist:   latencyHist,
		client:        clientset.CoreV1().Secrets(cfg.Namespace),
	}, nil
}

func (k *SecretSource) GetSecret(ctx context.Context, name string) (string, error) {
	ctx, op := k.o11y.Begin(ctx)
	defer op.End()

	defer op.Time(ctx, nil, k.latencyHist)()

	secretName, key, err := resolveName(name)
	if err != nil {
		k.errorCounter.Add(ctx, 1)
		return "", err
	}

	// Only the identifiers are observed here; secret values are never attached.
	op.Set(keys.SecretNameKey, secretName).Set(keys.SecretEntryKey, key)

	secret, err := k.client.Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		k.errorCounter.Add(ctx, 1)

		// Mapped, not passed through: secrets.ErrSecretNotFound exists so a
		// caller can tell "no such secret" from "could not reach the provider"
		// without knowing which provider it got.
		if apierrors.IsNotFound(err) {
			return "", op.Error(
				errors.Join(secrets.ErrSecretNotFound, err),
				"getting kubernetes secret %q", secretName,
			)
		}

		return "", op.Error(err, "getting kubernetes secret %q", secretName)
	}

	// A missing key inside an existing secret is the same answer as a missing
	// secret, as far as a caller asking for "secret-name/key" is concerned.
	data, ok := secret.Data[key]
	if !ok {
		k.errorCounter.Add(ctx, 1)
		return "", op.Error(
			errors.Wrapf(secrets.ErrSecretNotFound, "key %q in kubernetes secret %q", key, secretName),
			"getting kubernetes secret key",
		)
	}

	k.lookupCounter.Add(ctx, 1)

	return string(data), nil
}

func (k *SecretSource) Close() error {
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

// Ensure SecretSource implements secrets.SecretSource.
var _ secrets.SecretSource = (*SecretSource)(nil)
