package papers

import "testing"

func TestNormalizePaperID(t *testing.T) {
	cases := map[string]string{
		"P-001":   "P-001",
		"p-1":     "P-001",
		"P1":      "P-001",
		"P001":    "P-001",
		"p-10":    "P-010",
		"P-1000":  "P-1000",
		"":        "",
		"nope":    "",
		"title-x": "",
		// Anchored: a slug or directory base that merely contains a `p-<n>` run
		// must NOT mint a spurious canonical id (issue 14).
		"deep-2":     "",
		"group-2":    "",
		"clip-2":     "",
		"setup-1":    "",
		"P-001-beta": "",
	}
	for in, want := range cases {
		if got := NormalizePaperID(in); got != want {
			t.Errorf("NormalizePaperID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeResearchID(t *testing.T) {
	cases := map[string]string{
		"H-001": "H-001",
		"h2":    "H-002",
		"H2":    "H-002",
		"H-10":  "H-010",
		"":      "",
		"xyz":   "",
		// Anchored: "path-1" must not normalize to H-001 (issue 14).
		"path-1":  "",
		"graph-3": "",
	}
	for in, want := range cases {
		if got := NormalizeResearchID(in); got != want {
			t.Errorf("NormalizeResearchID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Attention Is All You Need": "attention-is-all-you-need",
		"  Spaced  Out  ":           "spaced-out",
		"a/b/c":                     "a-b-c",
		"Hello, World!":             "hello-world",
		"C++ & Rust":                "c-rust",
		"":                          "",
		"---":                       "",
		"Привет Мир":                "привет-мир", // unicode letters preserved
	}
	for in, want := range cases {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidSlug(t *testing.T) {
	valid := []string{"a", "p-001", "attention-is-all-you-need", "привет", "abc123"}
	for _, s := range valid {
		if !ValidSlug(s) {
			t.Errorf("ValidSlug(%q) = false, want true", s)
		}
	}
	invalid := []string{"", ".", "..", ".hidden", "a/b", `a\b`, "../evil", "with space", "\x00bad", "---"}
	for _, s := range invalid {
		if ValidSlug(s) {
			t.Errorf("ValidSlug(%q) = true, want false", s)
		}
	}
}

func TestPaperRecordResolvedSlug(t *testing.T) {
	t.Run("explicit valid slug wins", func(t *testing.T) {
		r := PaperRecord{Slug: "my-slug", Title: "Ignored Title"}
		if got := r.ResolvedSlug(); got != "my-slug" {
			t.Errorf("got %q, want %q", got, "my-slug")
		}
	})
	t.Run("falls back to title", func(t *testing.T) {
		r := PaperRecord{Title: "Attention Is All You Need"}
		if got := r.ResolvedSlug(); got != "attention-is-all-you-need" {
			t.Errorf("got %q, want %q", got, "attention-is-all-you-need")
		}
	})
	t.Run("falls back to id", func(t *testing.T) {
		r := PaperRecord{ID: "P-007"}
		if got := r.ResolvedSlug(); got != "p-007" {
			t.Errorf("got %q, want %q", got, "p-007")
		}
	})
	t.Run("invalid slug ignored", func(t *testing.T) {
		r := PaperRecord{Slug: "../evil"}
		if got := r.ResolvedSlug(); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})
}

func TestNormalizeVerdictConfidenceMode(t *testing.T) {
	if got := NormalizeVerdict(" Accept "); got != VerdictAccepted {
		t.Errorf("NormalizeVerdict = %q, want %q", got, VerdictAccepted)
	}
	if got := NormalizeVerdict("**refuted**"); got != VerdictRejected {
		t.Errorf("NormalizeVerdict = %q, want %q", got, VerdictRejected)
	}
	if got := NormalizeVerdict("something-else"); got != Verdict("something-else") {
		t.Errorf("NormalizeVerdict passthrough = %q", got)
	}
	if got := NormalizeConfidence("Med"); got != ConfidenceMedium {
		t.Errorf("NormalizeConfidence = %q, want %q", got, ConfidenceMedium)
	}
	if got := NormalizeMode("DEEP"); got != ModeDeep {
		t.Errorf("NormalizeMode = %q, want %q", got, ModeDeep)
	}
}

func TestPaperLibraryGetAndByResearchID(t *testing.T) {
	lib := &PaperLibrary{
		Papers: []*PaperRecord{
			{ID: "P-001", Slug: "attention", Title: "Attention", ResearchIDs: []string{"H-001", "H-002"}},
			{ID: "P-002", Slug: "bert", Title: "BERT", ResearchIDs: []string{"H-002"}},
		},
	}
	if got := lib.Get("P-001"); got == nil || got.Slug != "attention" {
		t.Errorf("Get by id failed: %+v", got)
	}
	if got := lib.Get("bert"); got == nil || got.ID != "P-002" {
		t.Errorf("Get by slug failed: %+v", got)
	}
	if got := lib.Get("p2"); got == nil || got.Slug != "bert" {
		t.Errorf("Get by normalized id failed: %+v", got)
	}
	if got := lib.Get("missing"); got != nil {
		t.Errorf("Get(missing) = %+v, want nil", got)
	}
	if got := lib.ByResearchID("h2"); len(got) != 2 {
		t.Errorf("ByResearchID(h2) = %d papers, want 2", len(got))
	}
	if got := lib.ByResearchID("H-001"); len(got) != 1 {
		t.Errorf("ByResearchID(H-001) = %d papers, want 1", len(got))
	}
	if got := lib.ByResearchID("H-999"); got != nil {
		t.Errorf("ByResearchID(H-999) = %+v, want nil", got)
	}
}

func TestNormalizeModeAndReading(t *testing.T) {
	modes := map[string]Mode{
		"skim":       ModeSkim,
		"deep":       ModeDeep,
		"survey":     ModeSurvey,
		"review":     ModeReview,
		"Implement":  ModeImplement,
		"TEACH":      ModeTeach,
		"**review**": ModeReview,    // Markdown emphasis is stripped (issue 47/67)
		"`deep`.":    ModeDeep,      // emphasis + surrounding punctuation stripped
		"weird":      Mode("weird"), // unknown tokens pass verbatim
	}
	for in, want := range modes {
		if got := NormalizeMode(in); got != want {
			t.Errorf("NormalizeMode(%q) = %q, want %q", in, got, want)
		}
	}

	readings := map[string]Reading{
		"read in full":     ReadingFull,
		"full":             ReadingFull,
		"Read Selectively": ReadingSelective,
		"selective":        ReadingSelective,
		"skip":             ReadingSkip,
		"skipped":          ReadingSkip,
		"n/a":              "",
		"none":             "",
		"-":                "",
		"":                 "",
		"read twice":       Reading("read twice"), // unknown tokens pass verbatim
	}
	for in, want := range readings {
		if got := NormalizeReading(in); got != want {
			t.Errorf("NormalizeReading(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeVerdictAppraisalSynonyms(t *testing.T) {
	cases := map[string]Verdict{
		"weak accept": VerdictAccepted,
		"weak-accept": VerdictAccepted,
		"weak reject": VerdictRejected,
		"weak-reject": VerdictRejected,
		"borderline":  VerdictUncertain,
	}
	for in, want := range cases {
		if got := NormalizeVerdict(in); got != want {
			t.Errorf("NormalizeVerdict(%q) = %q, want %q", in, got, want)
		}
	}
}
