package images

import (
	"bytes"
	"errors"
	"fmt"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/primandproper/platform-go/observability"
	"github.com/primandproper/platform-go/observability/keys"
	tracingnoop "github.com/primandproper/platform-go/observability/tracing/noop"
	"github.com/primandproper/platform-go/testutils"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// newRecordingProcessor builds a uploadProcessor with a RecordingObserver swapped
// in, so a test can both drive the processor and assert which fields it observed.
func newRecordingProcessor(t *testing.T) (*uploadProcessor, *observability.RecordingObserver) {
	t.Helper()

	obs := observability.NewRecordingObserver()
	p := &uploadProcessor{o11y: obs}

	return p, obs
}

// errorWriter is an http.ResponseWriter whose Write always returns an error.
type errorWriter struct {
	header http.Header
}

func (e *errorWriter) Header() http.Header {
	if e.header == nil {
		e.header = http.Header{}
	}
	return e.header
}
func (e *errorWriter) Write([]byte) (int, error) { return 0, errors.New("blah") }
func (e *errorWriter) WriteHeader(int)           {}

func newAvatarUploadRequest(t *testing.T, filename string, avatar io.Reader) *http.Request {
	t.Helper()

	ctx := t.Context()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	part, err := writer.CreateFormFile("avatar", fmt.Sprintf("avatar.%s", filepath.Ext(filename)))
	must.NoError(t, err)

	_, err = io.Copy(part, avatar)
	must.NoError(t, err)

	must.NoError(t, writer.Close())

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://whatever.whocares.gov", body)
	must.NoError(t, err)

	req.Header.Set(headerContentType, writer.FormDataContentType())

	return req
}

func buildPNGBytes(t *testing.T) *bytes.Buffer {
	t.Helper()

	b := new(bytes.Buffer)
	exampleImage := testutils.BuildArbitraryImage(256)
	must.NoError(t, png.Encode(b, exampleImage))

	expected := b.Bytes()
	return bytes.NewBuffer(expected)
}

func buildJPEGBytes(t *testing.T) *bytes.Buffer {
	t.Helper()

	b := new(bytes.Buffer)
	exampleImage := testutils.BuildArbitraryImage(256)
	must.NoError(t, jpeg.Encode(b, exampleImage, &jpeg.Options{Quality: jpeg.DefaultQuality}))

	expected := b.Bytes()
	return bytes.NewBuffer(expected)
}

func buildGIFBytes(t *testing.T) *bytes.Buffer {
	t.Helper()

	b := new(bytes.Buffer)
	exampleImage := testutils.BuildArbitraryImage(256)
	must.NoError(t, gif.Encode(b, exampleImage, &gif.Options{NumColors: 256}))

	expected := b.Bytes()
	return bytes.NewBuffer(expected)
}

func newMultiFileUploadRequest(t *testing.T, files map[string][]byte) *http.Request {
	t.Helper()

	ctx := t.Context()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	for filename, data := range files {
		part, err := writer.CreateFormFile(filename, filename)
		must.NoError(t, err)
		_, err = io.Copy(part, bytes.NewReader(data))
		must.NoError(t, err)
	}

	must.NoError(t, writer.Close())

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://whatever.whocares.gov", body)
	must.NoError(t, err)

	req.Header.Set(headerContentType, writer.FormDataContentType())

	return req
}

func Test_contentTypeFromFilename(T *testing.T) {
	T.Parallel()

	T.Run("png", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, imagePNG, contentTypeFromFilename("photo.png"))
	})

	T.Run("jpeg", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, imageJPEG, contentTypeFromFilename("photo.jpeg"))
	})

	T.Run("gif", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, imageGIF, contentTypeFromFilename("photo.gif"))
	})

	T.Run("falls back to mime.TypeByExtension", func(t *testing.T) {
		t.Parallel()

		actual := contentTypeFromFilename("document.html")
		test.StrContains(t, actual, "text/html")
	})

	T.Run("unknown extension", func(t *testing.T) {
		t.Parallel()

		actual := contentTypeFromFilename("file.xyznotreal")
		test.EqOp(t, "", actual)
	})
}

