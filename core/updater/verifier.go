package updater

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
)

// ErrChecksumMismatch is returned when an archive's computed SHA256 digest does
// not equal the digest recorded for its asset in the SHA256SUMS file. This is a
// fail-closed condition: the caller MUST delete the archive.
var ErrChecksumMismatch = errors.New("checksum mismatch")

// Verifier checks the integrity of a downloaded archive against a checksums
// file. It is an interface so the verification strategy can evolve (e.g. a
// future signature-based verifier) without changing the downloader.
type Verifier interface {
	// Verify confirms that the archive at archivePath matches the entry for
	// assetName in the SHA256SUMS file at sumsPath. It returns nil on success
	// and a non-nil error (wrapping ErrChecksumNotFound, ErrChecksumMismatch,
	// or a read error) on any failure.
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
	sumsData, err := os.ReadFile(sumsPath)
	if err != nil {
		return fmt.Errorf("reading checksums %q: %w", sumsPath, err)
	}

	expected, err := FindChecksum(sumsData, assetName)
	if err != nil {
		return err
	}

	archive, err := os.Open(archivePath)
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

// equalFoldHex compares two hex strings. SHA256SUMS files may use either case,
// so the comparison is case-insensitive for the expected value while the
// computed value is always lowercase. The decoded bytes are compared with
// crypto/subtle.ConstantTimeCompare to avoid timing leaks on the integrity
// check.
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
