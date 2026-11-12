// SPDX-License-Identifier: Apache-2.0

package config

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// loadWebAccessYAML loads a config from an in-memory YAML document through
// the real pipeline.
func loadWebAccessYAML(t *testing.T, doc string) (cfg *Config, warnings []string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	result, err := LoadWithResult(path)
	if err != nil {
		t.Fatalf("LoadWithResult: %v", err)
	}
	return result.Config, result.LoadErrors
}

// minimalWebAccessYAML is the smallest document the loader accepts (default_model
// + one provider with an enabled model), plus an optional web_access block.
func minimalWebAccessYAML(webAccess string) string {
	doc := `
llm:
  default_model: anthropic/claude-3-haiku
  anthropic:
    api_key: "test-key"
    models:
      - claude-3-haiku
`
	if webAccess != "" {
		doc += "\n" + webAccess + "\n"
	}
	return doc
}

// writeWebAccessTLSPair generates a real self-signed certificate/key pair
// (ECDSA P-256) into dir and returns the two PEM paths. The load path reads
// and parses the pair (ADR-102), so tests exercising an enabled subsection
// need real, loadable files on disk.
func writeWebAccessTLSPair(t *testing.T, dir string) (certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "web_access_test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return certPath, keyPath
}

func TestWebAccess_AbsentSectionDefaultsOff(t *testing.T) {
	// The acceptance contract: an absent section is the zero value — the
	// sidecar stays off and nothing is seeded except the explicit loopback
	// bind default.
	cfg, warnings := loadWebAccessYAML(t, minimalWebAccessYAML(""))
	if len(warnings) != 0 {
		t.Fatalf("absent section must load without warnings, got %v", warnings)
	}
	if cfg.WebAccess.Enabled {
		t.Error("absent section must default to disabled")
	}
	if got := cfg.WebAccess.Bind; got != DefaultWebAccessBind {
		t.Errorf("bind = %q, want seeded default %q", got, DefaultWebAccessBind)
	}
	if got := cfg.WebAccess.Port; got != 0 {
		t.Errorf("port = %d, want 0 (auto)", got)
	}
	if got := cfg.WebAccess.Token; got != "" {
		t.Errorf("token = %q, want empty (ephemeral per-launch)", got)
	}
}

func TestWebAccess_ValidSectionLoads(t *testing.T) {
	// Every loopback bind class rides the full pipeline warning-free.
	for _, bind := range []string{"127.0.0.1", "::1", "localhost", "127.8.8.8", "::ffff:127.0.0.1"} {
		cfg, warnings := loadWebAccessYAML(t, minimalWebAccessYAML(`
web_access:
  enabled: true
  bind: "`+bind+`"
  port: 8765
  token: s3cret-token
`))
		if len(warnings) != 0 {
			t.Fatalf("bind %q: valid section must load without warnings, got %v", bind, warnings)
		}
		if !cfg.WebAccess.Enabled {
			t.Errorf("bind %q: section must stay enabled", bind)
		}
		if cfg.WebAccess.Bind != bind {
			t.Errorf("bind %q: round-tripped as %q", bind, cfg.WebAccess.Bind)
		}
		if cfg.WebAccess.Port != 8765 {
			t.Errorf("bind %q: port = %d, want 8765", bind, cfg.WebAccess.Port)
		}
		if cfg.WebAccess.Token != "s3cret-token" {
			t.Errorf("bind %q: token = %q", bind, cfg.WebAccess.Token)
		}
	}
	// Port variants over the default bind: 0 = auto, explicit = verbatim. An
	// omitted bind is seeded with the default (flipping just `enabled` on is
	// the documented safe shortcut).
	for name, port := range map[string]int{"auto-port": 0, "explicit": 8765} {
		cfg, warnings := loadWebAccessYAML(t, minimalWebAccessYAML(`
web_access:
  enabled: true
  port: `+strconv.Itoa(port)+`
`))
		if len(warnings) != 0 {
			t.Fatalf("%s: valid section must load without warnings, got %v", name, warnings)
		}
		if !cfg.WebAccess.Enabled {
			t.Errorf("%s: section must stay enabled", name)
		}
		if cfg.WebAccess.Bind != DefaultWebAccessBind {
			t.Errorf("%s: omitted bind = %q, want seeded %q", name, cfg.WebAccess.Bind, DefaultWebAccessBind)
		}
		if cfg.WebAccess.Port != port {
			t.Errorf("%s: port = %d, want %d", name, cfg.WebAccess.Port, port)
		}
		if cfg.WebAccess.Token != "" {
			t.Errorf("%s: token = %q, want empty (ephemeral)", name, cfg.WebAccess.Token)
		}
	}
}

func TestWebAccess_NonLoopbackBindLoadsWithWarning(t *testing.T) {
	// Stage 2 + the insecure-LAN gate: a valid non-loopback bind over plain
	// HTTP is REFUSED BY DEFAULT — exactly one warning naming the fix, the
	// section disabled, values preserved for the fix-and-re-enable flow.
	// With the explicit allow_insecure_lan opt-in the shape loads ENABLED
	// with exactly one accepted-risk warning. The warning fires only while
	// the compound gate can actually produce a listener, so the fixture
	// opens the experimental master switch (CS #65).
	for _, bind := range []string{"0.0.0.0", "::", "192.168.1.10", "10.0.0.5"} {
		t.Run(bind+" refused without the opt-in", func(t *testing.T) {
			cfg, warnings := loadWebAccessYAML(t, minimalWebAccessYAML(`
experimental:
  enabled: true
web_access:
  enabled: true
  bind: "`+bind+`"
  port: 8765
  token: s3cret-token
`))
			if len(warnings) != 1 {
				t.Fatalf("bind %q: expected exactly one load warning, got %v", bind, warnings)
			}
			if !strings.Contains(warnings[0], "web_access") ||
				!strings.Contains(warnings[0], "not loopback") ||
				!strings.Contains(warnings[0], "allow_insecure_lan") {
				t.Errorf("bind %q: warning must name the section and the opt-in, got %q", bind, warnings[0])
			}
			if !strings.Contains(warnings[0], "the sidecar stays disabled") {
				t.Errorf("bind %q: warning must name the disabled outcome, got %q", bind, warnings[0])
			}
			if cfg.WebAccess.Enabled {
				t.Errorf("bind %q: the insecure-LAN section must load DISABLED by default", bind)
			}
			if cfg.WebAccess.Bind != bind || cfg.WebAccess.Port != 8765 || cfg.WebAccess.Token != "s3cret-token" {
				t.Errorf("bind %q: section values must be preserved, got %+v", bind, cfg.WebAccess)
			}
		})
		t.Run(bind+" enabled with the opt-in", func(t *testing.T) {
			cfg, warnings := loadWebAccessYAML(t, minimalWebAccessYAML(`
experimental:
  enabled: true
web_access:
  enabled: true
  bind: "`+bind+`"
  port: 8765
  token: s3cret-token
  allow_insecure_lan: true
`))
			if len(warnings) != 1 {
				t.Fatalf("bind %q: expected exactly one load warning, got %v", bind, warnings)
			}
			if !strings.Contains(warnings[0], "web_access") ||
				!strings.Contains(warnings[0], "not loopback") ||
				!strings.Contains(warnings[0], "network-accessible") {
				t.Errorf("bind %q: warning must name the section and the network exposure, got %q", bind, warnings[0])
			}
			// ADR-102: the load warning exists only for the plain-HTTP
			// combination, so the message names the transport and the
			// accepted-risk opt-in, and must never carry the retired
			// TLS-encrypted variant.
			if !strings.Contains(warnings[0], "plain HTTP without TLS") ||
				!strings.Contains(warnings[0], "accepted via allow_insecure_lan") {
				t.Errorf("bind %q: warning must name the plain-HTTP transport and the opt-in, got %q", bind, warnings[0])
			}
			if strings.Contains(warnings[0], "TLS-encrypted") {
				t.Errorf("bind %q: plain-HTTP warning must not mention TLS encryption, got %q", bind, warnings[0])
			}
			if !cfg.WebAccess.Enabled {
				t.Errorf("bind %q: the opted-in section must stay enabled", bind)
			}
			if cfg.WebAccess.Bind != bind || cfg.WebAccess.Port != 8765 || cfg.WebAccess.Token != "s3cret-token" {
				t.Errorf("bind %q: section values must be preserved, got %+v", bind, cfg.WebAccess)
			}
			if !cfg.WebAccess.AllowInsecureLAN {
				t.Errorf("bind %q: the opt-in must round-trip", bind)
			}
		})
	}
}

