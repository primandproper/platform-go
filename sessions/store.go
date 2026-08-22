package sessions

import (
	"context"
	stderrors "errors"
	"fmt"
	"time"

	"github.com/primandproper/platform-go/v13/clock"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/metrics"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var _ Store[struct{}] = (*BackendStore[struct{}])(nil)

// BackendStore is the one Store implementation: a Policy, an identifier mint,
// and a Backend. It is exported, and returned by NewStore, so a caller can
// depend on the store it built rather than on the Store seam.
type BackendStore[T any] struct {
	backend Backend[T]
	clock   clock.Clock
	o11y    observability.Observer

	createdCounter       metrics.Int64Counter
	renewedCounter       metrics.Int64Counter
	endedCounter         metrics.Int64Counter
	expiredCounter       metrics.Int64Counter
	touchCounter         metrics.Int64Counter
	touchFailureCounter  metrics.Int64Counter
	staleRecordCounter   metrics.Int64Counter
	backendErrorsCounter metrics.Int64Counter
	latencyHist          metrics.Float64Histogram

	policy Policy
}

// NewStore builds a Store over a Backend.
//
// The Backend is required and has no default. An implicit in-memory one would
// work in every test and lose every session on deploy in production, which is
// the failure mode that looks like intermittent sign-outs for a week before
// anyone finds it.
func NewStore[T any](backend Backend[T], opts ...Option) (*BackendStore[T], error) {
	if backend == nil {
		return nil, ErrNilBackend
	}

	o := newStoreOptions(opts)

	touch := DefaultTouchInterval
	if o.touch != nil {
		touch = *o.touch
	}

	// Clamped rather than rejected: a short idle timeout is a legitimate
	// choice, and failing construction because the default touch interval does
	// not fit inside it would make WithIdleTimeout(30*time.Second) an error
	// nobody caused. An explicit WithTouchInterval that does not fit is still
	// rejected by Policy.Validate, since that one was asked for.
	if o.touch == nil && o.idleTimeout > 0 && touch >= o.idleTimeout {
		touch = o.idleTimeout / 2
	}

	policy := Policy{
		Absolute: o.absoluteTimeout,
		Idle:     o.idleTimeout,
		Touch:    touch,
		Grace:    o.grace,
	}

	if err := policy.Validate(); err != nil {
		return nil, err
	}

	s := &BackendStore[T]{
		backend: backend,
		clock:   o.clock,
		policy:  policy,
		o11y:    observability.NewObserver(serviceName, o.logger, o.tracerProvider),
	}

	mp := metrics.EnsureMetricsProvider(o.metricsProvider)

	var err error
	if s.createdCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_created", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating sessions created counter")
	}
	if s.renewedCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_renewed", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating sessions renewed counter")
	}
	if s.endedCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_ended", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating sessions ended counter")
	}
	if s.expiredCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_expired", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating sessions expired counter")
	}
	if s.touchCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_touches", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating session touches counter")
	}
	if s.touchFailureCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_touch_failures", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating session touch failures counter")
	}
	if s.staleRecordCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_stale_records", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating stale session records counter")
	}
	if s.backendErrorsCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_backend_errors", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating session backend errors counter")
	}
	if s.latencyHist, err = mp.NewFloat64Histogram(fmt.Sprintf("%s_latency_ms", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating session latency histogram")
	}

	return s, nil
}

// Policy reports the expiry rule this store enforces.
func (s *BackendStore[T]) Policy() Policy {
	return s.policy
}

// New establishes a session around data.
func (s *BackendStore[T]) New(ctx context.Context, data *T) (*Session[T], error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()
	defer s.observe(ctx, operationNew, s.clock.Now())

	id, err := NewID(ctx)
	if err != nil {
		return nil, op.Error(err, "minting session identifier")
	}

	now := s.now()
	record := &Record[T]{
		CreatedAt:  now,
		LastSeenAt: now,
		Data:       data,
		Version:    recordVersion,
	}

	if err = s.backend.Create(ctx, id, record, s.policy.RetentionTTL(now, now)); err != nil {
		s.backendErrorsCounter.Add(ctx, 1)

		return nil, op.Error(err, "storing new session")
	}

	s.createdCounter.Add(ctx, 1)

	session := s.session(id, record)
	op.Set(createdAtKey, session.CreatedAt).Set(expiresAtKey, session.ExpiresAt)

	return session, nil
}

// Get reads a session and refreshes its idle deadline when the touch interval
// has elapsed.
func (s *BackendStore[T]) Get(ctx context.Context, id string) (*Session[T], error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()
	defer s.observe(ctx, operationGet, s.clock.Now())

	record, now, err := s.live(ctx, op, id)
	if err != nil {
		return nil, err
	}

	if !s.policy.ShouldTouch(record.LastSeenAt, now) {
		op.Set(touchedKey, false)

		return s.session(id, record), nil
	}

	touched := *record
	touched.LastSeenAt = now

	if err = s.backend.Update(ctx, id, &touched, s.policy.RetentionTTL(touched.CreatedAt, now)); err != nil {
		// The read still succeeded, and the session is still live — it just
		// keeps the deadline it already had. Failing here would sign a user out
		// over a blip in the store, which is a worse answer than a session that
		// expires on its old schedule. sessions_touch_failures is the signal.
		s.touchFailureCounter.Add(ctx, 1)
		s.backendErrorsCounter.Add(ctx, 1)
		op.Acknowledge(err, "refreshing session idle deadline")
		op.Set(touchedKey, false)

		return s.session(id, record), nil
	}

	s.touchCounter.Add(ctx, 1)
	op.Set(touchedKey, true)

	return s.session(id, &touched), nil
}

