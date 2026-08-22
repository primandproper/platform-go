package dataprivacy

import (
	"context"
	"time"

	"github.com/primandproper/platform-go/v13/audit"
	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/compression"
	"github.com/primandproper/platform-go/v13/cryptography/encryption"
	"github.com/primandproper/platform-go/v13/cryptography/shredding"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
	"github.com/primandproper/platform-go/v13/uploads"
)

// EmailNotifierOption configures an EmailNotifier.
type EmailNotifierOption func(*EmailNotifier)

// WithMessageRenderer replaces the default message.
func WithMessageRenderer(renderer MessageRenderer) EmailNotifierOption {
	return func(n *EmailNotifier) {
		if renderer != nil {
			n.renderer = renderer
		}
	}
}

// ServiceOption configures a Service.
type ServiceOption func(*StoreService)

// WithServiceClock swaps the clock stamping submission, deadline, and expiry.
func WithServiceClock(c clock.Clock) ServiceOption {
	return func(s *StoreService) {
		if c != nil {
			s.clock = c
		}
	}
}

// WithServiceLogger attaches a logger.
func WithServiceLogger(logger logging.Logger) ServiceOption {
	return func(s *StoreService) {
		s.logger = logger
	}
}

// WithServiceTracerProvider attaches a tracer provider.
func WithServiceTracerProvider(tracerProvider tracing.Provider) ServiceOption {
	return func(s *StoreService) {
		s.tracerProvider = tracerProvider
	}
}

// WithServiceMetricsProvider attaches a metrics provider.
func WithServiceMetricsProvider(metricsProvider metrics.Provider) ServiceOption {
	return func(s *StoreService) {
		s.metricsProvider = metricsProvider
	}
}

// WithServiceUploadManager supplies the storage artifacts are read from.
//
// It must be the same storage, and the same path prefix, that the Fulfiller
// writes to. Nothing here can check that, and a mismatch surfaces as an artifact that
// exists in the bucket and cannot be found by the service that promised it.
func WithServiceUploadManager(manager uploads.UploadManager) ServiceOption {
	return func(s *StoreService) {
		if manager != nil {
			s.uploader = manager
		}
	}
}

// WithServiceCompressor supplies the compressor artifacts were written with. It
// must match the Fulfiller's, or Open returns garbage.
func WithServiceCompressor(compressor compression.Compressor) ServiceOption {
	return func(s *StoreService) {
		if compressor != nil {
			s.packager.compressor = compressor
		}
	}
}

// WithServiceDecryptor supplies the decryptor for artifacts written encrypted.
// It must match the Fulfiller's encryptor.
//
// Setting it also disables Download: see ErrArtifactEncrypted.
func WithServiceDecryptor(decryptor encryption.Decryptor) ServiceOption {
	return func(s *StoreService) {
		if decryptor != nil {
			s.packager.decryptor = decryptor
			// Recorded so Download can refuse rather than hand out a link to
			// ciphertext. The Service never encrypts — only the Fulfiller does
			// — but a configured decryptor is proof that it does, and is the
			// only evidence of that available on this side.
			s.packager.encryptor = encryptorPresent{}
		}
	}
}

// WithServiceAuditRecorder attaches the audit log this package writes to.
//
// Every submission and every state change it drives is recorded. That is not
// decoration: an export artifact is the most sensitive object an application
// produces, and a system that can produce one without leaving a record of who
// asked has a data exfiltration path with no alarm on it.
func WithServiceAuditRecorder(recorder audit.Recorder) ServiceOption {
	return func(s *StoreService) {
		if recorder != nil {
			s.recorder = recorder
		}
	}
}

// WithActorResolver supplies the principal recorded in audit entries.
func WithActorResolver(resolver ActorResolver) ServiceOption {
	return func(s *StoreService) {
		if resolver != nil {
			s.actor = resolver
		}
	}
}

// SQLStoreOption configures a SQL Store.
type SQLStoreOption func(*SQLStore)

// WithTablePrefix overrides DefaultTablePrefix. It must be a plain SQL
// identifier fragment: it is interpolated into the query text, not bound as a
// parameter, and it must match the prefix the migrations were rendered with.
func WithTablePrefix(prefix string) SQLStoreOption {
	return func(s *SQLStore) {
		if prefix != "" {
			s.tables = newTables(prefix)
		}
	}
}