func Test_isImage(T *testing.T) {
	T.Parallel()

	T.Run("png", func(t *testing.T) {
		t.Parallel()

		test.True(t, isImage("photo.png"))
	})

	T.Run("jpeg", func(t *testing.T) {
		t.Parallel()

		test.True(t, isImage("photo.jpeg"))
	})

	T.Run("gif", func(t *testing.T) {
		t.Parallel()

		test.True(t, isImage("photo.gif"))
	})

	T.Run("non-image", func(t *testing.T) {
		t.Parallel()

		test.False(t, isImage("document.html"))
	})

	T.Run("unknown extension", func(t *testing.T) {
		t.Parallel()

		test.False(t, isImage("file.xyznotreal"))
	})
}

func TestNewMediaUploadProcessor(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		p := NewMediaUploadProcessor(nil, tracingnoop.NewTracerProvider())
		test.NotNil(t, p)
	})
}

func TestImage_DataURI(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := &Upload{
			Filename:    t.Name(),
			ContentType: "things/stuff",
			Data:        []byte(t.Name()),
			Size:        12345,
		}

		expected := "data:things/stuff;base64,VGVzdEltYWdlX0RhdGFVUkkvc3RhbmRhcmQ="
		actual := i.DataURI()

		test.EqOp(t, expected, actual)
	})
}

func TestImage_Write(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		data := []byte(t.Name())
		i := &Upload{
			Filename:    t.Name(),
			ContentType: "things/stuff",
			Data:        data,
			Size:        len(data),
		}

		res := httptest.NewRecorder()
		test.NoError(t, i.Write(res))

		test.EqOp(t, "things/stuff", res.Header().Get(headerContentType))
		test.EqOp(t, strconv.Itoa(len(data)), res.Header().Get("RawHTML-Length"))
		test.Eq(t, data, res.Body.Bytes())
	})

	T.Run("with write error", func(t *testing.T) {
		t.Parallel()

		i := &Upload{
			Filename:    t.Name(),
			ContentType: "things/stuff",
			Data:        []byte(t.Name()),
			Size:        12345,
		}

		res := &errorWriter{}

		test.Error(t, i.Write(res))
	})
}

func TestImage_Thumbnail(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		imgBytes := buildPNGBytes(t).Bytes()

		i := &Upload{
			Filename:    t.Name(),
			ContentType: imagePNG,
			Data:        imgBytes,
			Size:        len(imgBytes),
		}

		tempFile, err := os.CreateTemp("", "")
		must.NoError(t, err)

		actual, err := i.Thumbnail(123, 123, tempFile.Name())
		test.NoError(t, err)
		test.NotNil(t, actual)

		must.NoError(t, os.Remove(tempFile.Name()))
	})

	T.Run("with invalid content type", func(t *testing.T) {
		t.Parallel()

		i := &Upload{
			ContentType: t.Name(),
		}

		actual, err := i.Thumbnail(123, 123, t.Name())
		test.Error(t, err)
		test.Nil(t, actual)
	})
}

func TestLimitFileSize(T *testing.T) {
	T.Parallel()

	T.Run("with zero max uses default", func(t *testing.T) {
		t.Parallel()

		imgBytes := buildPNGBytes(t)
		req := newAvatarUploadRequest(t, "avatar.png", imgBytes)
		res := httptest.NewRecorder()

		LimitFileSize(0, res, req)
	})

	T.Run("with explicit max size", func(t *testing.T) {
		t.Parallel()

		imgBytes := buildPNGBytes(t)
		req := newAvatarUploadRequest(t, "avatar.png", imgBytes)
		res := httptest.NewRecorder()

		LimitFileSize(2048, res, req)
	})
}

