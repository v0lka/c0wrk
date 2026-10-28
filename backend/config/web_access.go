// SPDX-License-Identifier: Apache-2.0
//
// web_access.go — the `web_access` config section: the browser web-access
// sidecar's operator switch. Pure data, default OFF, externally gated by the
// experimental master switch; load-time validity FAIL-SOFT, save-time
// FAIL-CLOSED; a valid non-loopback bind is a warned LAN listener refused
// without TLS or allow_insecure_lan; port 0 = auto, empty token = GENERATED
// and persisted at first start. See specs/domains/web-access.md,
// specs/decisions/101-browser-web-access.md, specs/decisions/102-web-access-tls.md.

package config

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"strings"
	"time"
)

// DefaultWebAccessBind is the seeded bind: loopback IPv4, kept explicit in
// the persisted shape (specs/decisions/101-browser-web-access.md).
const DefaultWebAccessBind = "127.0.0.1"

// WebAccessConfig is the top-level `web_access:` section (the sidecar is
// pure data; see specs/domains/web-access.md).
type WebAccessConfig struct {
	// Enabled turns the browser web-access sidecar on; default false, and an
	// absent section is this zero value.
	Enabled bool `yaml:"enabled"`
	// Bind: any IP literal or the "localhost" name; a valid non-loopback
	// bind is a LAN listener (warned at load, saved as-is); invalid values
	// fail soft at load, refused at save.
	Bind string `yaml:"bind"`
	// Port is the TCP port; 0 (default) = auto, picked at sidecar start.
	// Out-of-range fails soft at load, refused at save.
	Port int `yaml:"port"`
	// Token: the sidecar credential; empty = GENERATED at first start and
	// persisted (stable across restarts). Hand-edited static values are
	// honored (warned under 16 chars); written via desktop's
	// SetWebAccessToken / RegenerateWebAccessToken (webAccessMu-serialized).
	Token string `yaml:"token"`
	// AllowInsecureLAN opts in to the one shape refused by default: enabled +
	// non-loopback + plain HTTP — fail-soft at load, fail-closed at save,
	// refused at the interactive boundaries and sidecar mount; inert for TLS
	// or loopback sections.
	AllowInsecureLAN bool `yaml:"allow_insecure_lan"`
	// TLS is the `tls:` subsection (ADR-102), validated only while the
	// sidecar is enabled — a disabled section is inert. See WebAccessTLSConfig.
	TLS WebAccessTLSConfig `yaml:"tls"`
}

// WebAccessTLSConfig is the `web_access.tls:` subsection (ADR-102): the
// sidecar served over HTTPS with a PEM certificate/key pair.
type WebAccessTLSConfig struct {
	// Enabled serves the sidecar over HTTPS; default false = plain HTTP,
	// byte-identical to pre-ADR-102. An enabled subsection that is
	// structurally incomplete or whose pair fails to load is UNUSABLE: load
	// turns the whole section off (never downgraded to plain HTTP); the
	// interactive commit refuses up front.
	Enabled bool `yaml:"enabled"`
	// Cert: the PEM certificate path (full chain when available). The
	// app-owned pair lives at WebAccessCertPath, written by the Generate
	// control; operator-authored certs are hand-authored here.
	Cert string `yaml:"cert"`
	// Key: the PEM private-key path, authored explicitly next to Cert (never
	// derived). The interactive commit loads the pair before persisting
	// (refusing mismatched/unreadable); empty-while-enabled is structurally
	// invalid, unloadable unusable the same way — both turn the section off
	// at load (ADR-102).
	Key string `yaml:"key"`
}

// IsLoopbackBind reports whether bind names a loopback interface (an IP
// literal incl. IPv4-mapped forms, or exactly "localhost"); since stage 2 a
// WARNING classification over valid binds, not validity (see
// specs/domains/web-access.md).
func IsLoopbackBind(bind string) bool {
	if bind == "localhost" {
		return true
	}
	ip := net.ParseIP(bind)
	return ip != nil && ip.IsLoopback()
}

