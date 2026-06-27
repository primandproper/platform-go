package elasticsearch

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/primandproper/platform-go/circuitbreaking"
	"github.com/primandproper/platform-go/errors"
	"github.com/primandproper/platform-go/observability"
	"github.com/primandproper/platform-go/observability/logging"
	"github.com/primandproper/platform-go/observability/tracing"
	textsearch "github.com/primandproper/platform-go/search/text"

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

func ProvideIndexManager[T any](ctx context.Context, logger logging.Logger, tracerProvider tracing.TracerProvider, cfg *Config, indexName string, circuitBreaker circuitbreaking.CircuitBreaker) (textsearch.Index[T], error) {
	c, err := provideElasticsearchClient(cfg)
	if err != nil {
		return nil, errors.Wrap(err, "initializing search client")
	}

	logger = logging.EnsureLogger(logger)

	if ready := elasticsearchIsReadyToInit(ctx, cfg, logger, 10); !ready {
		return nil, errors.Wrap(err, "initializing search client")
	}

	im := &indexManager[T]{
		o11y:                  observability.NewObserver(fmt.Sprintf("search_%s", indexName), logger, tracerProvider),
		esClient:              c,
		indexOperationTimeout: cfg.IndexOperationTimeout,
		indexName:             indexName,
		circuitBreaker:        circuitBreaker,
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
		var res *esapi.Response
		res, err = (esapi.InfoRequest{}).Do(ctx, c)
		if err != nil && res != nil && !res.IsError() {
			logger.WithValue("attempt_count", attemptCount).Debug("ping failed, waiting for elasticsearch")
			time.Sleep(time.Second)

			attemptCount++
			if attemptCount >= int(maxAttempts) {
				break
			}
		} else {
			return true
		}
	}

	return false
}

func (sm *indexManager[T]) ensureIndices(ctx context.Context) error {
	ctx, op := sm.o11y.Begin(ctx)
	defer op.End()

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

	if res.StatusCode == http.StatusNotFound {
		if _, err = (esapi.IndicesCreateRequest{Index: strings.ToLower(sm.indexName)}).Do(ctx, sm.esClient); err != nil {
			sm.circuitBreaker.Failed()
			return observability.PrepareError(err, op.Span(), "checking index existence")
		}
	}

	sm.circuitBreaker.Succeeded()
	return nil
}
