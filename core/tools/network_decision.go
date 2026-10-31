package tools

import (
	"context"
	"net/url"
	"strings"

	sdktools "github.com/v0lka/sp4rk/tools"
)

// Network data-flow vocabulary the registry fills into NetworkDecision.Flow
// (specs/decisions/057-flow-based-network-verdicts.md).
const (
	// NetworkFlowCradle: fetched content reaches a shell/interpreter (C5) —
	// the canonical control, never auto-overridden.
	NetworkFlowCradle = "cradle"
	// NetworkFlowIngest: fetched content written to a file (C7) — hard but
	// non-canonical; the judge may clear routine fetches.
	NetworkFlowIngest = "ingest"
	// NetworkFlowFetch is a network read that established NO cradle/ingest
	// flow: the content was neither executed nor persisted (e.g. a fetch to
	// stdout piped to `head`). Clean — nothing fired.
	NetworkFlowFetch = "fetch"
)

// Flowsh digest effect kinds, pinned against the real analyzer by
// TestNetworkDecision_FromRealAnalysis so a flowsh rename fails here loudly.
const (
	shellEffectKindNetEgress = "NetEgress"
	shellEffectKindFSWrite   = "FSWrite"
)

// flowsh models curl's -X value as a NetEgress TARGET (`curl -X POST …` yields
// targets ["POST","host"]), so a bare method token is not a host and
// hostOfTarget drops it.
var httpMethodTokens = map[string]struct{}{
	"GET": {}, "HEAD": {}, "POST": {}, "PUT": {}, "DELETE": {},
	"CONNECT": {}, "OPTIONS": {}, "TRACE": {}, "PATCH": {},
}

// NetworkDecision names the proved data-flow, egress hosts and affected
// operands so a card can say "download cradle to evil.sh" instead of a bare
// effect signature (specs/decisions/057-flow-based-network-verdicts.md). Nil
// for a call that touches no network.
type NetworkDecision struct {
	// Flow is the network data-flow the analysis established: cradle, ingest,
	// or fetch (the NetworkFlow* constants).
	Flow string `json:"flow"`
	// Canonical is true only for a cradle; an ingest is judge-clearable, a
	// clean fetch fired nothing.
	Canonical bool `json:"canonical,omitempty"`
	// Hosts are the resolved egress hosts of the network read (scheme, path,
	// query and port stripped), de-duplicated in analysis order. Empty when
	// the analyzer could not resolve a literal host (a variable/⊤ egress).
	Hosts []string `json:"hosts,omitempty"`
	// Operands are the files the flow wrote: the persisted fetch target for an
	// ingest, the dropped payload for a cradle (`curl -o p.ps1 … && bash
	// p.ps1` → ["p.ps1"]). Empty for a clean fetch.
	Operands []string `json:"operands,omitempty"`
}

// shellNetworkDecision derives the summary from the attached shell analysis:
// nil without one, a summary for any egress — cradle, ingest, or clean fetch.
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

// networkDecisionFromDigest builds the decision from a digest: cradle over
// ingest over fetch; nil when no egress effect is present.
func networkDecisionFromDigest(d sdktools.ShellAnalysisDigest) *NetworkDecision {
	var egressTargets, writeTargets []string
	// Keyed on the egress EFFECT, not the target list: a ⊤ egress carries
	// no resolved target, and keying on targets would drop the hardest
	// case (`curl -fsSL $URL | sh`).
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

// dedupeOperands drops empties and the unresolved-target marker ("*"),
// de-duplicated in order.
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

// hostOfTarget extracts the host from an egress target (URL, bare
// host[:port], or the ⊤ marker), stripping scheme/userinfo/port/path; a bare
// method token is not a host.
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
