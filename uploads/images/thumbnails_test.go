package images

import (
	"bytes"
	"testing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func Test_newThumbnailer(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		for _, ct := range []string{imagePNG, imageJPEG, imageGIF} {
			x, err := newThumbnailer(ct)
			test.NoError(t, err)
			test.NotNil(t, x)
		}
	})

	T.Run("invalid content type", func(t *testing.T) {
		t.Parallel()

		x, err := newThumbnailer(t.Name())
		test.Error(t, err)
		test.Nil(t, x)
	})
}

func Test_preprocess(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := &Image{ContentType: imagePNG, Data: buildPNGBytes(t)}

		img, err := preprocess(i, 128, 128)
		test.NoError(t, err)
		test.NotNil(t, img)
	})

	T.Run("with invalid content", func(t *testing.T) {
		t.Parallel()

		i := &Image{ContentType: imagePNG, Data: []byte(t.Name())}

		img, err := preprocess(i, 128, 128)
		test.Error(t, err)
		test.Nil(t, img)
	})

	T.Run("rejects zero dimensions instead of producing a 1x1 image", func(t *testing.T) {
		t.Parallel()

		i := &Image{ContentType: imagePNG, Data: buildPNGBytes(t)}

		_, err := preprocess(i, 0, 0)
		test.ErrorIs(t, err, ErrInvalidThumbnailDimensions)

		_, err = preprocess(i, 128, 0)
		test.ErrorIs(t, err, ErrInvalidThumbnailDimensions)
	})
}

func Test_jpegThumbnailer_Thumbnail(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := &Image{ContentType: imageJPEG, Data: buildJPEGBytes(t)}

		actual, err := (&jpegThumbnailer{}).Thumbnail(i, 128, 128)
		test.NoError(t, err)
		must.NotNil(t, actual)
		test.EqOp(t, imageJPEG, actual.ContentType)

		roundTrip, err := Decode(bytes.NewReader(actual.Data))
		test.NoError(t, err)
		must.NotNil(t, roundTrip)
		test.EqOp(t, imageJPEG, roundTrip.ContentType)
	})

	T.Run("with invalid content", func(t *testing.T) {
		t.Parallel()

		i := &Image{ContentType: imageJPEG, Data: []byte(t.Name())}

		actual, err := (&jpegThumbnailer{}).Thumbnail(i, 128, 128)
		test.Error(t, err)
		test.Nil(t, actual)
	})
}

func Test_pngThumbnailer_Thumbnail(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := &Image{ContentType: imagePNG, Data: buildPNGBytes(t)}

		actual, err := (&pngThumbnailer{}).Thumbnail(i, 128, 128)
		test.NoError(t, err)
		must.NotNil(t, actual)
		test.EqOp(t, imagePNG, actual.ContentType)

		roundTrip, err := Decode(bytes.NewReader(actual.Data))
		test.NoError(t, err)
		must.NotNil(t, roundTrip)
		test.EqOp(t, imagePNG, roundTrip.ContentType)
	})

	T.Run("with invalid content", func(t *testing.T) {
		t.Parallel()

		i := &Image{ContentType: imagePNG, Data: []byte(t.Name())}

		actual, err := (&pngThumbnailer{}).Thumbnail(i, 128, 128)
		test.Error(t, err)
		test.Nil(t, actual)
	})
}

func Test_gifThumbnailer_Thumbnail(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := &Image{ContentType: imageGIF, Data: buildGIFBytes(t)}

		actual, err := (&gifThumbnailer{}).Thumbnail(i, 128, 128)
		test.NoError(t, err)
		must.NotNil(t, actual)
		test.EqOp(t, imageGIF, actual.ContentType)

		roundTrip, err := Decode(bytes.NewReader(actual.Data))
		test.NoError(t, err)
		must.NotNil(t, roundTrip)
		test.EqOp(t, imageGIF, roundTrip.ContentType)
	})

	T.Run("with invalid content", func(t *testing.T) {
		t.Parallel()

		i := &Image{ContentType: imageGIF, Data: []byte(t.Name())}

		actual, err := (&gifThumbnailer{}).Thumbnail(i, 128, 128)
		test.Error(t, err)
		test.Nil(t, actual)
	})
}
