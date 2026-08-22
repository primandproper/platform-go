package elasticsearch

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/primandproper/platform-go/v13/circuitbreaking"
	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"
	"github.com/primandproper/platform-go/v13/observability/logging"
	textsearch "github.com/primandproper/platform-go/v13/search/text"

	"github.com/elastic/go-elasticsearch/v8"
	"github.com/elastic/go-elasticsearch/v8/esapi"
)

// serviceName scopes this backend's instrument names.
const serviceName = "elasticsearch_index"

var (
	_ textsearch.Index[any] = (*IndexManager[any])(nil)
)

type (
	// IndexManager is the Elasticsearch textsearch.Index. It is exported, and
	// returned by NewIndexManager, so a caller who has chosen Elasticsearch can
	// depend on that choice rather than on the seam every text index shares.
	IndexManager[T any] struct {
		o11y                  observability.Observer
		circuitBreaker        circuitbreaking.CircuitBreaker
		esClient              *elasticsearch.Client
		instruments           *textsearch.Instruments
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

func NewIndexManager[T any](ctx context.Context, cfg *Config, indexName string, circuitBreaker circuitbreaking.CircuitBreaker, opts ...Option) (*IndexManager[T], error) {
	c, err := provideElasticsearchClient(cfg)
	if err != nil {
		return nil, errors.Wrap(err, "initializing search client")
	}

	o := newOptions(opts)
	logger := logging.EnsureLogger(o.logger)

	if err = elasticsearchIsReadyToInit(ctx, cfg, logger, 10); err != nil {
		return nil, err
	}

	// Elasticsearch index names must be lowercase. Normalize once so create,
	// existence-check, index, delete, and search all target the same name — the
	// previous code created a lowercased index but read/wrote the original case.
	// The observed value is the normalized one for the same reason: it is the
	// name the requests below actually carry.
	normalizedIndex := strings.ToLower(indexName)

	instruments, err := textsearch.NewInstruments(serviceName, normalizedIndex, o.metricsProvider)
	if err != nil {
		return nil, err
	}

	im := &IndexManager[T]{
		o11y: observability.NewObserverWithValues(fmt.Sprintf("search_%s", indexName), logger, o.tracerProvider,
			map[string]any{keys.IndexNameKey: normalizedIndex}),
		esClient:              c,
		instruments:           instruments,
		indexOperationTimeout: cfg.IndexOperationTimeout,
		indexName:             normalizedIndex,
		circuitBreaker:        circuitBreaker,
	}

	if indexErr := im.ensureIndices(ctx); indexErr != nil {
		return nil, indexErr
	}

	return im, nil
}

// ErrNotReady indicates a cluster that did not answer a ping within the
// attempts allowed.
var ErrNotReady = errors.New("elasticsearch not ready")

// elasticsearchIsReadyToInit pings the cluster until it answers, reporting why
// it gave up rather than a bare bool — "the config is wrong", "the cluster
// never came up", and "the caller went away" are three different answers, and
// only the first two are this package's fault.
func elasticsearchIsReadyToInit(
	ctx context.Context,
	cfg *Config,
	l logging.Logger,
	maxAttempts uint8,
) error {
	attemptCount := 0

	logger := l.WithValues(map[string]any{
		"interval": time.Second.String(),
		"address":  cfg.Address,
	})

	logger.Debug("checking if elasticsearch is ready")

	c, err := provideElasticsearchClient(cfg)
	if err != nil {
		// Returning rather than falling through: the loop below calls Do() on the
		// client, and a failed construction leaves it nil, so the very next line
		// panicked. A client that cannot be built will not build on the next tick
		// either — the config is wrong, and no amount of waiting fixes it.
		logger.WithValue("attempt_count", attemptCount).Error("client setup failed, cannot probe elasticsearch", err)

		return errors.Wrap(err, "building elasticsearch readiness probe client")
	}

	for {
		// Checked before the ping as well as during the wait: a context that was
		// already done should not buy the caller one more round trip.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return errors.Wrap(ctxErr, "waiting for elasticsearch")
		}

		res, pingErr := (esapi.InfoRequest{}).Do(ctx, c)
		ready := pingErr == nil && res != nil && !res.IsError()

		if res != nil {
			_ = res.Body.Close() //nolint:errcheck // best-effort close of the readiness-probe response body
		}

		if ready {
			return nil
		}

		logger.WithValue("attempt_count", attemptCount).Debug("ping failed, waiting for elasticsearch")

		attemptCount++
		if attemptCount >= int(maxAttempts) {
			return errors.Wrapf(ErrNotReady, "after %d attempts", attemptCount)
		}

		// A sleep that ignores cancellation makes construction take ten seconds
		// to notice a caller that has already given up, which for a startup
		// probe is the whole of a shutdown deadline.
		select {
		case <-ctx.Done():
			return errors.Wrap(ctx.Err(), "waiting for elasticsearch")
		case <-time.After(time.Second):
		}
	}
}

func (sm *IndexManager[T]) ensureIndices(ctx context.Context) error {
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
