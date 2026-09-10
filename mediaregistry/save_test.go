package mediaregistry_test

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/mediaregistry"
	mediaregistrymock "github.com/primandproper/platform-go/v14/mediaregistry/mock"

	"github.com/primandproper/primitives-go/database"
	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/tenancy"
	"github.com/primandproper/primitives-go/uploads"
	uploadsmock "github.com/primandproper/primitives-go/uploads/mock"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// drainingManager is the UploadManager the happy paths run against: it reads
// the reader to the end, which is what a real provider does and what makes the
// byte count mean anything.
func drainingManager(t *testing.T, stored *[]byte, opts *uploads.SaveOptions) *uploadsmock.UploadManagerMock {
	t.Helper()

	return &uploadsmock.UploadManagerMock{
		SaveFunc: func(_ context.Context, _ string, r io.Reader, saveOpts ...uploads.SaveOption) error {
			read, err := io.ReadAll(r)
			if err != nil {
				return err
			}

			*stored = read
			*opts = uploads.BuildSaveOptions(saveOpts...)

			return nil
		},
	}
}

func TestStoreAndRecord(T *testing.T) {
	T.Parallel()

	scope := tenancy.Of("tenant_1")

	// The transaction is the caller's, and these cases are about what happens
	// between the two seams rather than about what commits — the store is a
	// mock and there is no database behind it. A real caller's Tx comes from
	// database.Client.WithTransaction.
	tx := database.NewTxForTesting(nil)

	newObj := func() *mediaregistry.Object {
		return &mediaregistry.Object{
			Key:         "avatars/grace/original.png",
			ContentType: "image/png",
			OwnerID:     "user_1",
		}
	}

	T.Run("stores the bytes and records what was stored", func(t *testing.T) {
		t.Parallel()

		var (
			stored []byte
			opts   uploads.SaveOptions
		)

		manager := drainingManager(t, &stored, &opts)

		var recorded *mediaregistry.Object

		store := &mediaregistrymock.StoreMock{
			RecordObjectFunc: func(_ context.Context, _ database.Tx, _ tenancy.Scope, object *mediaregistry.Object) error {
				recorded = object

				return nil
			},
		}

		object := newObj()
		content := "not really a png"

		must.NoError(t, mediaregistry.StoreAndRecord(t.Context(), tx, scope, manager, store, object, strings.NewReader(content)))

		test.EqOp(t, content, string(stored))
		must.NotNil(t, recorded)
		test.EqOp(t, object, recorded)

		// The size is what went past, not what anybody claimed.
		test.EqOp(t, int64(len(content)), object.Size)

		// The content type reaches the provider too, so the stored object and
		// its row agree about what it is.
		test.EqOp(t, "image/png", opts.ContentType)
	})

	T.Run("leaves the content type to the provider when the row does not state one", func(t *testing.T) {
		t.Parallel()

		var (
			stored []byte
			opts   uploads.SaveOptions
		)

		manager := drainingManager(t, &stored, &opts)
		store := &mediaregistrymock.StoreMock{
			RecordObjectFunc: func(context.Context, database.Tx, tenancy.Scope, *mediaregistry.Object) error { return nil },
		}

		object := newObj()
		object.ContentType = ""

		must.NoError(t, mediaregistry.StoreAndRecord(t.Context(), tx, scope, manager, store, object, strings.NewReader("x")))

		// Naming it explicitly as "" would replace a sniffed answer with none.
		test.EqOp(t, "", opts.ContentType)
	})

	T.Run("passes the caller's save options through", func(t *testing.T) {
		t.Parallel()

		var (
			stored []byte
			opts   uploads.SaveOptions
		)

		manager := drainingManager(t, &stored, &opts)
		store := &mediaregistrymock.StoreMock{
			RecordObjectFunc: func(context.Context, database.Tx, tenancy.Scope, *mediaregistry.Object) error { return nil },
		}

		must.NoError(t, mediaregistry.StoreAndRecord(t.Context(), tx, scope, manager, store, newObj(),
			strings.NewReader("x"), uploads.WithCacheControl("max-age=3600")))

		test.EqOp(t, "max-age=3600", opts.CacheControl)
	})

	T.Run("does not record when the bytes did not land", func(t *testing.T) {
		t.Parallel()

		saveErr := platformerrors.New("bucket unreachable")

		manager := &uploadsmock.UploadManagerMock{
			SaveFunc: func(context.Context, string, io.Reader, ...uploads.SaveOption) error { return saveErr },
		}

		// RecordObjectFunc is left nil on purpose: the generated mock panics if
		// it is called, which is the assertion. A row pointing at bytes that
		// are not there is the failure this order exists to prevent.
		store := &mediaregistrymock.StoreMock{}

		must.ErrorIs(t, mediaregistry.StoreAndRecord(t.Context(), tx, scope, manager, store, newObj(), strings.NewReader("x")), saveErr)
	})

	T.Run("reports what the registration reported", func(t *testing.T) {
		t.Parallel()

		var (
			stored []byte
			opts   uploads.SaveOptions
		)

		manager := drainingManager(t, &stored, &opts)
		store := &mediaregistrymock.StoreMock{
			RecordObjectFunc: func(context.Context, database.Tx, tenancy.Scope, *mediaregistry.Object) error {
				return mediaregistry.ErrObjectKeyTaken
			},
		}

		// The bytes are in the bucket and the row is not: an object with no
		// row, which is invisible to every read and exactly what an orphan
		// sweep is later written to find.
		must.ErrorIs(t, mediaregistry.StoreAndRecord(t.Context(), tx, scope, manager, store, newObj(), strings.NewReader("x")), mediaregistry.ErrObjectKeyTaken)
	})

	T.Run("refuses the pieces it cannot do without", func(t *testing.T) {
		t.Parallel()

		manager := &uploadsmock.UploadManagerMock{}
		store := &mediaregistrymock.StoreMock{}

		must.ErrorIs(t, mediaregistry.StoreAndRecord(t.Context(), nil, scope, manager, store, newObj(), strings.NewReader("x")), mediaregistry.ErrNilExecutor)
		must.ErrorIs(t, mediaregistry.StoreAndRecord(t.Context(), tx, scope, nil, store, newObj(), strings.NewReader("x")), mediaregistry.ErrNilUploadManager)
		must.ErrorIs(t, mediaregistry.StoreAndRecord(t.Context(), tx, scope, manager, nil, newObj(), strings.NewReader("x")), mediaregistry.ErrNilStore)
		must.ErrorIs(t, mediaregistry.StoreAndRecord(t.Context(), tx, scope, manager, store, nil, strings.NewReader("x")), mediaregistry.ErrNilObject)
		must.ErrorIs(t, mediaregistry.StoreAndRecord(t.Context(), tx, scope, manager, store, newObj(), nil), mediaregistry.ErrNilReader)
	})

	T.Run("refuses an unset scope before the bytes go", func(t *testing.T) {
		t.Parallel()

		// Both funcs are left nil on purpose: the generated mocks panic if they
		// are called. A write that could never have been filed must not spend an
		// upload first.
		manager := &uploadsmock.UploadManagerMock{}
		store := &mediaregistrymock.StoreMock{}

		var unset tenancy.Scope

		must.ErrorIs(t,
			mediaregistry.StoreAndRecord(t.Context(), tx, unset, manager, store, newObj(), strings.NewReader("x")),
			tenancy.ErrNoScope)
	})
}
