// SPDX-License-Identifier: Apache-2.0
//
// certgen.go — self-signed certificate generation for the web-access
// sidecar (ADR-102): the Generate control writes the fixed app-owned PEM
// pair, a leaf scoped to the currently pinned bind. Deliberately
// STDLIB-ONLY, like the whole sidecar core (nodeps_test.go). Key
// properties — ECDSA P-256 leaf, ServerAuth, 825-day validity, SANs
// covering the pinned bind, atomic staged publication (key first, exact
// 0600/0644), the mismatch/refusal/regeneration flow:
// specs/domains/web-access.md, specs/decisions/102-web-access-tls.md
// (decisions 2, 3, 5).

package webaccess

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// webAccessCertValidity is the generated leaf's validity window (ADR-102
// decision 3): 825 days.
const webAccessCertValidity = 825 * 24 * time.Hour

// GenerateWebAccessCertificate generates the app-owned self-signed pair for
// bind and publishes it atomically: certPath 0644, keyPath 0600. bind must
// be a valid sidecar bind — the SANs derive from it (certSANs), so the
// browser-facing host of the CURRENT bind is always covered; regeneration
// after a bind change is the documented mismatch fix (ADR-102 decision 5).
//
// A pure overwrite of the pair it owns: no reads of pre-existing files, no
// parent-directory creation, no flip of web_access.tls.enabled; persisting
// the paths and the hot-restart are the desktop layer's side (ADR-102
// decision 3).
func GenerateWebAccessCertificate(bind, certPath, keyPath string) error {
	if certPath == "" || keyPath == "" {
		return errors.New("webaccess: tls: certificate and key paths are required")
	}
	tmpl, err := certTemplate(bind)
	if err != nil {
		return err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("webaccess: tls: generating key: %w", err)
	}
	// Self-signed: the leaf is its own issuer.
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("webaccess: tls: creating certificate: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("webaccess: tls: marshaling key: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	// Stage both files completely, then publish — key first, so a crash
	// mid-publication leaves a NEW key with the OLD certificate: a mismatch
	// the sidecar refuses fail-closed (ADR-102 decision 2), never a listener
	// whose key nobody holds.
	if err := writeFileAtomic(keyPath, keyPEM, 0o600); err != nil {
		return err
	}
	if err := writeFileAtomic(certPath, certPEM, 0o644); err != nil {
		return err
	}
	// Both renames are in: fsync the directory entry too, so a power loss
	// cannot revert them. Best-effort — the pair is already published; a
	// failure here only narrows the crash-durability window.
	if d, derr := os.Open(filepath.Dir(certPath)); derr == nil {
		_ = d.Sync() //nolint:errcheck // best-effort durability
		_ = d.Close()
	}
	return nil
}

// certSANs maps a validated sidecar bind to the certificate's SAN set
// (ADR-102 decision 3, as amended — specs/domains/web-access.md):
// "localhost"/loopback → the loopback set; a wildcard bind (0.0.0.0/::) →
// the loopback set PLUS the machine's non-loopback global unicast
// addresses (bounded, best-effort — interfaceUnicastIPs: the address a LAN
// browser actually opens); a specific non-loopback IP → the loopback set
// PLUS itself. The loopback set is always present so any bind stays
// reachable as localhost from the machine itself.
func certSANs(bind string) (dnsNames []string, ips []net.IP, err error) {
	dnsNames = []string{"localhost"}
	// Cap 3: the loopback pair, plus the bind itself for the specific-IP class.
	ips = make([]net.IP, 0, 3)
	ips = append(ips, net.IPv4(127, 0, 0, 1), net.ParseIP("::1"))
	if bind == "localhost" {
		return dnsNames, ips, nil
	}
	ip := net.ParseIP(bind)
	if ip == nil {
		return nil, nil, fmt.Errorf("webaccess: tls: bind %q is not a valid IP literal or \"localhost\"", bind)
	}
	if ip.IsLoopback() {
		return dnsNames, ips, nil
	}
	if ip.IsUnspecified() {
		// Wildcard bind: LAN-reachable addresses join the SAN set so the
		// primary use case (open https://<LAN-IP>:port from another device)
		// gets a cert covering the address actually opened. Sorted and
		// capped HERE, at the consumption point, so the certificate body is
		// deterministic regardless of enumeration source; beyond-cap
		// addresses still surface as cert_status=mismatch (regeneration the
		// fix).
		lan := webAccessInterfaceIPs()
		sort.Slice(lan, func(i, j int) bool { return lan[i].String() < lan[j].String() })
		if len(lan) > wildcardIPSANCaps {
			lan = lan[:wildcardIPSANCaps]
		}
		ips = append(ips, lan...)
		return dnsNames, ips, nil
	}
	// Specific non-loopback bind: normalize IPv4-mapped forms to their 4-byte
	// representation so the SAN carries the canonical encoding browsers and
	// verifiers compare against.
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	return dnsNames, append(ips, ip), nil
}

// wildcardIPSANCaps bounds the interface-derived IP SAN set for a wildcard
// bind: dozens of SANs are a size/parsing hazard, and a host with more
// non-loopback global unicast addresses than this is far outside the
// sidecar's target topology — leftovers stay covered by the
// mismatch+regeneration flow.
const wildcardIPSANCaps = 8

// webAccessInterfaceIPs is the wildcard-bind address source, behind a
// package-level indirection so tests can stub the enumeration — SAN
// assertions must not depend on the host's live interfaces.
var webAccessInterfaceIPs = interfaceUnicastIPs

// interfaceUnicastIPs enumerates the machine's non-loopback, non-link-local
// global unicast addresses — what a LAN browser can actually open a
// wildcard-bound sidecar under. Best-effort by contract: any error or empty
// table degrades to nil (the loopback-only SAN set), never a failed
// Generate. IPv4-mapped forms are normalized to 4 bytes; the consumer
// (certSANs) owns the deterministic sort and cap.
func interfaceUnicastIPs() []net.IP {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	seen := make(map[string]bool, len(addrs))
	ips := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipnet.IP
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() || !ip.IsGlobalUnicast() {
			continue
		}
		if v4 := ip.To4(); v4 != nil {
			ip = v4
		}
		key := ip.String()
		if seen[key] {
			continue
		}
		seen[key] = true
		ips = append(ips, ip)
	}
	return ips
}

