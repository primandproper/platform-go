package service

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v14/metering"
	asyncnoop "github.com/primandproper/platform-go/v14/notifications/async/noop"
	"github.com/primandproper/platform-go/v14/operations"
	"github.com/primandproper/platform-go/v14/outbox"
	"github.com/primandproper/platform-go/v14/saga"
	"github.com/primandproper/platform-go/v14/webhooks"

	"github.com/primandproper/primitives-go/analytics"
	analyticsmock "github.com/primandproper/primitives-go/analytics/mock"
	"github.com/primandproper/primitives-go/database"
	databasemock "github.com/primandproper/primitives-go/database/mock"
	"github.com/primandproper/primitives-go/distributedlock"
	distributedlockmock "github.com/primandproper/primitives-go/distributedlock/mock"
	"github.com/primandproper/primitives-go/featureflags"
	featureflagsmock "github.com/primandproper/primitives-go/featureflags/mock"
	"github.com/primandproper/primitives-go/jobs"
	"github.com/primandproper/primitives-go/messagequeue"
	messagequeuemock "github.com/primandproper/primitives-go/messagequeue/mock"
	"github.com/primandproper/primitives-go/ratelimiting"
	"github.com/primandproper/primitives-go/secrets"
	grpcserver "github.com/primandproper/primitives-go/server/grpc"
	httpserver "github.com/primandproper/primitives-go/server/http"
	"github.com/primandproper/primitives-go/uploads"
	uploadsmock "github.com/primandproper/primitives-go/uploads/mock"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// fakeSecretSource and fakeRateLimiter stand in for the two components with no
// generated mock. Neither is called: the test they exist for is about where a
// component lands in the ordering, not what it does.
type fakeSecretSource struct{}

func (fakeSecretSource) GetSecret(context.Context, string) (string, error) { return "", nil }
func (fakeSecretSource) Close() error                                      { return nil }

type fakeRateLimiter struct{}

func (fakeRateLimiter) Allow(context.Context, string) (bool, error) { return true, nil }
func (fakeRateLimiter) Close() error                                { return nil }

func TestNew_ordering(T *testing.T) {
	T.Parallel()

	T.Run("gives every registered component its place in the order", func(t *testing.T) {
		t.Parallel()

		// The ordering table, asserted in one place. Everything here is
		// registered directly rather than configured, because what is under
		// test is where each component lands on the way up and therefore on the
		// way down — not that its package can build one. The loops are
		// zero-valued for the same reason: nothing invokes them.
		notifier, err := asyncnoop.NewAsyncNotifier()
		must.NoError(t, err)

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, &Config{Name: "example"})

		do.ProvideValue[database.Client](i, &databasemock.ClientMock{})
		do.ProvideValue[messagequeue.PublisherProvider](i, &messagequeuemock.PublisherProviderMock{})
		do.ProvideValue[messagequeue.ConsumerProvider](i, &messagequeuemock.ConsumerProviderMock{})
		do.ProvideValue[secrets.SecretSource](i, fakeSecretSource{})
		do.ProvideValue[uploads.UploadManager](i, &uploadsmock.UploadManagerMock{})
		do.ProvideValue[distributedlock.Locker](i, &distributedlockmock.LockerMock{})
		do.ProvideValue[ratelimiting.RateLimiter](i, fakeRateLimiter{})
		do.ProvideValue[analytics.EventReporter](i, &analyticsmock.EventReporterMock{})
		do.ProvideValue[featureflags.FeatureFlagManager](i, &featureflagsmock.FeatureFlagManagerMock{})
		do.ProvideValue(i, notifier)

		do.ProvideValue(i, &outbox.Relay{})
		do.ProvideValue(i, &jobs.Pool{})
		do.ProvideValue(i, &jobs.Scheduler{})
		do.ProvideValue(i, &saga.Worker{})
		do.ProvideValue(i, &webhooks.Worker{})
		do.ProvideValue(i, &operations.Worker{})

		do.ProvideValue(i, &metering.Flusher{})

		do.ProvideValue[httpserver.Server](i, newFakeServer(&journal{}, "http"))
		do.ProvideValue(i, &grpcserver.Server{})

		svc, err := New(i)
		must.NoError(t, err)

		// Built first, released last: the database is at the head of the list
		// every loop above it can still reach on its way out.
		test.Eq(t, []string{
			"database client",
			"message queue publishers",
			"message queue consumers",
			"secret source",
			"upload manager",
			"distributed locker",
			"rate limiter",
			"analytics reporter",
			"feature flag manager",
			"async notifier",
		}, names(svc.closers))

		// Start order, which shutdown reverses.
		test.Eq(t, []string{
			"outbox relay",
			"jobs pool",
			"jobs scheduler",
			"saga worker",
			"webhooks worker",
			"operations worker",
		}, names(svc.runners))

		test.Eq(t, []string{"metering flusher"}, names(svc.flushes))

		// Ingress last, so nothing can be asked for before it exists.
		test.Eq(t, []string{"HTTP server", "gRPC server"}, names(svc.servers))
	})
}

var (
	_ secrets.SecretSource     = fakeSecretSource{}
	_ ratelimiting.RateLimiter = fakeRateLimiter{}
)
