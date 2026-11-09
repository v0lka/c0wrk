// SPDX-License-Identifier: Apache-2.0
//
// webaccess_tls_test.go — the DESKTOP surfaces of the web-access TLS story
// (ADR-102): the GetWebAccessInfo certificate classification matrix the
// Settings section renders, and the config-load-warning channel the control
// RPCs keep in step. The SetWebAccessTLS/GenerateWebAccessCertificate
// lifecycle tests moved to the backend package with their code (P-11).

package desktop

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend"
	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/internal/webaccess"
)

// The cert_status vocabulary (ADR-102 decision 5), mirrored from
// backend/config: the canonical constants are unexported there (the
// classification itself is pinned by backend/config/web_access_cert_test.go).
const (
	webAccessCertStatusOK         = "ok"
	webAccessCertStatusMismatch   = "mismatch"
	webAccessCertStatusUnreadable = "unreadable"
	webAccessCertStatusExpired    = "expired"
)

// writeTLSPairFiles writes an arbitrary self-signed PEM pair (the certgen
// helper only produces valid 825-day pairs; the cert_status matrix needs an
// expired one too) and returns the file paths.
func writeTLSPairFiles(t *testing.T, dir, certName, keyName string, notBefore, notAfter time.Time, ips ...string) (certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	ipSANs := make([]net.IP, 0, len(ips))
	for _, s := range ips {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("test SAN %q is not an IP", s)
		}
		ipSANs = append(ipSANs, ip)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "webaccess_tls_test"},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  false,
		DNSNames:              []string{"localhost"},
		IPAddresses:           ipSANs,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	certPath = filepath.Join(dir, certName)
	keyPath = filepath.Join(dir, keyName)
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return certPath, keyPath
}

// writeGeneratedPair generates a valid app-shaped pair through the real
// certgen helper and returns the paths.
func writeGeneratedPair(t *testing.T, dir, certName, keyName string) (certPath, keyPath string) {
	t.Helper()
	certPath = filepath.Join(dir, certName)
	keyPath = filepath.Join(dir, keyName)
	if err := webaccess.GenerateWebAccessCertificate("127.0.0.1", certPath, keyPath); err != nil {
		t.Fatalf("generate pair: %v", err)
	}
	return certPath, keyPath
}

