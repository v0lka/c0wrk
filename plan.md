# Adjust Stage 1 Per-Tool Truncation Defaults for Security

## Context

Current `PerToolTruncation` defaults are too tight — `read_file` at 2000 lines, `ripgrep`/`glob`/`list_directory` at 200 lines trigger truncation on normal usage. The goal is to reposition these limits as **memory exhaustion attack prevention**, not token optimization. Normal project usage should never hit Stage 1 truncation.

## Change

One file, one block: [`backend/config/defaults.go`](file:///Users/vkochetkov/Repositories/c0wrk/backend/config/defaults.go#L263-L272).

Replace the map values:

```go
cfg.ToolLimits.PerToolTruncation = map[string]ToolTruncationConfig{
    "read_file":      {MaxLines: 50000, MaxBytes: 0},
    "ripgrep":        {MaxLines: 5000,  MaxBytes: 0},
    "glob":           {MaxLines: 5000,  MaxBytes: 0},
    "list_directory": {MaxLines: 5000,  MaxBytes: 0},
    "web_fetch":      {MaxLines: 0,     MaxBytes: 2097152},
    "bash_exec":      {MaxLines: 10000, MaxBytes: 0},
}
```

`web_fetch` remains unchanged (2 MiB already generous).

## Verification

- `go test ./backend/config/...` — verifies defaults logic
- `go build ./...` — ensures compilation
- Manual sanity: no existing tests assert specific PerToolTruncation values, so no test breakage expected
