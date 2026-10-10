package session

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	_ "image/gif" // register gif decoder for image.Decode
	"image/jpeg"
	_ "image/png" // register png decoder for image.Decode
	"io"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // register webp decoder for image.Decode

	"github.com/v0lka/sp4rk/safeio"
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

	// maxImageFileBytes caps how much of the encoded attachment file is read
	// into memory. Files above the provider cap get re-encoded anyway, so a
	// generous ceiling (well above the 5 MB re-encode threshold, far below
	// anything a user attachment realistically needs) bounds the raw buffer
	// instead of letting safeio.ReadFile accept an arbitrarily large file.
	maxImageFileBytes = 32 << 20 // 32 MB

	// maxDecodedPixels bounds the DECODED pixel buffer. image.Decode
	// allocates the full W×H buffer from the format header alone — before any
	// pixel data is read — so a few-hundred-byte crafted PNG declaring
	// 65535×65535 would allocate ~17 GB and fatally OOM the desktop process.
	// The bound equals the worst case the dimension cap already admitted
	// (maxImageDimension × maxImageDimension), so every image that previously
	// decoded without OOM still decodes (and is resized exactly as before);
	// only header-declared allocations beyond that fail fast, as a per-file
	// attach error. Enforced via DecodeConfig BEFORE the full decode.
	maxDecodedPixels = maxImageDimension * maxImageDimension
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
//
// The read goes through safeio so a non-regular attachment path (a FIFO or
// device) is refused instead of blocking the attach RPC's read-open forever,
// and is capped at maxImageFileBytes so an oversized file fails with an error
// instead of being buffered whole.
func processImage(path string) (base64Data, mediaType, thumbnailDataURI string, sizeBytes int64, err error) {
	raw, err := readImageFileCapped(path, maxImageFileBytes)
	if err != nil {
		return "", "", "", 0, err
	}
	sizeBytes = int64(len(raw))

	// Pre-decode guard: parse only the format header and reject a header-
	// declared pixel allocation beyond the bound BEFORE image.Decode
	// materializes the full buffer. DecodeConfig reads the same header bytes
	// the full decoder would, so any image that decodes at all yields a
	// config here; images whose config fails would have failed the full
	// decode too, just after wasting the allocation.
	cfg, _, cfgErr := image.DecodeConfig(bytes.NewReader(raw))
	if cfgErr != nil {
		return "", "", "", 0, fmt.Errorf("decode image: %w", cfgErr)
	}
	if cfg.Width < 0 || cfg.Height < 0 || cfg.Width*cfg.Height > maxDecodedPixels {
		return "", "", "", 0, fmt.Errorf(
			"image dimensions %dx%d exceed the %d pixel limit and cannot be attached",
			cfg.Width, cfg.Height, maxDecodedPixels)
	}

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

// readImageFileCapped reads path through safeio (FIFO/device-refusing open)
// but bounds the in-memory copy at limit bytes: a larger file fails with an
// explicit "too large" error instead of being buffered whole. A file of
// exactly limit bytes is accepted; only strictly larger content is refused
// (the +1 read admits detecting the overflow without unbounded buffering).
func readImageFileCapped(path string, limit int64) ([]byte, error) {
	f, err := safeio.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read image: %w", err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read image: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("read image: file exceeds the %d MB attachment read limit", limit>>20)
	}
	return data, nil
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
