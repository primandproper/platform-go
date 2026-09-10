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

	newInput := func() mediaregistry.ObjectInput {
		return mediaregistry.ObjectInput{
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

		// The store answers with the row, the way the SQL one answers with what
		// it read back; here that is the value it was handed, which is what
		// makes the assertions below about what StoreAndRecord passed on.
		var offered mediaregistry.ObjectInput

		store := &mediaregistrymock.StoreMock{
			RecordObjectFunc: func(_ context.Context, _ database.Tx, _ tenancy.Scope, in mediaregistry.ObjectInput) (*mediaregistry.Object, error) {
				offered = in

				return &mediaregistry.Object{Key: in.Key, ContentType: in.ContentType, OwnerID: in.OwnerID, Size: in.Size}, nil
			},
		}

		input := newInput()
		content := "not really a png"

		recorded, err := mediaregistry.StoreAndRecord(t.Context(), tx, scope, manager, store, input, strings.NewReader(content))
		must.NoError(t, err)

		test.EqOp(t, content, string(stored))
		must.NotNil(t, recorded)

		// The size is what went past, not what anybody claimed. It reaches the
		// store on the input and comes back on the row; the caller's own input
		// is a separate value and still says nothing about size.
		test.EqOp(t, int64(len(content)), offered.Size)
		test.EqOp(t, int64(len(content)), recorded.Size)
		test.EqOp(t, int64(0), input.Size)

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
			RecordObjectFunc: func(_ context.Context, _ database.Tx, _ tenancy.Scope, in mediaregistry.ObjectInput) (*mediaregistry.Object, error) {
				return &mediaregistry.Object{Key: in.Key}, nil
			},
		}

		input := newInput()
		input.ContentType = ""

		_, err := mediaregistry.StoreAndRecord(t.Context(), tx, scope, manager, store, input, strings.NewReader("x"))
		must.NoError(t, err)

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
			RecordObjectFunc: func(_ context.Context, _ database.Tx, _ tenancy.Scope, in mediaregistry.ObjectInput) (*mediaregistry.Object, error) {
				return &mediaregistry.Object{Key: in.Key}, nil
			},
		}

		_, err := mediaregistry.StoreAndRecord(t.Context(), tx, scope, manager, store, newInput(),
			strings.NewReader("x"), uploads.WithCacheControl("max-age=3600"))
		must.NoError(t, err)

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

		recorded, err := mediaregistry.StoreAndRecord(t.Context(), tx, scope, manager, store, newInput(), strings.NewReader("x"))
		must.ErrorIs(t, err, saveErr)
		test.Nil(t, recorded)
	})

	T.Run("reports what the registration reported", func(t *testing.T) {
		t.Parallel()

		var (
			stored []byte
			opts   uploads.SaveOptions
		)

		manager := drainingManager(t, &stored, &opts)
		store := &mediaregistrymock.StoreMock{
			RecordObjectFunc: func(context.Context, database.Tx, tenancy.Scope, mediaregistry.ObjectInput) (*mediaregistry.Object, error) {
				return nil, mediaregistry.ErrObjectKeyTaken
			},
		}

		// The bytes are in the bucket and the row is not: an object with no
		// row, which is invisible to every read and exactly what an orphan
		// sweep is later written to find.
		recorded, err := mediaregistry.StoreAndRecord(t.Context(), tx, scope, manager, store, newInput(), strings.NewReader("x"))
		must.ErrorIs(t, err, mediaregistry.ErrObjectKeyTaken)
		test.Nil(t, recorded)
	})

	T.Run("refuses the pieces it cannot do without", func(t *testing.T) {
		t.Parallel()

		manager := &uploadsmock.UploadManagerMock{}
		store := &mediaregistrymock.StoreMock{}

		for _, missing := range []struct {
			call func() (*mediaregistry.Object, error)
			want error
		}{
			{
				call: func() (*mediaregistry.Object, error) {
					return mediaregistry.StoreAndRecord(t.Context(), nil, scope, manager, store, newInput(), strings.NewReader("x"))
				},
				want: mediaregistry.ErrNilExecutor,
			},
			{
				call: func() (*mediaregistry.Object, error) {
					return mediaregistry.StoreAndRecord(t.Context(), tx, scope, nil, store, newInput(), strings.NewReader("x"))
				},
				want: mediaregistry.ErrNilUploadManager,
			},
			{
				call: func() (*mediaregistry.Object, error) {
					return mediaregistry.StoreAndRecord(t.Context(), tx, scope, manager, nil, newInput(), strings.NewReader("x"))
				},
				want: mediaregistry.ErrNilStore,
			},
			{
				call: func() (*mediaregistry.Object, error) {
					return mediaregistry.StoreAndRecord(t.Context(), tx, scope, manager, store, newInput(), nil)
				},
				want: mediaregistry.ErrNilReader,
			},
		} {
			recorded, err := missing.call()
			must.ErrorIs(t, err, missing.want)
			test.Nil(t, recorded)
		}
	})

	T.Run("refuses an unset scope before the bytes go", func(t *testing.T) {
		t.Parallel()

		// Both funcs are left nil on purpose: the generated mocks panic if they
		// are called. A write that could never have been filed must not spend an
		// upload first.
		manager := &uploadsmock.UploadManagerMock{}
		store := &mediaregistrymock.StoreMock{}

		var unset tenancy.Scope

		recorded, err := mediaregistry.StoreAndRecord(t.Context(), tx, unset, manager, store, newInput(), strings.NewReader("x"))
		must.ErrorIs(t, err, tenancy.ErrNoScope)
		test.Nil(t, recorded)
	})
}
