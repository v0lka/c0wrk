# Handling Different Entry Points

Exploration starts wherever the user is. Recognize the entry point, adapt to it,
and — in every case — ground the discussion in the project's spec system before
theorizing. The examples below are illustrative of tone and shape, not scripts.

```
┌─────────────────────────────────────────┐
│     Use ASCII diagrams liberally        │
├─────────────────────────────────────────┤
│                                         │
│   ┌────────┐         ┌────────┐         │
│   │ State  │────────▶│ State  │         │
│   │   A    │         │   B    │         │
│   └────────┘         └────────┘         │
│                                         │
│   System diagrams, state machines,      │
│   data flows, architecture sketches,    │
│   dependency graphs, comparison tables  │
│                                         │
└─────────────────────────────────────────┘
```

---

## A vague idea

The user names a space, not a problem. Map the space, then ask what's driving it —
and check what the specs already say about it.

```
User: I'm thinking about adding caching to this API

You: Caching is a big space. Let me think about this...

     CACHING STRATEGIES
     ════════════════════════════════════════════

     Client-Side      Server Memory       Persistent
        │                 │                  │
        ▼                 ▼                  ▼
    ┌────────┐       ┌────────┐        ┌────────┐
    │Browser │       │ In-Mem │        │ Redis  │
    │LocalSt │       │ Map/LRU│        │Memcache│
    │"Reduce │       │"Fast   │        │"Shared │
    │ Load"  │       │ Access"│        │ State" │
    └────────┘       └────────┘        └────────┘
        │                 │                  │
     Simple          Moderate            Complex

     What's driving this? Performance? Cost? Scale?
```

**Spec-aware move:** before recommending a strategy, consult `specs/`. Caching
touches data flow and possibly a boundary — read `specs/architecture/data-flow.md`
and the relevant contract. Surface any invariant it must respect (e.g. "reads
always reflect the latest write", or a consistency rule in a contract). If caching
is not covered by any spec, flag it:

```
UNDOCUMENTED: response caching — no spec coverage
```

---

## A specific problem

The user reports a symptom. Investigate the code — but let the specs tell you what
the intended behavior is, so that "the code is slow" and "the code is wrong" stay
distinguishable.

```
User: Users are complaining about slow page loads

You: [reads specs first: specs/domains/*/README.md for the request flow,
      specs/architecture/data-flow.md for the documented lifecycle]
     [then reads code to confirm]

     ┌─────────────────────────────────────────────┐
     │         CURRENT REQUEST FLOW                │
     └──────────────┬──────────────────────────────┘
                    │
                    ▼
           ┌──────────────────┐
           │   Auth Check     │
           └────────┬─────────┘
                    │
                    ▼
           ┌──────────────────┐
           │  N+1 DB Queries  │ ← Problem?
           └────────┬─────────┘
                    │
                    ▼
           ┌──────────────────┐
           │ Heavy Transform  │ ← Problem?
           └────────┬─────────┘
                    │
                    ▼
           ┌──────────────────┐
           │  Render (2MB)    │ ← Problem?
           └──────────────────┘

     Three potential bottlenecks. Have you profiled?
```

**Spec-aware move:** the documented flow is your reference model. If the spec
claims a step that the code skips (or vice versa), that is drift — note it and
suggest `spec-check`, but do not fix it during exploration. If the spec says
"the request path issues a single query", the N+1 above is a documented-invariant
violation, which is a stronger finding than "slow".

---

## A stuck implementation

The user is mid-change and lost. Step back, trace the dependency web, and offer a
smaller aperture — the spec system often supplies the missing structure.

```
User: This refactoring is spiraling out of control

You: [reads the recent changes and the specs for the affected domain]

     You started by extracting one function...
     ...and now you're touching 15 files.

     Let me trace the dependency web:

     [draws a diagram showing the entanglement]

     Want to step back? We could:
     - Find a smaller first step
     - Identify a seam to cut
     - Enter Plan Mode to map phases
```

**Spec-aware move:** contracts exist precisely to make "seams" explicit. Read
`specs/contracts/` for the boundary being crossed — the contract's *Breaking Change
Checklist* often explains the ripple. If the refactor crosses a documented
boundary in a way the contract forbids, the scope creep has a name; name it.

---

## An options comparison

The user wants a recommendation between approaches. Reject the generic answer;
get context, compare concretely, and filter the options through the invariants.

```
User: Should we use REST or GraphQL for this API?

You: Generic answer is boring. What's the context?

User: Internal tool, small team, mostly CRUD operations

You: That changes everything.

     ┌─────────────────────────────────────────────────┐
     │       API PATTERN FOR INTERNAL TOOLS            │
     └─────────────────────────────────────────────────┘

                  REST            GraphQL
     Learning     easy ✓          steep ✗
     Tooling      mature ✓        good
     Flexibility  rigid           dynamic ✓
     Setup        minimal ✓       complex
     Team size    any ✓           needs buy-in

     For internal CRUD with a small team?
     REST. Save GraphQL for when flexibility becomes pain.

     Unless... are there lots of nested relationships?
```

**Spec-aware move:** before recommending, check `specs/architecture/` for layer
invariants and `specs/contracts/` for the existing API boundary. An option that
contradicts a documented invariant or breaking-change rule is not really on the
table without a decision to change it. Add a constraints row to the comparison
table:

```
                  REST            GraphQL
     Invariants   respects ✓      changes contract ✗ (needs ADR)
```