func TestWebAccess_InsecureLANGateMatrix(t *testing.T) {
	// The gate predicate (webAccessInsecureLAN / ValidateWebAccessExposure)
	// is the one rule shared by load, save, the interactive boundaries and
	// the mount backstop — pinned here in the pure, so drift between the
	// enforcement points shows up as a unit failure before it shows up as a
	// policy hole.
	refused := []WebAccessConfig{
		{Enabled: true, Bind: "0.0.0.0"},
		{Enabled: true, Bind: "::"},
		{Enabled: true, Bind: "192.168.1.10"},
	}
	for _, sec := range refused {
		err := ValidateWebAccessExposure(sec)
		if err == nil {
			t.Errorf("ValidateWebAccessExposure(%+v) = nil, want the insecure-LAN refusal", sec)
			continue
		}
		if !strings.Contains(err.Error(), "allow_insecure_lan") || !strings.Contains(err.Error(), "tls") {
			t.Errorf("refusal must name both ways out, got %q", err.Error())
		}
	}
	allowed := []WebAccessConfig{
		{Enabled: true, Bind: "127.0.0.1"},                                            // loopback
		{Enabled: true, Bind: "localhost"},                                            // loopback name
		{Enabled: true, Bind: "::1"},                                                  // loopback v6
		{Enabled: true, Bind: "192.168.1.10", TLS: WebAccessTLSConfig{Enabled: true}}, // encrypted
		{Enabled: true, Bind: "0.0.0.0", AllowInsecureLAN: true},                      // explicit opt-in
		{Enabled: false, Bind: "0.0.0.0"},                                             // paper state
		{},                                                                            // zero section
	}
	for _, sec := range allowed {
		if err := ValidateWebAccessExposure(sec); err != nil {
			t.Errorf("ValidateWebAccessExposure(%+v) = %v, want nil", sec, err)
		}
	}

	// Fail-closed at save: the refused shape must never persist through any
	// config write, while the opted-in counterpart saves fine.
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	cfg := minimalValidConfig()
	cfg.WebAccess = WebAccessConfig{Enabled: true, Bind: "0.0.0.0"}
	if err := Save(cfg, path); err == nil {
		t.Error("Save must refuse the insecure-LAN shape")
	} else if !strings.Contains(err.Error(), "allow_insecure_lan") {
		t.Errorf("save refusal must name the opt-in, got %q", err.Error())
	}
	cfg.WebAccess.AllowInsecureLAN = true
	if err := Save(cfg, path); err != nil {
		t.Fatalf("Save must accept the opted-in shape, got %v", err)
	}
}

func TestWebAccess_NonLoopbackBindSuppressedBehindClosedGate(t *testing.T) {
	// CS #65: the accepted-risk exposure warning is present-tense — it must
	// not fire at load time while the experimental master switch guarantees
	// the sidecar cannot mount. The section (with the allow_insecure_lan
	// opt-in in place — without it the load gate disables it) loads enabled
	// and silent; the runtime recompute after a live gate flip still owns
	// the warning.
	cfg, warnings := loadWebAccessYAML(t, minimalWebAccessYAML(`
web_access:
  enabled: true
  bind: "192.168.1.10"
  port: 8765
  token: s3cret-token
  allow_insecure_lan: true
`))
	if len(warnings) != 0 {
		t.Fatalf("closed gate must suppress the exposure warning, got %v", warnings)
	}
	if !cfg.WebAccess.Enabled {
		t.Fatal("the section must stay enabled — suppression is about the warning, not the switch")
	}
	// Parity: the section-level predicate is unchanged — it still classifies
	// the exposure; only the load call site gates on the master switch.
	if got := WebAccessLoadWarnings(cfg.WebAccess, true); len(got) != 1 {
		t.Fatalf("WebAccessLoadWarnings must still classify the exposure, got %v", got)
	}
}

func TestWebAccessTLS_NonLoopbackBindLoadsWarningFree(t *testing.T) {
	// ADR-102 + ADR-102: the config-load exposure warning is reserved for
	// the plain-HTTP combination, and an enabled subsection must carry a
	// pair that actually loads (the config layer reads the files). A
	// TLS-encrypted non-loopback bind over a REAL usable pair is a
	// deliberate, encrypted LAN listener: it loads WARNING-FREE, with the
	// section and the subsection preserved verbatim.
	certPath, keyPath := writeWebAccessTLSPair(t, t.TempDir())
	doc := fmt.Sprintf(`
web_access:
  enabled: true
  bind: "192.168.1.10"
  port: 8765
  token: s3cret-token
  tls:
    enabled: true
    cert: %q
    key: %q
`, certPath, keyPath)
	cfg, warnings := loadWebAccessYAML(t, minimalWebAccessYAML(doc))
	if len(warnings) != 0 {
		t.Fatalf("TLS-encrypted non-loopback bind over a usable pair must load warning-free, got %v", warnings)
	}
	if !cfg.WebAccess.Enabled || cfg.WebAccess.Bind != "192.168.1.10" || cfg.WebAccess.Port != 8765 || cfg.WebAccess.Token != "s3cret-token" {
		t.Errorf("section must be preserved verbatim, got %+v", cfg.WebAccess)
	}
	if !cfg.WebAccess.TLS.Enabled || cfg.WebAccess.TLS.Cert != certPath || cfg.WebAccess.TLS.Key != keyPath {
		t.Errorf("tls subsection must be preserved verbatim, got %+v", cfg.WebAccess.TLS)
	}
}

