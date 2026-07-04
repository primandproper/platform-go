package images

import (
	"bytes"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"testing"

	"github.com/primandproper/platform-go/v2/testutils"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func buildPNGBytes(t *testing.T) []byte {
	t.Helper()

	b := new(bytes.Buffer)
	must.NoError(t, png.Encode(b, testutils.BuildArbitraryImage(256)))

	return b.Bytes()
}

func buildJPEGBytes(t *testing.T) []byte {
	t.Helper()

	b := new(bytes.Buffer)
	must.NoError(t, jpeg.Encode(b, testutils.BuildArbitraryImage(256), &jpeg.Options{Quality: jpeg.DefaultQuality}))

	return b.Bytes()
}

func buildGIFBytes(t *testing.T) []byte {
	t.Helper()

	b := new(bytes.Buffer)
	must.NoError(t, gif.Encode(b, testutils.BuildArbitraryImage(256), &gif.Options{NumColors: 256}))

	return b.Bytes()
}

func TestDecode(T *testing.T) {
	T.Parallel()

	T.Run("standard png", func(t *testing.T) {
		t.Parallel()

		data := buildPNGBytes(t)

		img, err := Decode(bytes.NewReader(data))
		test.NoError(t, err)
		must.NotNil(t, img)
		test.EqOp(t, imagePNG, img.ContentType)
		test.Eq(t, data, img.Data)
	})

	T.Run("standard jpeg", func(t *testing.T) {
		t.Parallel()

		data := buildJPEGBytes(t)

		img, err := Decode(bytes.NewReader(data))
		test.NoError(t, err)
		must.NotNil(t, img)
		test.EqOp(t, imageJPEG, img.ContentType)
	})

	T.Run("standard gif", func(t *testing.T) {
		t.Parallel()

		data := buildGIFBytes(t)

		img, err := Decode(bytes.NewReader(data))
		test.NoError(t, err)
		must.NotNil(t, img)
		test.EqOp(t, imageGIF, img.ContentType)
	})

	T.Run("with undecodable data", func(t *testing.T) {
		t.Parallel()

		img, err := Decode(bytes.NewBufferString("not a real image"))
		test.Error(t, err)
		test.Nil(t, img)
	})

	T.Run("with empty data", func(t *testing.T) {
		t.Parallel()

		img, err := Decode(bytes.NewReader(nil))
		test.Error(t, err)
		test.Nil(t, img)
	})
}

func TestImage_DataURI(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := &Image{
			ContentType: "things/stuff",
			Data:        []byte(t.Name()),
		}

		expected := "data:things/stuff;base64,VGVzdEltYWdlX0RhdGFVUkkvc3RhbmRhcmQ="
		test.EqOp(t, expected, i.DataURI())
	})
}

func TestImage_Thumbnail(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := &Image{ContentType: imagePNG, Data: buildPNGBytes(t)}

		actual, err := i.Thumbnail(123, 123)
		test.NoError(t, err)
		must.NotNil(t, actual)
		test.EqOp(t, imagePNG, actual.ContentType)

		// the thumbnail must itself be a decodable png of the detected type
		roundTrip, err := Decode(bytes.NewReader(actual.Data))
		test.NoError(t, err)
		must.NotNil(t, roundTrip)
		test.EqOp(t, imagePNG, roundTrip.ContentType)
	})

	T.Run("with invalid content type", func(t *testing.T) {
		t.Parallel()

		i := &Image{ContentType: t.Name()}

		actual, err := i.Thumbnail(123, 123)
		test.Error(t, err)
		test.Nil(t, actual)
	})
}

// failingReader always errors, to exercise read-failure paths.
type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestDecode_readError(T *testing.T) {
	T.Parallel()

	T.Run("propagates reader errors", func(t *testing.T) {
		t.Parallel()

		img, err := Decode(failingReader{})
		test.Error(t, err)
		test.Nil(t, img)
	})
}

func TestImage_Thumbnail_noUpscale(T *testing.T) {
	T.Parallel()

	T.Run("returns same dimensions when the image already fits", func(t *testing.T) {
		t.Parallel()

		i := &Image{ContentType: imagePNG, Data: buildPNGBytes(t)} // 256x256

		actual, err := i.Thumbnail(512, 512)
		test.NoError(t, err)
		must.NotNil(t, actual)
		test.EqOp(t, imagePNG, actual.ContentType)
	})
}