// IsValidWebAccessBind reports whether bind is a valid listener address
// (ADR-101 stage 2): any IP literal (incl. IPv4-mapped) or exactly
// "localhost". Loopback is deliberately not validity — IsLoopbackBind
// classifies it.
func IsValidWebAccessBind(bind string) bool {
	if bind == "localhost" {
		return true
	}
	return net.ParseIP(bind) != nil
}

// validateWebAccessBind checks an enabled section's bind VALIDITY: invalid
// fails soft at load, closed at save; valid non-loopback only draws the
// exposure warning.
func validateWebAccessBind(bind string) error {
	if IsValidWebAccessBind(bind) {
		return nil
	}
	if bind == "" {
		return errors.New("web_access: bind is required when enabled")
	}
	return fmt.Errorf("web_access: bind %q is not a valid IP literal or \"localhost\" (default %q)", bind, DefaultWebAccessBind)
}

// validateWebAccessPort checks an enabled section's port: 0 = auto, 1–65535
// explicit.
func validateWebAccessPort(port int) error {
	if port < 0 || port > 65535 {
		return fmt.Errorf("web_access: port %d is out of range (0 = auto, 1-65535 explicit)", port)
	}
	return nil
}

// ValidateWebAccessPort is the exported port rule for desktop's
// SetWebAccessConnection: the load/save paths' own message.
func ValidateWebAccessPort(port int) error {
	return validateWebAccessPort(port)
}

// ValidateWebAccessBind is the exported bind rule (mirror of
// ValidateWebAccessPort) for desktop's runtime mutators: any IP literal or
// "localhost", loopback or not.
func ValidateWebAccessBind(bind string) error {
	return validateWebAccessBind(bind)
}

// validateWebAccessTLS checks STRUCTURAL validity (ADR-102, decision 2):
// enabled requires both PEM paths, disabled is inert; an unreadable pair
// passes here and fails closed at sidecar mount.
func validateWebAccessTLS(tlsCfg WebAccessTLSConfig) error {
	if !tlsCfg.Enabled {
		return nil
	}
	if tlsCfg.Cert == "" {
		return errors.New("web_access: tls.cert is required when tls is enabled (generate a pair in Settings → Web Access or point cert/key at PEM files)")
	}
	if tlsCfg.Key == "" {
		return errors.New("web_access: tls.key is required when tls is enabled")
	}
	return nil
}

// ValidateWebAccessTLS is the exported structural rule — the mirror of the
// other exported validators — for desktop's SetWebAccessTLS.
func ValidateWebAccessTLS(tlsCfg WebAccessTLSConfig) error {
	return validateWebAccessTLS(tlsCfg)
}

// webAccessTLSProbeTimeout bounds the certificate-pair probe (CS #47) — it
// reads both PEM files on EVERY config load, so a hanging mount would stall
// startup; fail-closed within the budget (ADR-102), never downgraded; the
// goroutine left blocked on a wedged file is bounded by the probe's rarity
// and drained into the buffered channel.
const webAccessTLSProbeTimeout = 5 * time.Second

// webAccessTLSPairError is the tls subsection's usability predicate
// (ADR-102): structure first, then the pair itself — the deliberate reversal
// of the never-reads-the-files doctrine; a failing subsection is UNUSABLE,
// never downgraded to plain HTTP (specs/decisions/102-web-access-tls.md).
func webAccessTLSPairError(tlsCfg WebAccessTLSConfig) error {
	if err := validateWebAccessTLS(tlsCfg); err != nil {
		return err
	}
	if !tlsCfg.Enabled {
		return nil
	}
	errCh := make(chan error, 1)
	go func() {
		_, err := tls.LoadX509KeyPair(tlsCfg.Cert, tlsCfg.Key)
		errCh <- err
	}()
	select {
	case err := <-errCh:
		if err == nil {
			return nil
		}
		// Platform-neutral wording: the raw strerror is OS-localized and
		// the warning — and the tests pinning it — must not depend on the
		// host OS.
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("certificate pair failed to load: certificate or key file not found: %w", err)
		}
		return fmt.Errorf("certificate pair failed to load: %w", err)
	case <-time.After(webAccessTLSProbeTimeout):
		return fmt.Errorf("certificate pair failed to load: reading %s did not complete within %s",
			tlsCfg.Cert, webAccessTLSProbeTimeout)
	}
}

