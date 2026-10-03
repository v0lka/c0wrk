package main

import "testing"

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
