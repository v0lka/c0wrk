package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestResultsCoverage(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*results)
		wantOK bool
	}{
		{name: "all repetitions pass", wantOK: true},
		{name: "missing test", change: func(r *results) { delete(r.runs, required[0]); delete(r.passes, required[0]) }},
		{name: "incomplete passes", change: func(r *results) { r.passes[required[0]]-- }},
		{name: "extra retry", change: func(r *results) { r.runs[required[0]]++; r.passes[required[0]]++ }},
		{name: "skipped test", change: func(r *results) { r.observe(testEvent{Action: "skip", Test: required[0]}) }},
		{name: "package failure", change: func(r *results) { r.observe(testEvent{Action: "fail"}) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newResults()
			for _, name := range required {
				for range repetitions {
					r.observe(testEvent{Action: "run", Test: name})
					r.observe(testEvent{Action: "pass", Test: name})
				}
			}
			if tc.change != nil {
				tc.change(r)
			}
			if err := r.verify(); (err == nil) != tc.wantOK {
				t.Errorf("verify(%s) = %v, want success=%t", tc.name, err, tc.wantOK)
			}
		})
	}
}

// TestConsumeAndReport pins the compact-log contract: the buffered stream is
// silent on success, and a failing run reports the buffered output of every
// failed or skipped test (package-level events included) while passing tests
// keep their output out of the report.
func TestConsumeAndReport(t *testing.T) {
	events := []testEvent{
		{Action: "run", Test: required[0]},
		{Action: "output", Test: required[0], Output: "    install_test.go:42: concurrent writer lost update\n"},
		{Action: "fail", Test: required[0]},
		{Action: "run", Test: required[1]},
		{Action: "pass", Test: required[1]},
		{Action: "output", Test: required[1], Output: "--- PASS: passing noise must stay buffered\n"},
		{Action: "output", Test: "", Output: "WARNING: DATA RACE\n"},
		{Action: "fail", Test: ""},
		{Action: "run", Test: "TestStressSkipped"},
		{Action: "skip", Test: "TestStressSkipped"},
	}
	var stream strings.Builder
	encoder := json.NewEncoder(&stream)
	for _, e := range events {
		if err := encoder.Encode(e); err != nil {
			t.Fatalf("encode event: %v", err)
		}
	}
	r := newResults()
	if err := r.consume(strings.NewReader(stream.String())); err != nil {
		t.Fatalf("consume: %v", err)
	}
	if !r.bad {
		t.Fatal("consume marked run as clean despite fail/skip events")
	}
	report := r.report()
	for _, want := range []string{
		required[0] + " failed or was skipped:",
		"install_test.go:42: concurrent writer lost update",
		"package-level failure or skip:",
		"WARNING: DATA RACE",
		"TestStressSkipped failed or was skipped:",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("report lacks %q; report:\n%s", want, report)
		}
	}
	if strings.Contains(report, "passing noise must stay buffered") {
		t.Errorf("report leaks output of a passing test; report:\n%s", report)
	}

	clean := newResults()
	for _, name := range required {
		for range repetitions {
			clean.observe(testEvent{Action: "run", Test: name})
			clean.observe(testEvent{Action: "pass", Test: name})
		}
	}
	if report := clean.report(); report != "" {
		t.Errorf("clean run report = %q, want empty", report)
	}
	if err := clean.consume(strings.NewReader("")); err != nil {
		t.Errorf("consume empty stream: %v", err)
	}
}