func Test_uploadProcessor_Process(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		p, obs := newRecordingProcessor(t)
		expectedFieldName := "avatar"

		imgBytes := buildPNGBytes(t)
		expected := imgBytes.Bytes()

		req := newAvatarUploadRequest(t, "avatar.png", imgBytes)

		actual, err := p.ProcessFile(ctx, req, expectedFieldName)
		test.NotNil(t, actual)
		test.NoError(t, err)

		test.Eq(t, expected, actual.Data)

		obs.ObservedOperationWithData(t, map[string]any{
			keys.FilenameKey: expectedFieldName,
		})
	})

	T.Run("with missing form file", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		p, obs := newRecordingProcessor(t)
		expectedFieldName := "avatar"

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://tests.verygoodsoftwarenotvirus.ru", http.NoBody)
		must.NoError(t, err)

		actual, err := p.ProcessFile(ctx, req, expectedFieldName)
		test.Nil(t, actual)
		test.Error(t, err)

		// The FormFile failure is reported via PrepareAndLogError (it carries
		// request data the op's Error helper does not), so it is not recorded on
		// the operation; we still assert the filename was observed before failure.
		obs.ObservedOperationWithData(t, map[string]any{
			keys.FilenameKey: expectedFieldName,
		})
	})

	T.Run("with error decoding image", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		p, obs := newRecordingProcessor(t)
		expectedFieldName := "avatar"

		req := newAvatarUploadRequest(t, "avatar.png", bytes.NewBufferString(""))

		actual, err := p.ProcessFile(ctx, req, expectedFieldName)
		test.Nil(t, actual)
		test.Error(t, err)

		// The decode failure flows through the inner operation's Error helper, so
		// some recorded operation observed the filename and recorded the error.
		obs.ObservedOperationWithData(t, map[string]any{
			keys.FilenameKey: expectedFieldName,
		})

		var sawError bool
		for _, op := range obs.Operations {
			if len(op.Errors) > 0 {
				sawError = true
			}
		}
		test.True(t, sawError)
	})

	T.Run("with non-image file", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		p := NewMediaUploadProcessor(nil, tracingnoop.NewTracerProvider())
		expectedFieldName := "document"

		body := &bytes.Buffer{}
		writer := multipart.NewWriter(body)
		part, err := writer.CreateFormFile(expectedFieldName, "notes.txt")
		must.NoError(t, err)
		_, err = part.Write([]byte("hello world"))
		must.NoError(t, err)
		must.NoError(t, writer.Close())

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://whatever.whocares.gov", body)
		must.NoError(t, err)
		req.Header.Set(headerContentType, writer.FormDataContentType())

		actual, err := p.ProcessFile(ctx, req, expectedFieldName)
		test.NotNil(t, actual)
		test.NoError(t, err)
	})
}

func Test_uploadProcessor_ProcessFiles(T *testing.T) {
	T.Parallel()

	T.Run("standard with single file", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		p := NewMediaUploadProcessor(nil, tracingnoop.NewTracerProvider())

		imgBytes := buildPNGBytes(t).Bytes()
		req := newMultiFileUploadRequest(t, map[string][]byte{
			"photo.png": imgBytes,
		})

		actual, err := p.ProcessFiles(ctx, req, "upload")
		test.NoError(t, err)
		test.SliceLen(t, 1, actual)
	})

	T.Run("standard with multiple files", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		p := NewMediaUploadProcessor(nil, tracingnoop.NewTracerProvider())

		pngBytes := buildPNGBytes(t).Bytes()
		jpegBytes := buildJPEGBytes(t).Bytes()
		req := newMultiFileUploadRequest(t, map[string][]byte{
			"photo1.png":  pngBytes,
			"photo2.jpeg": jpegBytes,
		})

		actual, err := p.ProcessFiles(ctx, req, "upload")
		test.NoError(t, err)
		test.SliceLen(t, 2, actual)
	})

	T.Run("with no multipart form", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		p := NewMediaUploadProcessor(nil, tracingnoop.NewTracerProvider())

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://whatever.whocares.gov", http.NoBody)
		must.NoError(t, err)

		actual, err := p.ProcessFiles(ctx, req, "upload")
		test.Error(t, err)
		test.Nil(t, actual)
	})

	T.Run("with invalid image data", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		p := NewMediaUploadProcessor(nil, tracingnoop.NewTracerProvider())

		req := newMultiFileUploadRequest(t, map[string][]byte{
			"photo.png": []byte("not a real png"),
		})

		actual, err := p.ProcessFiles(ctx, req, "upload")
		test.Error(t, err)
		test.Nil(t, actual)
	})

	T.Run("with non-image files", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		p := NewMediaUploadProcessor(nil, tracingnoop.NewTracerProvider())

		req := newMultiFileUploadRequest(t, map[string][]byte{
			"notes.txt": []byte("just a text file"),
		})

		actual, err := p.ProcessFiles(ctx, req, "upload")
		test.NoError(t, err)
		test.SliceLen(t, 1, actual)
	})

	T.Run("with already parsed multipart form", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		p := NewMediaUploadProcessor(nil, tracingnoop.NewTracerProvider())

		imgBytes := buildPNGBytes(t).Bytes()
		req := newMultiFileUploadRequest(t, map[string][]byte{
			"photo.png": imgBytes,
		})

		must.NoError(t, req.ParseMultipartForm(defaultMaxMemory))

		actual, err := p.ProcessFiles(ctx, req, "upload")
		test.NoError(t, err)
		test.SliceLen(t, 1, actual)
	})
}