func TestWebAccessTLS_NonLoopbackBindBrokenTLSStaysDisabled(t *testing.T) {
	// ADR-102: a TLS subsection the sidecar cannot serve — here the
	// structural shape, empty paths — never downgrades the listener to
	// plain HTTP. The whole section turns OFF with exactly one warning
	// naming the subsection, and the non-loopback bind draws NO exposure
	// warning: there is no listener left to expose.
	cfg, warnings := loadWebAccessYAML(t, minimalWebAccessYAML(`
web_access:
  enabled: true
  bind: "192.168.1.10"
  port: 8765
  token: s3cret-token
  tls:
    enabled: true
    cert: ""
    key: ""
`))
	if len(warnings) != 1 {
		t.Fatalf("expected exactly the tls-unusable warning, got %v", warnings)
	}
	if !strings.Contains(warnings[0], "web_access.tls unusable") ||
		!strings.Contains(warnings[0], "the sidecar stays disabled") ||
		!strings.Contains(warnings[0], "never downgraded to plain HTTP") {
		t.Errorf("warning must name the unusable TLS and the no-plain-HTTP rule, got %q", warnings[0])
	}
	if cfg.WebAccess.Enabled {
		t.Errorf("the section must be disabled — a requested TLS is never downgraded to plain HTTP, got %+v", cfg.WebAccess)
	}
	if cfg.WebAccess.Bind != "192.168.1.10" || cfg.WebAccess.Port != 8765 || cfg.WebAccess.Token != "s3cret-token" {
		t.Errorf("connection values must be preserved for the fix-and-re-enable flow, got %+v", cfg.WebAccess)
	}
	if !cfg.WebAccess.TLS.Enabled {
		t.Errorf("the tls subsection itself must be preserved as authored, got %+v", cfg.WebAccess.TLS)
	}
}

func TestWebAccess_InvalidBindFailsSoft(t *testing.T) {
	// Only INVALID binds reset the section: unparseable literals and
	// non-"localhost" hostnames. "0.0.0.0" and LAN IPs are valid since stage
	// 2 (covered by TestWebAccess_NonLoopbackBindLoadsWithWarning). An
	// explicit empty bind is NOT in this table: ApplyDefaults seeds it with
	// the default before normalization, so `enabled: true` with no bind is
	// the valid "default bind" shortcut, never a warning; the in-memory
	// empty-bind reset is pinned by TestWebAccess_EmptyBindFailsSoftInMemory.
	for _, bind := range []string{"garbage", "example.com", "localhost.localdomain", "127.0.0.1:8080"} {
		cfg, warnings := loadWebAccessYAML(t, minimalWebAccessYAML(`
web_access:
  enabled: true
  bind: "`+bind+`"
  port: 8765
`))
		if len(warnings) != 1 {
			t.Fatalf("bind %q: expected exactly one load warning, got %v", bind, warnings)
		}
		if !strings.Contains(warnings[0], "web_access") || !strings.Contains(warnings[0], "ignored") {
			t.Errorf("bind %q: warning must name the section and the reset, got %q", bind, warnings[0])
		}
		// Reset to the safe default state: sidecar off, default bind, auto
		// port, ephemeral token.
		if cfg.WebAccess.Enabled {
			t.Errorf("bind %q: invalid section must be disabled", bind)
		}
		if cfg.WebAccess.Bind != DefaultWebAccessBind {
			t.Errorf("bind %q: reset bind = %q, want %q", bind, cfg.WebAccess.Bind, DefaultWebAccessBind)
		}
		if cfg.WebAccess.Port != 0 || cfg.WebAccess.Token != "" {
			t.Errorf("bind %q: reset section carries port %d / token %q", bind, cfg.WebAccess.Port, cfg.WebAccess.Token)
		}
	}
}

func TestWebAccess_EmptyBindFailsSoftInMemory(t *testing.T) {
	// The load pipeline never sees an empty bind (ApplyDefaults seeds the
	// default first), but an in-memory mutation can produce one — and the
	// fail-soft normalize contract must treat it as INVALID, not as a valid
	// class: one warning and a reset to the safe default state.
	cfg := &Config{WebAccess: WebAccessConfig{Enabled: true, Bind: "", Port: 8765}}
	warnings := normalizeWebAccess(cfg)
	if len(warnings) != 1 {
		t.Fatalf("expected exactly one load warning, got %v", warnings)
	}
	if !strings.Contains(warnings[0], "web_access") || !strings.Contains(warnings[0], "ignored") {
		t.Errorf("warning must name the section and the reset, got %q", warnings[0])
	}
	if cfg.WebAccess != defaultWebAccessState() {
		t.Errorf("empty-bind section must reset to the default state, got %+v", cfg.WebAccess)
	}
}

func TestWebAccess_BadPortFailsSoft(t *testing.T) {
	for _, port := range []string{"-1", "65536", "70000"} {
		cfg, warnings := loadWebAccessYAML(t, minimalWebAccessYAML(`
web_access:
  enabled: true
  bind: "127.0.0.1"
  port: `+port+`
`))
		if len(warnings) != 1 {
			t.Fatalf("port %s: expected exactly one load warning, got %v", port, warnings)
		}
		if !strings.Contains(warnings[0], "port") {
			t.Errorf("port %s: warning must name the port, got %q", port, warnings[0])
		}
		if cfg.WebAccess.Enabled {
			t.Errorf("port %s: invalid section must be disabled", port)
		}
		if cfg.WebAccess.Port != 0 {
			t.Errorf("port %s: reset port = %d, want 0 (auto)", port, cfg.WebAccess.Port)
		}
	}
}

func TestWebAccess_DisabledSectionAcceptsAnything(t *testing.T) {
	// An inert section (enabled false or absent) accepts any values without
	// warning — exactly like an inactive shell_exec override. The values are
	// preserved as authored (nothing is validated, nothing is reset); the
	// bind default is only seeded when the key is empty.
	cfg, warnings := loadWebAccessYAML(t, minimalWebAccessYAML(`
web_access:
  enabled: false
  bind: "0.0.0.0"
  port: 70000
  token: whatever
`))
	if len(warnings) != 0 {
		t.Fatalf("disabled section must not warn, got %v", warnings)
	}
	if cfg.WebAccess.Enabled {
		t.Error("section must stay disabled")
	}
	if cfg.WebAccess.Bind != "0.0.0.0" || cfg.WebAccess.Port != 70000 || cfg.WebAccess.Token != "whatever" {
		t.Errorf("disabled section values must be preserved as authored, got %+v", cfg.WebAccess)
	}
}

