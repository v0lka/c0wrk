package backend

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/core/gittrust"
	"github.com/v0lka/c0wrk/core/workspace"
)

// Semantic trust-lifecycle tests ──────────────────────────────────────────
//
// The trust lifecycle is bound to the SEMANTIC fingerprint of the snapshot
// captured at trust time (the hash of its DANGEROUS subset — see
// workspace.SemanticSnapshot): inert branch/alias churn keeps the trust and
// silently refreshes the stored bytes, any semantic change evicts the trust
// and warns with a semantic diff, and v1 records (written before the
// semantic layer) are migrated from their stored raw snapshot on the next
// open — fail-closed when that snapshot cannot be recovered.

// semanticTrustScanFingerprint scans dir through the real pipeline and
// returns both fresh fingerprints (raw, semantic).
func semanticTrustScanFingerprint(t *testing.T, dir string) (raw, semantic string) {
	t.Helper()
	info, err := workspace.ScanGitConfig(dir)
	if err != nil {
		t.Fatalf("ScanGitConfig: %v", err)
	}
	info.ResolveIncludes()
	return info.Fingerprint(), info.SemanticFingerprint()
}

// semanticTrustedEntry returns the single trusted entry of f's config.
func semanticTrustedEntry(t *testing.T, f *FrontendAPI) config.TrustedGitRepo {
	t.Helper()
	f.configMu.RLock()
	defer f.configMu.RUnlock()
	if len(f.config.Security.TrustedGitRepos) != 1 {
		t.Fatalf("trusted repos = %v, want exactly one", f.config.Security.TrustedGitRepos)
	}
	return f.config.Security.TrustedGitRepos[0]
}

// downgradeTrustEntryToV1 simulates a v1 (pre-semantic) trust record: the
// semantic fingerprint is cleared and the semantic snapshot file removed,
// leaving exactly what an older build persisted. Returns the record with its
// raw fingerprint (the migration's only input).
func downgradeTrustEntryToV1(t *testing.T, f *FrontendAPI) config.TrustedGitRepo {
	t.Helper()
	entry := semanticTrustedEntry(t, f)
	if entry.SemanticFingerprint != "" {
		if err := os.Remove(filepath.Join(config.GitConfigSnapshotsDir(f.agentDir), entry.SemanticFingerprint)); err != nil {
			t.Fatalf("removing the semantic snapshot file: %v", err)
		}
	}
	f.configMu.Lock()
	f.config.Security.TrustedGitRepos[0].SemanticFingerprint = ""
	f.configMu.Unlock()
	return config.TrustedGitRepo{Path: entry.Path, Fingerprint: entry.Fingerprint}
}

// setTrustEntryFingerprints overwrites both fingerprints of the single
// trusted entry (crafting arbitrary record shapes for the fail-closed tests).
func setTrustEntryFingerprints(t *testing.T, f *FrontendAPI, raw, semantic string) {
	t.Helper()
	f.configMu.Lock()
	f.config.Security.TrustedGitRepos[0].Fingerprint = raw
	f.config.Security.TrustedGitRepos[0].SemanticFingerprint = semantic
	f.configMu.Unlock()
}

