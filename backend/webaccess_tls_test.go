// SPDX-License-Identifier: Apache-2.0
//
// webaccess_tls_test.go — the TLS surface of the web-access control
// operations (ADR-102), driven directly through the transport-neutral
// WebAccessService (the tests moved here from the desktop package with their
// code, P-11): SetTLS toggles the web_access.tls subsection with the
// section's persist + hot-restart + rollback discipline (an unloadable pair
// fails closed and never leaves the sidecar down), GenerateCertificate
// writes the fixed app-owned pair for the pinned bind into an isolated agent
// dir and persists its paths.

package backend

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

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
// helper only produces valid 825-day pairs; the expired-bucket test needs one
// too) and returns the file paths.
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

// tlsRootPool builds a RootCAs pool from a PEM certificate file.
func tlsRootPool(t *testing.T, certPath string) *x509.CertPool {
	t.Helper()
	pemBytes, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("read cert %s: %v", certPath, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		t.Fatalf("no certificates parsed from %s", certPath)
	}
	return pool
}

// TestSetWebAccessEnabled_BrokenTLSPairRefused: ADR-102's interactive gate
// on the enable flip — turning the section on while its TLS subsection
// points at an unloadable pair is refused before anything is mutated or
// persisted, so the toggle never produces a persisted-on switch above a
// silently-down sidecar and never downgrades the requested TLS to plain
// HTTP.
func TestSetWebAccessEnabled_BrokenTLSPairRefused(t *testing.T) {
	dir := t.TempDir()
	absentCert := filepath.Join(dir, "absent.crt")
	absentKey := filepath.Join(dir, "absent.key")
	doc := fmt.Sprintf("web_access:\n  enabled: false\n  tls:\n    enabled: true\n    cert: %q\n    key: %q\n", absentCert, absentKey)
	cfg, path := writeWebAccessConfig(t, doc)
	s := newTestWebAccessService(t, cfg, path)

	err := s.SetEnabled(true)
	if err == nil {
		t.Fatal("SetEnabled onto an unloadable pair: expected an error")
	}
	if !strings.Contains(err.Error(), "certificate pair failed to load") {
		t.Errorf("error must name the load failure, got %q", err.Error())
	}
	if cfg.WebAccess.Enabled {
		t.Errorf("in-memory section must stay disabled, got %+v", cfg.WebAccess)
	}
	if !cfg.WebAccess.TLS.Enabled || cfg.WebAccess.TLS.Cert != absentCert || cfg.WebAccess.TLS.Key != absentKey {
		t.Errorf("in-memory subsection must be preserved as authored, got %+v", cfg.WebAccess.TLS)
	}
	if srv := s.server.Load(); srv != nil {
		t.Errorf("the sidecar must stay down, got %+v", srv)
	}
	reloaded, err := config.LoadWithResult(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(reloaded.LoadErrors) != 0 {
		t.Errorf("the persisted config must be untouched (a disabled section is inert), got %v", reloaded.LoadErrors)
	}
	if reloaded.Config.WebAccess.Enabled || !reloaded.Config.WebAccess.TLS.Enabled ||
		reloaded.Config.WebAccess.TLS.Cert != absentCert || reloaded.Config.WebAccess.TLS.Key != absentKey {
		t.Errorf("disk section must equal the authored state, got %+v", reloaded.Config.WebAccess)
	}
}

// TestSetWebAccessEnabled_UsablePairEnables: the preflight must not
// over-block — a flip onto a REAL usable pair persists and mounts the
// sidecar with TLS on.
func TestSetWebAccessEnabled_UsablePairEnables(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeTLSPairFiles(t, dir, "real.crt", "real.key", time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour), "127.0.0.1")
	doc := fmt.Sprintf("web_access:\n  enabled: false\n  tls:\n    enabled: true\n    cert: %q\n    key: %q\n", certPath, keyPath)
	cfg, path := writeWebAccessConfig(t, doc)
	s := newTestWebAccessService(t, cfg, path)

	if err := s.SetEnabled(true); err != nil {
		t.Fatalf("SetEnabled onto a usable pair: %v", err)
	}
	srv := s.server.Load()
	if srv == nil {
		t.Fatal("the sidecar must be up after the flip")
	}
	if !srv.TLSEnabled() {
		t.Error("the sidecar must serve TLS")
	}
	if !cfg.WebAccess.Enabled {
		t.Errorf("the in-memory section must be enabled, got %+v", cfg.WebAccess)
	}
}

