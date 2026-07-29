package session

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	_ "image/gif" // register gif decoder for image.Decode
	"image/jpeg"
	_ "image/png" // register png decoder for image.Decode
	"os"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // register webp decoder for image.Decode
)

// Image processing constraints. Images that exceed maxImageBytes or
// maxImageDimension are downscaled to resizeLongEdge and re-encoded as JPEG
// so they fit within common provider attachment limits.
const (
	maxImageBytes     = 5 * 1024 * 1024 // 5 MB — provider attachment size cap
	maxImageDimension = 8000            // max width or height in pixels
	resizeLongEdge    = 1568            // target long edge (px) when downscaling
	thumbnailSize     = 64              // thumbnail long edge in pixels
	jpegQuality       = 90              // quality for re-encoded full image
	thumbnailQuality  = 70              // quality for thumbnail
)

// processImage reads an image file, decodes it (png/jpeg/gif/webp), and
// returns a base64-encoded copy plus a small JPEG thumbnail data URI.
//
// Images that exceed 5 MB or 8000×8000 pixels are downscaled to a 1568px
// long edge and re-encoded as JPEG (quality 90) so they fit within provider
// limits. Images within limits are returned in their original encoded form.
//
// The thumbnail is always a 64px JPEG (quality 70) data URI suitable for UI
// display. The returned sizeBytes reflects the encoded payload size (original
// file size when unchanged, re-encoded JPEG size when resized).
func processImage(path string) (base64Data, mediaType, thumbnailDataURI string, sizeBytes int64, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", "", "", 0, fmt.Errorf("read image: %w", err)
	}
	sizeBytes = int64(len(raw))

	// image.Decode handles png/jpeg/gif (stdlib) and webp (golang.org/x/image/webp)
	// via format decoders registered by the blank imports above.
	img, format, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return "", "", "", 0, fmt.Errorf("decode image: %w", err)
	}

	mediaType = "image/" + format

	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	needsResize := sizeBytes > maxImageBytes ||
		width > maxImageDimension ||
		height > maxImageDimension

	var fullBytes []byte
	if needsResize {
		resized := resizeToLongEdge(img, resizeLongEdge)
		var buf bytes.Buffer
		if encErr := jpeg.Encode(&buf, resized, &jpeg.Options{Quality: jpegQuality}); encErr != nil {
			return "", "", "", 0, fmt.Errorf("encode resized image: %w", encErr)
		}
		fullBytes = buf.Bytes()
		mediaType = "image/jpeg"
		sizeBytes = int64(len(fullBytes))
	} else {
		fullBytes = raw
	}

	base64Data = base64.StdEncoding.EncodeToString(fullBytes)

	// Thumbnail: always JPEG, 64px long edge, generated from the decoded image.
	thumb := resizeToLongEdge(img, thumbnailSize)
	var thumbBuf bytes.Buffer
	if encErr := jpeg.Encode(&thumbBuf, thumb, &jpeg.Options{Quality: thumbnailQuality}); encErr != nil {
		return "", "", "", 0, fmt.Errorf("encode thumbnail: %w", encErr)
	}
	thumbnailDataURI = "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(thumbBuf.Bytes())

	return base64Data, mediaType, thumbnailDataURI, sizeBytes, nil
}

// imageFileExtension maps a "image/xxx" media type to the file extension used
// when persisting the processed image to disk (session/images/{uuid}.ext).
// processImage only re-encodes to JPEG when resizing is required — images
// within limits keep their original encoding (png/gif/webp) — so the on-disk
// file must carry a matching extension rather than an always-".jpg" name that
// would misrepresent the actual file content. Falls back to ".jpg" for any
// unrecognized/empty media type (matches the resize path's JPEG output).
func imageFileExtension(mediaType string) string {
	switch mediaType {
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	default:
		return ".jpg"
	}
}

// resizeToLongEdge scales img so its longest edge equals target, preserving
// aspect ratio. Images already at or below target are returned unchanged.
// Uses CatmullRom interpolation for high-quality downscaling.
func resizeToLongEdge(img image.Image, target int) image.Image {
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	longEdge := width
	if height > longEdge {
		longEdge = height
	}
	if longEdge <= target {
		return img
	}

	scale := float64(target) / float64(longEdge)
	newW := max(1, int(float64(width)*scale))
	newH := max(1, int(float64(height)*scale))

	dst := image.NewRGBA(image.Rect(0, 0, newW, newH))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, bounds, draw.Src, nil)
	return dst
}
