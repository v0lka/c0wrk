package core

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/orchestration"
)

// planNormalizeCase is one normalizePlanDescription table row.
type planNormalizeCase struct {
	name string
	in   string
	want string
}

// runPlanNormalizeCases executes rows as subtests with exact-output matching.
func runPlanNormalizeCases(t *testing.T, cases []planNormalizeCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizePlanDescription(tc.in); got != tc.want {
				t.Errorf("normalizePlanDescription() mismatch:\n in:  %q\ngot:  %q\nwant: %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestNormalizePlanDescription_DemotesHeaders covers `# text` → `**text**`
// outside fences; a header whose text is a known label becomes the canonical
// `**Label**:` marker (with the separator's content, when present).
func TestNormalizePlanDescription_DemotesHeaders(t *testing.T) {
	runPlanNormalizeCases(t, []planNormalizeCase{
		{
			name: "unknown header becomes bold text",
			in:   "## Implement the parser\n\ncore/plan_description.go\n",
			want: "**Implement the parser**\n\ncore/plan_description.go\n",
		},
		{
			name: "header with bare known label becomes canonical marker",
			in:   "### What\nAdd the normalizer\n",
			want: "**What**:\nAdd the normalizer\n",
		},
		{
			name: "header label with colon keeps inline content",
			in:   "### Where: core/foo.go\n",
			want: "**Where**: core/foo.go\n",
		},
		{
			name: "header AC alias canonicalizes",
			in:   "## AC - all tests pass\n",
			want: "**Acceptance Criteria**: all tests pass\n",
		},
		{
			name: "hash run without space is not a header",
			in:   "#hashtag stays\n####### seven hashes stay\n",
			want: "#hashtag stays\n####### seven hashes stay\n",
		},
	})
}

// TestNormalizePlanDescription_CanonicalizesLabels covers plain-label lines
// (`:` or ` - ` separated, any spelling, including the AC alias) and bold
// non-canonical spellings; bullets are left alone.
func TestNormalizePlanDescription_CanonicalizesLabels(t *testing.T) {
	runPlanNormalizeCases(t, []planNormalizeCase{
		{
			name: "colon labels canonicalize case-insensitively",
			in:   "What: add it\nhow: extend Publish\n",
			want: "**What**: add it\n**How**: extend Publish\n",
		},
		{
			name: "AC with colon becomes Acceptance Criteria",
			in:   "AC:\n- green tests\n",
			want: "**Acceptance Criteria**:\n- green tests\n",
		},
		{
			name: "dash-separated label canonicalizes",
			in:   "how - extend Publish\n",
			want: "**How**: extend Publish\n",
		},
		{
			name: "spelled-out acceptance criteria canonicalizes",
			in:   "acceptance criteria: all tests pass\n",
			want: "**Acceptance Criteria**: all tests pass\n",
		},
		{
			name: "bold non-canonical label canonicalizes",
			in:   "**AC**: x\n",
			want: "**Acceptance Criteria**: x\n",
		},
		{
			name: "bullet line is not a label",
			in:   "- What: not rewritten\n",
			want: "- What: not rewritten\n",
		},
		{
			name: "bare label without separator is prose",
			in:   "What\n",
			want: "What\n",
		},
		{
			name: "label prefix inside a sentence is prose",
			in:   "TODO: fix What - the bug\n",
			want: "TODO: fix What - the bug\n",
		},
	})
}

// TestNormalizePlanDescription_StripsCheckboxes covers task-list checkboxes
// being stripped from prose list items (AC are declared as a plain unordered
// list): bullets keep their marker, checkboxed ordered items demote to
// bullets, non-task-list bracket tokens and fence bodies stay verbatim.
func TestNormalizePlanDescription_StripsCheckboxes(t *testing.T) {
	runPlanNormalizeCases(t, []planNormalizeCase{
		{
			name: "unchecked bullet item loses the checkbox",
			in:   "**Acceptance Criteria**:\n- [ ] tests pass\n",
			want: "**Acceptance Criteria**:\n- tests pass\n",
		},
		{
			name: "checked bullet item loses the checkbox",
			in:   "- [x] tests pass\n",
			want: "- tests pass\n",
		},
		{
			name: "uppercase X checkbox strips too",
			in:   "- [X] tests pass\n",
			want: "- tests pass\n",
		},
		{
			name: "star bullet checkbox strips",
			in:   "* [x] starred item\n",
			want: "* starred item\n",
		},
		{
			name: "indented nested checkbox strips with indent kept",
			in:   "  - [ ] nested item\n",
			want: "  - nested item\n",
		},
		{
			name: "checkboxed ordered item demotes to bullet",
			in:   "1. [ ] first criterion\n",
			want: "- first criterion\n",
		},
		{
			name: "bare checked item keeps just the marker",
			in:   "- [x]\n",
			want: "-\n",
		},
		{
			name: "double checkbox converges in one pass",
			in:   "- [ ] [x] weird nesting\n",
			want: "- weird nesting\n",
		},
		{
			name: "plain numbered item keeps its marker",
			in:   "1. first do this\n2. then that\n",
			want: "1. first do this\n2. then that\n",
		},
		{
			name: "bracket without checkbox semantics stays",
			in:   "- [] empty brackets stay\n- [y] odd token stays\n[ ] bare bracket stays\n",
			want: "- [] empty brackets stay\n- [y] odd token stays\n[ ] bare bracket stays\n",
		},
		{
			name: "checkbox inside a fence stays verbatim",
			in:   "```md\n- [ ] not an AC item\n```\n",
			want: "```md\n- [ ] not an AC item\n```\n",
		},
	})
}

// TestNormalizePlanDescription_FenceBalancing covers the unclosed trailing
// fence being closed with its own marker and length.
func TestNormalizePlanDescription_FenceBalancing(t *testing.T) {
	runPlanNormalizeCases(t, []planNormalizeCase{
		{
			name: "unclosed fence is closed",
			in:   "## Run\n\n```go\nfmt.Println()\n",
			want: "**Run**\n\n```go\nfmt.Println()\n```\n",
		},
		{
			name: "closing fence matches opening length",
			in:   "text\n````\ncode\n",
			want: "text\n````\ncode\n````\n",
		},
		{
			name: "tilde fence balances too",
			in:   "~~~\nverdict\n",
			want: "~~~\nverdict\n~~~\n",
		},
		{
			name: "shorter run does not close a longer fence",
			in:   "````\n```\nstill inside\n",
			want: "````\n```\nstill inside\n````\n",
		},
	})
}

// TestNormalizePlanDescription_FenceContentVerbatim covers fence bodies being
// untouched: headers, labels and blank runs inside a fence survive as-is.
func TestNormalizePlanDescription_FenceContentVerbatim(t *testing.T) {
	in := "Before.\n\n```sh\n# not a header\nWhat: also not a label\n\n\n\n\n```\n\nAfter.\n"
	want := "Before.\n\n```sh\n# not a header\nWhat: also not a label\n\n\n\n\n```\n\nAfter.\n"
	if got := normalizePlanDescription(in); got != want {
		t.Errorf("fence body must stay verbatim:\n in:  %q\ngot:  %q\nwant: %q", in, got, want)
	}
}

// TestNormalizePlanDescription_BlankRuns covers runs of 3+ blank lines
// collapsing to one while shorter runs survive; fence bodies are exempt.
func TestNormalizePlanDescription_BlankRuns(t *testing.T) {
	runPlanNormalizeCases(t, []planNormalizeCase{
		{
			name: "two blank lines survive",
			in:   "a\n\n\nb\n",
			want: "a\n\n\nb\n",
		},
		{
			name: "three blank lines collapse to one",
			in:   "a\n\n\n\nb\n",
			want: "a\n\nb\n",
		},
		{
			name: "trailing blank run collapses",
			in:   "a\n\n\n\n",
			want: "a\n\n",
		},
		{
			name: "missing final terminator is preserved",
			in:   "a\n\n\n\nb",
			want: "a\n\nb",
		},
	})
}

// TestNormalizePlanDescription_Idempotent covers normalize(normalize(x)) ==
// normalize(x): every canonical emission must be a fixed point of the
// normalizer.
func TestNormalizePlanDescription_Idempotent(t *testing.T) {
	inputs := []string{
		"## What\nAdd the normalizer\n\nWhere: core/\n\nHow - wire Publish\n\nAC:\n- tests green\n\n\n\nTrailing blanks collapse\n",
		"**Acceptance Criteria**:\n- [ ] a\n- [x] b\n",
		"### Where: core/foo.go\n\n```sh\ncode\n",
		"**What**: already canonical\n**Acceptance Criteria**:\n- bullet\n",
		"Run a no-op build so later measurements are stable.\n",
		"plain text, no structure at all\n",
	}
	for _, in := range inputs {
		once := normalizePlanDescription(in)
		if twice := normalizePlanDescription(once); twice != once {
			t.Errorf("normalization is not idempotent:\n in:   %q\nonce:  %q\ntwice: %q", in, once, twice)
		}
	}
}

// TestNormalizePlanDescription_CleanInputNoOp covers the serializer-test
// descriptions: already-canonical prose and markers must pass through
// unchanged, so plan_serializer_test.go fixtures are untouched by Publish.
func TestNormalizePlanDescription_CleanInputNoOp(t *testing.T) {
	runPlanNormalizeCases(t, []planNormalizeCase{
		{
			name: "serializer fixture prose",
			in:   "Run a no-op build so later measurements are stable.",
			want: "Run a no-op build so later measurements are stable.",
		},
		{
			name: "empty input",
			in:   "",
			want: "",
		},
		{
			name: "canonical multi-field description",
			in:   "**What**: do x\n**Where**: core/foo.go\n**How**: edit files\n**Acceptance Criteria**:\n- test a\n- test b\n",
			want: "**What**: do x\n**Where**: core/foo.go\n**How**: edit files\n**Acceptance Criteria**:\n- test a\n- test b\n",
		},
		{
			name: "bold label with inner colon stays",
			in:   "**What:**\nbody\n",
			want: "**What:**\nbody\n",
		},
	})
}

// publishNormalizeEmitter is a mockEmitter that captures PlanGenerated
// payloads for assertion.
type publishNormalizeEmitter struct {
	mockEmitter
	steps []orchestration.PlanStepEvent
}

func (e *publishNormalizeEmitter) PlanGenerated(_ int, steps []orchestration.PlanStepEvent) {
	e.steps = steps
}

// TestPublish_NormalizesPlanDescriptions verifies that conductorPublisher.Publish
// runs every description through normalizePlanDescription, so the plan file,
// the PlanGenerated events, and the blackboard plan all carry the same
// normalized text.
func TestPublish_NormalizesPlanDescriptions(t *testing.T) {
	bb := orchestration.NewMapBlackboard()
	emitter := &publishNormalizeEmitter{}
	p := &conductorPublisher{emitter: emitter, bb: bb, plansDir: t.TempDir()}

	raw := "## What\nAdd the normalizer\n\nAC:\n- [ ] tests green"
	want := "**What**:\nAdd the normalizer\n\n**Acceptance Criteria**:\n- tests green"
	path, err := p.Publish(context.Background(), []tools.PlanTaskInput{
		{ID: "s1", Summary: "Normalize", Description: raw},
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	md, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read plan file: %v", err)
	}
	if !strings.Contains(string(md), want) {
		t.Errorf("plan file must carry the normalized description;\nwant fragment %q\n--- file ---\n%s", want, md)
	}
	if strings.Contains(string(md), "## What") || strings.Contains(string(md), "AC:") || strings.Contains(string(md), "- [ ]") {
		t.Errorf("plan file still carries unnormalized text:\n%s", md)
	}

	if len(emitter.steps) != 1 || emitter.steps[0].Description != want {
		t.Errorf("PlanGenerated events must carry the normalized description, got %+v", emitter.steps)
	}
	plan := bb.GetPlan()
	if plan == nil || len(plan.Steps) != 1 || plan.Steps[0].Description != want {
		t.Errorf("blackboard plan must carry the normalized description, got %+v", plan)
	}
}