// dialWebAccessTLSLeaf performs a hostname-verified TLS handshake against the
// sidecar's resolved address and returns the served leaf certificate. The
// connection is closed before returning — an open conn would stall the next
// hot restart on the server's graceful-drain budget.
func dialWebAccessTLSLeaf(t *testing.T, addr string, roots *x509.CertPool) *x509.Certificate {
	t.Helper()
	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: 2 * time.Second},
		Config: &tls.Config{
			RootCAs:    roots,
			ServerName: "127.0.0.1",
			MinVersion: tls.VersionTLS12,
		},
	}
	conn, err := dialer.DialContext(t.Context(), "tcp", addr)
	if err != nil {
		t.Fatalf("TLS dial %s: %v", addr, err)
	}
	defer func() { _ = conn.Close() }()
	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		t.Fatalf("dial returned %T, want *tls.Conn", conn)
	}
	certs := tlsConn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		t.Fatal("TLS handshake returned no peer certificates")
	}
	return certs[0]
}

// plainHTTPReachable reports whether a plain HTTP request against addr
// gets an HTTP response (any status — the point is that plaintext is what
// answers).
func plainHTTPReachable(t *testing.T, addr string) bool {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+addr, http.NoBody)
	if err != nil {
		return false
	}
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		return false
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return true
}

// mustSidecarAddr returns the running sidecar's resolved address.
func mustSidecarAddr(t *testing.T, s *WebAccessService) string {
	t.Helper()
	srv := s.server.Load()
	if srv == nil {
		t.Fatal("sidecar is not running")
	}
	addr, ok := srv.Addr()
	if !ok {
		t.Fatal("running sidecar has no resolved address")
	}
	return addr
}

