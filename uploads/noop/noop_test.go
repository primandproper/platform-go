package noop

import (
	"bytes"
	"io"
	"testing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNewUploadManager(T *testing.T) {
	T.Parallel()

	T.Run("returns non-nil manager", func(t *testing.T) {
		t.Parallel()

		m := NewUploadManager()
		must.NotNil(t, m)
	})
}

func TestUploadManager_Save(T *testing.T) {
	T.Parallel()

	T.Run("returns no error", func(t *testing.T) {
		t.Parallel()

		m := NewUploadManager()
		test.NoError(t, m.Save(t.Context(), "path/to/file", bytes.NewReader([]byte("content"))))
	})
}

func TestUploadManager_Open(T *testing.T) {
	T.Parallel()

	T.Run("returns empty reader and no error", func(t *testing.T) {
		t.Parallel()

		m := NewUploadManager()
		r, err := m.Open(t.Context(), "path/to/file")
		must.NoError(t, err)
		must.NotNil(t, r)

		data, err := io.ReadAll(r)
		must.NoError(t, err)
		test.SliceEmpty(t, data)
		must.NoError(t, r.Close())
	})
}

func TestUploadManager_Delete(T *testing.T) {
	T.Parallel()

	T.Run("returns no error", func(t *testing.T) {
		t.Parallel()

		m := NewUploadManager()
		test.NoError(t, m.Delete(t.Context(), "path/to/file"))
	})
}

func TestUploadManager_Exists(T *testing.T) {
	T.Parallel()

	T.Run("reports false and no error", func(t *testing.T) {
		t.Parallel()

		m := NewUploadManager()
		exists, err := m.Exists(t.Context(), "path/to/file")
		test.NoError(t, err)
		test.False(t, exists)
	})
}

func TestUploadManager_OpenRange(T *testing.T) {
	T.Parallel()

	T.Run("returns empty reader and no error", func(t *testing.T) {
		t.Parallel()

		r, err := (&UploadManager{}).OpenRange(t.Context(), "path/to/file", 0, -1)
		must.NoError(t, err)
		must.NotNil(t, r)

		data, err := io.ReadAll(r)
		must.NoError(t, err)
		test.SliceEmpty(t, data)
		must.NoError(t, r.Close())
	})
}

func TestUploadManager_Attributes(T *testing.T) {
	T.Parallel()

	T.Run("returns empty attributes and no error", func(t *testing.T) {
		t.Parallel()

		attrs, err := (&UploadManager{}).Attributes(t.Context(), "path/to/file")
		test.NoError(t, err)
		must.NotNil(t, attrs)
	})
}

func TestUploadManager_List(T *testing.T) {
	T.Parallel()

	T.Run("yields no objects and no error", func(t *testing.T) {
		t.Parallel()

		count := 0
		for _, err := range (&UploadManager{}).List(t.Context(), "path/") {
			must.NoError(t, err)
			count++
		}

		test.EqOp(t, 0, count)
	})
}

func TestUploadManager_SignedURL(T *testing.T) {
	T.Parallel()

	T.Run("returns empty URL and no error", func(t *testing.T) {
		t.Parallel()

		signedURL, err := (&UploadManager{}).SignedURL(t.Context(), "path/to/file", nil)
		test.NoError(t, err)
		test.EqOp(t, "", signedURL)
	})
}