// TestNotifyGitConfigRisk_InertDriftKeepsTrustAndRefreshesSnapshot pins the
// inert path of the semantic trust lifecycle: branch-section churn after the
// trust decision keeps the trust, emits nothing, and silently refreshes the
// stored raw snapshot (and the entry's byte-level fingerprint) to the new
// bytes, while the semantic fingerprint — the thing the trust keys on —
// stays put.
func TestNotifyGitConfigRisk_InertDriftKeepsTrustAndRefreshesSnapshot(t *testing.T) {
	dir := t.TempDir()
	writeGitConfig(t, dir, "[core]\n\tfsmonitor = /tmp/evil\n[branch \"main\"]\n\tremote = origin\n")

	f, _, _ := newTestAPI(t)
	if err := f.TrustGitRepo(dir); err != nil {
		t.Fatalf("TrustGitRepo: %v", err)
	}
	entry := semanticTrustedEntry(t, f)
	if entry.SemanticFingerprint == "" {
		t.Fatal("TrustGitRepo did not record a semantic fingerprint")
	}

	// Inert churn: git adds a branch section (what branch --set-upstream does).
	writeGitConfig(t, dir, "[core]\n\tfsmonitor = /tmp/evil\n[branch \"main\"]\n\tremote = origin\n[branch \"dev\"]\n\tremote = origin\n\tmerge = refs/heads/main\n")
	rec := newRiskRecorder(t, f)
	f.notifyGitConfigRisk(GitConfigRiskSourceProject, dir)
	if rec.fired {
		t.Fatal("inert (branch-section) drift must not emit project:git_config_risk")
	}

	// The trust survives in the config and in the spawn-layer registry.
	entry = semanticTrustedEntry(t, f)
	if !gittrust.IsTrusted(dir) {
		t.Error("inert drift must not unregister the repo from the git trust registry")
	}

	// The byte-level binding followed the config; the semantic identity did
	// not move.
	wantRaw, wantSemantic := semanticTrustScanFingerprint(t, dir)
	if entry.Fingerprint != wantRaw {
		t.Errorf("raw fingerprint after inert drift = %q, want refreshed %q", entry.Fingerprint, wantRaw)
	}
	if entry.SemanticFingerprint != wantSemantic || entry.SemanticFingerprint != trustSemanticFingerprintOf(t, f, entry.SemanticFingerprint) {
		t.Errorf("semantic fingerprint changed across inert drift: %q", entry.SemanticFingerprint)
	}
	if _, err := os.Stat(filepath.Join(config.GitConfigSnapshotsDir(f.agentDir), wantRaw)); err != nil {
		t.Errorf("expected the refreshed raw snapshot file: %v", err)
	}
}

// trustSemanticFingerprintOf recovers a semantic fingerprint from the stored
// snapshot file, verifying the store holds the semantic snapshot for it.
func trustSemanticFingerprintOf(t *testing.T, f *FrontendAPI, fingerprint string) string {
	t.Helper()
	raw, err := f.readGitConfigSnapshot(fingerprint)
	if err != nil {
		t.Fatalf("reading stored semantic snapshot %s: %v", fingerprint, err)
	}
	fp, _, err := workspace.SemanticFingerprintFromSnapshot(raw)
	if err != nil {
		t.Fatalf("stored semantic snapshot is not interpretable: %v", err)
	}
	return fp
}

// TestNotifyGitConfigRisk_SemanticDriftEvictsWithFilteredDiff pins the drift
// path: a semantic change (here: the dangerous key's value changed AND an
// [alias] section appeared) evicts the trust and warns with a SEMANTIC diff —
// the inert alias churn is filtered out of the diff, which shows only the
// dangerous change.
func TestNotifyGitConfigRisk_SemanticDriftEvictsWithFilteredDiff(t *testing.T) {
	dir := t.TempDir()
	writeGitConfig(t, dir, "[core]\n\tfsmonitor = /tmp/evil\n[branch \"main\"]\n\tremote = origin\n")

	f, _, _ := newTestAPI(t)
	if err := f.TrustGitRepo(dir); err != nil {
		t.Fatalf("TrustGitRepo: %v", err)
	}

	writeGitConfig(t, dir, "[core]\n\tfsmonitor = /tmp/other\n[branch \"main\"]\n\tremote = origin\n[branch \"dev\"]\n\tmerge = refs/heads/main\n[alias]\n\tcam = commit\n")
	rec := newRiskRecorder(t, f)
	f.notifyGitConfigRisk(GitConfigRiskSourceProject, dir)
	if !rec.fired {
		t.Fatal("semantic drift must emit project:git_config_risk")
	}
	if rec.data.Reason == "" {
		t.Error("expected a drift reason in the payload")
	}
	if !strings.Contains(rec.data.Diff, "fsmonitor") {
		t.Errorf("semantic diff should show the dangerous change, got: %q", rec.data.Diff)
	}
	for _, inert := range []string{"alias", "branch", "remote", "merge"} {
		if strings.Contains(rec.data.Diff, inert) {
			t.Errorf("semantic diff must filter out inert churn (%s), got: %q", inert, rec.data.Diff)
		}
	}
	if got := f.GetTrustedGitRepos(); len(got) != 0 {
		t.Errorf("trusted repos after semantic drift = %v, want empty (evicted)", got)
	}
	if gittrust.IsTrusted(dir) {
		t.Error("expected the repo to be unregistered from the git trust registry after semantic drift")
	}
}

