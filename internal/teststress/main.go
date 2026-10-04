// Command teststress runs the bounded probabilistic test category and rejects
// missing, skipped or failed coverage. It is shared by Make and every CI OS.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"
)

const repetitions = 20

var required = []string{
	"TestStressReconfigureMCPProxyClient",
	"TestStressWriteManifestConcurrentWriters",
}

// testEvent is the consumed subset of the `go test -json` event stream. The
// stream is buffered instead of streamed: a clean run prints only the per-test
// coverage summary, while a failing run prints the buffered output of every
// failed or skipped test, so CI logs stay compact but fully diagnosable.
type testEvent struct {
	Action string
	Test   string
	Output string
}

type results struct {
	runs    map[string]int
	passes  map[string]int
	outputs map[string][]string
	failed  map[string]bool
	bad     bool
}

func newResults() *results {
	return &results{
		runs:    make(map[string]int),
		passes:  make(map[string]int),
		outputs: make(map[string][]string),
		failed:  make(map[string]bool),
	}
}

func (r *results) observe(e testEvent) {
	switch e.Action {
	case "run":
		r.runs[e.Test]++
	case "pass":
		if e.Test != "" {
			r.passes[e.Test]++
		}
	case "output":
		r.outputs[e.Test] = append(r.outputs[e.Test], e.Output)
	case "fail", "skip":
		r.bad = true
		r.failed[e.Test] = true
	}
}

// consume decodes the -json event stream into r. A clean end of stream is nil.
func (r *results) consume(stream io.Reader) error {
	decoder := json.NewDecoder(stream)
	for {
		var e testEvent
		if err := decoder.Decode(&e); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		r.observe(e)
	}
}

func (r *results) verify() error {
	if r.bad {
		return errors.New("stress run contains a failure or skip")
	}
	for _, name := range required {
		if r.runs[name] != repetitions || r.passes[name] != repetitions {
			return fmt.Errorf("stress coverage %s: run/pass = %d/%d, want %d/%d", name, r.runs[name], r.passes[name], repetitions, repetitions)
		}
	}
	return nil
}

// report renders the buffered output of every failed or skipped test. The
// empty test name covers package-level events (for example a race-detector
// report attributed to the package rather than a test).
func (r *results) report() string {
	if len(r.failed) == 0 {
		return ""
	}
	var b strings.Builder
	for _, name := range slices.Sorted(maps.Keys(r.failed)) {
		if name == "" {
			b.WriteString("package-level failure or skip:\n")
		} else {
			b.WriteString(name + " failed or was skipped:\n")
		}
		for _, out := range r.outputs[name] {
			b.WriteString(out)
		}
	}
	return b.String()
}

func (r *results) dumpFailures() {
	if report := r.report(); report != "" {
		fmt.Fprintln(os.Stderr, report)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "test", "-race", "-count=20", "-shuffle=on", "-json", "-timeout=8m", "-run", "^TestStress", "./core", "./core/embeddedllm")
	cmd.Stderr = os.Stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("open stress output: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start stress tests: %w", err)
	}
	r := newResults()
	if err := r.consume(out); err != nil {
		r.dumpFailures()
		cancel()
		waitErr := cmd.Wait()
		return fmt.Errorf("decode stress output: %w", errors.Join(err, waitErr))
	}
	if err := cmd.Wait(); err != nil {
		r.dumpFailures()
		return fmt.Errorf("stress tests: %w", err)
	}
	if err := r.verify(); err != nil {
		r.dumpFailures()
		return err
	}
	for _, name := range required {
		fmt.Printf("stress coverage: %s run=%d pass=%d\n", name, r.runs[name], r.passes[name])
	}
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