// TestSetWebAccessTLS_TogglePersistsAndHotRestarts: enabling TLS hot-restarts
// the running sidecar onto HTTPS with the authored pair persisted; the
// browser URL renders https; disabling returns the sidecar to plain HTTP and
// wipes the subsection the request carried.
func TestSetWebAccessTLS_TogglePersistsAndHotRestarts(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeGeneratedPair(t, dir, "custom.pem", "custom.key")
	cfg, path := writeWebAccessConfig(t, "web_access:\n  enabled: true\n")
	s := newTestWebAccessService(t, cfg, path)
	mountErr := s.MountWebAccess(s.log())
	first := s.server.Load()
	if first == nil {
		t.Fatalf("sidecar did not mount: %v", mountErr)
	}

	// ── Enable: the explicit pair, live HTTPS restart ──
	if err := s.SetTLS(true, certPath, keyPath); err != nil {
		t.Fatalf("SetTLS(true): %v", err)
	}
	sec := s.server.Load()
	if sec == nil || sec == first {
		t.Fatal("enabling TLS did not rebuild the sidecar")
	}
	if !sec.TLSEnabled() {
		t.Fatal("sidecar does not terminate TLS after the toggle")
	}
	addr, ok := sec.Addr()
	if !ok {
		t.Fatal("restarted sidecar has no resolved address")
	}
	dialWebAccessTLSLeaf(t, addr, tlsRootPool(t, certPath))
	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if !reloaded.WebAccess.TLS.Enabled ||
		reloaded.WebAccess.TLS.Cert != certPath ||
		reloaded.WebAccess.TLS.Key != keyPath {
		t.Errorf("disk TLS subsection = %+v, want enabled with the authored pair (%q, %q)",
			reloaded.WebAccess.TLS, certPath, keyPath)
	}
	info := s.Snapshot()
	if !info.TLSEnabled || info.TLSCert != certPath || info.TLSKey != keyPath {
		t.Errorf("info TLS fields = (%v, %q, %q), want (true, %q, %q)",
			info.TLSEnabled, info.TLSCert, info.TLSKey, certPath, keyPath)
	}
	if !strings.HasPrefix(info.URL, "https://") {
		t.Errorf("info.URL = %q, want the https scheme while TLS serves", info.URL)
	}
	if info.CertStatus != webAccessCertStatusOK {
		t.Errorf("info.CertStatus = %q (%q), want ok for the generated pair", info.CertStatus, info.CertDetail)
	}

	// ── Disable: back to plain HTTP, byte-identical pre-ADR behavior ──
	if err := s.SetTLS(false, "", ""); err != nil {
		t.Fatalf("SetTLS(false): %v", err)
	}
	third := s.server.Load()
	if third == nil || third == sec {
		t.Fatal("disabling TLS did not rebuild the sidecar")
	}
	if third.TLSEnabled() {
		t.Fatal("sidecar still terminates TLS after the toggle off")
	}
	addr3, ok := third.Addr()
	if !ok {
		t.Fatal("restarted sidecar has no resolved address")
	}
	if !plainHTTPReachable(t, addr3) {
		t.Error("plain HTTP does not answer after the toggle off")
	}
	// A verified TLS dial (RootCAs + ServerName) cannot succeed against a
	// plaintext listener — no cert is ever served to verify.
	plainRefuser := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: 2 * time.Second},
		Config: &tls.Config{
			RootCAs:    tlsRootPool(t, certPath),
			ServerName: "127.0.0.1",
			MinVersion: tls.VersionTLS12,
		},
	}
	if _, err := plainRefuser.DialContext(t.Context(), "tcp", addr3); err == nil {
		t.Error("a TLS handshake succeeded against the plain-HTTP sidecar")
	}
	reloaded, err = config.Load(path)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if reloaded.WebAccess.TLS != (config.WebAccessTLSConfig{}) {
		t.Errorf("disk TLS subsection after disable = %+v, want the zero subsection (the request carried no paths)", reloaded.WebAccess.TLS)
	}
	info = s.Snapshot()
	if strings.HasPrefix(info.URL, "https://") {
		t.Errorf("info.URL = %q, want the http scheme on plain HTTP", info.URL)
	}
	if info.TLSEnabled || info.TLSCert != "" || info.CertStatus != "" {
		t.Errorf("info after disable = (tls %v, cert %q, status %q), want all empty",
			info.TLSEnabled, info.TLSCert, info.CertStatus)
	}
}

