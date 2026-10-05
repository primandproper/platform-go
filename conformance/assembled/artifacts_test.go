package assembled_test

import (
	"context"
	"slices"
	"time"

	"github.com/primandproper/platform-go/v15/dataprivacy"

	"github.com/primandproper/primitives-go/v2/clock"
	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"
	"github.com/primandproper/primitives-go/v2/uploads"
)

// sweepReach is how many due artifacts the harness's sweep reads before it
// narrows to the one it was asked about. Every export in this server's run is
// due at the moment it sweeps at, and the one asked about is among the newest,
// so a reach the size of the package's batch could stop short of it.
const sweepReach = 1 << 16

// errArtifactNotExpired is what the ArtifactExpired action reports when its
// sweep ran and expired nothing.
var errArtifactNotExpired = platformerrors.New("conformance harness: the sweep did not expire that request's artifact")

// expireArtifact is the ArtifactExpired action: dataprivacy's own Sweeper,
// over the store and upload manager the composition root built, run at a
// clock one second past the request's expiry.
//
// Confined to the one request, because a sweep at that clock would otherwise
// expire every export this run has made — including the ones a parallel
// assertion is still reading an artifact reference off. The narrowing is in
// the store the sweeper is handed rather than in the sweep: the selection,
// the delete and the mark are dataprivacy's own, and only which rows the
// selection may answer with is this harness's. Nothing it sweeps is reaped or
// lapsed, for the same reason.
func expireArtifact(
	db database.Client,
	store dataprivacy.Store,
	manager uploads.UploadManager,
) func(context.Context, tenancy.Scope, string) error {
	return func(ctx context.Context, _ tenancy.Scope, requestID string) error {
		// Under every confinement rather than the caller's tenant: this
		// deployment's requests are the person's and name no scope, so a read
		// narrowed to one would find nothing. The identifier is exact either way.
		req, err := store.Get(ctx, db.Reader(), nil, requestID)
		if err != nil {
			return platformerrors.Wrap(err, "reading the request to expire")
		}

		if req.ArtifactRef == "" || req.ExpiresAt.IsZero() {
			return platformerrors.Wrapf(errArtifactNotExpired, "request %s names no artifact to expire", requestID)
		}

		sweeper, err := dataprivacy.NewSweeper(ctx,
			&dataprivacy.SweeperConfig{BatchSize: sweepReach, DisableReap: true},
			&oneRequest{Store: store, requestID: requestID},
			dataprivacy.WithSweeperUploadManager(manager),
			dataprivacy.WithSweeperClock(stoppedClock{at: req.ExpiresAt.Add(time.Second)}),
		)
		if err != nil {
			return platformerrors.Wrap(err, "building the sweeper")
		}

		result, err := sweeper.Sweep(ctx)
		if err != nil {
			return platformerrors.Wrap(err, "sweeping")
		}

		if result.ArtifactsExpired != 1 {
			return platformerrors.Wrapf(errArtifactNotExpired, "request %s", requestID)
		}

		return nil
	}
}

// oneRequest is a dataprivacy.Store whose sweep sees one request and nothing
// else. Every method the sweep does not select with is the store's own.
type oneRequest struct {
	dataprivacy.Store

	requestID string
}

func (o *oneRequest) ExpiringArtifacts(ctx context.Context, now time.Time, limit int) ([]*dataprivacy.Request, error) {
	due, err := o.Store.ExpiringArtifacts(ctx, now, limit)
	if err != nil {
		return nil, err
	}

	return slices.DeleteFunc(due, func(req *dataprivacy.Request) bool { return req.ID != o.requestID }), nil
}

// LapseUnconfirmed lapses nothing: at a clock days ahead, every erasure a
// parallel assertion is waiting to confirm would go with it.
func (o *oneRequest) LapseUnconfirmed(context.Context, time.Time, int) (int64, error) {
	return 0, nil
}

// stoppedClock reads one moment, whenever it is asked.
type stoppedClock struct {
	clock.WallClock

	at time.Time
}

func (c stoppedClock) Now() time.Time { return c.at }

func (c stoppedClock) Since(t time.Time) time.Duration { return c.at.Sub(t) }