// TestNotifyGitConfigRisk_V1RecordMigratesOnRecheck pins the v1→v2
// migration: a record written before the semantic layer recovers its
// semantic fingerprint from the raw snapshot it stored, persists it (config
// file on disk included), and then behaves as a v2 record — an unchanged or
// inertly-drifted config stays silent.
func TestNotifyGitConfigRisk_V1RecordMigratesOnRecheck(t *testing.T) {
	dir := t.TempDir()
	writeGitConfig(t, dir, "[core]\n\tfsmonitor = /tmp/evil\n")

	f, _, cfgPath := newTestAPI(t)
	if err := f.TrustGitRepo(dir); err != nil {
		t.Fatalf("TrustGitRepo: %v", err)
	}
	downgradeTrustEntryToV1(t, f)

	// Phase A: unchanged config — the migration happens and nothing is emitted.
	rec := newRiskRecorder(t, f)
	f.notifyGitConfigRisk(GitConfigRiskSourceProject, dir)
	if rec.fired {
		t.Fatal("migrating an unchanged v1 record must not emit project:git_config_risk")
	}
	entry := semanticTrustedEntry(t, f)
	_, wantSemantic := semanticTrustScanFingerprint(t, dir)
	if entry.SemanticFingerprint == "" || entry.SemanticFingerprint != wantSemantic {
		t.Fatalf("semantic fingerprint after migration = %q, want %q", entry.SemanticFingerprint, wantSemantic)
	}
	if got := trustSemanticFingerprintOf(t, f, entry.SemanticFingerprint); got != entry.SemanticFingerprint {
		t.Errorf("migrated semantic snapshot does not hash to the recorded fingerprint: %q vs %q", got, entry.SemanticFingerprint)
	}
	// The migration is persisted, not just in-memory.
	persisted, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("loading the persisted config: %v", err)
	}
	if len(persisted.Security.TrustedGitRepos) != 1 || persisted.Security.TrustedGitRepos[0].SemanticFingerprint != wantSemantic {
		t.Errorf("persisted config did not carry the migrated semantic fingerprint: %+v", persisted.Security.TrustedGitRepos)
	}

	// Phase B: inert drift afterwards behaves v2 — silent refresh, no event.
	writeGitConfig(t, dir, "[core]\n\tfsmonitor = /tmp/evil\n[branch \"main\"]\n\tremote = origin\n")
	rec = newRiskRecorder(t, f)
	f.notifyGitConfigRisk(GitConfigRiskSourceProject, dir)
	if rec.fired {
		t.Fatal("inert drift after migration must not emit project:git_config_risk")
	}
	entry = semanticTrustedEntry(t, f)
	wantRaw, wantSemantic := semanticTrustScanFingerprint(t, dir)
	if entry.Fingerprint != wantRaw || entry.SemanticFingerprint != wantSemantic {
		t.Errorf("entry after inert post-migration drift = (%q, %q), want (%q, %q)",
			entry.Fingerprint, entry.SemanticFingerprint, wantRaw, wantSemantic)
	}
	if got := f.GetTrustedGitRepos(); len(got) != 1 {
		t.Errorf("trusted repos after inert post-migration drift = %v, want the trust intact", got)
	}
}