// WithStoreLogger attaches a logger.
func WithStoreLogger(logger logging.Logger) SQLStoreOption {
	return func(s *SQLStore) {
		s.logger = logger
	}
}

// WithStoreTracerProvider attaches a tracer provider.
func WithStoreTracerProvider(tracerProvider tracing.Provider) SQLStoreOption {
	return func(s *SQLStore) {
		s.tracerProvider = tracerProvider
	}
}

// WithStoreMetricsProvider attaches a metrics provider.
func WithStoreMetricsProvider(metricsProvider metrics.Provider) SQLStoreOption {
	return func(s *SQLStore) {
		s.metricsProvider = metricsProvider
	}
}

// SweeperOption configures a Sweeper.
type SweeperOption func(*Sweeper)

// WithSweeperClock swaps the clock deciding what has expired.
func WithSweeperClock(c clock.Clock) SweeperOption {
	return func(s *Sweeper) {
		if c != nil {
			s.clock = c
		}
	}
}

// WithSweeperLogger attaches a logger.
func WithSweeperLogger(logger logging.Logger) SweeperOption {
	return func(s *Sweeper) {
		s.logger = logger
	}
}

// WithSweeperTracerProvider attaches a tracer provider.
func WithSweeperTracerProvider(tracerProvider tracing.Provider) SweeperOption {
	return func(s *Sweeper) {
		s.tracerProvider = tracerProvider
	}
}

// WithSweeperMetricsProvider attaches a metrics provider, enabling the overdue
// gauge — which is the one instrument in this package worth alerting on.
func WithSweeperMetricsProvider(metricsProvider metrics.Provider) SweeperOption {
	return func(s *Sweeper) {
		s.metricsProvider = metricsProvider
	}
}

// WithSweeperUploadManager supplies the storage artifacts are deleted from. It
// must be the same storage the Fulfiller writes to.
//
// Without it the Sweeper refuses to expire artifacts at all rather than marking
// rows expired against objects it cannot delete — a row that says the artifact
// is gone while the artifact is not is worse than no sweep, because it stops
// anybody looking.
func WithSweeperUploadManager(manager uploads.UploadManager) SweeperOption {
	return func(s *Sweeper) {
		if manager != nil {
			s.uploader = manager
		}
	}
}

// FulfillerOption configures a Fulfiller.
type FulfillerOption func(*Fulfiller)

// WithFulfillerClock swaps the clock stamping completions, artifact expiry, and
// audit entries.
func WithFulfillerClock(c clock.Clock) FulfillerOption {
	return func(f *Fulfiller) {
		if c != nil {
			f.clock = c
		}
	}
}

// WithFulfillerLogger attaches a logger. A failing collector is reported through
// it and nowhere else — there is no caller to return it to — so without one a
// domain that has been failing to collect for a week is visible only in
// metrics.
func WithFulfillerLogger(logger logging.Logger) FulfillerOption {
	return func(f *Fulfiller) {
		f.logger = logger
	}
}

// WithFulfillerTracerProvider attaches a tracer provider. The spans it produces
// hang under the operations worker's, so a slow export reads as one trace from
// the claim through to the domain that took the time.
func WithFulfillerTracerProvider(tracerProvider tracing.Provider) FulfillerOption {
	return func(f *Fulfiller) {
		f.tracerProvider = tracerProvider
	}
}

// WithFulfillerMetricsProvider attaches a metrics provider.
func WithFulfillerMetricsProvider(metricsProvider metrics.Provider) FulfillerOption {
	return func(f *Fulfiller) {
		f.metricsProvider = metricsProvider
	}
}

// WithFulfillerUploadManager supplies the storage artifacts are written to.
// Required for exports; an erasure-only Fulfiller does not need it.
func WithFulfillerUploadManager(manager uploads.UploadManager) FulfillerOption {
	return func(f *Fulfiller) {
		if manager != nil {
			f.uploader = manager
		}
	}
}

