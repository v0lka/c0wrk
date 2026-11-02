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

// Image processing constraints: oversize images are downscaled to
// resizeLongEdge and re-encoded as JPEG to fit provider attachment limits.
const (
	maxImageBytes     = 5 * 1024 * 1024 // 5 MB — provider attachment size cap
	maxImageDimension = 8000            // max width or height in pixels
	resizeLongEdge    = 1568            // target long edge (px) when downscaling
	thumbnailSize     = 64              // thumbnail long edge in pixels
	jpegQuality       = 90              // quality for re-encoded full image
	thumbnailQuality  = 70              // quality for thumbnail

	// maxImageFileBytes bounds the raw read buffer; above-cap files get
	// re-encoded anyway, so a generous ceiling suffices.
	maxImageFileBytes = 32 << 20 // 32 MB

	// maxDecodedPixels bounds the DECODED pixel buffer: image.Decode
	// allocates the full W×H buffer from the header alone, so a crafted
	// tiny PNG declaring 65535×65535 would allocate ~17 GB and OOM the
	// process (enforced via DecodeConfig first).
	maxDecodedPixels = maxImageDimension * maxImageDimension
)

// Reads an image file, decodes it (png/jpeg/gif/webp), and returns a
// base64-encoded copy plus a small JPEG thumbnail data URI; oversize images
// are downscaled and re-encoded as JPEG. The read is safeio-capped
// (FIFO/device refusal, size bound).
func processImage(path string) (base64Data, mediaType, thumbnailDataURI string, sizeBytes int64, err error) {
	raw, err := readImageFileCapped(path, maxImageFileBytes)
	if err != nil {
		return "", "", "", 0, err
	}
	sizeBytes = int64(len(raw))

	// Pre-decode guard: header-only parse rejects a beyond-bound allocation
	// BEFORE image.Decode materializes the buffer; any image that decodes
	// at all yields a config here.
	cfg, _, cfgErr := image.DecodeConfig(bytes.NewReader(raw))
	if cfgErr != nil {
		return "", "", "", 0, fmt.Errorf("decode image: %w", cfgErr)
	}
	if cfg.Width < 0 || cfg.Height < 0 || cfg.Width*cfg.Height > maxDecodedPixels {
		return "", "", "", 0, fmt.Errorf(
			"image dimensions %dx%d exceed the %d pixel limit and cannot be attached",
			cfg.Width, cfg.Height, maxDecodedPixels)
	}

	// png/jpeg/gif/webp via the decoders registered by the blank imports.
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

// Maps a media type to the persistence extension: within-limit images keep
// their original encoding, so the on-disk name must match the actual
// content; unrecognized/empty falls back to ".jpg".
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

// Reads path through safeio (FIFO/device-refusing open), bounding the
// in-memory copy at limit bytes; exactly limit is accepted, larger fails
// explicitly (the +1 read detects overflow).
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
