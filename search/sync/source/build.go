package syncsource

import (
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	searchsync "github.com/primandproper/platform-go/v13/search/sync"
	textsearch "github.com/primandproper/platform-go/v13/search/text"
)

// NewSyncer builds the searchsync.Syncer that applies one index event for this
// Source's entity, writing into index.
//
// It owns no goroutine and reads from no queue: its Handle is a jobs.Handler,
// and the jobs.Pool calling it supplies concurrency, retry with backoff,
// dead-lettering and a draining shutdown.
func NewSyncer[E, T any](source *Source[E, T], index textsearch.IndexManager, opts ...Option) (*searchsync.Syncer[T], error) {
	if source == nil {
		return nil, searchsync.ErrNilSource
	}

	o := newOptions(opts)

	target, err := searchsync.TextTarget[T](index)
	if err != nil {
		return nil, platformerrors.Wrapf(err, "building %s search target", source.name)
	}

	syncer, err := searchsync.NewSyncer(source.name, source, target,
		append([]searchsync.SyncerOption{
			searchsync.WithSyncerLogger(o.logger),
			searchsync.WithSyncerTracerProvider(o.tracerProvider),
			searchsync.WithSyncerMetricsProvider(o.metricsProvider),
		}, o.syncerOptions...)...)
	if err != nil {
		return nil, platformerrors.Wrapf(err, "building %s syncer", source.name)
	}

	return syncer, nil
}

// NewReindexer builds the rebuild backstop for this Source's index.
//
// A Syncer keeps an index current; a Reindexer rebuilds one. They answer
// different failures — a Syncer cannot repair an index that was already wrong
// before the first event was written, and a full walk is far too expensive to be
// the steady-state path — which is why both are built from the one Source rather
// than either being derived from the other.
//
// It owns no ticker. Register the result with a jobs.Scheduler, whose
// distributed lock is what makes the rebuild run once across a fleet rather than
// once per replica:
//
//	if err = scheduler.Register(reindexer.Job(jobs.MustCron("0 4 * * *"), time.Hour)); err != nil {
//	    return err
//	}
func NewReindexer[E, T any](source *Source[E, T], index textsearch.IndexManager, opts ...Option) (*searchsync.Reindexer[T], error) {
	if source == nil {
		return nil, searchsync.ErrNilSource
	}

	o := newOptions(opts)

	target, err := searchsync.TextTarget[T](index)
	if err != nil {
		return nil, platformerrors.Wrapf(err, "building %s search target", source.name)
	}

	reindexer, err := searchsync.NewReindexer(source.name, source, target,
		append([]searchsync.ReindexOption{
			searchsync.WithReindexLogger(o.logger),
			searchsync.WithReindexTracerProvider(o.tracerProvider),
			searchsync.WithReindexMetricsProvider(o.metricsProvider),
		}, o.reindexOptions...)...)
	if err != nil {
		return nil, platformerrors.Wrapf(err, "building %s reindexer", source.name)
	}

	return reindexer, nil
}
