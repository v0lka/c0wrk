// Package updater implements the self-update pipeline: downloading release
// archives into a staging directory, verifying their integrity against a
// SHA256SUMS file, and exposing the result for the apply phase.
//
// The downloader and verifier are fail-closed (ASI04-R2 — supply-chain
// integrity): a missing checksum entry or a hash mismatch always removes the
// archive and returns an error. Integrity is SHA256-only and unsigned —
// archive and checksums ride the same release over the same TLS channel, so
// the checksum pins the released bytes but does NOT prove release authorship
// (a trusted-CA MITM can substitute both). See specs/decisions/023-auto-update.md
// and SECURITY.md for the threat model and accepted trade-offs.
package updater

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

const (
	// sha256HexLen is the length of a SHA256 digest expressed as lowercase hex.
	sha256HexLen = 64

	// sumsFilename is the conventional name of the checksums file shipped
	// alongside release assets (mirrors the output of `sha256sum *`).
	sumsFilename = "SHA256SUMS"
)

var (
	// ErrChecksumNotFound is returned when the SHA256SUMS data contains no
	// entry for the requested asset. This is a fail-closed condition.
	ErrChecksumNotFound = errors.New("checksum not found for asset")

	// ErrMalformedChecksumLine is returned when a SHA256SUMS line does not
	// match the standard `sha256sum` format; parsing is all-or-nothing, since
	// a silently dropped entry could hide the one we need.
	ErrMalformedChecksumLine = errors.New("malformed checksum line")
)

// ParseChecksums parses SHA256SUMS content into a map of asset name →
// lowercase hex digest. The accepted format is the GNU coreutils `sha256sum`
// output: `<hex>  <filename>` (text mode, two spaces) or `<hex> *<filename>`
// (binary mode); blank lines are skipped. Any other line that cannot be split
// into a valid 64-char hex digest and a non-empty filename aborts the whole
// parse with ErrMalformedChecksumLine — fail-closed, so a parser quirk cannot
// mask a missing entry. Empty (or whitespace-only) input is valid and yields
// an empty map, so a later FindChecksum fails with ErrChecksumNotFound.
func ParseChecksums(data []byte) (map[string]string, error) {
	checksums := make(map[string]string)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	// sha256sum lines are short, but allow generous line length for long paths.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		raw := strings.TrimRight(scanner.Text(), "\r")
		if raw == "" {
			continue
		}

		filename, digest, ok := parseChecksumLine(raw)
		if !ok {
			return nil, fmt.Errorf("%w: %q", ErrMalformedChecksumLine, raw)
		}
		checksums[filename] = digest
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading checksums: %w", err)
	}
	return checksums, nil
}

// parseChecksumLine splits one SHA256SUMS line into (filename, digest):
// <64 hex><space><indicator><filename>, where the indicator is ' ' (text
// mode) or '*' (binary mode). The filename runs verbatim to end of line so
// names containing spaces survive. ok=false on a malformed line.
func parseChecksumLine(line string) (filename, digest string, ok bool) {
	// 64 hex digits + separator + mode indicator + at least one filename char.
	if len(line) < sha256HexLen+3 {
		return "", "", false
	}
	digest = line[:sha256HexLen]
	if !isLowerHex(digest) {
		return "", "", false
	}
	// Separator must be a single space.
	if line[sha256HexLen] != ' ' {
		return "", "", false
	}
	// Mode indicator: space (text) or '*' (binary).
	indicator := line[sha256HexLen+1]
	if indicator != ' ' && indicator != '*' {
		return "", "", false
	}
	filename = line[sha256HexLen+2:]
	if filename == "" {
		return "", "", false
	}
	return filename, digest, true
}

// isLowerHex reports whether s is entirely lowercase hex. hex.DecodeString
// accepts both cases, so lowercase is enforced explicitly (GNU sha256sum
// emits lowercase; accepting uppercase could mask a real mismatch).
func isLowerHex(s string) bool {
	if _, err := hex.DecodeString(s); err != nil {
		return false
	}
	// hex.DecodeString accepts both cases — enforce lowercase explicitly.
	return s == strings.ToLower(s)
}

// FindChecksum parses SHA256SUMS data and returns the digest for assetName.
// It returns ErrChecksumNotFound when the asset has no entry, and wraps
// ErrMalformedChecksumLine for unparseable input.
func FindChecksum(sumsData []byte, assetName string) (string, error) {
	checksums, err := ParseChecksums(sumsData)
	if err != nil {
		return "", err
	}
	digest, ok := checksums[assetName]
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrChecksumNotFound, assetName)
	}
	return digest, nil
}
