package images

import (
	"bytes"
	"image"
	"image/jpeg"
	"image/png"
	"math"
	"strings"

	"github.com/primandproper/platform-go/v6/errors"

	"github.com/disintegration/imaging"
	"golang.org/x/image/draw"
)

type thumbnailer interface {
	Thumbnail(i *Image, width, height uint) (*Image, error)
}

// newThumbnailer provides a thumbnailer given a particular content type.
func newThumbnailer(contentType string) (thumbnailer, error) {
	switch strings.TrimSpace(strings.ToLower(contentType)) {
	case imagePNG:
		return &pngThumbnailer{}, nil
	case imageJPEG:
		return &jpegThumbnailer{}, nil
	case imageGIF:
		return &gifThumbnailer{}, nil
	default:
		return nil, errors.Wrapf(ErrInvalidImageContentType, "%s", contentType)
	}
}

func preprocess(i *Image, width, height uint) (image.Image, error) {
	if width == 0 || height == 0 {
		return nil, ErrInvalidThumbnailDimensions
	}

	// imaging.AutoOrientation honors the EXIF Orientation tag (stdlib image/jpeg ignores it), so a
	// photo shot in portrait on a phone decodes upright instead of sideways. Applying it before
	// scaling means transposing orientations swap width/height, which the aspect math then respects.
	img, err := imaging.Decode(bytes.NewReader(i.Data), imaging.AutoOrientation(true))
	if err != nil {
		return nil, errors.Wrap(err, "decoding image")
	}

	return thumbnail(width, height, img), nil
}

// thumbnail downscales img to fit within width x height, preserving aspect ratio. Images already
// within the bounds are returned unchanged (it never upscales).
func thumbnail(width, height uint, img image.Image) image.Image {
	b := img.Bounds()
	srcW, srcH := b.Dx(), b.Dy()
	if srcW <= 0 || srcH <= 0 {
		return img
	}

	scale := math.Min(float64(width)/float64(srcW), float64(height)/float64(srcH))
	if scale >= 1 {
		return img
	}

	dstW := max(int(math.Round(float64(srcW)*scale)), 1)
	dstH := max(int(math.Round(float64(srcH)*scale)), 1)

	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)

	return dst
}

type jpegThumbnailer struct{}

// Thumbnail creates a JPEG thumbnail.
func (t *jpegThumbnailer) Thumbnail(img *Image, width, height uint) (*Image, error) {
	thumbnail, err := preprocess(img, width, height)
	if err != nil {
		return nil, err
	}

	var b bytes.Buffer
	if err = jpeg.Encode(&b, thumbnail, &jpeg.Options{Quality: jpeg.DefaultQuality}); err != nil {
		return nil, errors.Wrap(err, "encoding JPEG")
	}

	return &Image{ContentType: imageJPEG, Data: b.Bytes()}, nil
}

type gifThumbnailer struct{}

// Thumbnail creates a GIF thumbnail. Animated GIFs keep their animation (every frame is resized);
// single-frame GIFs stay single-frame.
func (t *gifThumbnailer) Thumbnail(img *Image, width, height uint) (*Image, error) {
	if width == 0 || height == 0 {
		return nil, ErrInvalidThumbnailDimensions
	}

	data, err := resizeGIF(img.Data, width, height)
	if err != nil {
		return nil, err
	}

	return &Image{ContentType: imageGIF, Data: data}, nil
}

type pngThumbnailer struct{}

// Thumbnail creates a PNG thumbnail.
func (t *pngThumbnailer) Thumbnail(img *Image, width, height uint) (*Image, error) {
	thumbnail, err := preprocess(img, width, height)
	if err != nil {
		return nil, err
	}

	var b bytes.Buffer
	if err = png.Encode(&b, thumbnail); err != nil {
		return nil, errors.Wrap(err, "encoding PNG")
	}

	return &Image{ContentType: imagePNG, Data: b.Bytes()}, nil
}