func TestWebAccess_SaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	cfg := minimalValidConfig()
	if cfg.WebAccess.Enabled || cfg.WebAccess.Bind != DefaultWebAccessBind {
		t.Fatalf("defaults: unexpected web_access state %+v", cfg.WebAccess)
	}
	if err := Save(cfg, path); err != nil {
		t.Fatalf("Save (defaults): %v", err)
	}
	loaded, err := LoadWithResult(path)
	if err != nil {
		t.Fatalf("LoadWithResult (defaults): %v", err)
	}
	if len(loaded.LoadErrors) != 0 {
		t.Fatalf("defaults round trip must be warning-free, got %v", loaded.LoadErrors)
	}
	if loaded.Config.WebAccess != (WebAccessConfig{Bind: DefaultWebAccessBind}) {
		t.Errorf("defaults round trip = %+v, want {Bind: %q}", loaded.Config.WebAccess, DefaultWebAccessBind)
	}

	// An enabled, valid loopback section round-trips unchanged and
	// warning-free.
	cfg.WebAccess = WebAccessConfig{Enabled: true, Bind: "::1", Port: 8765, Token: "s3cret-token"}
	if err := Save(cfg, path); err != nil {
		t.Fatalf("Save (enabled): %v", err)
	}
	loaded, err = LoadWithResult(path)
	if err != nil {
		t.Fatalf("LoadWithResult (enabled): %v", err)
	}
	if len(loaded.LoadErrors) != 0 {
		t.Fatalf("enabled round trip must be warning-free, got %v", loaded.LoadErrors)
	}
	if loaded.Config.WebAccess != cfg.WebAccess {
		t.Errorf("enabled round trip = %+v, want %+v", loaded.Config.WebAccess, cfg.WebAccess)
	}
}

func TestWebAccess_NonLoopbackBindSaveRoundTrip(t *testing.T) {
	// A valid non-loopback bind over plain HTTP is an operator decision
	// ONLY behind the explicit allow_insecure_lan opt-in: with the flag the
	// section saves without error and re-loads ENABLED with exactly the
	// accepted-risk warning, values intact; without it the save itself is
	// refused (the gate is fail-closed on the write path too). The exposure
	// warning fires only while the compound gate can produce a listener, so
	// the fixture opens the experimental master switch (CS #65).
	for _, bind := range []string{"0.0.0.0", "192.168.1.10"} {
		t.Run(bind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.yaml")
			cfg := minimalValidConfig()
			cfg.Experimental.Enabled = true
			cfg.WebAccess = WebAccessConfig{Enabled: true, Bind: bind, Port: 8765, Token: "s3cret-token"}
			if err := Save(cfg, path); err == nil {
				t.Fatalf("bind %q: Save must refuse the insecure-LAN shape without the opt-in", bind)
			} else if !strings.Contains(err.Error(), "allow_insecure_lan") {
				t.Fatalf("bind %q: save refusal must name the opt-in, got %v", bind, err)
			}
			if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
				t.Fatalf("bind %q: refused save must not create the file", bind)
			}

			cfg.WebAccess.AllowInsecureLAN = true
			if err := Save(cfg, path); err != nil {
				t.Fatalf("bind %q: Save must accept the opted-in non-loopback bind, got %v", bind, err)
			}
			if _, statErr := os.Stat(path); statErr != nil {
				t.Fatalf("bind %q: accepted save must create the file: %v", bind, statErr)
			}
			loaded, err := LoadWithResult(path)
			if err != nil {
				t.Fatalf("bind %q: LoadWithResult: %v", bind, err)
			}
			if len(loaded.LoadErrors) != 1 {
				t.Fatalf("bind %q: reload must carry exactly the loopback warning, got %v", bind, loaded.LoadErrors)
			}
			if !strings.Contains(loaded.LoadErrors[0], "not loopback") ||
				!strings.Contains(loaded.LoadErrors[0], "network-accessible") ||
				!strings.Contains(loaded.LoadErrors[0], "accepted via allow_insecure_lan") {
				t.Errorf("bind %q: reload warning must name the network exposure and the opt-in, got %q", bind, loaded.LoadErrors[0])
			}
			if loaded.Config.WebAccess != cfg.WebAccess {
				t.Errorf("bind %q: round trip = %+v, want %+v", bind, loaded.Config.WebAccess, cfg.WebAccess)
			}
		})
	}
}

func TestWebAccess_SaveRejectsUnsafeState(t *testing.T) {
	// Fail-closed keys on VALIDITY (stage 2): the load path heals an invalid
	// section before it can ever be re-saved, so these states are only
	// reachable through in-memory mutation — and the save must refuse to make
	// config.yaml their source. The empty-bind case is the in-memory
	// counterpart of the (valid) omitted-key load shortcut. A valid
	// non-loopback bind is NOT in this table: it saves (see
	// TestWebAccess_NonLoopbackBindSaveRoundTrip).
	cases := map[string]WebAccessConfig{
		"garbage bind":  {Enabled: true, Bind: "not-an-ip"},
		"hostname bind": {Enabled: true, Bind: "example.com"},
		"empty bind":    {Enabled: true},
		"negative port": {Enabled: true, Bind: "127.0.0.1", Port: -1},
		"port overflow": {Enabled: true, Bind: "127.0.0.1", Port: 65536},
	}
	for name, wa := range cases {
		dir := t.TempDir()
		path := filepath.Join(dir, "config.yaml")
		cfg := minimalValidConfig()
		cfg.WebAccess = wa
		err := Save(cfg, path)
		if err == nil {
			t.Errorf("%s: Save must refuse an invalid enabled section", name)
			continue
		}
		if !strings.Contains(err.Error(), "web_access") {
			t.Errorf("%s: error must name the section, got %q", name, err.Error())
		}
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Errorf("%s: refused save must not create the file", name)
		}
		// The disabled counterpart of the same state saves fine.
		wa.Enabled = false
		cfg.WebAccess = wa
		if err := Save(cfg, path); err != nil {
			t.Errorf("%s: the disabled state must save fine, got %v", name, err)
		}
	}
}

func TestIsLoopbackBind(t *testing.T) {
	loopback := []string{
		"127.0.0.1",
		"127.8.8.8",
		"::1",
		"localhost",
		"::ffff:127.0.0.1", // IPv4-mapped loopback
	}
	for _, bind := range loopback {
		if !IsLoopbackBind(bind) {
			t.Errorf("IsLoopbackBind(%q) = false, want true", bind)
		}
	}
	notLoopback := []string{
		"",
		"0.0.0.0",
		"192.168.1.10",
		"10.0.0.1",
		"example.com",
		"localhost.localdomain",
		"127.0.0.1:8080", // the port is a separate field, not part of bind
		"::ffff:192.168.1.10",
	}
	for _, bind := range notLoopback {
		if IsLoopbackBind(bind) {
			t.Errorf("IsLoopbackBind(%q) = true, want false", bind)
		}
	}
}

func TestIsValidWebAccessBind(t *testing.T) {
	// Validity (stage 2) accepts EVERY IP literal — loopback, unspecified,
	// specific IPv4/IPv6, IPv4-mapped — plus the one "localhost" name.
	valid := []string{
		"127.0.0.1",
		"127.8.8.8",
		"::1",
		"::ffff:127.0.0.1",
		"0.0.0.0",
		"::",
		"192.168.1.10",
		"10.0.0.1",
		"8.8.8.8",
		"fd00::1",
		"::ffff:192.168.1.10",
		"localhost",
	}
	for _, bind := range valid {
		if !IsValidWebAccessBind(bind) {
			t.Errorf("IsValidWebAccessBind(%q) = false, want true", bind)
		}
	}
	invalid := []string{
		"",
		"garbage",
		"example.com",
		"localhost.localdomain",
		"127.0.0.1:8080", // the port is a separate field, not part of bind
		" 127.0.0.1",     // whitespace is not an address
	}
	for _, bind := range invalid {
		if IsValidWebAccessBind(bind) {
			t.Errorf("IsValidWebAccessBind(%q) = true, want false", bind)
		}
	}
}

