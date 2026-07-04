package testutils

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"math"
	"net/http"
	"testing"
	"time"

	fake "github.com/brianvoe/gofakeit/v7"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func init() {
	if err := fake.Seed(time.Now().UnixNano()); err != nil {
		panic(err)
	}
}

const (
	Example32ByteKey = "HEREISA32CHARSECRETWHICHISMADEUP"
	Example64ByteKey = "HEREISA64CHARSECRETWHICHISMADEUPHEREISA64CHARSECRETWHICHISMADEUP"
)

// BuildArbitraryImage builds an image with a bunch of colors in it.
func BuildArbitraryImage(widthAndHeight int) image.Image {
	img := image.NewRGBA(image.Rectangle{Min: image.Point{}, Max: image.Point{X: widthAndHeight, Y: widthAndHeight}})

	// Set color for each pixel.
	for x := range widthAndHeight {
		for y := range widthAndHeight {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: uint8(x + y), A: math.MaxUint8})
		}
	}

	return img
}

// BuildArbitraryImagePNGBytes builds an image with a bunch of colors in it.
func BuildArbitraryImagePNGBytes(widthAndHeight int) (img image.Image, imgBytes []byte) {
	var b bytes.Buffer

	img = BuildArbitraryImage(widthAndHeight)
	if err := png.Encode(&b, img); err != nil {
		panic(err)
	}

	return img, b.Bytes()
}

// BuildTestRequest builds an arbitrary *http.Request.
func BuildTestRequest(t *testing.T) *http.Request {
	t.Helper()

	ctx := context.Background()
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodOptions,
		"https://whatever.whocares.gov",
		http.NoBody,
	)

	must.NotNil(t, req)
	test.NoError(t, err)

	return req
}
