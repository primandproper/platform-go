package elasticsearch

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/primandproper/platform-go/v5/circuitbreaking"
	"github.com/primandproper/platform-go/v5/errors"
	"github.com/primandproper/platform-go/v5/observability"
	"github.com/primandproper/platform-go/v5/observability/keys"
	"github.com/primandproper/platform-go/v5/observability/logging"
	"github.com/primandproper/platform-go/v5/observability/tracing"
	textsearch "github.com/primandproper/platform-go/v5/search/text"

	"github.com/elastic/go-elasticsearch/v8"
	"github.com/elastic/go-elasticsearch/v8/esapi"
)

var (
	_ textsearch.Index[any] = (*indexManager[any])(nil)
)

type (
	indexManager[T any] struct {
		o11y                  observability.Observer
		circuitBreaker        circuitbreaking.CircuitBreaker
		esClient              *elasticsearch.Client
		indexName             string
		indexOperationTimeout time.Duration
	}
)

func provideElasticsearchClient(cfg *Config) (*elasticsearch.Client, error) {
	c, err := elasticsearch.NewClient(elasticsearch.Config{
		Addresses: []string{
			cfg.Address,
		},
		Username:      cfg.Username,
		Password:      cfg.Password,
		CACert:        cfg.CACert,
		RetryOnStatus: nil,
		MaxRetries:    10,
		Transport:     nil,
		Logger:        nil,
	})
	if err != nil {
		return nil, errors.Wrap(err, "initializing search client")
	}

	return c, nil
}

func NewIndexManager[T any](ctx context.Context, logger logging.Logger, tracerProvider tracing.TracerProvider, cfg *Config, indexName string, circuitBreaker circuitbreaking.CircuitBreaker) (textsearch.Index[T], error) {
	c, err := provideElasticsearchClient(cfg)
	if err != nil {
		return nil, errors.Wrap(err, "initializing search client")
	}

	logger = logging.EnsureLogger(logger)

	if ready := elasticsearchIsReadyToInit(ctx, cfg, logger, 10); !ready {
		return nil, errors.New("elasticsearch not ready")
	}

	im := &indexManager[T]{
		o11y:                  observability.NewObserver(fmt.Sprintf("search_%s", indexName), logger, tracerProvider),
		esClient:              c,
		indexOperationTimeout: cfg.IndexOperationTimeout,
		// Elasticsearch index names must be lowercase. Normalize once so create,
		// existence-check, index, delete, and search all target the same name — the
		// previous code created a lowercased index but read/wrote the original case.
		indexName:      strings.ToLower(indexName),
		circuitBreaker: circuitBreaker,
	}

	if indexErr := im.ensureIndices(ctx); indexErr != nil {
		return nil, indexErr
	}

	return im, nil
}

func elasticsearchIsReadyToInit(
	ctx context.Context,
	cfg *Config,
	l logging.Logger,
	maxAttempts uint8,
) bool {
	attemptCount := 0

	logger := l.WithValues(map[string]any{
		"interval": time.Second.String(),
		"address":  cfg.Address,
	})

	logger.Debug("checking if elasticsearch is ready")

	c, err := provideElasticsearchClient(cfg)
	if err != nil {
		logger.WithValue("attempt_count", attemptCount).Debug("client setup failed, waiting for elasticsearch")
	}

	for {
		res, pingErr := (esapi.InfoRequest{}).Do(ctx, c)
		ready := pingErr == nil && res != nil && !res.IsError()
		if res != nil {
			_ = res.Body.Close() //nolint:errcheck // best-effort close of the readiness-probe response body
		}

		if ready {
			return true
		}

		logger.WithValue("attempt_count", attemptCount).Debug("ping failed, waiting for elasticsearch")

		attemptCount++
		if attemptCount >= int(maxAttempts) {
			return false
		}

		time.Sleep(time.Second)
	}
}

func (sm *indexManager[T]) ensureIndices(ctx context.Context) error {
	ctx, op := sm.o11y.Begin(ctx)
	defer op.End()

	op.Set(keys.IndexNameKey, sm.indexName)

	if sm.circuitBreaker.CannotProceed() {
		return circuitbreaking.ErrCircuitBroken
	}

	res, err := esapi.IndicesExistsRequest{
		Index:             []string{sm.indexName},
		IgnoreUnavailable: new(false),
		ErrorTrace:        false,
		FilterPath:        nil,
	}.Do(ctx, sm.esClient)
	if err != nil {
		sm.circuitBreaker.Failed()
		return observability.PrepareError(err, op.Span(), "checking index existence successfully")
	}
	defer func() {
		if closeErr := res.Body.Close(); closeErr != nil {
			op.Acknowledge(closeErr, "closing response body")
		}
	}()

	switch {
	case res.StatusCode == http.StatusNotFound:
		createRes, createErr := esapi.IndicesCreateRequest{Index: sm.indexName}.Do(ctx, sm.esClient)
		if createErr != nil {
			sm.circuitBreaker.Failed()
			return observability.PrepareError(createErr, op.Span(), "creating index")
		}
		defer func() {
			if closeErr := createRes.Body.Close(); closeErr != nil {
				op.Acknowledge(closeErr, "closing create-index response body")
			}
		}()
		if createRes.IsError() {
			sm.circuitBreaker.Failed()
			return observability.PrepareError(errors.New(createRes.String()), op.Span(), "creating index")
		}
	case res.IsError():
		// IndicesExists returns 200 (exists) or 404 (missing); anything else (401,
		// 500, ...) is a real error that must not be mistaken for "index exists".
		sm.circuitBreaker.Failed()
		return observability.PrepareError(errors.New(res.String()), op.Span(), "checking index existence")
	}

	sm.circuitBreaker.Succeeded()
	return nil
}
