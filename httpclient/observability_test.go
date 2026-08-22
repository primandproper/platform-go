package httpclient

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability/keys"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	metricsmock "github.com/primandproper/platform-go/v13/observability/metrics/mock"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// errRefused stands in for a metrics provider that will not create an
// instrument, which is the only thing NewHTTPClient's error reports.
var errRefused = platformerrors.New("provider refused the instrument")

// recordingProvider is a metrics.Provider whose instruments remember what they
// were told, so a test can assert that a transport recorded what its
// documentation claims it records.
type recordingProvider struct {
	metrics.Provider

	counts  map[string]int64
	values  map[string][]float64
	seen    map[string][]attribute.Set
	created []string

	mu sync.Mutex
}

func newRecordingProvider() *recordingProvider {
	p := &recordingProvider{
		counts: map[string]int64{},
		values: map[string][]float64{},
		seen:   map[string][]attribute.Set{},
	}

	p.Provider = &metricsmock.ProviderMock{
		NewInt64CounterFunc: func(name string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
			p.note(name)

			return &recordingCounter{provider: p, name: name}, nil
		},
		NewFloat64HistogramFunc: func(name string, _ ...metric.Float64HistogramOption) (metrics.Float64Histogram, error) {
			p.note(name)

			return &recordingHistogram{provider: p, name: name}, nil
		},
	}

	return p
}

func (p *recordingProvider) note(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.created = append(p.created, name)
}

func (p *recordingProvider) add(name string, delta int64, set attribute.Set) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.counts[name] += delta
	p.seen[name] = append(p.seen[name], set)
}

func (p *recordingProvider) record(name string, value float64, set attribute.Set) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.values[name] = append(p.values[name], value)
	p.seen[name] = append(p.seen[name], set)
}

// count reports the total recorded to an instrument, keyed by its unprefixed
// name so the tests read as the doc comment does.
func (p *recordingProvider) count(name string) int64 {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.counts[serviceName+"_"+name]
}

func (p *recordingProvider) recorded(name string) []float64 {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.values[serviceName+"_"+name]
}

// attrs returns the attribute set of the first measurement recorded to an
// instrument.
func (p *recordingProvider) attrs(t *testing.T, name string) attribute.Set {
	t.Helper()

	p.mu.Lock()
	defer p.mu.Unlock()

	sets := p.seen[serviceName+"_"+name]
	must.SliceNotEmpty(t, sets)

	return sets[0]
}

// attrValues returns one attribute's value from every recording of a counter,
// in order, which is how a test states the sequence of outcomes a run produced
// rather than just their total.
func (p *recordingProvider) attrValues(t *testing.T, name, key string) []string {
	t.Helper()

	p.mu.Lock()
	defer p.mu.Unlock()

	sets := p.seen[serviceName+"_"+name]
	must.SliceNotEmpty(t, sets)

	values := make([]string, 0, len(sets))
	for _, set := range sets {
		values = append(values, attrValue(t, set, key))
	}

	return values
}

func attrValue(t *testing.T, set attribute.Set, key string) string {
	t.Helper()

	value, ok := set.Value(attribute.Key(key))
	must.True(t, ok)

	return value.String()
}

type recordingCounter struct {
	provider *recordingProvider
	name     string
}

func (c *recordingCounter) Add(_ context.Context, incr int64, options ...metric.AddOption) {
	c.provider.add(c.name, incr, metric.NewAddConfig(options).Attributes())
}

type recordingHistogram struct {
	provider *recordingProvider
	name     string
}

func (h *recordingHistogram) Record(_ context.Context, incr float64, options ...metric.RecordOption) {
	h.provider.record(h.name, incr, metric.NewRecordConfig(options).Attributes())
}