// TestNotifyGitConfigRisk_V1MigrationFailuresFailClosed pins the fail-closed
// fallback: a v1 record whose identity cannot be recovered — the legacy
// bare-path form (no snapshot at all), a missing snapshot file, a corrupted
// snapshot (one that no longer hashes to its content-addressed name), or a
// snapshot this scanner cannot interpret — evicts the trust and warns with
// the unverifiable-trust reason instead of carrying the trust over.
func TestNotifyGitConfigRisk_V1MigrationFailuresFailClosed(t *testing.T) {
	setup := func(t *testing.T) (*FrontendAPI, string) {
		t.Helper()
		dir := t.TempDir()
		writeGitConfig(t, dir, "[core]\n\tfsmonitor = /tmp/evil\n")
		f, _, _ := newTestAPI(t)
		if err := f.TrustGitRepo(dir); err != nil {
			t.Fatalf("TrustGitRepo: %v", err)
		}
		return f, dir
	}
	expectFailClosed := func(t *testing.T, f *FrontendAPI, dir string) {
		t.Helper()
		rec := newRiskRecorder(t, f)
		f.notifyGitConfigRisk(GitConfigRiskSourceProject, dir)
		if !rec.fired {
			t.Fatal("an unverifiable v1 record must fail closed with a warning event")
		}
		if rec.data.Reason != gitConfigTrustUnverifiableReason {
			t.Errorf("reason = %q, want the unverifiable-trust reason", rec.data.Reason)
		}
		if len(rec.data.Findings) == 0 || rec.data.Findings[0].Key != "(trust unverifiable)" {
			t.Errorf("findings = %+v, want the (trust unverifiable) marker", rec.data.Findings)
		}
		if got := f.GetTrustedGitRepos(); len(got) != 0 {
			t.Errorf("trusted repos = %v, want empty (evicted fail-closed)", got)
		}
		if gittrust.IsTrusted(dir) {
			t.Error("expected the repo to be unregistered from the git trust registry")
		}
	}

	t.Run("legacy bare-path record (no snapshot at all)", func(t *testing.T) {
		f, dir := setup(t)
		setTrustEntryFingerprints(t, f, "", "")
		expectFailClosed(t, f, dir)
	})

	t.Run("missing snapshot file", func(t *testing.T) {
		f, dir := setup(t)
		entry := downgradeTrustEntryToV1(t, f)
		if err := os.Remove(filepath.Join(config.GitConfigSnapshotsDir(f.agentDir), entry.Fingerprint)); err != nil {
			t.Fatalf("removing the raw snapshot file: %v", err)
		}
		expectFailClosed(t, f, dir)
	})

	t.Run("corrupted snapshot (hash mismatch)", func(t *testing.T) {
		f, dir := setup(t)
		entry := downgradeTrustEntryToV1(t, f)
		if err := os.WriteFile(filepath.Join(config.GitConfigSnapshotsDir(f.agentDir), entry.Fingerprint), []byte("[core]\n\tfsmonitor = /tmp/evil\n\tuserInjected = yes\n"), 0o644); err != nil {
			t.Fatalf("corrupting the raw snapshot file: %v", err)
		}
		expectFailClosed(t, f, dir)
	})

	t.Run("snapshot this scanner cannot interpret", func(t *testing.T) {
		f, dir := setup(t)
		downgradeTrustEntryToV1(t, f)
		// Craft a snapshot-shaped record whose file hashes to its name but
		// whose content is not a snapshot: the hash check passes, the
		// interpretation must still fail closed.
		garbage := []byte("totally not a snapshot\n")
		sum := sha256.Sum256(garbage)
		bogus := hex.EncodeToString(sum[:])
		if err := os.WriteFile(filepath.Join(config.GitConfigSnapshotsDir(f.agentDir), bogus), garbage, 0o644); err != nil {
			t.Fatalf("writing the garbage snapshot file: %v", err)
		}
		setTrustEntryFingerprints(t, f, bogus, "")
		expectFailClosed(t, f, dir)
	})
}
