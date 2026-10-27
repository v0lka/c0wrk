//go:build windows

package session

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image/png"
	"unsafe"

	bmpdecoder "golang.org/x/image/bmp"
	"golang.org/x/sys/windows"
)

// Clipboard format constants (WinUser.h). Read via the cgo-free user32.dll
// procs below — no "import C", so the package stays CGO_ENABLED=0-compatible.
const (
	cfUnicodeText = 13 // CF_UNICODETEXT
	cfDIB         = 8  // CF_DIB — packed device-independent bitmap (BITMAPINFO)
	cfHDROP       = 15 // CF_HDROP — file list (Explorer/Finder-style "Copy")
)

var (
	modUser32   = windows.NewLazySystemDLL("user32.dll")
	modShell32  = windows.NewLazySystemDLL("shell32.dll")
	modKernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procIsClipboardFormatAvailable = modUser32.NewProc("IsClipboardFormatAvailable")
	procOpenClipboard              = modUser32.NewProc("OpenClipboard")
	procCloseClipboard             = modUser32.NewProc("CloseClipboard")
	procGetClipboardData           = modUser32.NewProc("GetClipboardData")

	procDragQueryFileW = modShell32.NewProc("DragQueryFileW")

	procGlobalLock   = modKernel32.NewProc("GlobalLock")
	procGlobalUnlock = modKernel32.NewProc("GlobalUnlock")
	procGlobalSize   = modKernel32.NewProc("GlobalSize")
)

// isClipboardFormatAvailable reports whether the given format is on the
// clipboard. It does not open the clipboard.
func isClipboardFormatAvailable(format uint) bool {
	r, _, _ := procIsClipboardFormatAvailable.Call(uintptr(format))
	return r != 0
}

// withClipboard opens the clipboard, runs fn, and always closes it, returning
// the OpenClipboard error when another app holds it open. Handles obtained
// inside fn are owned by the clipboard and must NOT be freed by the caller.
func withClipboard(fn func() error) error {
	r, _, e := procOpenClipboard.Call(0)
	if r == 0 {
		return fmt.Errorf("OpenClipboard: %w", e)
	}
	// CloseClipboard failure is non-actionable: clipboard ownership ends with the
	// function scope and the OS force-closes an abandoned handle.
	defer func() { _, _, _ = procCloseClipboard.Call() }()
	return fn()
}

// globalBytes locks an HGLOBAL from GetClipboardData and copies out its
// bytes; the handle is owned by the clipboard.
func globalBytes(h uintptr) ([]byte, error) {
	size, _, _ := procGlobalSize.Call(h)
	if size == 0 {
		return nil, errors.New("clipboard data is empty")
	}
	ptr, _, _ := procGlobalLock.Call(h)
	if ptr == 0 {
		return nil, errors.New("GlobalLock failed")
	}
	// Copy while locked: the clipboard HGLOBAL is GMEM_MOVEABLE, so once the
	// lock count drops the OS may relocate the memory — a slice into unlocked
	// memory would be use-after-unlock. GlobalUnlock failure is non-actionable
	// (the OS releases the lock on clipboard close).
	defer func() { _, _, _ = procGlobalUnlock.Call(h) }()
	buf := unsafe.Slice((*byte)(unsafe.Pointer(ptr)), size)
	return append([]byte(nil), buf...), nil
}

// clipboardImage reads a CF_DIB bitmap and re-encodes it as PNG so the media
// type matches processImage (png/jpeg/gif/webp only); ok is true only when a
// bitmap is present.
func clipboardImage(ctx context.Context) (data []byte, mediaType string, ok bool, err error) {
	_ = ctx // syscalls are not cancellable; accepted for seam uniformity.
	if !isClipboardFormatAvailable(cfDIB) {
		return nil, "", false, nil
	}
	var dib []byte
	if cerr := withClipboard(func() error {
		h, _, e := procGetClipboardData.Call(uintptr(cfDIB))
		if h == 0 {
			return fmt.Errorf("GetClipboardData(CF_DIB): %w", e)
		}
		b, gerr := globalBytes(h)
		if gerr != nil {
			return gerr
		}
		// Copy: the bytes are only valid while the clipboard is open/locked.
		dib = append([]byte(nil), b...)
		return nil
	}); cerr != nil {
		return nil, "", false, cerr
	}

	bmpBytes, mErr := dibToBMP(dib)
	if mErr != nil {
		return nil, "", false, mErr
	}
	img, dErr := bmpdecoder.Decode(bytes.NewReader(bmpBytes))
	if dErr != nil {
		return nil, "", false, fmt.Errorf("decode clipboard bitmap: %w", dErr)
	}
	var out bytes.Buffer
	if encErr := png.Encode(&out, img); encErr != nil {
		return nil, "", false, fmt.Errorf("encode clipboard bitmap to PNG: %w", encErr)
	}
	return out.Bytes(), "image/png", true, nil
}

