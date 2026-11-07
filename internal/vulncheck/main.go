// Command vulncheck is c0wrk's Go dependency vulnerability gate.
//
// It runs the pinned govulncheck in JSON mode and decides the outcome itself,
// so one advisory that is unreachable-by-construction in this build can be
// carried under a REVIEWED, DATED exception instead of disabling the gate or
// forking a dependency: govulncheck has no ignore flag, and a repo-wide waiver
// would hide the next finding as well.
//
// The allowlist is fail-closed in every direction:
//
//   - a finding with no matching entry fails the gate;
//   - an entry whose review_by date has passed fails the gate — an exception is
//     a decision with an expiry, never a permanent waiver;
//   - an entry that matches no finding fails the gate, so a fixed upstream
//     advisory deletes its entry instead of leaving it to rot;
//   - an unreadable, malformed, or unknown-field allowlist fails the gate.
//
// Usage (the Makefile passes the pinned govulncheck version, which stays the
// single source of truth for the scan toolchain):
//
//	make vulncheck
//	go run ./internal/vulncheck -govulncheck-version v1.7.0
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"
)

const (
	// defaultAllowlistPath is resolved from the repository root, which is the
	// working directory of `make vulncheck` and of the CI security job.
	defaultAllowlistPath = "internal/vulncheck/allowlist.json"
	// defaultScanLevel matches govulncheck's own default: only vulnerabilities
	// whose entry points are reachable from this module's code are reported.
	defaultScanLevel = "symbol"
	// scanTimeout bounds the nested `go run` (compiling govulncheck can take
	// minutes on a cold module cache; CI allows 30 for the whole job).
	scanTimeout = 20 * time.Minute
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		printf(os.Stderr, "vulncheck: FAIL: %v\n", err)
		os.Exit(1)
	}
}

// printf writes one line of the report. A failed write (a closed pipe, a full
// disk) must never mask the gate's verdict, so the write error is deliberately
// dropped: the exit code is the machine-readable result and the report is a
// convenience for the human reading the CI log.
func printf(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}

// run wires flags, the allowlist, and the scan together. The decision logic
// itself lives in evaluate, which is pure so it can be tested without a
// network or a toolchain.
func run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("vulncheck", flag.ContinueOnError)
	fs.SetOutput(stderr)
	version := fs.String("govulncheck-version", "", "pinned govulncheck version to run against the official vulnerability database (for example v1.7.0)")
	allowlistPath := fs.String("allowlist", defaultAllowlistPath, "reviewed exception list, relative to the repository root")
	scanLevel := fs.String("scan", defaultScanLevel, "govulncheck scan level: symbol, package, or module")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *version == "" {
		return errors.New("-govulncheck-version is required: the pin lives in the Makefile")
	}

	allow, err := loadAllowlist(*allowlistPath)
	if err != nil {
		return err
	}

	printf(stdout, "vulncheck: scanning ./... with govulncheck@%s (%s)\n", *version, *scanLevel)
	findings, summaries, err := scan(*version, *scanLevel)
	if err != nil {
		return err
	}

	verdict := evaluate(findings, allow, time.Now())
	if verdict.failed() {
		writeVerdict(stderr, verdict, summaries)
		return fmt.Errorf("%d unresolved finding(s) and %d stale exception(s)", len(verdict.uncovered)+len(verdict.expired), len(verdict.stale))
	}
	writeVerdict(stdout, verdict, summaries)
	return nil
}

// finding is one distinct (advisory, module) pair reported by govulncheck.
type finding struct {
	id      string
	module  string
	version string
}

// decision is the gate's verdict for one finding. entry is nil when no
// exception covers it; problem is empty for a covered finding.
type decision struct {
	finding finding
	entry   *allowlistEntry
	problem string
}

// verdict is the complete outcome of one run.
type verdict struct {
	allowed   []decision // covered by an unexpired exception
	uncovered []decision // no exception, or an expired one
	expired   []decision // covered syntactically, but the review date passed
	stale     []allowlistEntry
}

func (v verdict) failed() bool {
	return len(v.uncovered) > 0 || len(v.expired) > 0 || len(v.stale) > 0
}

