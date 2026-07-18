package images_test

import (
	"bytes"
	"fmt"
	"image"
	"image/png"

	"github.com/primandproper/platform-go/v5/uploads/images"
)

func ExampleDecode() {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		panic(err)
	}

	// Decode detects the content type from the data itself, not from any filename.
	img, err := images.Decode(&buf)
	if err != nil {
		panic(err)
	}

	fmt.Println(img.ContentType)
	// Output: image/png
}

func ExampleImage_DataURI() {
	img := &images.Image{ContentType: "text/plain", Data: []byte("hi")}

	fmt.Println(img.DataURI())
	// Output: data:text/plain;base64,aGk=
}

func ExampleImage_Thumbnail() {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 8, 4))); err != nil {
		panic(err)
	}

	img, err := images.Decode(&buf)
	if err != nil {
		panic(err)
	}

	thumb, err := img.Thumbnail(4, 4)
	if err != nil {
		panic(err)
	}

	fmt.Println(thumb.ContentType)
	// Output: image/png
}