// Save replaces a session's payload.
func (s *BackendStore[T]) Save(ctx context.Context, id string, data *T) error {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()
	defer s.observe(ctx, operationSave, s.clock.Now())

	record, now, err := s.live(ctx, op, id)
	if err != nil {
		return err
	}

	updated := *record
	updated.Data = data
	updated.LastSeenAt = now

	if err = s.backend.Update(ctx, id, &updated, s.policy.RetentionTTL(updated.CreatedAt, now)); err != nil {
		s.backendErrorsCounter.Add(ctx, 1)

		return op.Error(err, "saving session payload")
	}

	return nil
}

// Renew rotates a session's identifier.
//
// CreatedAt is carried across untouched, which is what keeps the absolute
// timeout absolute: a caller renewing on every privilege change — the correct
// thing to do — cannot thereby give a session an unbounded life.
func (s *BackendStore[T]) Renew(ctx context.Context, oldID string) (string, error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()
	defer s.observe(ctx, operationRenew, s.clock.Now())

	record, now, err := s.live(ctx, op, oldID)
	if err != nil {
		return "", err
	}

	newID, err := NewID(ctx)
	if err != nil {
		return "", op.Error(err, "minting renewed session identifier")
	}

	renewed := *record
	renewed.LastSeenAt = now

	if err = s.backend.Rename(ctx, oldID, newID, &renewed, s.policy.RetentionTTL(renewed.CreatedAt, now)); err != nil {
		s.backendErrorsCounter.Add(ctx, 1)

		// Deliberately no partial success. A caller that reads an error here
		// has to assume the old identifier still resolves, and refuse whatever
		// privilege change prompted the renewal — which is only actionable if
		// no new identifier came back with it.
		return "", op.Error(err, "renewing session identifier")
	}

	s.renewedCounter.Add(ctx, 1)

	return newID, nil
}

// Delete ends a session.
func (s *BackendStore[T]) Delete(ctx context.Context, id string) error {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()
	defer s.observe(ctx, operationDelete, s.clock.Now())

	if id == "" {
		return op.Error(ErrIDRequired, "ending session")
	}

	if err := s.backend.Delete(ctx, id); err != nil {
		s.backendErrorsCounter.Add(ctx, 1)

		return op.Error(err, "ending session")
	}

	s.endedCounter.Add(ctx, 1)

	return nil
}

// Close releases the backend.
func (s *BackendStore[T]) Close() error {
	return s.backend.Close()
}

// live loads the record behind id and reports it only if it is usable now:
// present, written by this shape of the package, and inside both deadlines.
//
// It is the single place any of those three is decided, which is what makes
// Get, Save, and Renew agree about what a live session is.
func (s *BackendStore[T]) live(
	ctx context.Context,
	op observability.Operation,
	id string,
) (*Record[T], time.Time, error) {
	now := s.now()

	if id == "" {
		return nil, now, op.Error(ErrIDRequired, "reading session")
	}

	record, err := s.backend.Load(ctx, id)
	if err != nil {
		// An absent session is an outcome, not a failure: on a public site most
		// requests carry no session at all. Logging one and marking the span
		// errored would make the ordinary case indistinguishable from the store
		// being down, and would do it once per anonymous request. A backend
		// failure is a different matter and goes through op.Error.
		if !stderrors.Is(err, ErrNotFound) {
			s.backendErrorsCounter.Add(ctx, 1)

			return nil, now, op.Error(err, "reading session")
		}

		return nil, now, platformerrors.Wrap(err, "reading session")
	}

	if record == nil {
		return nil, now, platformerrors.Wrap(ErrNotFound, "reading session")
	}

	if record.Version != recordVersion {
		// Read as absent rather than decoded. A record written by another shape
		// of this package would otherwise surface as a plausible but wrong
		// payload — a user holding fields that meant something else. Discarding
		// it costs a re-login and is counted, so a deploy that changes T shows
		// up as one spike rather than as a mystery.
		s.staleRecordCounter.Add(ctx, 1)
		s.discard(ctx, op, id)
		op.Set(recordVersionKey, record.Version)

		return nil, now, platformerrors.Wrap(ErrNotFound, "reading session")
	}

	op.Set(createdAtKey, record.CreatedAt).Set(lastSeenAtKey, record.LastSeenAt)

	if expiry := s.policy.Expiry(record.CreatedAt, record.LastSeenAt, now); expiry != ExpiryNone {
		s.expiredCounter.Add(ctx, 1, metric.WithAttributes(attribute.String(reasonKey, expiry.String())))
		s.discard(ctx, op, id)
		op.Set(expiryKey, expiry.String())

		return nil, now, platformerrors.Wrap(expiry.Err(), "reading session")
	}

	return record, now, nil
}

// discard removes a record the store has decided is unusable.
//
// Best-effort: the caller is being told the session is gone either way, and a
// backend that cannot delete will expire the record on its own TTL. Surfacing
// the failure would replace "your session ended" with "something broke", which
// is both less true and less useful.
func (s *BackendStore[T]) discard(ctx context.Context, op observability.Operation, id string) {
	if err := s.backend.Delete(ctx, id); err != nil {
		s.backendErrorsCounter.Add(ctx, 1)
		op.Acknowledge(err, "discarding unusable session")
	}
}

// now reads the clock at the resolution every stamped time uses. See
// timeResolution for why the truncation is not cosmetic.
func (s *BackendStore[T]) now() time.Time {
	return s.clock.Now().UTC().Truncate(timeResolution)
}

// observe records one operation's latency. It reads the clock rather than
// time.Now so that a synctest bubble measures bubble time.
func (s *BackendStore[T]) observe(ctx context.Context, operation string, startedAt time.Time) {
	s.latencyHist.Record(ctx,
		float64(s.clock.Since(startedAt).Milliseconds()),
		metric.WithAttributes(attribute.String(operationKey, operation)),
	)
}
