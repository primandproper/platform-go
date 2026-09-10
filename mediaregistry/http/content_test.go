package http

import (
	"io"
	nethttp "net/http"
	"testing"

	platformerrors "github.com/primandproper/primitives-go/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestDispositionFor(T *testing.T) {
	T.Parallel()

	cases := map[string]struct {
		contentType string
		expected    string
	}{
		"a PDF is a document":                      {contentType: "application/pdf", expected: dispositionInline},
		"an image is an image":                     {contentType: "image/png", expected: dispositionInline},
		"parameters do not change the answer":      {contentType: "text/plain; charset=utf-8", expected: dispositionInline},
		"casing does not change the answer":        {contentType: "TEXT/HTML", expected: dispositionAttachment},
		"whitespace does not change the answer":    {contentType: "  text/html  ", expected: dispositionAttachment},
		"HTML is executable":                       {contentType: "text/html", expected: dispositionAttachment},
		"XHTML is executable":                      {contentType: "application/xhtml+xml", expected: dispositionAttachment},
		"an SVG is executable":                     {contentType: "image/svg+xml", expected: dispositionAttachment},
		"XML can name a stylesheet":                {contentType: "application/xml", expected: dispositionAttachment},
		"and so can the other spelling of it":      {contentType: "text/xml", expected: dispositionAttachment},
		"a row that says nothing is an attachment": {contentType: "", expected: dispositionAttachment},
		"and so is one that says only a parameter": {contentType: "; charset=utf-8", expected: dispositionAttachment},
	}

	for name, testCase := range cases {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			test.EqOp(t, testCase.expected, dispositionFor(testCase.contentType))
		})
	}
}

func TestHandler_serveActiveContentTypes(T *testing.T) {
	T.Parallel()

	T.Run("an uploaded page is never served inline", func(t *testing.T) {
		t.Parallel()

		object := testObject()
		object.ContentType = "text/html"

		res := get(t, mount(t, storeReturning(object), newRangingObjects(), ownedCaller()), testObjectID, nil)

		test.EqOp(t, nethttp.StatusOK, res.Code)
		test.EqOp(t, dispositionAttachment, res.Header().Get(contentDispositionHeader))
		test.EqOp(t, "text/html", res.Header().Get(contentTypeHeader))
		test.EqOp(t, contentTypeOptionsValue, res.Header().Get(contentTypeOptionsHeader))
	})

	T.Run("a row that names no type is served as an opaque attachment", func(t *testing.T) {
		t.Parallel()

		object := testObject()
		object.ContentType = ""

		res := get(t, mount(t, storeReturning(object), newRangingObjects(), ownedCaller()), testObjectID, nil)

		test.EqOp(t, nethttp.StatusOK, res.Code)
		test.EqOp(t, dispositionAttachment, res.Header().Get(contentDispositionHeader))

		// Written out rather than left to be sniffed from the object's first
		// bytes, which is a guess whoever uploaded them gets to steer.
		test.EqOp(t, unknownContentType, res.Header().Get(contentTypeHeader))
	})
}