// evaluate decides every finding against the allowlist at the given instant.
// Findings are matched on the advisory ID and — when the entry names one — the
// module path, so an exception can never drift onto a different dependency.
func evaluate(findings []finding, allow *allowlist, now time.Time) verdict {
	var v verdict
	used := make(map[string]bool, len(allow.entries)) // entry ID+module that covered something
	for _, f := range findings {
		entry := allow.match(f)
		switch {
		case entry == nil:
			v.uncovered = append(v.uncovered, decision{finding: f, problem: "no reviewed exception in the allowlist"})
		case entry.expired(now):
			used[entry.key()] = true
			v.expired = append(v.expired, decision{finding: f, entry: entry})
		default:
			used[entry.key()] = true
			v.allowed = append(v.allowed, decision{finding: f, entry: entry})
		}
	}
	for _, entry := range allow.entries {
		if !used[entry.key()] {
			v.stale = append(v.stale, entry)
		}
	}
	return v
}

// writeVerdict renders the human-readable report. Findings and exceptions are
// printed in full: the report is the audit trail for why a gate run passed or
// failed.
func writeVerdict(w io.Writer, v verdict, summaries map[string]string) {
	total := len(v.allowed) + len(v.uncovered) + len(v.expired)
	printf(w, "vulncheck: %d distinct finding(s)\n", total)
	for _, d := range v.allowed {
		writeDecision(w, "ALLOWED", d, summaries)
		printf(w, "           exception reviewed until %s\n", d.entry.reviewBy.Format(time.DateOnly))
	}
	for _, d := range v.uncovered {
		writeDecision(w, "UNCOVERED", d, summaries)
		printf(w, "           %s\n", d.problem)
	}
	for _, d := range v.expired {
		writeDecision(w, "EXPIRED", d, summaries)
		printf(w, "           the exception expired on %s: re-review the mitigation, then extend or remove the entry\n", d.entry.reviewBy.Format(time.DateOnly))
	}
	for _, entry := range v.stale {
		printf(w, "  STALE     %s  %s\n", entry.ID, entry.Module)
		printf(w, "           the entry matches no finding: the advisory is fixed upstream or no longer reported — delete the entry\n")
	}
	switch {
	case v.failed():
		printf(w, "vulncheck: dependency vulnerability gate failed\n")
	case len(v.allowed) == 0:
		printf(w, "vulncheck: OK — no findings\n")
	default:
		printf(w, "vulncheck: OK — %d finding(s) covered by %d reviewed exception(s)\n", len(v.allowed), countEntries(v.allowed))
	}
}

func writeDecision(w io.Writer, label string, d decision, summaries map[string]string) {
	printf(w, "  %-9s %s  %s@%s\n", label, d.finding.id, d.finding.module, d.finding.version)
	if summary := summaries[d.finding.id]; summary != "" {
		printf(w, "           %s\n", summary)
	}
}

// countEntries returns how many distinct allowlist entries covered findings.
func countEntries(ds []decision) int {
	seen := make(map[string]bool, len(ds))
	for _, d := range ds {
		seen[d.entry.key()] = true
	}
	return len(seen)
}

// allowlist is the reviewed exception list.
type allowlist struct {
	entries []allowlistEntry
}

// allowlistEntry is one accepted risk. All four fields are mandatory: an entry
// without a module, a reason, or a review date is not a review.
type allowlistEntry struct {
	ID       string `json:"id"`
	Module   string `json:"module"`
	Reason   string `json:"reason"`
	ReviewBy string `json:"review_by"`

	reviewBy time.Time
}

// key identifies the entry for matching and reporting.
func (e allowlistEntry) key() string { return e.ID + " " + e.Module }

// expired reports whether the review date has passed. Dates are compared as
// calendar days in UTC — the day a review is due is still valid, the day after
// is not.
func (e allowlistEntry) expired(now time.Time) bool {
	return now.UTC().Format(time.DateOnly) > e.reviewBy.Format(time.DateOnly)
}

// match returns the entry covering the finding, or nil. An entry naming no
// module is honoured for its ID alone; an entry naming a module must match it.
func (a *allowlist) match(f finding) *allowlistEntry {
	for i := range a.entries {
		entry := &a.entries[i]
		if entry.ID != f.id {
			continue
		}
		if entry.Module == "" || entry.Module == f.module {
			return entry
		}
	}
	return nil
}