func TestValidateWebAccessBind(t *testing.T) {
	// The exported mirror of the section rule: valid binds — loopback or not
	// (loopback classification is a warning matter, IsLoopbackBind) — return
	// nil; empty and unparseable values are rejected.
	for _, bind := range []string{"127.0.0.1", "::1", "localhost", "0.0.0.0", "::", "192.168.1.10", "::ffff:10.0.0.1"} {
		if err := ValidateWebAccessBind(bind); err != nil {
			t.Errorf("ValidateWebAccessBind(%q) = %v, want nil", bind, err)
		}
	}
	for _, bind := range []string{"", "garbage", "example.com"} {
		err := ValidateWebAccessBind(bind)
		if err == nil {
			t.Errorf("ValidateWebAccessBind(%q) = nil, want error", bind)
			continue
		}
		if !strings.Contains(err.Error(), "web_access") {
			t.Errorf("ValidateWebAccessBind(%q): error must name the section, got %q", bind, err.Error())
		}
	}
	// The exact message the load/save paths produce for the empty bind.
	if got := ValidateWebAccessBind("").Error(); got != "web_access: bind is required when enabled" {
		t.Errorf("ValidateWebAccessBind(\"\") = %q, want the section-naming required-bind message", got)
	}
}

func TestValidateWebAccessToken(t *testing.T) {
	// The interactive-input rule (desktop's SetWebAccessToken and the
	// Settings field behind it): a non-empty credential within
	// [MinWebAccessTokenLength, MaxWebAccessTokenLength] passes; shorter
	// values — including the empty one, which must never overwrite an
	// existing credential — and oversized values are rejected.
	valid := []string{
		strings.Repeat("a", MinWebAccessTokenLength),
		"tok-abcdef0123456789",
		strings.Repeat("x", MaxWebAccessTokenLength),
		strings.Repeat(" ", MinWebAccessTokenLength), // length, not content
	}
	for _, tok := range valid {
		if err := ValidateWebAccessToken(tok); err != nil {
			t.Errorf("ValidateWebAccessToken(%q…) = %v, want nil", tok[:8], err)
		}
	}
	invalid := []string{
		"",
		"short",
		strings.Repeat("a", MinWebAccessTokenLength-1),
		strings.Repeat("x", MaxWebAccessTokenLength+1),
	}
	for _, tok := range invalid {
		err := ValidateWebAccessToken(tok)
		if err == nil {
			t.Errorf("ValidateAccessToken(len=%d) = nil, want error", len(tok))
			continue
		}
		if !strings.Contains(err.Error(), "web_access") {
			t.Errorf("ValidateWebAccessToken(len=%d): error must name the section, got %q", len(tok), err.Error())
		}
	}
	// The exact message the empty value produces — the Settings field's
	// no-credential guidance.
	want := "web_access: token must be at least 8 characters (leave it empty to keep the generated one)"
	if got := ValidateWebAccessToken("").Error(); got != want {
		t.Errorf("ValidateWebAccessToken(\"\") = %q, want %q", got, want)
	}
}

func TestWebAccessTLS_AbsentSubsectionDefaultsOff(t *testing.T) {
	// Additivity (ADR-102): a config authored before the tls subsection
	// existed — or one that simply omits it — loads identically: the zero
	// subsection, zero warnings, the enabled section untouched.
	cfg, warnings := loadWebAccessYAML(t, minimalWebAccessYAML(`
web_access:
  enabled: true
  port: 8765
  token: s3cret-token
`))
	if len(warnings) != 0 {
		t.Fatalf("absent tls subsection must load without warnings, got %v", warnings)
	}
	if !cfg.WebAccess.Enabled || cfg.WebAccess.Port != 8765 || cfg.WebAccess.Token != "s3cret-token" {
		t.Errorf("section must be untouched, got %+v", cfg.WebAccess)
	}
	if cfg.WebAccess.TLS != (WebAccessTLSConfig{}) {
		t.Errorf("tls = %+v, want the zero subsection (off, empty paths)", cfg.WebAccess.TLS)
	}
}

func TestWebAccessTLS_ValidSubsectionSavesAndRoundTrips(t *testing.T) {
	// A fully-configured subsection over a REAL usable pair (ADR-102: the
	// load path reads and parses the files) is valid: it saves without
	// error and reloads warning-free with every value intact.
	dir := t.TempDir()
	certPath, keyPath := writeWebAccessTLSPair(t, dir)
	path := filepath.Join(dir, "config.yaml")
	cfg := minimalValidConfig()
	cfg.WebAccess = WebAccessConfig{
		Enabled: true,
		Bind:    DefaultWebAccessBind,
		Port:    8765,
		TLS: WebAccessTLSConfig{
			Enabled: true,
			Cert:    certPath,
			Key:     keyPath,
		},
	}
	if err := Save(cfg, path); err != nil {
		t.Fatalf("Save must accept a fully-configured tls subsection, got %v", err)
	}
	loaded, err := LoadWithResult(path)
	if err != nil {
		t.Fatalf("LoadWithResult: %v", err)
	}
	if len(loaded.LoadErrors) != 0 {
		t.Fatalf("valid tls must reload warning-free, got %v", loaded.LoadErrors)
	}
	if loaded.Config.WebAccess != cfg.WebAccess {
		t.Errorf("round trip = %+v, want %+v", loaded.Config.WebAccess, cfg.WebAccess)
	}
}

func TestWebAccessTLS_DisabledSubsectionAcceptsAnything(t *testing.T) {
	// An inert subsection (tls.enabled false) accepts any values without
	// warning — the same doctrine as a disabled web_access section. Nothing
	// is validated, nothing is reset, values stay as authored.
	cfg, warnings := loadWebAccessYAML(t, minimalWebAccessYAML(`
web_access:
  enabled: true
  bind: "127.0.0.1"
  port: 8765
  tls:
    enabled: false
    cert: "not-a-path"
    key: ""
`))
	if len(warnings) != 0 {
		t.Fatalf("disabled tls subsection must not warn, got %v", warnings)
	}
	if !cfg.WebAccess.Enabled {
		t.Error("section must stay enabled")
	}
	want := WebAccessTLSConfig{Enabled: false, Cert: "not-a-path"}
	if cfg.WebAccess.TLS != want {
		t.Errorf("tls = %+v, want %+v (preserved as authored)", cfg.WebAccess.TLS, want)
	}
}