// TestGetWebAccessInfo_CertStatusMatrix pins the classification vocabulary
// (ADR-102 decision 5): empty when unconfigured, ok when the pair covers the
// pinned bind, mismatch after a bind change (the canonical trigger),
// unreadable for an unloadable pair, expired past NotAfter or not yet valid
// before NotBefore (the same clock-derived bucket) — and the
// wildcard-bind rewrite is shared with the URL renderer, so a pair covering
// localhost stays ok under 0.0.0.0.
func TestGetWebAccessInfo_CertStatusMatrix(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeGeneratedPair(t, dir, "std.crt", "std.key")
	cfg, path := writeWebAccessConfig(t, "web_access:\n  enabled: false\n")
	a := newWebAccessTestApp(t, cfg, path)

	status := func() (string, string) {
		t.Helper()
		info := a.GetWebAccessInfo()
		return info.CertStatus, info.CertDetail
	}

	if st, detail := status(); st != "" || detail != "" {
		t.Fatalf("unconfigured status = %q (%q), want empty", st, detail)
	}

	if err := a.SetWebAccessTLS(true, certPath, keyPath); err != nil {
		t.Fatalf("configure pair: %v", err)
	}
	if st, detail := status(); st != webAccessCertStatusOK || detail != "" {
		t.Fatalf("status with the covering pair = %q (%q), want ok with no detail", st, detail)
	}

	// Bind change → mismatch: the cert still covers the loopback set, the
	// pinned bind is now a LAN IP no SAN names.
	if err := a.SetWebAccessConnection("192.0.2.1", 0); err != nil {
		t.Fatalf("change bind: %v", err)
	}
	st, detail := status()
	if st != webAccessCertStatusMismatch {
		t.Fatalf("status after the bind change = %q (%q), want mismatch", st, detail)
	}
	if !strings.Contains(detail, "192.0.2.1") {
		t.Errorf("mismatch detail = %q, want it to name the uncovered host", detail)
	}

	// A wildcard bind rewrites to localhost (the URL renderer's rule), which
	// the loopback SAN set covers — ok again.
	if err := a.SetWebAccessConnection("0.0.0.0", 0); err != nil {
		t.Fatalf("wildcard bind: %v", err)
	}
	if st, detail := status(); st != webAccessCertStatusOK {
		t.Fatalf("status under the wildcard bind = %q (%q), want ok (localhost is covered)", st, detail)
	}

	// Unloadable pair → unreadable, with the load failure named. Seeded
	// straight into the live config: SetWebAccessTLS refuses an unloadable
	// pair before it can persist — the classification still has to name the
	// state for hand-edited configs that carry it.
	ref, _ := a.configRef()
	ref.WebAccess.TLS = config.WebAccessTLSConfig{
		Enabled: true,
		Cert:    filepath.Join(dir, "absent.crt"),
		Key:     filepath.Join(dir, "absent.key"),
	}
	st, detail = status()
	if st != webAccessCertStatusUnreadable || detail == "" {
		t.Fatalf("status with a missing pair = %q (%q), want unreadable with a detail", st, detail)
	}

	// Expired pair → expired, with the expiry date named.
	expCert, expKey := writeTLSPairFiles(t, dir, "old.crt", "old.key",
		time.Now().Add(-49*time.Hour), time.Now().Add(-24*time.Hour), "127.0.0.1")
	if err := a.SetWebAccessTLS(true, expCert, expKey); err != nil {
		t.Fatalf("configure expired pair: %v", err)
	}
	st, detail = status()
	if st != webAccessCertStatusExpired || detail == "" {
		t.Fatalf("status with the expired pair = %q (%q), want expired with a detail", st, detail)
	}

	// Not-yet-valid pair → the same clock-derived expired bucket, with the
	// validity start named (the vocabulary has no separate not-yet-valid
	// status; the detail tells the two cases apart).
	futureCert, futureKey := writeTLSPairFiles(t, dir, "future.crt", "future.key",
		time.Now().Add(24*time.Hour), time.Now().Add(48*time.Hour), "127.0.0.1")
	if err := a.SetWebAccessTLS(true, futureCert, futureKey); err != nil {
		t.Fatalf("configure not-yet-valid pair: %v", err)
	}
	st, detail = status()
	if st != webAccessCertStatusExpired || detail == "" {
		t.Fatalf("status with the not-yet-valid pair = %q (%q), want expired with a detail", st, detail)
	}
	if !strings.Contains(detail, "not valid until") {
		t.Errorf("not-yet-valid detail = %q, want it to name the validity start", detail)
	}
}

