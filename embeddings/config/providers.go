package embeddingscfg

import (
	"context"

	"github.com/primandproper/platform-go/v13/embeddings"
	"github.com/primandproper/platform-go/v13/embeddings/cohere"
	embeddingsnoop "github.com/primandproper/platform-go/v13/embeddings/noop"
	"github.com/primandproper/platform-go/v13/embeddings/ollama"
	"github.com/primandproper/platform-go/v13/embeddings/openai"
	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/internal/cfgnorm"
)

// NewEmbedder provides an Embedder from config.
//
// An unrecognized provider is an error rather than a noop embedder, so that a
// misspelling surfaces at construction instead of as silently absent vectors.
// The empty provider is the one exception: embeddings are an optional
// capability, so leaving them unconfigured is a supported deployment and yields
// the noop embedder, which ProviderNoop also names explicitly.
//
// Each provider is built into a variable and returned only once its error is
// known to be nil. The provider constructors return their own concrete types,
// so returning one straight through would convert a nil *openai.Embedder into a
// non-nil embeddings.Embedder on the error path, and a caller testing the result against
// nil would find an embedder that panics on first use.
func NewEmbedder(
	ctx context.Context,
	c *Config,
	opts ...Option,
) (embeddings.Embedder, error) {
	o := newOptions(opts)
	logger, tracerProvider, metricsProvider := o.logger, o.tracerProvider, o.metricsProvider

	if c == nil {
		return nil, errors.ErrNilInputParameter
	}

	provider, err := cfgnorm.SelectProvider(c.Provider, providers, "embeddings provider")
	if err != nil {
		return nil, err
	}

	if err = c.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating embeddings config")
	}

	switch provider {
	case ProviderOpenAI:
		e, embedErr := openai.NewEmbedder(ctx, c.OpenAI,
			openai.WithLogger(logger),
			openai.WithTracerProvider(tracerProvider),
			openai.WithMetricsProvider(metricsProvider),
		)
		if embedErr != nil {
			return nil, embedErr
		}

		return e, nil
	case ProviderOllama:
		e, embedErr := ollama.NewEmbedder(ctx, c.Ollama,
			ollama.WithLogger(logger),
			ollama.WithTracerProvider(tracerProvider),
			ollama.WithMetricsProvider(metricsProvider),
		)
		if embedErr != nil {
			return nil, embedErr
		}

		return e, nil
	case ProviderCohere:
		e, embedErr := cohere.NewEmbedder(ctx, c.Cohere,
			cohere.WithLogger(logger),
			cohere.WithTracerProvider(tracerProvider),
			cohere.WithMetricsProvider(metricsProvider),
		)
		if embedErr != nil {
			return nil, embedErr
		}

		return e, nil
	case ProviderNoop, "":
		return embeddingsnoop.NewEmbedder(), nil
	default:
		return nil, errors.Wrapf(errors.ErrUnknownProvider, "embeddings provider %q", c.Provider)
	}
}