// TestSetWebAccessTLS_UnreadablePairRejected: a commit onto a pair that
// cannot be loaded is refused BEFORE anything persists — the config on disk
// is untouched and a running sidecar is never rebuilt (the pair-load
// validation runs ahead of the persist and the restart, so the old
// rollback-on-restart path is not even reached). The mount-time fail-closed
// check stays as the backstop for hand-edited configs (ADR-102 decision 2).
func TestSetWebAccessTLS_UnreadablePairRejected(t *testing.T) {
	dir := t.TempDir()
	garbageCert := filepath.Join(dir, "garbage.crt")
	garbageKey := filepath.Join(dir, "garbage.key")
	for _, p := range []string{garbageCert, garbageKey} {
		if err := os.WriteFile(p, []byte("this is not a PEM file"), 0o600); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	cfg, path := writeWebAccessConfig(t, "web_access:\n  enabled: true\n  token: keep-me\n")
	s := newTestWebAccessService(t, cfg, path)
	mountErr := s.MountWebAccess(s.log())
	before := s.server.Load()
	if before == nil {
		t.Fatalf("sidecar did not mount: %v", mountErr)
	}

	for name, cert := range map[string]string{"garbage": garbageCert, "missing": filepath.Join(dir, "absent.crt")} {
		if err := s.SetTLS(true, cert, filepath.Join(dir, "absent.key")); err == nil {
			t.Fatalf("SetTLS with a %s pair: expected an error", name)
		} else if !strings.Contains(err.Error(), "unreadable") {
			t.Errorf("SetTLS with a %s pair: error = %v, want the unreadable-pair message", name, err)
		}
		// The refusal happens before any restart: the same sidecar instance
		// keeps serving plain HTTP on the state it started with.
		if got := s.server.Load(); got != before {
			t.Errorf("%s pair: the sidecar was rebuilt on a refused commit", name)
		}
		addr, ok := before.Addr()
		if !ok || !plainHTTPReachable(t, addr) {
			t.Errorf("%s pair: sidecar is not serving plain HTTP", name)
		}
		reloaded, err := config.Load(path)
		if err != nil {
			t.Fatalf("reload config: %v", err)
		}
		if reloaded.WebAccess.TLS != (config.WebAccessTLSConfig{}) {
			t.Errorf("%s pair: disk TLS subsection after the refusal = %+v, want the zero subsection", name, reloaded.WebAccess.TLS)
		}
	}
}

// TestSetWebAccessTLS_MismatchPairRejected: a commit whose key does not
// match its certificate is refused with the mismatch message before
// anything persists — the config on disk keeps the previous pair and the
// TLS-serving sidecar keeps serving its certificate.
func TestSetWebAccessTLS_MismatchPairRejected(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeGeneratedPair(t, dir, "std.crt", "std.key")
	_, otherKeyPath := writeGeneratedPair(t, dir, "other.crt", "other.key")
	cfg, path := writeWebAccessConfig(t, "web_access:\n  enabled: true\n  token: keep-me\n")
	s := newTestWebAccessService(t, cfg, path)
	mountErr := s.MountWebAccess(s.log())
	if s.server.Load() == nil {
		t.Fatalf("sidecar did not mount: %v", mountErr)
	}
	if err := s.SetTLS(true, certPath, keyPath); err != nil {
		t.Fatalf("configure the standard pair: %v", err)
	}
	serving := s.server.Load()
	if serving == nil || !serving.TLSEnabled() {
		t.Fatal("sidecar is not serving TLS on the standard pair")
	}
	served := dialWebAccessTLSLeaf(t, mustSidecarAddr(t, s), tlsRootPool(t, certPath))

	// Valid cert + a foreign (valid, but different) key: the pair loads as
	// files but the key does not match the certificate — refused as a
	// mismatch, before the no-op check or any persist.
	if err := s.SetTLS(true, certPath, otherKeyPath); err == nil {
		t.Fatal("committing a mismatched pair: expected an error")
	} else if !strings.Contains(err.Error(), "does not match") {
		t.Errorf("error = %v, want the key-does-not-match-certificate message", err)
	}
	if got := s.server.Load(); got != serving {
		t.Error("the sidecar was rebuilt on a refused commit")
	}
	if again := dialWebAccessTLSLeaf(t, mustSidecarAddr(t, s), tlsRootPool(t, certPath)); again.SerialNumber.Cmp(served.SerialNumber) != 0 {
		t.Error("the sidecar is no longer serving the previous certificate")
	}
	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if !reloaded.WebAccess.TLS.Enabled || reloaded.WebAccess.TLS.Cert != certPath || reloaded.WebAccess.TLS.Key != keyPath {
		t.Errorf("disk TLS subsection after the refusal = %+v, want the previous pair untouched", reloaded.WebAccess.TLS)
	}
}

// TestSetWebAccessTLS_ValidationNoopAndGuards: the structural rule runs
// through config.ValidateWebAccessTLS (enabled ⇒ non-empty cert AND key —
// the key path is never derived from the cert path), a non-empty pair is
// loaded (tls.LoadX509KeyPair) ahead of the structural rule and the no-op
// check, a matching readable request is a no-op that does not rewrite the
// file, and the operation shares the other control operations' config-ref
// guard.
func TestSetWebAccessTLS_ValidationNoopAndGuards(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeGeneratedPair(t, dir, "pair.crt", "pair.key")
	cfg, path := writeWebAccessConfig(t, "web_access:\n  enabled: false\n")
	s := newTestWebAccessService(t, cfg, path)

	// Enabled with no cert: the shared structural message, nothing persisted.
	if err := s.SetTLS(true, "", ""); err == nil {
		t.Fatal("expected the structural error for an enabled subsection without a cert")
	} else if !strings.Contains(err.Error(), "tls.cert is required") {
		t.Errorf("error = %v, want the shared tls.cert message", err)
	}
	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if reloaded.WebAccess.TLS.Enabled {
		t.Error("a refused request changed the persisted subsection")
	}

	// Enabled with a cert but no key: the shared structural key message —
	// no derivation happens at the operation, both paths must be authored.
	if err := s.SetTLS(true, certPath, ""); err == nil {
		t.Fatal("expected the structural error for an enabled subsection without a key")
	} else if !strings.Contains(err.Error(), "tls.key is required") {
		t.Errorf("error = %v, want the shared tls.key message", err)
	}
	reloaded, err = config.Load(path)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if reloaded.WebAccess.TLS.Enabled {
		t.Error("a refused request changed the persisted subsection")
	}

	// The subsection persists while the sidecar section is disabled (the pin
	// semantics of the connection fields): applies at the next gated start.
	if err := s.SetTLS(true, certPath, keyPath); err != nil {
		t.Fatalf("SetTLS while disabled: %v", err)
	}
	reloaded, err = config.Load(path)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if !reloaded.WebAccess.TLS.Enabled || reloaded.WebAccess.TLS.Cert != certPath || reloaded.WebAccess.TLS.Key != keyPath {
		t.Errorf("disk TLS subsection = %+v, want enabled with the pair", reloaded.WebAccess.TLS)
	}
	if s.server.Load() != nil {
		t.Error("the call started the sidecar while web access is disabled")
	}

	// A matching request is a no-op: the config file is not rewritten.
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if err := s.SetTLS(true, certPath, keyPath); err != nil {
		t.Fatalf("matching SetTLS: %v", err)
	}
	stat2, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat config after no-op: %v", err)
	}
	if !stat.ModTime().Equal(stat2.ModTime()) {
		t.Error("a matching request rewrote the config file")
	}

	// The pair-load validation runs ahead of the no-op check: once the
	// stored pair's key file disappears, re-committing the identical
	// subsection is refused (unreadable) instead of silently no-oping —
	// and the file is still not rewritten.
	if err := os.Remove(keyPath); err != nil {
		t.Fatalf("remove key: %v", err)
	}
	if err := s.SetTLS(true, certPath, keyPath); err == nil {
		t.Fatal("re-committing the stored subsection with a deleted key: expected an error")
	} else if !strings.Contains(err.Error(), "unreadable") {
		t.Errorf("error = %v, want the unreadable-pair message", err)
	}
	stat3, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat config after the refused no-op: %v", err)
	}
	if !stat.ModTime().Equal(stat3.ModTime()) {
		t.Error("a refused no-op rewrote the config file")
	}

	// The config-ref guard mirrors SetEnabled.
	bare := &WebAccessService{}
	if err := bare.SetTLS(true, certPath, keyPath); err == nil {
		t.Fatal("expected an error without a config ref, got nil")
	} else if !strings.Contains(err.Error(), "config not initialized") {
		t.Errorf("error = %v, want the config-not-initialized message", err)
	}
	if err := bare.GenerateCertificate(); err == nil {
		t.Fatal("expected an error without a config ref, got nil")
	} else if !strings.Contains(err.Error(), "config not initialized") {
		t.Errorf("error = %v, want the config-not-initialized message", err)
	}
}

