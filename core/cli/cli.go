// Package cli implements the unified command-line parser for the c0wrk
// binary: raw argv (os.Args[1:]) → a Request naming the action to perform.
// Fail-closed (an unrecognized argument is an error naming it); pure package
// (stdlib + core/version only). Grammar and invariants: specs/domains/cli.md.
package cli

import (
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/v0lka/c0wrk/core/version"
)

// Action identifies what the process should do after parsing.
type Action int

const (
	// ActionRun starts the normal application. It is the zero value so an
	// empty argv parses to a plain run request.
	ActionRun Action = iota
	// ActionHelp prints the usage text.
	ActionHelp
	// ActionVersion prints the version banner.
	ActionVersion
	// ActionSelfUpdate runs the staging self-updater — hidden re-exec
	// plumbing, never listed in help.
	ActionSelfUpdate
)

// SelfUpdateRequest carries the companion values of a --self-update
// invocation (populated exactly when Action is ActionSelfUpdate).
type SelfUpdateRequest struct {
	// PID is the parent (old) app's process id; the updater waits for its
	// exit before swapping.
	PID int
	// Stage is the staging directory (update archive + updater binary).
	Stage string
	// Target is the install root to replace.
	Target string
}

// Request is the fully parsed command line.
type Request struct {
	Action Action
	// SelfUpdate is non-nil exactly when Action is ActionSelfUpdate.
	SelfUpdate *SelfUpdateRequest
}

// Parse converts raw command-line arguments (os.Args[1:]) into a Request.
// The first action flag wins, value flags take the last occurrence, and the
// companion flags (--pid/--stage/--target) are validated only after the
// full scan — required when --self-update is selected, an error anywhere
// else; value errors abort the scan immediately. Any unrecognized argument
// is an error naming it. Grammar: specs/domains/cli.md.
func Parse(args []string) (Request, error) {
	req := Request{Action: ActionRun}
	chosen := false
	accept := func(a Action) {
		if !chosen {
			chosen = true
			req.Action = a
		}
	}

	var pid int
	var stage, target string
	var pidSeen, stageSeen, targetSeen bool
	var companion string // first companion flag seen without --self-update

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-h", "--help":
			accept(ActionHelp)
		case "-v", "--version":
			accept(ActionVersion)
		case "--self-update":
			accept(ActionSelfUpdate)
		case "--pid":
			if companion == "" {
				companion = "--pid"
			}
			val, ok := nextArg(args, &i)
			if !ok {
				return Request{}, errors.New("--pid requires a value")
			}
			p, perr := parseInt(val)
			if perr != nil {
				return Request{}, fmt.Errorf("invalid --pid %q: %w", val, perr)
			}
			pid, pidSeen = p, true
		case "--stage":
			if companion == "" {
				companion = "--stage"
			}
			val, ok := nextArg(args, &i)
			if !ok {
				return Request{}, errors.New("--stage requires a value")
			}
			if val == "" {
				return Request{}, errors.New("--stage requires a non-empty value")
			}
			stage, stageSeen = val, true
		case "--target":
			if companion == "" {
				companion = "--target"
			}
			val, ok := nextArg(args, &i)
			if !ok {
				return Request{}, errors.New("--target requires a value")
			}
			if val == "" {
				return Request{}, errors.New("--target requires a non-empty value")
			}
			target, targetSeen = val, true
		default:
			return Request{}, errors.New("unknown argument: " + args[i])
		}
	}

	if req.Action == ActionSelfUpdate {
		if !pidSeen {
			return Request{}, errors.New("--self-update requires --pid")
		}
		if !stageSeen {
			return Request{}, errors.New("--self-update requires --stage")
		}
		if !targetSeen {
			return Request{}, errors.New("--self-update requires --target")
		}
		req.SelfUpdate = &SelfUpdateRequest{PID: pid, Stage: stage, Target: target}
		return req, nil
	}
	if companion != "" {
		return Request{}, errors.New("unknown argument: " + companion)
	}
	return req, nil
}

// nextArg consumes and returns the value following args[*i] (a flag's
// argument), reporting whether one exists.
func nextArg(args []string, i *int) (string, bool) {
	if *i+1 >= len(args) {
		return "", false
	}
	*i++
	return args[*i], true
}

// parseInt parses a strict positive integer: decimal digits only, with
// strconv.Atoi providing the platform-int overflow bound.
func parseInt(s string) (int, error) {
	if s == "" {
		return 0, errors.New("not a positive integer")
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, errors.New("not a positive integer")
		}
	}
	// Atoi rejects values beyond the platform int range — the overflow bound
	// a hand-rolled accumulation would lack.
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, errors.New("not a positive integer")
	}
	return n, nil
}

// helpText is the user-facing usage; --self-update and its companions are
// hidden re-exec plumbing (specs/domains/cli.md).
const helpText = `Usage: c0wrk [flags]

Flags:
  -h, --help      Print this help and exit
  -v, --version   Print version and exit
`

// PrintHelp writes the usage text to w.
func PrintHelp(w io.Writer) {
	_, _ = fmt.Fprint(w, helpText)
}

// PrintVersion writes the one-line version banner (core/version metadata).
func PrintVersion(w io.Writer) {
	_, _ = fmt.Fprintf(w, "c0wrk %s (commit %s, built %s)\n", version.Version, version.GitCommit, version.BuildDate)
}
