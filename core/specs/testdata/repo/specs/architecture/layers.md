# Layers

Intro.

## Key Files

- `main.go` - the entry point
- `github.com/example/sdk/layer.go` - module path, exempt from the check
- `~/notes.md` - home path, exempt
- `<generated>/x.go` - placeholder, exempt

## Invariants

- See [the app domain](../domains/app.md) and [ADR-002](../decisions/002-two.md#decision).
- The [repo README](../../README.md) resolves outside the specs tree.
- Format example in a fence:

```markdown
- `fenced/entry.go` - never a Key Files entry
```