// loadAllowlist reads and validates the exception list. Unknown fields are
// rejected so a typo ("reviewBy", "modules") fails instead of silently
// disabling the exception's metadata.
func loadAllowlist(path string) (*allowlist, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- operator-supplied path from a Makefile/CI flag.
	if err != nil {
		return nil, fmt.Errorf("reading the allowlist %s: %w", path, err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var file struct {
		Entries []allowlistEntry `json:"entries"`
	}
	if err := dec.Decode(&file); err != nil {
		return nil, fmt.Errorf("parsing the allowlist %s: %w", path, err)
	}
	if dec.More() {
		return nil, fmt.Errorf("parsing the allowlist %s: trailing content after the document", path)
	}
	for i := range file.Entries {
		entry := &file.Entries[i]
		switch {
		case entry.ID == "":
			return nil, fmt.Errorf("allowlist entry %d: id is required", i)
		case entry.Module == "":
			return nil, fmt.Errorf("allowlist entry %d (%s): module is required", i, entry.ID)
		case entry.Reason == "":
			return nil, fmt.Errorf("allowlist entry %d (%s): reason is required — an exception without a justification is not reviewable", i, entry.ID)
		case entry.ReviewBy == "":
			return nil, fmt.Errorf("allowlist entry %d (%s): review_by is required — exceptions expire", i, entry.ID)
		}
		parsed, err := time.Parse(time.DateOnly, entry.ReviewBy)
		if err != nil {
			return nil, fmt.Errorf("allowlist entry %d (%s): review_by %q is not a YYYY-MM-DD date: %w", i, entry.ID, entry.ReviewBy, err)
		}
		entry.reviewBy = parsed
	}
	return &allowlist{entries: file.Entries}, nil
}

// scan runs govulncheck and reduces its JSON stream to distinct findings plus
// the advisory summaries (keyed by ID) used for reporting.
func scan(version, level string) ([]finding, map[string]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), scanTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "go", "run", "golang.org/x/vuln/cmd/govulncheck@"+version,
		"-format", "json", "-scan", level, "./...")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	findings, summaries, parseErr := parseStream(&stdout)
	if parseErr != nil {
		return nil, nil, fmt.Errorf("decoding the govulncheck report: %w", parseErr)
	}
	if runErr != nil && len(findings) == 0 {
		// No findings and a failing exit can only mean the scan did not run —
		// a missing toolchain, an unreachable database, or a build failure.
		// Fail closed rather than reporting a clean scan that never happened.
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = "no diagnostic output"
		}
		return nil, nil, fmt.Errorf("running govulncheck@%s: %w (%s)", version, runErr, strings.ReplaceAll(detail, "\n", "; "))
	}
	return findings, summaries, nil
}

// parseStream decodes govulncheck's JSON output, which is a stream of
// standalone objects rather than one document. Only two shapes matter: an
// "osv" entry carries the advisory text, a "finding" entry is one reported
// (advisory, module) pair.
func parseStream(r io.Reader) ([]finding, map[string]string, error) {
	type frame struct {
		Module  string `json:"module"`
		Version string `json:"version"`
	}
	type jsonFinding struct {
		OSV    string  `json:"osv"`
		Trace  []frame `json:"trace"`
		Module string  `json:"module"`
	}
	type jsonOSV struct {
		ID      string `json:"id"`
		Summary string `json:"summary"`
	}
	type message struct {
		Finding *jsonFinding `json:"finding"`
		OSV     *jsonOSV     `json:"osv"`
	}

	dec := json.NewDecoder(r)
	seen := make(map[finding]bool)
	var findings []finding
	summaries := make(map[string]string)
	for {
		var msg message
		if err := dec.Decode(&msg); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, nil, err
		}
		if msg.OSV != nil && msg.OSV.ID != "" {
			summaries[msg.OSV.ID] = msg.OSV.Summary
		}
		if msg.Finding == nil || msg.Finding.OSV == "" {
			continue
		}
		f := finding{id: msg.Finding.OSV, module: msg.Finding.Module, version: ""}
		for _, frame := range msg.Finding.Trace {
			if f.module == "" && frame.Module != "" {
				f.module, f.version = frame.Module, frame.Version
			}
			if f.version == "" && frame.Module == f.module {
				f.version = frame.Version
			}
		}
		if seen[f] {
			continue
		}
		seen[f] = true
		findings = append(findings, f)
	}
	slices.SortFunc(findings, func(a, b finding) int {
		if c := strings.Compare(a.id, b.id); c != 0 {
			return c
		}
		return strings.Compare(a.module, b.module)
	})
	return findings, maps.Clone(summaries), nil
}
