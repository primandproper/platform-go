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

	"github.com/primandproper/platform-go/v2/errors"
)

const (
	imagePNG  = "image/png"
	imageJPEG = "image/jpeg"
	imageGIF  = "image/gif"
)

var (
	// ErrInvalidImageContentType indicates the image was of an unsupported type.
	ErrInvalidImageContentType = errors.New("invalid image content type")
)

// Image is a decoded, in-memory image with its detected content type.
type Image struct {
	ContentType string
	Data        []byte
}

// Decode reads an image from r, validating that it is a supported, decodable image and
// detecting its content type from the data itself (not from any filename).
func Decode(r io.Reader) (*Image, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, errors.Wrap(err, "reading image data")
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
