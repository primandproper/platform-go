package indexing

import (
	"context"
	"database/sql"
	stderrors "errors"
	"fmt"
	"sync"

	"github.com/primandproper/platform-go/errors"
	"github.com/primandproper/platform-go/messagequeue"
	msgconfig "github.com/primandproper/platform-go/messagequeue/config"
	"github.com/primandproper/platform-go/observability"
	"github.com/primandproper/platform-go/observability/logging"
	"github.com/primandproper/platform-go/observability/metrics"
	"github.com/primandproper/platform-go/observability/tracing"
	"github.com/primandproper/platform-go/random"
	textsearch "github.com/primandproper/platform-go/search/text"

	"github.com/hashicorp/go-multierror"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const (
	serviceName = "indexer"
)

type Config struct {
	SearchDataIndexerTopicName string `env:"SEARCH_DATA_INDEX_TOPIC_NAME" json:"searchDataIndexerTopicName"`
}

type Function func(context.Context) ([]string, error)

type IndexScheduler struct {
	logger                   logging.Logger
	tracer                   tracing.Tracer
	handledRecordsCounter    metrics.Int64Counter
	searchDataIndexPublisher messagequeue.Publisher
	indexFunctions           map[string]Function
	allIndexTypes            []string
	indexManagementHat       sync.RWMutex
}

func NewIndexScheduler(
	ctx context.Context,
	logger logging.Logger,
	tracerProvider tracing.TracerProvider,
	metricsProvider metrics.Provider,
	messageQueuePublisherProvider messagequeue.PublisherProvider,
	queues *msgconfig.QueuesConfig,
	indexFunctions map[string]Function,
) (*IndexScheduler, error) {
	handledRecordsCounter, err := metricsProvider.NewInt64Counter(fmt.Sprintf("%s.handled_records", serviceName))
	if err != nil {
		return nil, err
	}

	searchDataIndexPublisher, err := messageQueuePublisherProvider.ProvidePublisher(ctx, queues.SearchIndexRequestsTopicName)
	if err != nil {
		return nil, err
	}

	indexFunctionsMap := indexFunctions
	if indexFunctions == nil {
		indexFunctionsMap = make(map[string]Function)
	}

	allIndexTypes := []string{}
	for k := range indexFunctionsMap {
		allIndexTypes = append(allIndexTypes, k)
	}

	return &IndexScheduler{
		handledRecordsCounter:    handledRecordsCounter,
		searchDataIndexPublisher: searchDataIndexPublisher,
		logger:                   logging.NewNamedLogger(logger, serviceName),
		tracer:                   tracing.NewNamedTracer(tracerProvider, serviceName),

		allIndexTypes:  allIndexTypes,
		indexFunctions: indexFunctionsMap,
	}, nil
}

func (i *IndexScheduler) IndexTypes(ctx context.Context) error {
	ctx, span := i.tracer.StartSpan(ctx)
	defer span.End()

	// figure out what records to join
	chosenIndex := random.Element(i.allIndexTypes)

	logger := i.logger.WithValue("chosen_index_type", chosenIndex)
	logger.Info("index type chosen")

	i.indexManagementHat.RLock()
	actionFunc, ok := i.indexFunctions[chosenIndex]
	if !ok {
		return errors.Newf("unknown index type %s", chosenIndex)
	}
	i.indexManagementHat.RUnlock()

	ids, err := actionFunc(ctx)
	if err != nil {
		if !stderrors.Is(err, sql.ErrNoRows) {
			observability.AcknowledgeError(err, logger, span, "getting %s IDs that need search indexing", chosenIndex)
			return err
		}
		return nil
	}

	if len(ids) > 0 {
		logger.WithValue("count", len(ids)).Info("publishing search index requests")
	}

	publishedIDCount := int64(0)
	errs := &multierror.Error{}
	for _, id := range ids {
		indexReq := &textsearch.IndexRequest{
			RowID:     id,
			IndexType: chosenIndex,
		}
		if err = i.searchDataIndexPublisher.Publish(ctx, indexReq); err != nil {
			errs = multierror.Append(errs, err)
		} else {
			publishedIDCount++
		}
	}

	i.handledRecordsCounter.Add(ctx, publishedIDCount, metric.WithAttributes(
		attribute.KeyValue{
			Key:   "record.type",
			Value: attribute.StringValue(chosenIndex),
		},
	))

	return nil
}
