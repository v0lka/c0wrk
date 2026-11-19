package tools

import (
	"context"

	sdktools "github.com/v0lka/sp4rk/tools"
)

// The delegated-subagent dispatch catalog: the fail-closed bridge between what
// a subagent's prompt ADVERTISES (its resolved descriptor set — the descriptor
// strip shapes what the model sees) and what its tool calls may actually
// DISPATCH through the shared session ToolRegistry. The registry consults the
// catalog injected into the call's context (Gate 2c, ahead of the system-group
// bypass) and refuses any name outside it: without the gate a hallucinated or
// smuggled call — a batch sub-call, a conductor-only tool stripped from the
// descriptors — would still execute under the session's policy gates, because
// the shared registry has no other knowledge of the delegation's grant. See
// specs/contracts/conductor-tools.md ("must never hold") and
// specs/domains/tool-system/README.md (gate pipeline).

// delegationCatalogKey carries the catalog via the tool-call context.
type delegationCatalogKey struct{}

// DelegationCatalog is the set of tool names a delegated subagent may
// dispatch. A nil Names map (present but empty) fails closed — nothing is
// dispatchable.
type DelegationCatalog struct {
	Names map[string]struct{}
}

// CatalogFromDescriptors builds the catalog from a task's advertised
// descriptor set — the exact list the subagent's prompt carries.
func CatalogFromDescriptors(descs []sdktools.ToolDescriptor) DelegationCatalog {
	names := make(map[string]struct{}, len(descs))
	for _, d := range descs {
		names[d.Name] = struct{}{}
	}
	return DelegationCatalog{Names: names}
}

// Allow adds a tool name to the catalog: the redelegating path re-adds
// delegate/cancel_delegation to the advertised set after resolveTaskTools
// stripped them (conductor.go runRedelegBlocking), so the dispatch catalog
// must mirror that extension or the child's own delegations would fail Gate 2c.
func (c DelegationCatalog) Allow(names ...string) {
	if c.Names == nil {
		return
	}
	for _, n := range names {
		c.Names[n] = struct{}{}
	}
}

// Allows reports whether name is dispatchable under the catalog.
func (c DelegationCatalog) Allows(name string) bool {
	_, ok := c.Names[name]
	return ok
}

// WithDelegationCatalog injects the catalog into the tool-call context; the
// ToolRegistry gate (Gate 2c) refuses calls that fall outside it.
func WithDelegationCatalog(ctx context.Context, catalog DelegationCatalog) context.Context {
	return context.WithValue(ctx, delegationCatalogKey{}, catalog)
}

// DelegationCatalogFromContext returns the injected catalog and whether the
// context carries one at all: no catalog means the call is NOT under a
// catalog-gated context and the gate must not engage (the Conductor's own
// dispatches and the E2S loop are unrestricted — each is self-consistent:
// the advertised set is the full session registry it dispatches into). Every
// other generative conduit — delegated subagents and the goal verifier's own
// pass — carries the catalog of its advertised descriptor set, and the gate
// refuses anything outside it.
func DelegationCatalogFromContext(ctx context.Context) (DelegationCatalog, bool) {
	if c, ok := ctx.Value(delegationCatalogKey{}).(DelegationCatalog); ok {
		return c, true
	}
	return DelegationCatalog{}, false
}
