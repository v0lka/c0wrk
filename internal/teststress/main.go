// Command teststress runs the bounded probabilistic test category and rejects
// missing, skipped or failed coverage. It is shared by Make and every CI OS.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"
)

const repetitions = 20

var required = []string{
	"TestStressReconfigureMCPProxyClient",
	"TestStressWriteManifestConcurrentWriters",
}

type testEvent struct {
	Action string
	Test   string
}

type results struct {
	runs   map[string]int
	passes map[string]int
	bad    bool
}

func newResults() *results {
	return &results{runs: make(map[string]int), passes: make(map[string]int)}
}

func (r *results) observe(e testEvent) {
	switch e.Action {
	case "run":
		r.runs[e.Test]++
	case "pass":
		if e.Test != "" {
			r.passes[e.Test]++
		}
	case "fail", "skip":
		r.bad = true
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
	decoder := json.NewDecoder(io.TeeReader(out, os.Stdout))
	for {
		var e testEvent
		if err := decoder.Decode(&e); err != nil {
			if !errors.Is(err, io.EOF) {
				cancel()
				waitErr := cmd.Wait()
				return fmt.Errorf("decode stress output: %w", errors.Join(err, waitErr))
			}
			break
		}
		r.observe(e)
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("stress tests: %w", err)
	}
	if err := r.verify(); err != nil {
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