func TestWebAccessTLS_EmptyPathsDisableAtLoad(t *testing.T) {
	// ADR-102 (structural half of the usability rule): tls enabled with
	// either path empty is UNUSABLE at load — the section turns off (the
	// sidecar never serves plain HTTP in its place), the subsection and the
	// connection values are preserved for the fix-and-re-enable flow, and
	// exactly one warning names the subsection.
	cases := map[string]WebAccessTLSConfig{
		"both empty": {},
		"cert empty": {Key: "/keys/privkey.pem"},
		"key empty":  {Cert: "/certs/fullchain.pem"},
	}
	for name, tlsCfg := range cases {
		tlsCfg.Enabled = true
		doc := fmt.Sprintf(`
web_access:
  enabled: true
  bind: "127.0.0.1"
  port: 8765
  token: s3cret-token
  tls:
    enabled: true
    cert: %q
    key: %q
`, tlsCfg.Cert, tlsCfg.Key)
		cfg, warnings := loadWebAccessYAML(t, minimalWebAccessYAML(doc))
		if len(warnings) != 1 {
			t.Fatalf("%s: expected exactly one load warning, got %v", name, warnings)
		}
		if !strings.Contains(warnings[0], "web_access.tls unusable") || !strings.Contains(warnings[0], "the sidecar stays disabled") {
			t.Errorf("%s: warning must name the unusable subsection, got %q", name, warnings[0])
		}
		if cfg.WebAccess.Enabled {
			t.Errorf("%s: the section must be disabled — plain-HTTP fallback is not allowed, got %+v", name, cfg.WebAccess)
		}
		if cfg.WebAccess.Bind != DefaultWebAccessBind || cfg.WebAccess.Port != 8765 || cfg.WebAccess.Token != "s3cret-token" {
			t.Errorf("%s: section values must be preserved, got %+v", name, cfg.WebAccess)
		}
		if cfg.WebAccess.TLS != tlsCfg {
			t.Errorf("%s: tls = %+v, want the subsection preserved as authored (%+v)", name, cfg.WebAccess.TLS, tlsCfg)
		}
	}
}

func TestWebAccessTLS_BrokenPairFilesDisableAtLoad(t *testing.T) {
	// ADR-102 (file half of the usability rule): an enabled subsection
	// whose pair does not load — missing files, garbage PEM, a key that
	// does not match its certificate — is UNUSABLE: the section turns off
	// with exactly one warning naming the reason, the subsection is
	// preserved as authored, and a non-loopback bind draws NO exposure
	// warning (no listener is left to expose).
	dir := t.TempDir()
	certPath, keyPath := writeWebAccessTLSPair(t, dir)
	otherCertPath, otherKeyPath := writeWebAccessTLSPair(t, t.TempDir())
	garbage := filepath.Join(dir, "garbage.pem")
	if err := os.WriteFile(garbage, []byte("this is not a PEM block\n"), 0o644); err != nil {
		t.Fatalf("write garbage: %v", err)
	}
	cases := []struct {
		name       string
		cert, key  string
		wantReason string
	}{
		// The missing-file cases assert the platform-neutral classified
		// phrase ("file not found", minted by webAccessTLSPairError's
		// errors.Is(err, fs.ErrNotExist) branch) — NOT the raw strerror,
		// which is OS-localized ("no such file or directory" vs Windows'
		// "The system cannot find the file specified.") and would fail the
		// windows-latest CI leg deterministically.
		{"missing cert", filepath.Join(dir, "absent.crt"), keyPath, "file not found"},
		{"missing key", certPath, filepath.Join(dir, "absent.key"), "file not found"},
		{"garbage cert", garbage, keyPath, "failed to load"},
		{"mismatched pair", certPath, otherKeyPath, "failed to load"},
		{"mismatched pair (other side)", otherCertPath, keyPath, "failed to load"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := fmt.Sprintf(`
web_access:
  enabled: true
  bind: "192.168.1.10"
  port: 8765
  token: s3cret-token
  tls:
    enabled: true
    cert: %q
    key: %q
`, tc.cert, tc.key)
			cfg, warnings := loadWebAccessYAML(t, minimalWebAccessYAML(doc))
			if len(warnings) != 1 {
				t.Fatalf("expected exactly one load warning, got %v", warnings)
			}
			if !strings.Contains(warnings[0], "web_access.tls unusable") || !strings.Contains(warnings[0], "the sidecar stays disabled") {
				t.Errorf("warning must name the unusable subsection, got %q", warnings[0])
			}
			if !strings.Contains(warnings[0], tc.wantReason) {
				t.Errorf("warning must name the reason (%s), got %q", tc.wantReason, warnings[0])
			}
			if cfg.WebAccess.Enabled {
				t.Errorf("the section must be disabled — plain-HTTP fallback is not allowed, got %+v", cfg.WebAccess)
			}
			if cfg.WebAccess.Bind != "192.168.1.10" || cfg.WebAccess.Port != 8765 || cfg.WebAccess.Token != "s3cret-token" {
				t.Errorf("connection values must be preserved, got %+v", cfg.WebAccess)
			}
			if !cfg.WebAccess.TLS.Enabled || cfg.WebAccess.TLS.Cert != tc.cert || cfg.WebAccess.TLS.Key != tc.key {
				t.Errorf("tls subsection must be preserved as authored, got %+v", cfg.WebAccess.TLS)
			}
		})
	}
}

func TestWebAccessTLS_SaveGateIgnoresFiles(t *testing.T) {
	// The save gate is deliberately NOT a file gate (ADR-102): filesystem
	// state must never brick an unrelated settings save. An enabled section
	// with a structurally valid subsection pointing at nonexistent files
	// saves fine — the unusable pair is the load path's fail-soft concern
	// and the interactive boundaries' fail-closed concern. The reload of
	// that same file heals the section to disabled with exactly one
	// warning.
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	cfg := minimalValidConfig()
	cfg.WebAccess = WebAccessConfig{
		Enabled: true,
		Bind:    DefaultWebAccessBind,
		TLS: WebAccessTLSConfig{
			Enabled: true,
			Cert:    filepath.Join(dir, "deliberately-absent.crt"),
			Key:     filepath.Join(dir, "deliberately-absent.key"),
		},
	}
	if err := Save(cfg, path); err != nil {
		t.Fatalf("Save must not gate on the pair's on-disk usability, got %v", err)
	}
	loaded, err := LoadWithResult(path)
	if err != nil {
		t.Fatalf("LoadWithResult: %v", err)
	}
	if len(loaded.LoadErrors) != 1 {
		t.Fatalf("the reload of the unusable pair must draw exactly one warning, got %v", loaded.LoadErrors)
	}
	if loaded.Config.WebAccess.Enabled {
		t.Errorf("the reload must heal the section to disabled (ADR-102), got %+v", loaded.Config.WebAccess)
	}
	if !loaded.Config.WebAccess.TLS.Enabled {
		t.Errorf("the subsection must survive the healing untouched, got %+v", loaded.Config.WebAccess.TLS)
	}
}

