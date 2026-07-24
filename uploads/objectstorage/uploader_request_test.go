package objectstorage

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	circuitbreakingcfg "github.com/primandproper/platform-go/v6/circuitbreaking/config"
	cbnoop "github.com/primandproper/platform-go/v6/circuitbreaking/noop"
	"github.com/primandproper/platform-go/v6/observability"
	"github.com/primandproper/platform-go/v6/observability/metrics"
	metricsnoop "github.com/primandproper/platform-go/v6/observability/metrics/noop"
	"github.com/primandproper/platform-go/v6/uploads"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	s3v2 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"gocloud.dev/blob"
	"gocloud.dev/blob/s3blob"
)

// newBucketUploader builds an Uploader backed by the supplied bucket with noop
// observability/metrics/breaker, so a test can drive Save/Open/etc. against a real
// (test) backend and inspect the outgoing requests.
func newBucketUploader(t *testing.T, bucket *blob.Bucket) *Uploader {
	t.Helper()

	mp := metrics.EnsureMetricsProvider(metricsnoop.NewMetricsProvider())

	saveCounter, err := mp.NewInt64Counter("test_saves")
	must.NoError(t, err)
	saveErrCounter, err := mp.NewInt64Counter("test_save_errors")
	must.NoError(t, err)
	latencyHist, err := mp.NewFloat64Histogram("test_latency_ms")
	must.NoError(t, err)

	return &Uploader{
		bucket:         bucket,
		o11y:           observability.NewRecordingObserver(),
		circuitBreaker: circuitbreakingcfg.EnsureCircuitBreaker(cbnoop.NewCircuitBreaker()),
		saveCounter:    saveCounter,
		saveErrCounter: saveErrCounter,
		latencyHist:    latencyHist,
	}
}

// TestUploader_Save_S3RequestShape drives a real Save through the gocloud s3blob + aws-sdk-go-v2
// stack (the same code path the S3/R2/Backblaze providers use in selectBucket) against an httptest
// server standing in for S3, and asserts the outbound PUT targets the right bucket/key and carries
// the body. The existing selectBucket tests only assert a bucket was opened, never that an operation
// issues a well-formed S3 request — this closes that blind spot (g-02).
func TestUploader_Save_S3RequestShape(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		const (
			bucketName = "test-bucket"
			objectKey  = "nested/path/object.txt"
			body       = "the object body"
		)

		var (
			mu         sync.Mutex
			gotMethod  string
			gotPath    string
			gotBody    string
			gotContent string
		)
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, _ := io.ReadAll(r.Body)
			mu.Lock()
			// The final PUT is the object write; earlier probe requests (if any) are harmless.
			if r.Method == http.MethodPut {
				gotMethod = r.Method
				gotPath = r.URL.Path
				gotBody = string(raw)
				gotContent = r.Header.Get("Content-Type")
			}
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(ts.Close)

		client := s3v2.New(s3v2.Options{
			BaseEndpoint: aws.String(ts.URL),
			Credentials:  credentials.NewStaticCredentialsProvider("test", "test", ""),
			Region:       "us-east-1",
			UsePathStyle: true,
		})

		bucket, err := s3blob.OpenBucketV2(ctx, client, bucketName, &s3blob.Options{UseLegacyList: false})
		must.NoError(t, err)
		t.Cleanup(func() { _ = bucket.Close() })

		u := newBucketUploader(t, bucket)

		must.NoError(t, u.Save(ctx, objectKey, strings.NewReader(body), uploads.WithContentType("text/plain")))

		mu.Lock()
		defer mu.Unlock()

		// Assert the request shape, not just that Save returned nil: right verb, right
		// path-style bucket/key, and the body actually reached the wire.
		test.EqOp(t, http.MethodPut, gotMethod)
		test.EqOp(t, "/"+bucketName+"/"+objectKey, gotPath)
		test.StrContains(t, gotBody, body)
		test.EqOp(t, "text/plain", gotContent)
	})
}