// ValidateWebAccessTLSPair is the exported usability rule (ADR-102) for
// desktop's SetWebAccessEnabled: a flip onto an unusable TLS is refused
// before anything persists.
func ValidateWebAccessTLSPair(tlsCfg WebAccessTLSConfig) error {
	return webAccessTLSPairError(tlsCfg)
}

// Interactive token bounds (ADR-101), enforced at the entry points;
// deliberately NOT a gate on a hand-edited config.yaml (honored, warned
// under 16 chars at sidecar start) so tightening can never lock an operator
// out of their own config. See specs/domains/web-access.md.
const (
	MinWebAccessTokenLength = 8
	MaxWebAccessTokenLength = 256
)

// ValidateWebAccessToken is the interactive-input rule (length within the
// bounds after trimming); empty is rejected — emptiness means GENERATED,
// expressed by leaving the field alone. See specs/domains/web-access.md.
func ValidateWebAccessToken(token string) error {
	if len(token) < MinWebAccessTokenLength {
		return fmt.Errorf("web_access: token must be at least %d characters (leave it empty to keep the generated one)", MinWebAccessTokenLength)
	}
	if len(token) > MaxWebAccessTokenLength {
		return fmt.Errorf("web_access: token is too long — at most %d characters", MaxWebAccessTokenLength)
	}
	return nil
}

// webAccessLoopbackWarning is the load warning for a valid non-loopback
// plain-HTTP bind WITH the allow_insecure_lan opt-in — accepted risk, still
// surfaced. TLS-encrypted binds deliberately do not draw it (ADR-102);
// without the flag the shape is turned off by normalizeWebAccess instead.
func webAccessLoopbackWarning(bind string) string {
	return fmt.Sprintf("web_access: bind %q is not loopback — the listener is network-accessible, traffic is plain HTTP without TLS (accepted via allow_insecure_lan)", bind)
}

// webAccessInsecureLAN reports the one shape refused by default (enabled +
// non-loopback + plain HTTP + no opt-in) — the single predicate shared by
// load, save, the interactive boundaries and the sidecar mount so the rule
// cannot drift apart between them; callers flipping Enabled consult the
// would-be-next state.
func webAccessInsecureLAN(section WebAccessConfig) bool {
	return section.Enabled && !IsLoopbackBind(section.Bind) && !section.TLS.Enabled && !section.AllowInsecureLAN
}

// webAccessInsecureLANError is the single refusal message every enforcement
// point reports for the webAccessInsecureLAN shape, naming both ways out.
func webAccessInsecureLANError(bind string) error {
	return fmt.Errorf("web_access: bind %q is not loopback and tls is off — an unencrypted LAN-exposed listener needs allow_insecure_lan: true (an explicit accepted-risk opt-in) or web_access.tls enabled", bind)
}

// ValidateWebAccessExposure is the exported exposure gate for desktop's
// runtime mutators; the caller passes the WOULD-BE-NEXT state (post-flip),
// so a pre-staged non-loopback pin is caught at the door, not at mount.
func ValidateWebAccessExposure(section WebAccessConfig) error {
	if webAccessInsecureLAN(section) {
		return webAccessInsecureLANError(section.Bind)
	}
	return nil
}

// WebAccessLoadWarnings is the pure state-only counterpart of
// normalizeWebAccess's warning tail (ADR-102), keeping the config_errors →
// Settings banner live for runtime mutators; idempotent and safe after
// rollbacks. experimentalEnabled is part of the condition (CS #65 — a closed
// gate cannot mount); only the exposure warning is computable from state.
// A disabled section warns nothing; the refused insecure-LAN shape never
// reaches the recompute. See specs/domains/web-access.md.
func WebAccessLoadWarnings(section WebAccessConfig, experimentalEnabled bool) []string {
	if !section.Enabled || !experimentalEnabled {
		return nil
	}
	if IsLoopbackBind(section.Bind) || section.TLS.Enabled || !section.AllowInsecureLAN {
		return nil
	}
	return []string{webAccessLoopbackWarning(section.Bind)}
}