func TestRangedObject(T *testing.T) {
	T.Parallel()

	// object builds a seeker over the fixture bytes.
	object := func() (*rangingObjects, *rangedObject) {
		manager := newRangingObjects()

		return manager, &rangedObject{
			ctx:    T.Context(),
			reader: manager,
			key:    testObjectKey,
			size:   int64(len(testBody)),
		}
	}

	T.Run("opens nothing until something is read", func(t *testing.T) {
		t.Parallel()

		manager, content := object()

		end, err := content.Seek(0, io.SeekEnd)
		must.NoError(t, err)
		test.EqOp(t, int64(len(testBody)), end)

		start, err := content.Seek(0, io.SeekStart)
		must.NoError(t, err)
		test.EqOp(t, int64(0), start)

		// Which is the whole of ServeContent's preamble, at no cost.
		test.EqOp(t, int64(0), manager.opens.Load())

		all, err := io.ReadAll(content)
		must.NoError(t, err)
		test.EqOp(t, testBody, string(all))
		test.EqOp(t, int64(1), manager.opens.Load())
	})

	T.Run("a range costs exactly one read of storage", func(t *testing.T) {
		t.Parallel()

		manager, content := object()

		at, err := content.Seek(6, io.SeekStart)
		must.NoError(t, err)
		test.EqOp(t, int64(6), at)

		slice := make([]byte, 5)

		read, err := io.ReadFull(content, slice)
		must.NoError(t, err)
		test.EqOp(t, 5, read)
		test.EqOp(t, testBody[6:11], string(slice))
		test.EqOp(t, int64(1), manager.opens.Load())

		must.NoError(t, content.Close())
		test.EqOp(t, int64(1), manager.closed.Load())
	})

	T.Run("seeking again drops the reader it was holding", func(t *testing.T) {
		t.Parallel()

		manager, content := object()

		slice := make([]byte, 4)

		_, err := io.ReadFull(content, slice)
		must.NoError(t, err)
		test.EqOp(t, testBody[0:4], string(slice))

		_, err = content.Seek(20, io.SeekStart)
		must.NoError(t, err)
		test.EqOp(t, int64(1), manager.closed.Load())

		_, err = io.ReadFull(content, slice)
		must.NoError(t, err)
		test.EqOp(t, testBody[20:24], string(slice))
		test.EqOp(t, int64(2), manager.opens.Load())
	})

	T.Run("seeking to where it already is keeps the reader", func(t *testing.T) {
		t.Parallel()

		manager, content := object()

		slice := make([]byte, 4)

		_, err := io.ReadFull(content, slice)
		must.NoError(t, err)

		_, err = content.Seek(0, io.SeekCurrent)
		must.NoError(t, err)

		_, err = io.ReadFull(content, slice)
		must.NoError(t, err)
		test.EqOp(t, testBody[4:8], string(slice))
		test.EqOp(t, int64(1), manager.opens.Load())
	})

	T.Run("seeking relative to the end", func(t *testing.T) {
		t.Parallel()

		_, content := object()

		at, err := content.Seek(-4, io.SeekEnd)
		must.NoError(t, err)
		test.EqOp(t, int64(len(testBody)-4), at)

		all, err := io.ReadAll(content)
		must.NoError(t, err)
		test.EqOp(t, testBody[len(testBody)-4:], string(all))
	})

	T.Run("reads past the row's size are the end of the file", func(t *testing.T) {
		t.Parallel()

		manager, content := object()

		_, err := content.Seek(0, io.SeekEnd)
		must.NoError(t, err)

		read, err := content.Read(make([]byte, 4))
		must.ErrorIs(t, err, io.EOF)
		test.EqOp(t, 0, read)

		// Reported here rather than asked of the provider.
		test.EqOp(t, int64(0), manager.opens.Load())
	})

	T.Run("refuses a seek to a negative offset", func(t *testing.T) {
		t.Parallel()

		_, content := object()

		_, err := content.Seek(-1, io.SeekStart)
		must.ErrorIs(t, err, errNegativeSeek)
	})

	T.Run("refuses a seek from an unknown whence", func(t *testing.T) {
		t.Parallel()

		_, content := object()

		_, err := content.Seek(0, 17)
		must.ErrorIs(t, err, errUnknownWhence)
	})

	T.Run("reports a provider that will not open the range", func(t *testing.T) {
		t.Parallel()

		manager, content := object()
		manager.err = platformerrors.New("the bucket is unreachable")

		_, err := content.Read(make([]byte, 4))
		must.ErrorIs(t, err, manager.err)
	})

	T.Run("closing what was never read closes nothing", func(t *testing.T) {
		t.Parallel()

		manager, content := object()

		must.NoError(t, content.Close())
		test.EqOp(t, int64(0), manager.closed.Load())
	})

	T.Run("closing twice closes once", func(t *testing.T) {
		t.Parallel()

		manager, content := object()

		_, err := content.Read(make([]byte, 4))
		must.NoError(t, err)

		must.NoError(t, content.Close())
		must.NoError(t, content.Close())
		test.EqOp(t, int64(1), manager.closed.Load())
	})
}
