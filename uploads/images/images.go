package images

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	// Registered for their decoders so image.Decode can sniff these formats.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"

	"github.com/primandproper/platform-go/v6/errors"
)

const (
	imagePNG  = "image/png"
	imageJPEG = "image/jpeg"
	imageGIF  = "image/gif"

	// maxImageBytes caps the encoded image size we will read into memory.
	maxImageBytes = 32 << 20 // 32 MiB
	// maxImageDimension caps the width and height (in pixels) we will decode, guarding against
	// small files that declare enormous dimensions to force a multi-gigabyte pixel allocation.
	maxImageDimension = 10000
)

var (
	// ErrInvalidImageContentType indicates the image was of an unsupported type.
	ErrInvalidImageContentType = errors.New("invalid image content type")
	// ErrInvalidThumbnailDimensions indicates a zero width or height was requested.
	ErrInvalidThumbnailDimensions = errors.New("thumbnail width and height must both be greater than zero")
	// ErrImageTooLarge indicates the image exceeds the configured size or dimension limits.
	ErrImageTooLarge = errors.New("image too large")
)

// Image is a decoded, in-memory image with its detected content type.
type Image struct {
	ContentType string
	Data        []byte
}

// Decode reads an image from r, validating that it is a supported, decodable image and
// detecting its content type from the data itself (not from any filename).
func Decode(r io.Reader) (*Image, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxImageBytes+1))
	if err != nil {
		return nil, errors.Wrap(err, "reading image data")
	}
	if len(data) > maxImageBytes {
		return nil, errors.Wrapf(ErrImageTooLarge, "image exceeds maximum size of %d bytes", maxImageBytes)
	}

	// Check the declared dimensions from the header before decoding pixels, so an oversized image
	// is rejected before it can force a huge allocation.
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, errors.Wrap(err, "decoding image config")
	}
	if cfg.Width > maxImageDimension || cfg.Height > maxImageDimension {
		return nil, errors.Wrapf(ErrImageTooLarge, "image dimensions %dx%d exceed maximum of %d", cfg.Width, cfg.Height, maxImageDimension)
	}

	_, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, errors.Wrap(err, "decoding image")
	}

	contentType := "image/" + format
	switch contentType {
	case imagePNG, imageJPEG, imageGIF:
	default:
		return nil, errors.Wrapf(ErrInvalidImageContentType, "%s", contentType)
	}

	return &Image{ContentType: contentType, Data: data}, nil
}

// DataURI returns the image encoded as a base64 data URI.
func (i *Image) DataURI() string {
	return fmt.Sprintf("data:%s;base64,%s", i.ContentType, base64.StdEncoding.EncodeToString(i.Data))
}

// Thumbnail returns a resized copy of the image, re-encoded in its original format.
func (i *Image) Thumbnail(width, height uint) (*Image, error) {
	t, err := newThumbnailer(i.ContentType)
	if err != nil {
		return nil, err
	}

	return t.Thumbnail(i, width, height)
}
