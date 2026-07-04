package images

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"testing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// jpegWithOrientation encodes img as JPEG and splices an EXIF APP1 segment declaring the given
// Orientation immediately after the SOI marker. stdlib image/jpeg writes no EXIF, so this lets a
// test produce a JPEG whose orientation the thumbnailer must honor.
func jpegWithOrientation(t *testing.T, img image.Image, orientation uint16) []byte {
	t.Helper()

	var base bytes.Buffer
	must.NoError(t, jpeg.Encode(&base, img, &jpeg.Options{Quality: jpeg.DefaultQuality}))
	b := base.Bytes()

	// Big-endian ("MM") TIFF with a single IFD0 entry: Orientation (0x0112), SHORT, count 1.
	tiff := []byte{
		'M', 'M',
		0x00, 0x2A,
		0x00, 0x00, 0x00, 0x08, // IFD0 offset
		0x00, 0x01, // one entry
		0x01, 0x12, // tag: Orientation
		0x00, 0x03, // type: SHORT
		0x00, 0x00, 0x00, 0x01, // count: 1
		0x00, 0x00, 0x00, 0x00, // value (filled below)
		0x00, 0x00, 0x00, 0x00, // next IFD offset
	}
	binary.BigEndian.PutUint16(tiff[18:20], orientation)

	payload := append([]byte("Exif\x00\x00"), tiff...)

	seg := []byte{0xFF, 0xE1, 0x00, 0x00}
	binary.BigEndian.PutUint16(seg[2:4], uint16(len(payload)+2))
	seg = append(seg, payload...)

	out := make([]byte, 0, len(b)+len(seg))
	out = append(out, b[:2]...) // SOI
	out = append(out, seg...)
	out = append(out, b[2:]...)

	return out
}

func TestJPEGThumbnailHonorsOrientation(T *testing.T) {
	T.Parallel()

	T.Run("a portrait-tagged landscape image thumbnails to portrait", func(t *testing.T) {
		t.Parallel()

		// A landscape 8x4 image tagged orientation 6 (rotate 90 CW) is displayed portrait (4 wide
		// x 8 tall). The thumbnail must reflect the displayed orientation, so it comes out taller
		// than it is wide — the opposite of the same image with no EXIF tag.
		landscape := image.NewRGBA(image.Rect(0, 0, 8, 4))

		tagged := &Image{ContentType: imageJPEG, Data: jpegWithOrientation(t, landscape, 6)}
		thumb, err := tagged.Thumbnail(100, 100)
		must.NoError(t, err)

		cfg, _, err := image.DecodeConfig(bytes.NewReader(thumb.Data))
		must.NoError(t, err)
		test.True(t, cfg.Height > cfg.Width)

		untagged := &Image{ContentType: imageJPEG, Data: jpegWithOrientation(t, landscape, 1)}
		baseThumb, err := untagged.Thumbnail(100, 100)
		must.NoError(t, err)

		baseCfg, _, err := image.DecodeConfig(bytes.NewReader(baseThumb.Data))
		must.NoError(t, err)
		test.True(t, baseCfg.Width > baseCfg.Height)
	})
}

// buildAnimatedGIF builds an animated GIF with the given number of distinct frames, each w x h.
func buildAnimatedGIF(t *testing.T, frames, w, h int) []byte {
	t.Helper()

	g := &gif.GIF{
		LoopCount: 3,
		Config:    image.Config{Width: w, Height: h},
	}
	pal := color.Palette{color.RGBA{A: 255}, color.RGBA{R: 255, A: 255}, color.RGBA{B: 255, A: 255}}
	for f := range frames {
		frame := image.NewPaletted(image.Rect(0, 0, w, h), pal)
		// Fill each frame with a different palette index so frames are visibly distinct.
		idx := uint8(1 + f%2)
		for y := range h {
			for x := range w {
				frame.SetColorIndex(x, y, idx)
			}
		}
		g.Image = append(g.Image, frame)
		g.Delay = append(g.Delay, 10)
		g.Disposal = append(g.Disposal, gif.DisposalNone)
	}

	var b bytes.Buffer
	must.NoError(t, gif.EncodeAll(&b, g))

	return b.Bytes()
}

func TestGIFThumbnailPreservesAnimation(T *testing.T) {
	T.Parallel()

	T.Run("a multi-frame GIF keeps every frame", func(t *testing.T) {
		t.Parallel()

		img := &Image{ContentType: imageGIF, Data: buildAnimatedGIF(t, 3, 40, 20)}

		thumb, err := img.Thumbnail(10, 10)
		must.NoError(t, err)
		test.EqOp(t, imageGIF, thumb.ContentType)

		decoded, err := gif.DecodeAll(bytes.NewReader(thumb.Data))
		must.NoError(t, err)

		// Animation preserved: same frame count, loop count carried through, and downscaled.
		test.EqOp(t, 3, len(decoded.Image))
		test.EqOp(t, 3, decoded.LoopCount)
		test.EqOp(t, 10, decoded.Config.Width)
		test.EqOp(t, 5, decoded.Config.Height)
	})

	T.Run("a single-frame GIF stays single-frame", func(t *testing.T) {
		t.Parallel()

		img := &Image{ContentType: imageGIF, Data: buildAnimatedGIF(t, 1, 40, 20)}

		thumb, err := img.Thumbnail(10, 10)
		must.NoError(t, err)

		decoded, err := gif.DecodeAll(bytes.NewReader(thumb.Data))
		must.NoError(t, err)
		test.EqOp(t, 1, len(decoded.Image))
	})

	T.Run("rejects zero dimensions", func(t *testing.T) {
		t.Parallel()

		img := &Image{ContentType: imageGIF, Data: buildAnimatedGIF(t, 2, 40, 20)}

		_, err := img.Thumbnail(0, 10)
		test.Error(t, err)
	})
}
