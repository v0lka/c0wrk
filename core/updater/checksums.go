// Package updater implements the self-update pipeline: downloading release
// archives into a staging directory, verifying their integrity against a
// SHA256SUMS file, and exposing the result for the apply phase.
//
// The downloader and verifier are fail-closed by design (ASI04-R2 —
// supply-chain integrity): a missing checksum entry or a hash mismatch always
// results in the archive being removed and an error returned, never a silent
// acceptance of an unverified artifact.
//
// Integrity is SHA256-only and unsigned. The archive and its SHA256SUMS file
// are fetched from the same release over the same TLS channel, so the checksum
// pins the archive to the released bytes but does NOT prove release authorship:
// a TLS-interception proxy whose CA the app trusts, or a fully compromised
// release, can substitute both archive and checksum. The fail-closed gate
// therefore protects against transport corruption and non-interception
// attackers, not against a trusted-CA MITM. See ADR-023 and SECURITY.md for
// the full threat model and accepted trade-offs.
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
	// match the standard `sha256sum` format. Parsing is all-or-nothing: a
	// single malformed line aborts the whole parse rather than silently
	// dropping entries (a dropped entry could hide the one we need).
	ErrMalformedChecksumLine = errors.New("malformed checksum line")
)

// ParseChecksums parses the content of a SHA256SUMS file into a map of
// asset name → lowercase hex digest. The accepted format is the one produced
// by GNU coreutils `sha256sum`:
//
//	<hex>  <filename>   (text mode — two spaces)
//	<hex> *<filename>   (binary mode — space + asterisk)
//
// Blank lines are skipped. Any other line that cannot be split into a valid
// 64-char hex digest and a non-empty filename is rejected: ParseChecksums
// returns ErrMalformedChecksumLine for the first offending line. This
// fail-closed behaviour prevents a parser quirk from masking a missing entry.
//
// An empty (or whitespace-only) input is valid and yields an empty map with a
// nil error; a subsequent FindChecksum for any asset will then fail with
// ErrChecksumNotFound — which is the desired fail-closed outcome for a missing
// entry.
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

// parseChecksumLine splits a single SHA256SUMS line into (filename, digest).
// The expected layout is: <64 hex><space><indicator><filename> where the
// indicator is a space (text mode) or '*' (binary mode). The filename is taken
// verbatim to the end of the line so that filenames containing spaces survive.
// Returns ok=false if the line is malformed.
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

// isLowerHex reports whether s is composed entirely of lowercase hexadecimal
// digits. GNU sha256sum emits lowercase hex; accepting uppercase would
// introduce ambiguity that could mask a real mismatch, so we stay strict.
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