// TestWebAccessControlRPCsRefreshConfigLoadWarnings pins the ADR-102 wire
// end-to-end: the transport-affecting control RPCs keep the UI's
// config-load-warning channel (GetConfig().config_errors → the Settings
// "Configuration warning" banner) in step with the live section — an
// HTTP→HTTPS flip drops the plain-HTTP exposure warning, the reverse flip
// and a re-bind onto a non-loopback address bring it back, a loopback
// re-bind and the disable drop it, and an unrelated section's load warning
// survives every transition untouched. The TLS/re-bind/disable transitions
// are persist-only (no listener is running, so no restart path has anything
// to rebuild), but the re-enable and the gate reopen DO mount the sidecar
// for real — so the bind must be an address every test host can listen on.
// The unspecified wildcard "0.0.0.0" is that address: non-loopback (the
// exposure warning still applies, IsLoopbackBind classifies it) yet bindable
// even on CI runners with no LAN interface, where a specific private literal
// such as 192.168.23.2 fails with "cannot assign requested address".
func TestWebAccessControlRPCsRefreshConfigLoadWarnings(t *testing.T) {
	const nonLoopbackBind = "0.0.0.0"
	dir := t.TempDir()
	certPath := filepath.Join(dir, "pair.crt")
	keyPath := filepath.Join(dir, "pair.key")
	if err := webaccess.GenerateWebAccessCertificate(nonLoopbackBind, certPath, keyPath); err != nil {
		t.Fatalf("generate the app-owned pair: %v", err)
	}
	cfg, path := writeWebAccessConfig(t, fmt.Sprintf(`
web_access:
  enabled: true
  bind: %s
  allow_insecure_lan: true
  tls:
    enabled: false
    cert: %q
    key: %q
`, nonLoopbackBind, certPath, keyPath))

	// The opt-in rides the exposure seed too: the accepted-risk warning is
	// what the flag-on state draws (without it the gate disables the section
	// and the warning channel never sees exposure at all).
	exposure := config.WebAccessLoadWarnings(config.WebAccessConfig{Enabled: true, Bind: nonLoopbackBind, AllowInsecureLAN: true}, true)[0]
	unrelated := "autonomy: legacy smart_approve folded into silent_mode"

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	// A standalone (nil-app) FrontendAPI so the channel is observable through
	// GetConfig; the swap happens before any goroutine, so the published-once
	// seed invariant is not at play.
	fapi := backend.NewFrontendAPI(backend.FrontendAPIConfig{Config: cfg, ConfigPath: path, Logger: quiet})
	a := NewApp()
	a.FrontendAPI = fapi
	a.logger = quiet
	a.setConfigRef(cfg, path)
	t.Cleanup(func() { a.stopWebAccess(context.Background()) })

	// A plain-HTTP launch hands the exposure warning to the channel.
	fapi.Lifecycle().SetConfigLoadState([]string{exposure, unrelated})
	assertChannel := func(phase string, want ...string) {
		t.Helper()
		if got := fapi.GetConfig().ConfigErrors; !reflect.DeepEqual(got, want) {
			t.Errorf("%s: ConfigErrors = %v, want %v", phase, got, want)
		}
	}
	assertChannel("seeded", exposure, unrelated)

	if err := a.SetWebAccessTLS(true, certPath, keyPath); err != nil {
		t.Fatalf("SetWebAccessTLS(on): %v", err)
	}
	assertChannel("after TLS on", unrelated)

	if err := a.SetWebAccessTLS(false, certPath, keyPath); err != nil {
		t.Fatalf("SetWebAccessTLS(off): %v", err)
	}
	assertChannel("after TLS off", unrelated, exposure)

	if err := a.SetWebAccessConnection("127.0.0.1", 0); err != nil {
		t.Fatalf("SetWebAccessConnection(loopback): %v", err)
	}
	assertChannel("after re-bind to loopback", unrelated)

	if err := a.SetWebAccessConnection(nonLoopbackBind, 0); err != nil {
		t.Fatalf("SetWebAccessConnection(non-loopback): %v", err)
	}
	assertChannel("after re-bind to non-loopback", unrelated, exposure)

	if err := a.SetWebAccessEnabled(false); err != nil {
		t.Fatalf("SetWebAccessEnabled(false): %v", err)
	}
	assertChannel("after disable", unrelated)

	// The ADR-101 gate-flip case (CS #65): the flip recomputes the section's
	// slice INLINE under configMu (UpdateExperimentalFeatures holds configMu;
	// the lifecycle refresh would self-deadlock). A closed gate suppresses
	// the exposure warning — the compound gate keeps the sidecar unmounted,
	// so a present-tense listener warning would overstate the exposure — and
	// reopening the gate restores it, without any section RPC in between.
	// The section is re-enabled first so the flip has exposure to move.
	if err := a.SetWebAccessEnabled(true); err != nil {
		t.Fatalf("SetWebAccessEnabled(true): %v", err)
	}
	assertChannel("after re-enable (gate open)", unrelated, exposure)

	if err := fapi.UpdateExperimentalFeatures(false); err != nil {
		t.Fatalf("UpdateExperimentalFeatures(false): %v", err)
	}
	assertChannel("after gate close", unrelated)

	if err := fapi.UpdateExperimentalFeatures(true); err != nil {
		t.Fatalf("UpdateExperimentalFeatures(true): %v", err)
	}
	assertChannel("after gate reopen", unrelated, exposure)
}