func TestWebAccessTLS_SectionResetPreservesSubsection(t *testing.T) {
	// The bind/port fail-soft reset preserves the authored tls subsection
	// (only Enabled/Bind/Port/Token return to the default state) — the same
	// preserve-the-subsection contract as the ADR-102 branch below it. A
	// doubly-broken section therefore stacks the tls warning on top of the
	// section one: both repair flows are named, and the section ends up
	// disabled either way.
	cfg := &Config{WebAccess: WebAccessConfig{
		Enabled: true,
		Bind:    "garbage",
		Port:    8765,
		TLS:     WebAccessTLSConfig{Enabled: true},
	}}
	warnings := normalizeWebAccess(cfg)
	if len(warnings) != 2 {
		t.Fatalf("expected the section-reset and tls warnings stacked, got %v", warnings)
	}
	if !strings.Contains(warnings[0], "ignored") || strings.Contains(warnings[0], "tls") {
		t.Errorf("the first warning must be the section reset, got %q", warnings[0])
	}
	if !strings.Contains(warnings[1], "tls") {
		t.Errorf("the second warning must name tls, got %q", warnings[1])
	}
	// The connection/credential fields reset; the authored subsection
	// survives untouched.
	if cfg.WebAccess.Enabled || cfg.WebAccess.Bind != DefaultWebAccessBind ||
		cfg.WebAccess.Port != 0 || cfg.WebAccess.Token != "" {
		t.Errorf("connection/credential fields must reset to the default state, got %+v", cfg.WebAccess)
	}
	if cfg.WebAccess.TLS != (WebAccessTLSConfig{Enabled: true}) {
		t.Errorf("the authored subsection must survive the reset, got %+v", cfg.WebAccess.TLS)
	}
}

func TestWebAccessTLS_BindResetPreservesUsablePair(t *testing.T) {
	// The realistic loss scenario: an operator hand-authors a usable PEM
	// pair (config.example.yaml documents hand-editing the paths as THE way
	// to bring an operator-owned certificate) and a static token alongside a
	// malformed bind. The fail-soft reset must heal only the connection
	// fields — the cert/key paths AND the hand-authored token survive
	// verbatim, so the next unrelated settings save cannot persist the pair
	// or the credential away (silently wiping the token would mint a
	// DIFFERENT credential at the next sidecar start — the exact loss the
	// reset's warning, which names bind/port alone, would never hint at),
	// and the fix-and-re-enable flow (ADR-102) finds its subsection intact.
	dir := t.TempDir()
	certPath, keyPath := writeWebAccessTLSPair(t, dir)
	cfg, warnings := loadWebAccessYAML(t, minimalWebAccessYAML(`
web_access:
  enabled: true
  bind: "127.0.0.1:8080"
  port: 8765
  token: my-static-token-0123456789
  tls:
    enabled: true
    cert: `+strconv.Quote(certPath)+`
    key: `+strconv.Quote(keyPath)+`
`))
	if len(warnings) != 1 {
		t.Fatalf("expected exactly the section-reset warning, got %v", warnings)
	}
	if !strings.Contains(warnings[0], "ignored") {
		t.Errorf("warning must name the section reset, got %q", warnings[0])
	}
	if cfg.WebAccess.Enabled {
		t.Error("the section must be disabled after the bind reset")
	}
	if cfg.WebAccess.Bind != DefaultWebAccessBind || cfg.WebAccess.Port != 0 {
		t.Errorf("bind/port must reset to the defaults, got %q/%d", cfg.WebAccess.Bind, cfg.WebAccess.Port)
	}
	if cfg.WebAccess.Token != "my-static-token-0123456789" {
		t.Errorf("the hand-authored token must survive the reset verbatim, got %q", cfg.WebAccess.Token)
	}
	if !cfg.WebAccess.TLS.Enabled || cfg.WebAccess.TLS.Cert != certPath || cfg.WebAccess.TLS.Key != keyPath {
		t.Errorf("the authored pair must survive the reset verbatim, got %+v", cfg.WebAccess.TLS)
	}
}

func TestWebAccessTLS_SaveRejectsEmptyPaths(t *testing.T) {
	// Fail-closed (ADR-102, decision 2): an enabled sidecar with a
	// structurally invalid tls subsection must never be persisted — the load
	// path resets it before it can be re-saved, so these states are only
	// reachable through in-memory mutation. The disabled counterpart of the
	// same subsection saves fine.
	cases := map[string]WebAccessTLSConfig{
		"both empty": {},
		"cert empty": {Key: "/keys/privkey.pem"},
		"key empty":  {Cert: "/certs/fullchain.pem"},
	}
	for name, tlsCfg := range cases {
		tlsCfg.Enabled = true
		dir := t.TempDir()
		path := filepath.Join(dir, "config.yaml")
		cfg := minimalValidConfig()
		cfg.WebAccess = WebAccessConfig{Enabled: true, Bind: DefaultWebAccessBind, TLS: tlsCfg}
		err := Save(cfg, path)
		if err == nil {
			t.Errorf("%s: Save must refuse a structurally invalid tls subsection", name)
			continue
		}
		if !strings.Contains(err.Error(), "web_access") {
			t.Errorf("%s: error must name the section, got %q", name, err.Error())
		}
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Errorf("%s: refused save must not create the file", name)
		}
		// The disabled counterpart of the same subsection saves fine.
		tlsCfg.Enabled = false
		cfg.WebAccess.TLS = tlsCfg
		if err := Save(cfg, path); err != nil {
			t.Errorf("%s: the disabled subsection must save fine, got %v", name, err)
		}
	}
}

func TestWebAccessTLS_SaveGateInertWhenSectionDisabled(t *testing.T) {
	// The subsection is validated only while the sidecar itself is enabled:
	// a disabled web_access section is inert, subsection included — the same
	// half-typed subsection saves without error when the sidecar is off.
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	cfg := minimalValidConfig()
	cfg.WebAccess = WebAccessConfig{
		Bind: DefaultWebAccessBind,
		TLS:  WebAccessTLSConfig{Enabled: true},
	}
	if err := Save(cfg, path); err != nil {
		t.Fatalf("Save must accept a tls subsection under a disabled section, got %v", err)
	}
}

func TestValidateWebAccessTLS(t *testing.T) {
	// The exported mirror of the subsection rule: a disabled subsection is
	// inert (anything passes), an enabled one needs both PEM paths, and the
	// messages name the section exactly like the load/save paths.
	if err := ValidateWebAccessTLS(WebAccessTLSConfig{}); err != nil {
		t.Errorf("disabled empty = %v, want nil", err)
	}
	if err := ValidateWebAccessTLS(WebAccessTLSConfig{Cert: "x", Key: "y"}); err != nil {
		t.Errorf("disabled with values = %v, want nil", err)
	}
	if err := ValidateWebAccessTLS(WebAccessTLSConfig{Enabled: true, Cert: "/c", Key: "/k"}); err != nil {
		t.Errorf("enabled with both paths = %v, want nil", err)
	}
	err := ValidateWebAccessTLS(WebAccessTLSConfig{Enabled: true, Key: "/k"})
	if err == nil {
		t.Error("enabled with empty cert = nil, want error")
	} else if got := err.Error(); got != "web_access: tls.cert is required when tls is enabled (generate a pair in Settings → Web Access or point cert/key at PEM files)" {
		t.Errorf("enabled with empty cert = %q, want the cert-required message", got)
	}
	err = ValidateWebAccessTLS(WebAccessTLSConfig{Enabled: true, Cert: "/c"})
	if err == nil {
		t.Error("enabled with empty key = nil, want error")
	} else if got := err.Error(); got != "web_access: tls.key is required when tls is enabled" {
		t.Errorf("enabled with empty key = %q, want the key-required message", got)
	}
}

