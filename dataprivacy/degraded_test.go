package dataprivacy

import (
	"context"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/compression"
	"github.com/primandproper/platform-go/v13/database"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/filtering"
	"github.com/primandproper/platform-go/v13/identifiers"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// errPackaging is what the failing codecs below return.
var errPackaging = platformerrors.New("codec is unavailable")

// brokenCompressor fails in both directions.
type brokenCompressor struct{}

var _ compression.Compressor = (*brokenCompressor)(nil)

func (*brokenCompressor) CompressBytes([]byte) ([]byte, error)   { return nil, errPackaging }
func (*brokenCompressor) DecompressBytes([]byte) ([]byte, error) { return nil, errPackaging }

// brokenCrypto fails in both directions.
type brokenCrypto struct{}

func (*brokenCrypto) Encrypt(context.Context, []byte, []byte) ([]byte, error) {
	return nil, errPackaging
}

func (*brokenCrypto) Decrypt(context.Context, []byte, []byte) ([]byte, error) {
	return nil, errPackaging
}

func TestPackager_CodecFailures(T *testing.T) {
	T.Parallel()

	T.Run("a failing compressor fails the encode", func(t *testing.T) {
		t.Parallel()

		p := &packager{compressor: &brokenCompressor{}}

		_, err := p.encode(t.Context(), testDocument(), testRequestID)
		must.ErrorIs(t, err, errPackaging)
		test.StrContains(t, err.Error(), "compressing")
	})

	T.Run("a failing encryptor fails the encode", func(t *testing.T) {
		t.Parallel()

		p := &packager{encryptor: &brokenCrypto{}}

		_, err := p.encode(t.Context(), testDocument(), testRequestID)
		must.ErrorIs(t, err, errPackaging)
		test.StrContains(t, err.Error(), "encrypting")
	})

	T.Run("a failing decryptor fails the decode", func(t *testing.T) {
		t.Parallel()

		p := &packager{decryptor: &brokenCrypto{}}

		_, err := p.decode(t.Context(), []byte("ciphertext"), testRequestID)
		must.ErrorIs(t, err, errPackaging)
		test.StrContains(t, err.Error(), "decrypting")
	})

	T.Run("a failing decompressor fails the decode", func(t *testing.T) {
		t.Parallel()

		p := &packager{compressor: &brokenCompressor{}}

		_, err := p.decode(t.Context(), []byte("compressed"), testRequestID)
		must.ErrorIs(t, err, errPackaging)
		test.StrContains(t, err.Error(), "decompressing")
	})

	T.Run("an encrypted artifact is described as opaque bytes", func(t *testing.T) {
		t.Parallel()

		// Describing it by the compression underneath would invite a client to
		// try to decompress base64 ciphertext.
		p := &packager{compressor: &brokenCompressor{}, encryptor: &brokenCrypto{}}
		test.EqOp(t, "application/octet-stream", p.contentType())
	})

	T.Run("an export whose packaging fails is not stored", func(t *testing.T) {
		t.Parallel()

		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterCollector("identity", staticCollector(`{"ok":true}`)))
		}, WithFulfillerCompressor(&brokenCompressor{}))

		req := env.submitAndRun(t, RequestExport)

		// A half-packaged artifact must not reach the bucket. The failure is
		// retryable, so the row only records it on the final attempt — which is
		// the one submitAndRun runs.
		test.EqOp(t, StatusFailed, req.Status)
		test.StrContains(t, req.LastError, "compressing")
		test.SliceEmpty(t, env.uploader.paths())
	})
}

// failingUploadStore fails only CompleteExport, so the worker's
// artifact-written-but-row-not-updated path is reachable.
type failingCompletionStore struct {
	Store
}

func (s *failingCompletionStore) CompleteExport(context.Context, database.SQLQueryExecutor, *Request, time.Time) error {
	return platformerrors.New("the write replica is unreachable")
}

// failingFailStore fails Fail itself, which is the worst case: the worker
// cannot even record that it could not record.
type failingFailStore struct {
	Store
}

func (s *failingFailStore) Fail(context.Context, string, string, time.Time) (bool, error) {
	return false, platformerrors.New("the write replica is unreachable")
}

// failingMarkExpiredStore deletes fine but cannot update the row.
type failingMarkExpiredStore struct {
	Store
}

func (s *failingMarkExpiredStore) MarkExpired(context.Context, string, time.Time) error {
	return platformerrors.New("the write replica is unreachable")
}

func TestFulfiller_StoreFailurePaths(T *testing.T) {
	T.Parallel()

	T.Run("a failed completion leaves the artifact for a retry to overwrite", func(t *testing.T) {
		t.Parallel()

		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterCollector("identity", staticCollector(`{"ok":true}`)))
		})

		req := saveRequest(t, env.store, newRequest(identifiers.New(), RequestExport, testSubject, env.clock.read()))

		env.fulfiller.store = &failingCompletionStore{Store: env.store}

		_, err := env.run(t, req.ID, RequestExport, newFinalReporter())
		must.Error(t, err)

		// The object is written even though the row was never updated. The
		// reference is derived from the request ID rather than random, so the
		// retry overwrites it instead of orphaning one per attempt.
		paths := env.uploader.paths()
		must.SliceLen(t, 1, paths)
		test.StrContains(t, paths[0], req.ID)
	})

	T.Run("a failure that cannot itself be recorded is survivable", func(t *testing.T) {
		t.Parallel()

		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterCollector("identity", failingCollector(platformerrors.New("down"))))
		})

		req := saveRequest(t, env.store, newRequest(identifiers.New(), RequestExport, testSubject, env.clock.read()))

		// The operation records the failure on its own row regardless, so what
		// is lost is only this package's copy of it. What must not happen is a
		// panic, or the runner swallowing the cause it was about to return.
		env.fulfiller.store = &failingFailStore{Store: env.store}

		_, err := env.run(t, req.ID, RequestExport, newFinalReporter())
		must.Error(t, err)
		test.ErrorIs(t, err, ErrEverySectionFailed)
	})
}