// defaultWebAccessState is the fail-soft reset target (disabled, loopback,
// auto port, empty token) — keeping the persisted shape always valid and
// self-describing (the shell_exec doctrine).
func defaultWebAccessState() WebAccessConfig {
	return WebAccessConfig{
		Enabled: false,
		Bind:    DefaultWebAccessBind,
		Port:    0,
		Token:   "",
		TLS:     WebAccessTLSConfig{},
	}
}

// normalizeWebAccess validates the web_access section in place and returns
// load warnings. VALIDITY is FAIL-SOFT: invalid bind/port warns and resets
// the connection fields while the authored tls subsection, static token and
// opt-in survive; an unusable tls subsection turns the whole section off,
// preserved as authored. Runs AFTER ApplyDefaults, BEFORE validate. See
// specs/domains/web-access.md.
func normalizeWebAccess(cfg *Config) []string {
	if !cfg.WebAccess.Enabled {
		return nil
	}
	var warnings []string
	var reasons []string
	if err := validateWebAccessBind(cfg.WebAccess.Bind); err != nil {
		reasons = append(reasons, err.Error())
	}
	if err := validateWebAccessPort(cfg.WebAccess.Port); err != nil {
		reasons = append(reasons, err.Error())
	}
	if len(reasons) > 0 {
		warning := "web_access ignored (the sidecar stays disabled; fix bind/port and re-enable): " +
			strings.Join(reasons, "; ")
		// The reset preserves the authored tls subsection, token and
		// opt-in: a bind/port typo must not silently wipe hand-authored
		// operator config on the next unrelated save.
		authored := cfg.WebAccess
		cfg.WebAccess = defaultWebAccessState()
		cfg.WebAccess.TLS = authored.TLS
		cfg.WebAccess.Token = authored.Token
		cfg.WebAccess.AllowInsecureLAN = authored.AllowInsecureLAN
		warnings = append(warnings, warning)
	}
	// An unusable tls subsection turns the whole section off (never
	// downgraded to plain HTTP); the subsection survives as authored and
	// both repair flows are named.
	if err := webAccessTLSPairError(cfg.WebAccess.TLS); err != nil {
		warning := "web_access.tls unusable (the sidecar stays disabled — a configured TLS is never downgraded to plain HTTP; fix the certificate pair or turn tls off, then enable web_access again): " +
			err.Error()
		cfg.WebAccess.Enabled = false
		warnings = append(warnings, warning)
	}
	// Fail-soft like the TLS branch: the section turns OFF with a warning
	// naming both ways out; save, the interactive boundaries and the mount
	// backstop enforce the same predicate.
	if webAccessInsecureLAN(cfg.WebAccess) {
		warnings = append(warnings, "web_access ignored (the sidecar stays disabled): "+
			webAccessInsecureLANError(cfg.WebAccess.Bind).Error())
		cfg.WebAccess.Enabled = false
	}
	// Fires only for a SURVIVING enabled section; delegates to
	// WebAccessLoadWarnings so load and runtime recompute cannot drift in
	// text or condition (CS #65).
	warnings = append(warnings, WebAccessLoadWarnings(cfg.WebAccess, cfg.Experimental.Enabled)...)
	return warnings
}

// validateWebAccessForSave is the save-time fail-closed gate: an INVALID
// enabled section (bind, port, tls structure) or the insecure-LAN shape must
// never persist — the load path heals first, so this fires only on in-memory
// mutations. The pair's on-disk usability is deliberately NOT a save gate.
func validateWebAccessForSave(cfg *Config) error {
	if !cfg.WebAccess.Enabled {
		return nil
	}
	if err := validateWebAccessBind(cfg.WebAccess.Bind); err != nil {
		return err
	}
	if err := validateWebAccessPort(cfg.WebAccess.Port); err != nil {
		return err
	}
	if err := validateWebAccessTLS(cfg.WebAccess.TLS); err != nil {
		return err
	}
	// The insecure-LAN gate is a save gate too (fail-closed): the refused
	// shape must never persist through ANY config write — an unrelated
	// settings save must not resurrect a section the load path disabled.
	return ValidateWebAccessExposure(cfg.WebAccess)
}