// TestGenerateWebAccessCertificate_IsolatedHome: generation writes the fixed
// app-owned pair under the agent dir (an isolated HOME — never the real
// one), persists the pair's paths without ever flipping tls.enabled, and the
// pair classifies as ok for the pinned bind.
func TestGenerateWebAccessCertificate_IsolatedHome(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	t.Setenv("USERPROFILE", fakeHome) // os.UserHomeDir on Windows
	agentDir := config.AgentDir()
	if agentDir != filepath.Join(fakeHome, config.DefaultAgentDir) {
		t.Fatalf("AgentDir = %q, want under the isolated HOME %q", agentDir, fakeHome)
	}
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatalf("mkdir agent dir: %v", err)
	}

	cfg, path := writeWebAccessConfig(t, "web_access:\n  enabled: false\n")
	s := newTestWebAccessService(t, cfg, path)

	if err := s.GenerateCertificate(); err != nil {
		t.Fatalf("GenerateCertificate: %v", err)
	}
	certPath := config.WebAccessCertPath(agentDir)
	keyPath := config.WebAccessKeyPath(agentDir)
	for _, p := range []string{certPath, keyPath} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected the generated pair at %s: %v", p, err)
		}
	}
	if runtime.GOOS != "windows" {
		keyStat, err := os.Stat(keyPath)
		if err != nil {
			t.Fatalf("stat key: %v", err)
		}
		if keyStat.Mode().Perm() != 0o600 {
			t.Errorf("key mode = %v, want 0600", keyStat.Mode().Perm())
		}
		certStat, err := os.Stat(certPath)
		if err != nil {
			t.Fatalf("stat cert: %v", err)
		}
		if certStat.Mode().Perm() != 0o644 {
			t.Errorf("cert mode = %v, want 0644", certStat.Mode().Perm())
		}
	}

	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if reloaded.WebAccess.TLS.Enabled {
		t.Error("generation flipped tls.enabled on (it must never, ADR-102 decision 3)")
	}
	if reloaded.WebAccess.TLS.Cert != certPath || reloaded.WebAccess.TLS.Key != keyPath {
		t.Errorf("disk TLS paths = (%q, %q), want the standard pair (%q, %q)",
			reloaded.WebAccess.TLS.Cert, reloaded.WebAccess.TLS.Key, certPath, keyPath)
	}
	info := s.Snapshot()
	if info.CertStatus != webAccessCertStatusOK || info.CertDetail != "" {
		t.Errorf("info.CertStatus = %q (%q), want ok for the freshly generated pair on the default bind", info.CertStatus, info.CertDetail)
	}
	if info.TLSEnabled {
		t.Error("info reports TLS enabled although generation never flips the switch")
	}
}

