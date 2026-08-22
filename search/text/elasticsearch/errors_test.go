package elasticsearch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	cbnoop "github.com/primandproper/platform-go/v13/circuitbreaking/noop"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	textsearch "github.com/primandproper/platform-go/v13/search/text"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func Test_decodeErrorBody(T *testing.T) {
	T.Parallel()

	T.Run("says what elasticsearch objected to", func(t *testing.T) {
		t.Parallel()

		body := strings.NewReader(`{"error":{"type":"index_not_found_exception","reason":"no such index [test]"},"status":404}`)

		err := decodeErrorBody(body, "404 Not Found")
		must.Error(t, err)
		test.StrContains(t, err.Error(), "index_not_found_exception")
		test.StrContains(t, err.Error(), "no such index [test]")
		test.StrContains(t, err.Error(), "404 Not Found")
	})

	T.Run("falls back to the reason alone", func(t *testing.T) {
		t.Parallel()

		err := decodeErrorBody(strings.NewReader(`{"error":{"reason":"all shards failed"}}`), "500 Internal Server Error")
		must.Error(t, err)
		test.StrContains(t, err.Error(), "all shards failed")
	})

	T.Run("a body with nothing in it still names the status", func(t *testing.T) {
		t.Parallel()

		err := decodeErrorBody(strings.NewReader(`{}`), "401 Unauthorized")
		must.Error(t, err)
		test.StrContains(t, err.Error(), "401 Unauthorized")
	})

	T.Run("a body that will not decode is reported as such", func(t *testing.T) {
		t.Parallel()

		err := decodeErrorBody(strings.NewReader(`<html>gateway timeout</html>`), "504 Gateway Timeout")
		must.Error(t, err)
		test.StrContains(t, err.Error(), "undecodable body")
	})
}

func TestIndexManager_Search_SurfacesTheErrorBody(T *testing.T) {
	T.Parallel()

	T.Run("a rejected query reports the cluster's reason", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Elastic-Product", "Elasticsearch")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"type":"search_phase_execution_exception","reason":"all shards failed"}}`))
		}))
		t.Cleanup(server.Close)

		im, _ := buildTestIndexManagerWithServer(t, server, cbnoop.NewCircuitBreaker())

		_, err := im.Search(t.Context(), textsearch.SearchRequest{Query: "anything"})
		must.Error(t, err)
		// The regression: this used to decode the body and then return an error
		// built from res.Warnings(), which is empty on almost every failure.
		test.StrContains(t, err.Error(), "search_phase_execution_exception")
		test.StrContains(t, err.Error(), "all shards failed")
	})
}

func Test_elasticsearchIsReadyToInit_HonorsCancellation(T *testing.T) {
	T.Parallel()

	T.Run("a canceled context stops the wait instead of sleeping it out", func(t *testing.T) {
		t.Parallel()

		// Nothing listens here, so every ping fails and the loop would otherwise
		// sleep a second between each of its twenty attempts.
		cfg := &Config{Address: "http://127.0.0.1:1"}

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		started := time.Now()

		err := elasticsearchIsReadyToInit(ctx, cfg, loggingnoop.NewLogger(), 20)
		must.Error(t, err)
		test.ErrorIs(t, err, context.Canceled)
		test.Less(t, time.Second, time.Since(started))
	})
}
