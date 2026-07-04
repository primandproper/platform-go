/*
Package images provides small, pure helpers for validating, encoding, and thumbnailing images.
It takes bytes/io.Reader in and gives bytes out; it has no knowledge of HTTP or object storage.

Supported formats are PNG, JPEG, and GIF. Thumbnailing preserves aspect ratio and never upscales.

Orientation: JPEG thumbnails honor the EXIF Orientation tag, so a photo captured in portrait on a
phone (which stores landscape pixels plus an orientation flag) thumbnails upright. Orientation is
read only from JPEG data; PNG and GIF have no equivalent tag.

Animation: animated GIFs keep their animation — every frame is resized and re-quantized, and the
loop count and per-frame delays are preserved. Single-frame GIFs stay single-frame.
*/
package images
