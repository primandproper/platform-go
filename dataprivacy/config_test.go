package dataprivacy

import (
	"strings"
	"testing"
	"time"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/operations"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestServiceConfig(T *testing.T) {
	T.Parallel()

	T.Run("fills defaults", func(t *testing.T) {
		t.Parallel()

		cfg := &ServiceConfig{}
		cfg.EnsureDefaults()

		test.EqOp(t, DefaultResponseWindow, cfg.ExportResponseWindow)
		test.EqOp(t, DefaultResponseWindow, cfg.ErasureResponseWindow)
		test.EqOp(t, DefaultSignedURLTTL, cfg.SignedURLTTL)

		// Zero by default: erasures are queued on submission and Confirm is
		// never needed unless an operator asks for it.
		test.EqOp(t, time.Duration(0), cfg.ConfirmationWindow)

		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("leaves configured values alone", func(t *testing.T) {
		t.Parallel()

		cfg := &ServiceConfig{
			ExportResponseWindow:  time.Hour,
			ErasureResponseWindow: 2 * time.Hour,
			SignedURLTTL:          time.Minute,
			ConfirmationWindow:    72 * time.Hour,
		}
		cfg.EnsureDefaults()

		test.EqOp(t, time.Hour, cfg.ExportResponseWindow)
		test.EqOp(t, 2*time.Hour, cfg.ErasureResponseWindow)
		test.EqOp(t, time.Minute, cfg.SignedURLTTL)
		test.EqOp(t, 72*time.Hour, cfg.ConfirmationWindow)
	})

	T.Run("selects the window per request type", func(t *testing.T) {
		t.Parallel()

		cfg := &ServiceConfig{ExportResponseWindow: time.Hour, ErasureResponseWindow: 2 * time.Hour}

		test.EqOp(t, time.Hour, cfg.responseWindow(RequestExport))
		test.EqOp(t, 2*time.Hour, cfg.responseWindow(RequestErasure))
	})
}

func TestFulfillerConfig(T *testing.T) {
	T.Parallel()

	T.Run("fills defaults", func(t *testing.T) {
		t.Parallel()

		cfg := &FulfillerConfig{}
		cfg.EnsureDefaults()

		test.EqOp(t, DefaultCollectorConcurrency, cfg.CollectorConcurrency)
		test.EqOp(t, DefaultCollectorTimeout, cfg.CollectorTimeout)
		test.EqOp(t, DefaultFulfillmentTimeout, cfg.FulfillmentTimeout)
		test.EqOp(t, DefaultMaxAttempts, cfg.MaxAttempts)
		test.EqOp(t, DefaultArtifactTTL, cfg.ArtifactTTL)
		test.EqOp(t, DefaultArtifactPathPrefix, cfg.ArtifactPathPrefix)
		test.EqOp(t, DefaultMaxDocumentBytes, cfg.MaxDocumentBytes)

		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("the default attempt is bounded and lower than the operations worker's", func(t *testing.T) {
		t.Parallel()

		cfg := &FulfillerConfig{}
		cfg.EnsureDefaults()

		// One attempt is a fan-out over every registered domain, and a request
		// that is going to fail is worth failing while there is still time
		// inside the statutory window to fix the cause.
		test.Greater(t, 0, cfg.MaxAttempts)
		test.Less(t, operations.DefaultWorkerMaxAttempts, cfg.MaxAttempts)
	})

	T.Run("the default attempt outlasts one collector", func(t *testing.T) {
		t.Parallel()

		cfg := &FulfillerConfig{}
		cfg.EnsureDefaults()

		test.Greater(t, cfg.CollectorTimeout, cfg.FulfillmentTimeout)
	})

	T.Run("rejects a collector timeout that outlasts the whole attempt", func(t *testing.T) {
		t.Parallel()

		cfg := &FulfillerConfig{FulfillmentTimeout: time.Minute, CollectorTimeout: time.Hour}
		cfg.EnsureDefaults()

		err := cfg.ValidateWithContext(t.Context())
		must.Error(t, err)
		test.StrContains(t, err.Error(), "must exceed collector timeout")
	})
}

func TestSweeperConfig(T *testing.T) {
	T.Parallel()

	T.Run("fills defaults", func(t *testing.T) {
		t.Parallel()

		cfg := &SweeperConfig{}
		cfg.EnsureDefaults()

		test.EqOp(t, DefaultRequestRetention, cfg.RequestRetention)
		test.EqOp(t, DefaultSweepBatchSize, cfg.BatchSize)
		test.False(t, cfg.DisableReap)

		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})
}

func TestSubject(T *testing.T) {
	T.Parallel()

	T.Run("requires an ID", func(t *testing.T) {
		t.Parallel()

		test.ErrorIs(t, Subject{}.validate(), ErrEmptySubjectID)
		test.NoError(t, Subject{ID: "user-1"}.validate())
	})
}

func TestTruncate(T *testing.T) {
	T.Parallel()

	T.Run("leaves a short error alone", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "short", truncateError(platformerrors.New("short")))
	})

	T.Run("cuts without splitting a rune", func(t *testing.T) {
		t.Parallel()

		// A truncated error still has to be valid UTF-8 or it will not store.
		// maxStoredErrorLength lands mid-rune here: 1023 'a's then a two-byte '£'.
		cut := truncateError(platformerrors.New(strings.Repeat("a", maxStoredErrorLength-1) + "£"))

		test.EqOp(t, strings.Repeat("a", maxStoredErrorLength-1), cut)
		test.True(t, utf8ValidString(cut))
	})

	T.Run("renders a nil error as empty", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "", truncateError(nil))
	})
}

// utf8ValidString is a tiny indirection so the assertion above reads as an
// assertion rather than as a call into unicode/utf8.
func utf8ValidString(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}

	return true
}