func TestTransportObservability(T *testing.T) {
	T.Parallel()

	T.Run("counts every attempt beyond the first", func(t *testing.T) {
		t.Parallel()

		provider := newRecordingProvider()

		var calls int

		client := newClient(t,
			WithTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
				calls++
				if calls < 3 {
					return response(http.StatusServiceUnavailable, ""), nil
				}

				return response(http.StatusOK, ""), nil
			})),
			WithRetryPolicy(&immediatePolicy{attempts: 4}),
			WithMetricsProvider(provider),
		)

		resp, err := get(t.Context(), client, "http://example.com:8080/widgets/1234")
		must.NoError(t, err)
		must.NoError(t, resp.Body.Close())

		// Three calls, two of them retries.
		test.EqOp(t, int64(2), provider.count("retry_attempts"))
		test.EqOp(t, int64(0), provider.count("retries_exhausted"))
	})

	T.Run("records the attempts a loop gave up on", func(t *testing.T) {
		t.Parallel()

		provider := newRecordingProvider()

		client := newClient(t,
			WithTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
				return response(http.StatusServiceUnavailable, ""), nil
			})),
			WithRetryPolicy(&immediatePolicy{attempts: 3}),
			WithMetricsProvider(provider),
		)

		resp, err := get(t.Context(), client, "http://example.com/widgets")
		must.NoError(t, err)
		must.NoError(t, resp.Body.Close())

		// The caller gets the 503 and no error, which is why the counter has to
		// exist: this is the only place the exhaustion is visible at all.
		test.EqOp(t, http.StatusServiceUnavailable, resp.StatusCode)
		test.EqOp(t, int64(1), provider.count("retries_exhausted"))

		set := provider.attrs(t, "retries_exhausted")
		test.EqOp(t, "503", attrValue(t, set, keys.ResponseStatusKey))
	})

	T.Run("a terminal response is not counted as an exhausted loop", func(t *testing.T) {
		t.Parallel()

		provider := newRecordingProvider()

		client := newClient(t,
			WithTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
				return response(http.StatusNotFound, ""), nil
			})),
			WithRetryPolicy(&immediatePolicy{attempts: 3}),
			WithMetricsProvider(provider),
		)

		resp, err := get(t.Context(), client, "http://example.com/widgets")
		must.NoError(t, err)
		must.NoError(t, resp.Body.Close())

		// Every 404 this client ever sees comes through the same path as an
		// exhausted loop; counting them together would make the metric useless.
		test.EqOp(t, int64(0), provider.count("retries_exhausted"))
		test.EqOp(t, int64(0), provider.count("retry_attempts"))
	})

	T.Run("counts requests an open circuit refused", func(t *testing.T) {
		t.Parallel()

		provider := newRecordingProvider()

		client := newClient(t,
			WithTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
				t.Error("the base transport should not have been reached")

				return nil, nil
			})),
			WithCircuitBreaker(openBreaker()),
			WithMetricsProvider(provider),
		)

		_, err := get(t.Context(), client, "http://example.com/widgets")
		must.Error(t, err)

		test.EqOp(t, int64(1), provider.count("circuit_rejections"))
		test.EqOp(t, int64(0), provider.count("circuit_outcomes"))
	})

	T.Run("records how each completed request was classified", func(t *testing.T) {
		t.Parallel()

		provider := newRecordingProvider()

		client := newClient(t,
			WithTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
				return response(http.StatusInternalServerError, ""), nil
			})),
			WithCircuitBreaker(closedBreaker()),
			WithMetricsProvider(provider),
		)

		resp, err := get(t.Context(), client, "http://example.com/widgets")
		must.NoError(t, err)
		must.NoError(t, resp.Body.Close())

		test.EqOp(t, int64(1), provider.count("circuit_outcomes"))

		set := provider.attrs(t, "circuit_outcomes")
		test.EqOp(t, OutcomeFailure.String(), attrValue(t, set, keys.OutcomeKey))
	})

	T.Run("counts requests the local limiter refused", func(t *testing.T) {
		t.Parallel()

		provider := newRecordingProvider()

		client := newClient(t,
			WithTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
				t.Error("the base transport should not have been reached")

				return nil, nil
			})),
			WithRateLimit(&stubLimiter{allow: func(string) (bool, error) { return false, nil }}),
			WithMetricsProvider(provider),
		)

		_, err := get(t.Context(), client, "http://example.com/widgets")
		must.Error(t, err)

		test.EqOp(t, int64(1), provider.count("rate_limited"))
	})

	T.Run("records the Retry-After delays it honored", func(t *testing.T) {
		t.Parallel()

		provider := newRecordingProvider()

		var calls int

		client := newClient(t,
			WithTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					return withHeader(response(http.StatusTooManyRequests, ""), "Retry-After", "1"), nil
				}

				return response(http.StatusOK, ""), nil
			})),
			WithRetryPolicy(&immediatePolicy{attempts: 2}, WithMaxRetryAfter(2*time.Second)),
			WithMetricsProvider(provider),
		)

		resp, err := get(t.Context(), client, "http://example.com/widgets")
		must.NoError(t, err)
		must.NoError(t, resp.Body.Close())

		test.Eq(t, []float64{1}, provider.recorded("retry_after_seconds"))
	})

	T.Run("attributes carry the host and method, never the path", func(t *testing.T) {
		t.Parallel()

		provider := newRecordingProvider()

		client := newClient(t,
			WithTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
				return response(http.StatusOK, ""), nil
			})),
			WithCircuitBreaker(closedBreaker()),
			WithMetricsProvider(provider),
		)

		// A per-request ID in the path is exactly what must not become a metric
		// attribute: it is unbounded, and one client would blow up the series.
		resp, err := get(t.Context(), client, "http://example.com:8080/widgets/01HQZX9J")
		must.NoError(t, err)
		must.NoError(t, resp.Body.Close())

		set := provider.attrs(t, "circuit_outcomes")
		test.EqOp(t, "example.com:8080", attrValue(t, set, keys.ServerAddressKey))
		test.EqOp(t, http.MethodGet, attrValue(t, set, keys.RequestMethodKey))

		for _, kv := range set.ToSlice() {
			test.False(t, strings.Contains(kv.Value.String(), "01HQZX9J"))
		}
	})

	T.Run("the cache records how each request was answered", func(t *testing.T) {
		t.Parallel()

		provider := newRecordingProvider()
		clk := &steppingClock{now: time.Unix(1_700_000_000, 0)}

		var calls int
		client := newClient(t,
			WithTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
				calls++

				if calls == 1 {
					resp := withHeader(response(http.StatusOK, "jwks"), "Cache-Control", "max-age=60")

					return withHeader(resp, "ETag", `"v1"`), nil
				}

				return response(http.StatusNotModified, ""), nil
			})),
			WithHTTPCache(cacheForTest(t), WithCacheClock(clk)),
			WithMetricsProvider(provider),
		)

		read := func() {
			resp, err := get(t.Context(), client, cacheURL)
			must.NoError(t, err)
			must.NoError(t, resp.Body.Close())
		}

		read() // miss
		read() // hit

		clk.advance(61 * time.Second)

		read() // revalidated

		resp, err := post(t.Context(), client, cacheURL, strings.NewReader("body"))
		must.NoError(t, err)
		must.NoError(t, resp.Body.Close())

		test.Eq(t, []string{
			cacheOutcomeMiss,
			cacheOutcomeHit,
			cacheOutcomeRevalidated,
			cacheOutcomeUncacheable,
		}, provider.attrValues(t, "cache_outcomes", keys.CacheOutcomeKey))
	})

	T.Run("every instrument the transports record to is created", func(t *testing.T) {
		t.Parallel()

		provider := newRecordingProvider()

		_, err := NewHTTPClient(WithMetricsProvider(provider))
		must.NoError(t, err)

		// The names are package constants, so this is the check that keeps the
		// error NewHTTPClient returns honest: if one of them were malformed, the
		// provider would refuse it here rather than at the first request.
		test.SliceContainsAll(t, []string{
			serviceName + "_retry_attempts",
			serviceName + "_retries_exhausted",
			serviceName + "_circuit_rejections",
			serviceName + "_circuit_outcomes",
			serviceName + "_rate_limited",
			serviceName + "_cache_outcomes",
			serviceName + "_signing_failures",
			serviceName + "_retry_after_seconds",
		}, provider.created)
	})

	T.Run("a refused instrument fails construction", func(t *testing.T) {
		t.Parallel()

		provider := &metricsmock.ProviderMock{
			NewInt64CounterFunc: func(string, ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				return nil, errRefused
			},
		}

		client, err := NewHTTPClient(WithMetricsProvider(provider))
		test.Nil(t, client)
		test.ErrorIs(t, err, errRefused)
	})

	T.Run("a client given no pillars records nowhere and still works", func(t *testing.T) {
		t.Parallel()

		client := newClient(t,
			WithTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
				return response(http.StatusOK, ""), nil
			})),
			WithRetryPolicy(&immediatePolicy{attempts: 2}),
			WithCircuitBreaker(closedBreaker()),
			WithRateLimit(&stubLimiter{allow: func(string) (bool, error) { return true, nil }}),
		)

		resp, err := get(t.Context(), client, "http://example.com/widgets")
		must.NoError(t, err)
		must.NoError(t, resp.Body.Close())

		test.EqOp(t, http.StatusOK, resp.StatusCode)
	})
}