// TestGenerateWebAccessCertificate_RestartsLiveSidecarOntoFreshPair: with TLS
// already serving the standard pair, regeneration replaces the key material
// on disk and hot-restarts the sidecar so the NEW certificate is what a
// browser is served — even though the persisted paths did not change.
func TestGenerateWebAccessCertificate_RestartsLiveSidecarOntoFreshPair(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	t.Setenv("USERPROFILE", fakeHome)
	agentDir := config.AgentDir()
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatalf("mkdir agent dir: %v", err)
	}

	cfg, path := writeWebAccessConfig(t, "web_access:\n  enabled: true\n")
	s := newTestWebAccessService(t, cfg, path)
	mountErr := s.MountWebAccess(s.log())
	if s.server.Load() == nil {
		t.Fatalf("sidecar did not mount: %v", mountErr)
	}

	// First generation persists the standard paths; the toggle turns TLS on
	// with them.
	if err := s.GenerateCertificate(); err != nil {
		t.Fatalf("first GenerateCertificate: %v", err)
	}
	certPath := config.WebAccessCertPath(agentDir)
	keyPath := config.WebAccessKeyPath(agentDir)
	if err := s.SetTLS(true, certPath, keyPath); err != nil {
		t.Fatalf("SetTLS(true): %v", err)
	}
	before := s.server.Load()
	if before == nil || !before.TLSEnabled() {
		t.Fatal("sidecar is not serving TLS after the toggle")
	}
	firstLeaf := dialWebAccessTLSLeaf(t, mustSidecarAddr(t, s), tlsRootPool(t, certPath))

	// Regeneration: fresh key material, same persisted paths, live restart.
	if err := s.GenerateCertificate(); err != nil {
		t.Fatalf("second GenerateCertificate: %v", err)
	}
	after := s.server.Load()
	if after == nil || after == before {
		t.Fatal("regeneration did not rebuild the live sidecar")
	}
	if !after.TLSEnabled() {
		t.Fatal("sidecar lost TLS after regeneration")
	}
	secondLeaf := dialWebAccessTLSLeaf(t, mustSidecarAddr(t, s), tlsRootPool(t, certPath))
	if firstLeaf.SerialNumber.Cmp(secondLeaf.SerialNumber) == 0 {
		t.Error("the sidecar still serves the previous certificate (same serial) after regeneration")
	}
	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if !reloaded.WebAccess.TLS.Enabled {
		t.Error("regeneration flipped tls.enabled off")
	}
}