// WithFulfillerCompressor compresses artifacts before they are stored.
//
// Worth setting. An export is JSON assembled from every domain in an
// application, which is the most compressible shape there is — and the artifact
// is written once and read at most once, so the compression is nearly free.
func WithFulfillerCompressor(compressor compression.Compressor) FulfillerOption {
	return func(f *Fulfiller) {
		if compressor != nil {
			f.packager.compressor = compressor
		}
	}
}

// WithFulfillerEncryptor encrypts artifacts at rest.
//
// It changes what delivery is possible: an encrypted artifact cannot be handed
// out as a signed URL, because the subject would receive ciphertext. See
// ErrArtifactEncrypted. Configure the Service with the matching decryptor.
func WithFulfillerEncryptor(encryptor encryption.Encryptor) FulfillerOption {
	return func(f *Fulfiller) {
		if encryptor != nil {
			f.packager.encryptor = encryptor
		}
	}
}

// WithFulfillerShredder destroys the subject's data key as part of an erasure, so
// the erasure reaches media that deletion cannot.
//
// Without it an erasure deletes rows, and the rows stay in every backup taken
// before it ran — for the whole retention window, which is the part of "we
// erased you" that is not true. Destroying the key makes every ciphertext it
// protected unreadable everywhere at once, including in snapshots nobody can
// write to.
//
// It is not a substitute for the erasers. Only the columns an application chose
// to encrypt under the subject's key are covered, the shred does not run inside
// their transaction, and what it destroys it destroys whether or not they
// succeed. See Fulfiller.erase's ordering, which is deliberate and stated in the
// source.
//
// Setting it on a Fulfiller whose application encrypts nothing per subject is
// harmless and close to pointless: every erasure writes a tombstone and destroys
// nothing, which Request.KeyShreddedAt will happily record.
func WithFulfillerShredder(shredder shredding.Shredder) FulfillerOption {
	return func(f *Fulfiller) {
		if shredder != nil {
			f.shredder = shredder
		}
	}
}

// WithFulfillerNotifier supplies who to tell when a request finishes.
func WithFulfillerNotifier(notifier Notifier) FulfillerOption {
	return func(f *Fulfiller) {
		if notifier != nil {
			f.notifier = notifier
		}
	}
}

// WithFulfillerAuditRecorder attaches the audit log completions are recorded in.
//
// The completion entry is the one that says what was actually disclosed or
// destroyed, and it is written in the same transaction as the state change it
// describes.
func WithFulfillerAuditRecorder(recorder audit.Recorder) FulfillerOption {
	return func(f *Fulfiller) {
		if recorder != nil {
			f.recorder = recorder
		}
	}
}

// WithFulfillerActorResolver supplies the principal recorded in audit entries.
func WithFulfillerActorResolver(resolver ActorResolver) FulfillerOption {
	return func(f *Fulfiller) {
		if resolver != nil {
			f.actor = resolver
		}
	}
}

// WithFulfillerURLSigner supplies how a notification's download URL is minted.
//
// It exists so the Fulfiller can hand the subject a link without holding a
// Service — which would be circular, since a Service is the thing that reads
// what this Fulfiller writes. The signer returns the URL and its expiry; an
// empty URL means the notification carries no link, which is correct for
// encrypted artifacts and for providers that cannot sign.
func WithFulfillerURLSigner(signer func(ctx context.Context, req *Request) (url string, expiresAt time.Time)) FulfillerOption {
	return func(f *Fulfiller) {
		if signer != nil {
			f.signer = signer
		}
	}
}

// URLSignerOption configures the signer NewArtifactURLSigner returns.
//
// It is its own type rather than a FulfillerOption because the signer is built
// before the Fulfiller it is handed to, and is equally usable by a caller that
// has no Fulfiller at all.
type URLSignerOption func(*clock.Clock)

// WithURLSignerClock swaps the clock the signer stamps its expiry against, so a
// Fulfiller under a test clock and the notification it sends agree about when
// the link stops working. An absent clock reads the wall clock.
func WithURLSignerClock(c clock.Clock) URLSignerOption {
	return func(dst *clock.Clock) {
		if c != nil {
			*dst = c
		}
	}
}