// dibToBMP prepends a BITMAPFILEHEADER to a CF_DIB payload, producing a
// complete BMP byte stream for x/image/bmp.Decode.
func dibToBMP(dib []byte) ([]byte, error) {
	const fileHeaderSize = 14
	if len(dib) < 40 {
		return nil, errors.New("CF_DIB payload too small for BITMAPINFOHEADER")
	}
	biSize := binary.LittleEndian.Uint32(dib[0:4])
	if biSize == 0 || uint64(biSize) > uint64(len(dib)) {
		return nil, errors.New("invalid BITMAPINFOHEADER biSize")
	}
	biBitCount := binary.LittleEndian.Uint16(dib[14:16])
	biClrUsed := binary.LittleEndian.Uint32(dib[32:36])

	var paletteEntries uint32
	if biClrUsed != 0 {
		paletteEntries = biClrUsed
	} else if biBitCount <= 8 {
		paletteEntries = 1 << biBitCount
	}
	pixelOffset := uint32(fileHeaderSize) + biSize + paletteEntries*4

	header := make([]byte, fileHeaderSize)
	header[0] = 'B'
	header[1] = 'M'
	binary.LittleEndian.PutUint32(header[2:6], uint32(fileHeaderSize+len(dib))) // total file size
	binary.LittleEndian.PutUint32(header[6:10], 0)                              // reserved
	binary.LittleEndian.PutUint32(header[10:14], pixelOffset)                   // offset to pixel data

	bmp := make([]byte, 0, fileHeaderSize+len(dib))
	bmp = append(bmp, header...)
	bmp = append(bmp, dib...)
	return bmp, nil
}

// clipboardFiles reads file paths from a CF_HDROP — Explorer's "Copy"
// content; ok is true only when at least one file is present.
func clipboardFiles(ctx context.Context) (paths []string, ok bool, err error) {
	_ = ctx // syscalls are not cancellable; accepted for seam uniformity.
	if !isClipboardFormatAvailable(cfHDROP) {
		return nil, false, nil
	}
	if cerr := withClipboard(func() error {
		h, _, e := procGetClipboardData.Call(uintptr(cfHDROP))
		if h == 0 {
			return fmt.Errorf("GetClipboardData(CF_HDROP): %w", e)
		}
		// DragQueryFileW(h, 0xFFFFFFFF, nil, 0) returns the file count.
		count, _, _ := procDragQueryFileW.Call(h, 0xffffffff, 0, 0)
		for i := uintptr(0); i < count; i++ {
			length, _, _ := procDragQueryFileW.Call(h, i, 0, 0) // length excludes the NUL
			buf := make([]uint16, length+1)
			// length was pre-queried; a short read still yields a NUL-terminated
			// buffer that UTF16ToString handles. Best-effort per file.
			_, _, _ = procDragQueryFileW.Call(h, i, uintptr(unsafe.Pointer(&buf[0])), length+1)
			paths = append(paths, windows.UTF16ToString(buf))
		}
		return nil
	}); cerr != nil {
		return nil, false, cerr
	}
	return paths, len(paths) > 0, nil
}

// clipboardText reads CF_UNICODETEXT from the clipboard. ok is true only when
// non-empty text is present.
func clipboardText(ctx context.Context) (text string, ok bool, err error) {
	_ = ctx // syscalls are not cancellable; accepted for seam uniformity.
	if !isClipboardFormatAvailable(cfUnicodeText) {
		return "", false, nil
	}
	if cerr := withClipboard(func() error {
		h, _, e := procGetClipboardData.Call(uintptr(cfUnicodeText))
		if h == 0 {
			return fmt.Errorf("GetClipboardData(CF_UNICODETEXT): %w", e)
		}
		ptr, _, _ := procGlobalLock.Call(h)
		if ptr == 0 {
			return errors.New("GlobalLock failed for text")
		}
		// Text is fully read via UTF16PtrToString below; unlock failure is
		// non-actionable and the OS releases the lock on clipboard close.
		defer func() { _, _, _ = procGlobalUnlock.Call(h) }()
		text = windows.UTF16PtrToString((*uint16)(unsafe.Pointer(ptr)))
		return nil
	}); cerr != nil {
		return "", false, cerr
	}
	if text == "" {
		return "", false, nil
	}
	return text, true, nil
}
