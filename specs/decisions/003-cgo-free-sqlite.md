# ADR-003: CGO-Free SQLite

## Status

Accepted

## Context

c0wrk needs an embedded database for session persistence, message history, task state, and event logging. SQLite is the natural choice for a desktop application. The two main Go SQLite options are:

1. `mattn/go-sqlite3` — CGO binding (wraps C SQLite library)
2. `modernc.org/sqlite` — pure Go translation of SQLite (no CGO)

## Decision

Use `modernc.org/sqlite` (CGO-free SQLite) for all database operations.

## Consequences

**Positive:**

- Cross-compilation works without CGO toolchain (critical for `wails build` on macOS targeting multiple archs)
- No C compiler dependency for contributors
- Simpler CI/CD — no platform-specific C library management
- Single binary distribution (no dynamic library dependencies)
- `go test` works without CGO_ENABLED=1

**Negative:**

- Slightly slower than native C SQLite (~10-20% for write-heavy workloads)
- Larger binary size (SQLite compiled into Go)
- Less community usage than mattn (fewer Stack Overflow answers)
- Some SQLite extensions not available in pure Go translation

## Alternatives Considered

**mattn/go-sqlite3 (CGO)**: Better performance and wider adoption. Rejected because CGO breaks cross-compilation, complicates the build (requires C toolchain), and conflicts with Wails' build system expectations. The performance difference is negligible for c0wrk's workload (small writes, infrequent reads).

**Bolt/bbolt**: Simpler key-value store. Rejected because the data model benefits from SQL (relational queries across sessions, projects, messages).

**No database (file-based JSON)**: Simplest option. Rejected because concurrent access, querying, and data integrity become problems as the app grows.
