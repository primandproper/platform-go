package uploads_test

import (
	"bytes"
	"context"
	"io"
	"iter"
	"testing"

	"github.com/primandproper/platform-go/v4/errors"
	"github.com/primandproper/platform-go/v4/uploads"
	mockuploads "github.com/primandproper/platform-go/v4/uploads/mock"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestSaveFile(T *testing.T) {
	T.Parallel()

	T.Run("saves the byte content via Save", func(t *testing.T) {
		t.Parallel()

		var saved []byte
		m := &mockuploads.UploadManagerMock{
			SaveFunc: func(_ context.Context, _ string, r io.Reader, _ ...uploads.SaveOption) error {
				var err error
				saved, err = io.ReadAll(r)
				return err
			},
		}

		content := []byte(t.Name())
		test.NoError(t, uploads.SaveFile(t.Context(), m, "a/b.txt", content))
		test.Eq(t, content, saved)
		test.SliceLen(t, 1, m.SaveCalls())
	})
}

func TestReadFile(T *testing.T) {
	T.Parallel()

	T.Run("reads the whole object via Open", func(t *testing.T) {
		t.Parallel()

		content := []byte(t.Name())
		m := &mockuploads.UploadManagerMock{
			OpenFunc: func(context.Context, string) (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewReader(content)), nil
			},
		}

		got, err := uploads.ReadFile(t.Context(), m, "a/b.txt")
		test.NoError(t, err)
		test.Eq(t, content, got)
		test.SliceLen(t, 1, m.OpenCalls())
	})

	T.Run("propagates Open errors", func(t *testing.T) {
		t.Parallel()

		m := &mockuploads.UploadManagerMock{
			OpenFunc: func(context.Context, string) (io.ReadCloser, error) {
				return nil, errors.New("nope")
			},
		}

		got, err := uploads.ReadFile(t.Context(), m, "a/b.txt")
		test.Error(t, err)
		must.Nil(t, got)
	})
}

// errLister is a Lister whose stream yields a single error.
type errLister struct{}

func (errLister) List(context.Context, string) iter.Seq2[uploads.ObjectInfo, error] {
	return func(yield func(uploads.ObjectInfo, error) bool) {
		yield(uploads.ObjectInfo{}, errors.New("boom"))
	}
}

func TestListAll(T *testing.T) {
	T.Parallel()

	T.Run("collects every object", func(t *testing.T) {
		t.Parallel()

		m := &mockuploads.ListerMock{
			ListFunc: func(context.Context, string) iter.Seq2[uploads.ObjectInfo, error] {
				return func(yield func(uploads.ObjectInfo, error) bool) {
					_ = yield(uploads.ObjectInfo{Path: "a"}, nil) && yield(uploads.ObjectInfo{Path: "b"}, nil)
				}
			},
		}

		objs, err := uploads.ListAll(t.Context(), m, "")
		test.NoError(t, err)
		test.SliceLen(t, 2, objs)
	})

	T.Run("propagates stream errors", func(t *testing.T) {
		t.Parallel()

		objs, err := uploads.ListAll(t.Context(), errLister{}, "")
		test.Error(t, err)
		must.Nil(t, objs)
	})
}
