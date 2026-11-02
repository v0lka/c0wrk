package updater

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"github.com/v0lka/sp4rk/safeio"
)

// ErrChecksumMismatch: the archive's digest differs from its SHA256SUMS entry —
// fail-closed, the caller MUST delete the archive
// (specs/decisions/023-auto-update.md).
var ErrChecksumMismatch = errors.New("checksum mismatch")

// Verifier checks a downloaded archive against a checksums file; an interface
// so a signature-based strategy can replace SHA256 without touching the
// downloader (specs/decisions/023-auto-update.md).
type Verifier interface {
	// Verify reports whether archivePath matches assetName's entry in the
	// SHA256SUMS file at sumsPath; a non-nil error wraps ErrChecksumNotFound,
	// ErrChecksumMismatch, or a read error.
	Verify(archivePath, assetName, sumsPath string) error
}

// SHA256Verifier is the production Verifier. It parses the SHA256SUMS file,
// looks up the expected digest for assetName, computes the archive's actual
// SHA256 digest, and compares the two in constant time.
type SHA256Verifier struct{}

// Compile-time check that SHA256Verifier implements Verifier.
var _ Verifier = SHA256Verifier{}

// Verify implements Verifier.
func (SHA256Verifier) Verify(archivePath, assetName, sumsPath string) error {
	sumsData, err := safeio.ReadFile(sumsPath)
	if err != nil {
		return fmt.Errorf("reading checksums %q: %w", sumsPath, err)
	}

	expected, err := FindChecksum(sumsData, assetName)
	if err != nil {
		return err
	}

	archive, err := safeio.Open(archivePath)
	if err != nil {
		return fmt.Errorf("opening archive %q: %w", archivePath, err)
	}
	defer func() { _ = archive.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, archive); err != nil {
		return fmt.Errorf("hashing archive %q: %w", archivePath, err)
	}
	actual := hex.EncodeToString(h.Sum(nil))

	if !equalFoldHex(actual, expected) {
		return fmt.Errorf("%w: asset %q: expected %s, got %s",
			ErrChecksumMismatch, assetName, expected, actual)
	}
	return nil
}

// equalFoldHex compares two hex strings case-insensitively (SHA256SUMS may use
// either case; the computed value is always lowercase), decoded and compared
// with crypto/subtle.ConstantTimeCompare to avoid timing leaks.
func equalFoldHex(a, b string) bool {
	if len(a) != sha256HexLen || len(b) != sha256HexLen {
		return false
	}
	ab, err := hex.DecodeString(a)
	if err != nil {
		return false
	}
	bb, err := hex.DecodeString(b)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(ab, bb) == 1
}
