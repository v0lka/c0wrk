package main

// Tests for the vulnerability gate's decision logic. The gate is fail-closed in
// four directions (uncovered finding, expired exception, stale exception,
// malformed allowlist) and each one is pinned here: a regression that turns any
// of them into a pass would silently reopen the gate the exceptions exist to
// keep narrow.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// mustDate parses a date used by the fixtures.
func mustDate(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.DateOnly, value)
	if err != nil {
		t.Fatalf("time.Parse(%q) = %v", value, err)
	}
	return parsed
}

// entry builds a validated allowlist entry for the evaluate fixtures.
func entry(t *testing.T, id, module, reviewBy string) allowlistEntry {
	t.Helper()
	return allowlistEntry{ID: id, Module: module, Reason: "fixture", ReviewBy: reviewBy, reviewBy: mustDate(t, reviewBy)}
}

func TestEvaluate(t *testing.T) {
	now := mustDate(t, "2026-11-07")
	const id = "GO-2026-6911"
	const module = "github.com/mark3labs/mcp-go"
	found := finding{id: id, module: module, version: "v0.45.0"}

	tests := []struct {
		name       string
		findings   []finding
		entries    []allowlistEntry
		wantFailed bool
		wantAllow  int
		wantUncov  int
		wantExpire int
		wantStale  int
	}{
		{
			name:      "covered by an unexpired exception",
			findings:  []finding{found},
			entries:   []allowlistEntry{entry(t, id, module, "2027-05-07")},
			wantAllow: 1,
		},
		{
			name:      "review date is the deadline, not the day before it",
			findings:  []finding{found},
			entries:   []allowlistEntry{entry(t, id, module, "2026-11-07")},
			wantAllow: 1,
		},
		{
			name:       "expired exception fails even though it matches",
			findings:   []finding{found},
			entries:    []allowlistEntry{entry(t, id, module, "2026-11-06")},
			wantFailed: true,
			wantExpire: 1,
		},
		{
			name:       "finding with no exception fails",
			findings:   []finding{found},
			wantFailed: true,
			wantUncov:  1,
		},
		{
			name:       "exception naming another module does not cover the finding",
			findings:   []finding{found},
			entries:    []allowlistEntry{entry(t, id, "example.com/other", "2027-05-07")},
			wantFailed: true,
			wantUncov:  1,
			wantStale:  1,
		},
		{
			name:       "exception that matches nothing is stale",
			findings:   []finding{found},
			entries:    []allowlistEntry{entry(t, id, module, "2027-05-07"), entry(t, "GO-2025-0001", module, "2027-05-07")},
			wantFailed: true,
			wantAllow:  1,
			wantStale:  1,
		},
		{
			name:       "empty allowlist with an unfixed finding fails",
			findings:   []finding{found},
			wantFailed: true,
			wantUncov:  1,
		},
		{
			name:     "clean scan with an empty allowlist passes",
			findings: nil,
		},
		{
			name:      "module-less exception matches by advisory id alone",
			findings:  []finding{found},
			entries:   []allowlistEntry{entry(t, id, "", "2027-05-07")},
			wantAllow: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := evaluate(tt.findings, &allowlist{entries: tt.entries}, now)
			if got := v.failed(); got != tt.wantFailed {
				t.Errorf("failed() = %v, want %v (verdict %+v)", got, tt.wantFailed, v)
			}
			if len(v.allowed) != tt.wantAllow {
				t.Errorf("allowed = %d, want %d", len(v.allowed), tt.wantAllow)
			}
			if len(v.uncovered) != tt.wantUncov {
				t.Errorf("uncovered = %d, want %d", len(v.uncovered), tt.wantUncov)
			}
			if len(v.expired) != tt.wantExpire {
				t.Errorf("expired = %d, want %d", len(v.expired), tt.wantExpire)
			}
			if len(v.stale) != tt.wantStale {
				t.Errorf("stale = %d, want %d", len(v.stale), tt.wantStale)
			}
		})
	}
}

