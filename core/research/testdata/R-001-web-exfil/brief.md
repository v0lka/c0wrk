# [R-001] Web Exfil Detection

| Field | Value |
|---|---|
| **Identifier** | R-001 |
| **Status** | Active |
| **Problem domain** | Web application security |
| **Quarter** | 2025-Q2 |
| **Researcher(s)** | A. Researcher |
| **Related researches** | — |

---

## Research Question

Can client-side analysis of single-page web applications reliably detect
exfiltration channels before they reach the network?

## Success Criteria

Detection coverage of known exfil channels >= 90% with < 5% false-positive rate
on a curated benchmark.

## Scope Boundaries

### In scope

Bundled JavaScript analysis, runtime interception, service workers.

### Out of scope

Native mobile applications, server-side detection.

## Known Constraints

Three-month timebox; benchmark limited to 200 applications.

## Prior Art (Summary)

Several academic detectors exist but focus on network traffic, not client-side
bundle analysis.

*Detailed catalog: [prior-art.md](prior-art.md)*

## Implementation Plan

Findings will inform a detector module integrated into the analysis pipeline.

## Ethical Boundaries

Only synthetic and authorized test applications are used.

---

## Navigation

- [Hypothesis Graph](hypotheses/graph.md)
- [Prior Art & References](prior-art.md)
- [Final Report](report.md) *(created at completion)*
