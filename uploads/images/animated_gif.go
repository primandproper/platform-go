package images

import (
	"bytes"
	"image"
	"image/color/palette"
	"image/draw"
	"image/gif"
	"math"

	"github.com/primandproper/platform-go/v3/errors"

	xdraw "golang.org/x/image/draw"
)

// resizeGIF decodes every frame of a GIF, downscales each to fit within width x height (preserving
// aspect ratio and, for animated GIFs, the animation), and re-encodes. Single-frame GIFs stay
// single-frame. Frames are composited onto a running canvas honoring disposal, then each output
// frame is emitted as a full frame (Disposal=None) so partial/dirty-rectangle frames re-quantize
// correctly.
func resizeGIF(data []byte, width, height uint) ([]byte, error) {
	src, err := gif.DecodeAll(bytes.NewReader(data))
	if err != nil {
		return nil, errors.Wrap(err, "decoding GIF")
	}

	// Logical screen bounds; fall back to the first frame if the header omits them.
	screen := image.Rect(0, 0, src.Config.Width, src.Config.Height)
	if screen.Empty() && len(src.Image) > 0 {
		screen = src.Image[0].Bounds()
	}
	srcW, srcH := screen.Dx(), screen.Dy()
	if srcW <= 0 || srcH <= 0 {
		return nil, errors.Wrap(ErrInvalidImageContentType, "GIF has no usable frame bounds")
	}

	scale := math.Min(float64(width)/float64(srcW), float64(height)/float64(srcH))
	if scale > 1 {
		scale = 1 // never upscale
	}
	dstW := max(int(math.Round(float64(srcW)*scale)), 1)
	dstH := max(int(math.Round(float64(srcH)*scale)), 1)
	dstRect := image.Rect(0, 0, dstW, dstH)

	out := &gif.GIF{
		LoopCount:       src.LoopCount,
		BackgroundIndex: src.BackgroundIndex,
		Config: image.Config{
			ColorModel: src.Config.ColorModel,
			Width:      dstW,
			Height:     dstH,
		},
	}

	canvas := image.NewRGBA(screen)
	for i, frame := range src.Image {
		var snapshot *image.RGBA
		if disposalAt(src, i) == gif.DisposalPrevious {
			snapshot = image.NewRGBA(canvas.Bounds())
			draw.Draw(snapshot, snapshot.Bounds(), canvas, canvas.Bounds().Min, draw.Src)
		}

		draw.Draw(canvas, frame.Bounds(), frame, frame.Bounds().Min, draw.Over)

		scaled := image.NewRGBA(dstRect)
		xdraw.CatmullRom.Scale(scaled, dstRect, canvas, screen, xdraw.Src, nil)

		paletted := image.NewPaletted(dstRect, palette.Plan9)
		draw.FloydSteinberg.Draw(paletted, dstRect, scaled, image.Point{})

		out.Image = append(out.Image, paletted)
		out.Delay = append(out.Delay, delayAt(src, i))
		out.Disposal = append(out.Disposal, gif.DisposalNone)

		switch disposalAt(src, i) {
		case gif.DisposalBackground:
			// Clear the frame's region back to transparent for the next composite.
			draw.Draw(canvas, frame.Bounds(), image.Transparent, image.Point{}, draw.Src)
		case gif.DisposalPrevious:
			if snapshot != nil {
				draw.Draw(canvas, canvas.Bounds(), snapshot, canvas.Bounds().Min, draw.Src)
			}
		}
	}

	var b bytes.Buffer
	if err = gif.EncodeAll(&b, out); err != nil {
		return nil, errors.Wrap(err, "encoding GIF")
	}

	return b.Bytes(), nil
}

func disposalAt(g *gif.GIF, i int) byte {
	if i < len(g.Disposal) {
		return g.Disposal[i]
	}
	return gif.DisposalNone
}

func delayAt(g *gif.GIF, i int) int {
	if i < len(g.Delay) {
		return g.Delay[i]
	}
	return 0
}