func TestSweeper_StoreFailurePaths(T *testing.T) {
	T.Parallel()

	T.Run("a deleted object whose row will not update is retried", func(t *testing.T) {
		t.Parallel()

		env := newSweeperEnv(t, &SweeperConfig{})

		req := env.completedExport(t, baseTime.Add(-time.Minute))

		env.sweeper.store = &failingMarkExpiredStore{Store: env.sweeper.store}

		result, err := env.sweeper.Sweep(t.Context())
		must.NoError(t, err)

		// Nothing counted as expired, because the row still says otherwise.
		test.EqOp(t, int64(0), result.ArtifactsExpired)

		// The object is gone. The next sweep selects the row again, fails to
		// delete something already deleted, takes the already-gone path, and
		// converges — which is why deleteArtifact treats absence as success.
		_, stillThere := env.uploader.get(req.ArtifactRef)
		test.False(t, stillThere)
	})
}

func TestSQLStore_CursorPagination(T *testing.T) {
	T.Parallel()

	T.Run("walks a subject's history a page at a time", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)
		store := env.newStore(t)

		var ids []string
		for range 5 {
			req := saveRequest(t, store, newRequest(identifiers.New(), RequestExport, testSubject, baseTime))
			ids = append(ids, req.ID)
		}

		filter := filtering.DefaultQueryFilter()
		filter.MaxResponseSize = new(uint16(2))

		var seen []string

		for range 3 {
			page, err := store.List(t.Context(), testSubject, filter)
			must.NoError(t, err)

			for _, req := range page.Data {
				seen = append(seen, req.ID)
			}

			if page.Cursor == "" {
				break
			}

			filter.Cursor = &page.Cursor
		}

		// Every request appears exactly once. A cursor comparison that faced
		// the wrong way would either repeat the first page forever or skip
		// straight past the rest.
		test.Eq(t, ids, seen)
	})

	T.Run("a descending walk returns the reverse", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)
		store := env.newStore(t)

		var ids []string
		for range 4 {
			req := saveRequest(t, store, newRequest(identifiers.New(), RequestExport, testSubject, baseTime))
			ids = append(ids, req.ID)
		}

		filter := filtering.DefaultQueryFilter()
		filter.SortBy = filtering.SortDescending
		filter.MaxResponseSize = new(uint16(2))

		first, err := store.List(t.Context(), testSubject, filter)
		must.NoError(t, err)
		must.SliceLen(t, 2, first.Data)

		test.EqOp(t, ids[3], first.Data[0].ID)
		test.EqOp(t, ids[2], first.Data[1].ID)

		filter.Cursor = &first.Cursor

		second, err := store.List(t.Context(), testSubject, filter)
		must.NoError(t, err)
		must.SliceLen(t, 2, second.Data)

		test.EqOp(t, ids[1], second.Data[0].ID)
		test.EqOp(t, ids[0], second.Data[1].ID)
	})
}

func TestStatus_TerminalRejectsUnknown(T *testing.T) {
	T.Parallel()

	T.Run("an unrecognized status is not terminal", func(t *testing.T) {
		t.Parallel()

		// Not terminal, so an unknown status read back from a database written
		// by a newer build is left alone rather than being treated as finished.
		test.False(t, Status("rectifying").Terminal())
		test.False(t, Status("").Terminal())
	})
}

func TestRegistry_EraserValidation(T *testing.T) {
	T.Parallel()

	T.Run("rejects keys that are not plain identifiers", func(t *testing.T) {
		t.Parallel()

		registry := NewRegistry()

		for _, key := range []string{"Identity", "meal planning", "billing-v2"} {
			err := registry.RegisterEraser(key, countingEraser(0, 0, nil, nil))
			test.ErrorIs(t, err, ErrInvalidKey, test.Sprintf("key %q", key))
		}
	})

	T.Run("rejects a duplicate eraser", func(t *testing.T) {
		t.Parallel()

		registry := NewRegistry()

		must.NoError(t, registry.RegisterEraser("identity", countingEraser(0, 0, nil, nil)))

		err := registry.RegisterEraser("identity", countingEraser(0, 0, nil, nil))
		test.ErrorIs(t, err, ErrDuplicateKey)
	})
}

func TestNewFulfiller_NilArguments(T *testing.T) {
	T.Parallel()

	T.Run("refuses a nil config, store, and registry", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)
		store := env.newStore(t)

		_, err := NewFulfiller(t.Context(), nil, store, NewRegistry())
		test.Error(t, err)

		_, err = NewFulfiller(t.Context(), &FulfillerConfig{}, nil, NewRegistry())
		test.ErrorIs(t, err, ErrNilStore)

		_, err = NewFulfiller(t.Context(), &FulfillerConfig{}, store, nil)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})

	T.Run("refuses to register into a nil operations registry", func(t *testing.T) {
		t.Parallel()

		env := newFulfillerEnv(t, func(r *Registry) {
			must.NoError(t, r.RegisterCollector("identity", staticCollector(`{}`)))
		})

		test.ErrorIs(t, env.fulfiller.Register(nil), platformerrors.ErrNilInputParameter)
	})
}