// certTemplate builds the certificate body: SANs from the bind, a fresh
// serial, the fixed 825-day window, and the leaf-only usage set.
func certTemplate(bind string) (*x509.Certificate, error) {
	dnsNames, ips, err := certSANs(bind)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("webaccess: tls: generating serial: %w", err)
	}
	// Backdate one hour for clock skew; the tolerance costs nothing and
	// spares the operator an acausal-certificate browser error.
	notBefore := time.Now().Add(-time.Hour)
	return &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{Organization: []string{"c0wrk"}, CommonName: "c0wrk web access"},
		NotBefore:             notBefore,
		NotAfter:              notBefore.Add(webAccessCertValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  false,
		DNSNames:              dnsNames,
		IPAddresses:           ips,
	}, nil
}

// writeFileAtomic stages data into path's directory (fsync, exact fchmod —
// the published mode survives any ambient umask) and renames it into place.
// On any failure the staging file is removed and the target is untouched.
func writeFileAtomic(path string, data []byte, mode os.FileMode) (err error) {
	dir, base := filepath.Dir(path), filepath.Base(path)
	tmp, err := os.CreateTemp(dir, "."+base+".tmp-")
	if err != nil {
		return fmt.Errorf("webaccess: tls: staging %s: %w", base, err)
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("webaccess: tls: writing %s: %w", base, err)
	}
	if err = tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("webaccess: tls: syncing %s: %w", base, err)
	}
	// Exact published mode: Chmod is not umask-filtered, unlike the staging
	// create; the rename below publishes this entry verbatim.
	if err = tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("webaccess: tls: chmod %s: %w", base, err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("webaccess: tls: closing %s: %w", base, err)
	}
	if err = os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("webaccess: tls: publishing %s: %w", base, err)
	}
	return nil
}