func TestValidateWebAccessTLSPair(t *testing.T) {
	// The exported usability mirror (ADR-102): a disabled subsection is
	// inert, structural incompleteness fails with the same messages as
	// ValidateWebAccessTLS, an unloadable pair fails with the load reason,
	// and a real pair on disk passes.
	dir := t.TempDir()
	certPath, keyPath := writeWebAccessTLSPair(t, dir)
	if err := ValidateWebAccessTLSPair(WebAccessTLSConfig{}); err != nil {
		t.Errorf("disabled empty = %v, want nil", err)
	}
	if err := ValidateWebAccessTLSPair(WebAccessTLSConfig{Cert: "x", Key: "y"}); err != nil {
		t.Errorf("disabled with values = %v, want nil", err)
	}
	err := ValidateWebAccessTLSPair(WebAccessTLSConfig{Enabled: true, Key: "/k"})
	if err == nil || !strings.Contains(err.Error(), "tls.cert is required") {
		t.Errorf("enabled with empty cert = %v, want the structural cert message", err)
	}
	err = ValidateWebAccessTLSPair(WebAccessTLSConfig{Enabled: true, Cert: certPath, Key: filepath.Join(dir, "absent.key")})
	if err == nil || !strings.Contains(err.Error(), "certificate pair failed to load") {
		t.Errorf("enabled with a missing key file = %v, want the load failure", err)
	}
	if err := ValidateWebAccessTLSPair(WebAccessTLSConfig{Enabled: true, Cert: certPath, Key: keyPath}); err != nil {
		t.Errorf("enabled with a real usable pair = %v, want nil", err)
	}
}

func TestWebAccessLoadWarnings(t *testing.T) {
	// The pure runtime counterpart of the normalizeWebAccess warning tail
	// (ADR-102): state-only, no mutation, and — for every section state the
	// load path leaves surviving — byte-identical to what the load pipeline
	// produces, so the runtime recompute can never drift from the load text.
	dir := t.TempDir()
	certPath, keyPath := writeWebAccessTLSPair(t, dir)

	t.Run("disabled section warns nothing even non-loopback plain", func(t *testing.T) {
		section := WebAccessConfig{Enabled: false, Bind: "192.168.23.2"}
		if got := WebAccessLoadWarnings(section, true); got != nil {
			t.Errorf("disabled non-loopback plain = %v, want nil (no listener, nothing to expose)", got)
		}
	})

	t.Run("enabled loopback warns nothing", func(t *testing.T) {
		section := WebAccessConfig{Enabled: true, Bind: DefaultWebAccessBind}
		if got := WebAccessLoadWarnings(section, true); got != nil {
			t.Errorf("enabled loopback = %v, want nil", got)
		}
	})

	t.Run("enabled non-loopback plain draws the exposure warning", func(t *testing.T) {
		section := WebAccessConfig{Enabled: true, Bind: "192.168.23.2", AllowInsecureLAN: true}
		got := WebAccessLoadWarnings(section, true)
		if len(got) != 1 {
			t.Fatalf("got %v, want exactly one warning", got)
		}
		want := `web_access: bind "192.168.23.2" is not loopback — the listener is network-accessible, traffic is plain HTTP without TLS (accepted via allow_insecure_lan)`
		if got[0] != want {
			t.Errorf("warning = %q, want %q", got[0], want)
		}
	})

	t.Run("non-loopback plain without the opt-in warns nothing — it cannot survive", func(t *testing.T) {
		// The refused shape is disabled by the load gate and refused by the
		// interactive boundaries, so the runtime recompute never sees it;
		// the predicate is total anyway — no flag, no accepted-risk warning.
		section := WebAccessConfig{Enabled: true, Bind: "192.168.23.2"}
		if got := WebAccessLoadWarnings(section, true); got != nil {
			t.Errorf("non-opted-in non-loopback plain = %v, want nil (the state cannot survive to here)", got)
		}
	})

	t.Run("unspecified bind is non-loopback class", func(t *testing.T) {
		section := WebAccessConfig{Enabled: true, Bind: "0.0.0.0", AllowInsecureLAN: true}
		if got := WebAccessLoadWarnings(section, true); len(got) != 1 {
			t.Errorf("0.0.0.0 plain HTTP = %v, want the exposure warning", got)
		}
	})

	t.Run("enabled non-loopback TLS loads warning-free", func(t *testing.T) {
		section := WebAccessConfig{
			Enabled: true,
			Bind:    "192.168.23.2",
			TLS:     WebAccessTLSConfig{Enabled: true, Cert: certPath, Key: keyPath},
		}
		if got := WebAccessLoadWarnings(section, true); got != nil {
			t.Errorf("TLS-encrypted exposure = %v, want nil (ADR-102)", got)
		}
	})

	t.Run("closed gate suppresses the exposure warning", func(t *testing.T) {
		// CS #65 lives inside the shared predicate: gate closed → the
		// sidecar cannot mount, so the present-tense exposure warning would
		// overstate the exposure — nil, exactly like the load tail.
		section := WebAccessConfig{Enabled: true, Bind: "192.168.23.2"}
		if got := WebAccessLoadWarnings(section, false); got != nil {
			t.Errorf("gate-closed non-loopback plain = %v, want nil", got)
		}
	})

	t.Run("matches the load pipeline for surviving states", func(t *testing.T) {
		// Parity holds while the compound gate is OPEN (the runtime recompute
		// runs after a live gate flip — the warning's own case): the load
		// pipeline then emits the same exposure warning the section-level
		// predicate draws. The closed-gate suppression (CS #65) is no longer
		// a load-site divergence — the gate conjunction lives INSIDE
		// WebAccessLoadWarnings, so both paths share it (pinned by the
		// "closed gate suppresses" subtest below).
		for name, section := range map[string]WebAccessConfig{
			"loopback plain": {Enabled: true, Bind: DefaultWebAccessBind},
			"non-loopback plain (opted-in)": {Enabled: true, Bind: "192.168.23.2",
				AllowInsecureLAN: true},
			"non-loopback TLS": {Enabled: true, Bind: "192.168.23.2",
				TLS: WebAccessTLSConfig{Enabled: true, Cert: certPath, Key: keyPath}},
			"disabled": {Enabled: false, Bind: "192.168.23.2"},
		} {
			live := Config{WebAccess: section, Experimental: ExperimentalConfig{Enabled: true}}
			loadWarnings := normalizeWebAccess(&live)
			got := WebAccessLoadWarnings(section, true)
			if len(got) != len(loadWarnings) {
				t.Errorf("%s: runtime = %v, load pipeline = %v", name, got, loadWarnings)
				continue
			}
			for i := range got {
				if got[i] != loadWarnings[i] {
					t.Errorf("%s: runtime[%d] = %q, load pipeline = %q", name, i, got[i], loadWarnings[i])
				}
			}
			if section.Enabled && section.Bind != live.WebAccess.Bind {
				t.Errorf("%s: normalizeWebAccess mutated the section", name)
			}
		}
	})
}