func TestLoadAllowlist(t *testing.T) {
	valid := `{
  "entries": [
    {"id": "GO-2026-6911", "module": "github.com/mark3labs/mcp-go", "reason": "not reachable", "review_by": "2027-05-07"}
  ]
}`
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{name: "valid", body: valid},
		{name: "empty list", body: `{"entries": []}`},
		{name: "unknown field", body: `{"entries": [{"id": "GO-2026-6911", "module": "m", "reason": "r", "reviewBy": "2027-05-07"}]}`, wantErr: "unknown field"},
		{name: "missing id", body: `{"entries": [{"module": "m", "reason": "r", "review_by": "2027-05-07"}]}`, wantErr: "id is required"},
		{name: "missing module", body: `{"entries": [{"id": "GO-1", "reason": "r", "review_by": "2027-05-07"}]}`, wantErr: "module is required"},
		{name: "missing reason", body: `{"entries": [{"id": "GO-1", "module": "m", "review_by": "2027-05-07"}]}`, wantErr: "reason is required"},
		{name: "missing review date", body: `{"entries": [{"id": "GO-1", "module": "m", "reason": "r"}]}`, wantErr: "review_by is required"},
		{name: "unparsable review date", body: `{"entries": [{"id": "GO-1", "module": "m", "reason": "r", "review_by": "07.05.2027"}]}`, wantErr: "is not a YYYY-MM-DD date"},
		{name: "malformed json", body: `{"entries": [`, wantErr: "parsing the allowlist"},
		{name: "trailing document", body: valid + "\n{}", wantErr: "trailing content"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "allowlist.json")
			if err := os.WriteFile(path, []byte(tt.body), 0o600); err != nil {
				t.Fatalf("writing the fixture = %v", err)
			}
			allow, err := loadAllowlist(path)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("loadAllowlist() = %v, want nil", err)
				}
				if allow == nil {
					t.Fatal("loadAllowlist() = nil allowlist")
				}
				return
			}
			if err == nil {
				t.Fatalf("loadAllowlist() = nil error, want one containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("loadAllowlist() error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}

	t.Run("missing file", func(t *testing.T) {
		_, err := loadAllowlist(filepath.Join(t.TempDir(), "absent.json"))
		if err == nil || !strings.Contains(err.Error(), "reading the allowlist") {
			t.Fatalf("loadAllowlist(missing) = %v, want a read error", err)
		}
	})
}

// TestParseStream pins the decoder against the shapes govulncheck emits: a
// stream of standalone objects (config, SBOM, osv, finding), with the module
// carried on the first trace frame and repeated findings per trace.
func TestParseStream(t *testing.T) {
	stream := `{"config": {"scanner_name": "govulncheck"}}
{"SBOM": {"modules": [{"path": "github.com/v0lka/c0wrk"}]}}
{"osv": {"id": "GO-2026-6911", "summary": "SSRF and credential leak via unvalidated Location header in github.com/mark3labs/mcp-go"}}
{"finding": {"osv": "GO-2026-6911", "trace": [{"module": "github.com/mark3labs/mcp-go", "version": "v0.45.0"}]}}
{"finding": {"osv": "GO-2026-6911", "trace": [{"module": "github.com/mark3labs/mcp-go", "version": "v0.45.0", "package": "github.com/mark3labs/mcp-go/client/transport"}, {"module": "github.com/v0lka/c0wrk", "version": ""}]}}
{"finding": {"osv": "GO-2026-0001", "fixed_version": "v1.2.3", "trace": [{"module": "example.com/dep", "version": "v1.0.0"}]}}
`

	findings, summaries, err := parseStream(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("parseStream() = %v", err)
	}
	if len(findings) != 2 {
		t.Fatalf("findings = %+v, want 2 distinct entries (repeated traces collapse)", findings)
	}
	if findings[0].id != "GO-2026-0001" || findings[0].module != "example.com/dep" || findings[0].version != "v1.0.0" {
		t.Errorf("findings[0] = %+v, want the sorted GO-2026-0001/example.com/dep entry", findings[0])
	}
	if findings[1].id != "GO-2026-6911" || findings[1].module != "github.com/mark3labs/mcp-go" || findings[1].version != "v0.45.0" {
		t.Errorf("findings[1] = %+v, want GO-2026-6911 on github.com/mark3labs/mcp-go@v0.45.0", findings[1])
	}
	if got := summaries["GO-2026-6911"]; !strings.Contains(got, "unvalidated Location header") {
		t.Errorf("summaries[GO-2026-6911] = %q, want the advisory summary", got)
	}

	if _, _, err := parseStream(strings.NewReader(`{"finding": `)); err == nil {
		t.Error("parseStream(truncated) = nil error, want a decode error")
	}
}
