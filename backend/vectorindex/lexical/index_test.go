package lexical

import (
	"bytes"
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/blevesearch/bleve/v2/analysis"
)

func TestSplitIdentifier(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"single_char", "x", []string{"x"}},
		{"plain_word", "hello", []string{"hello"}},
		{"camel", "camelCase", []string{"camel", "Case"}},
		{"pascal", "PascalCase", []string{"Pascal", "Case"}},
		{"acronym_then_word", "HTMLParser", []string{"HTML", "Parser"}},
		{"word_then_acronym_then_word", "parseHTMLDoc", []string{"parse", "HTML", "Doc"}},
		{"letter_digit_letter", "foo123bar", []string{"foo", "123", "bar"}},
		{"digits_only", "12345", []string{"12345"}},
		{"all_upper", "HTTP", []string{"HTTP"}},
		{"all_lower", "http", []string{"http"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := splitIdentifier([]byte(tc.in))
			if tc.want == nil {
				if got != nil {
					t.Fatalf("splitIdentifier(%q) = %v, want nil", tc.in, got)
				}
				return
			}
			gotStrs := make([]string, len(got))
			for i, b := range got {
				gotStrs[i] = string(b)
			}
			if !reflect.DeepEqual(gotStrs, tc.want) {
				t.Fatalf("splitIdentifier(%q) = %v, want %v", tc.in, gotStrs, tc.want)
			}
		})
	}
}

func TestCamelCaseFilter_PreservesOffsetsAndPositions(t *testing.T) {
	f := &camelCaseFilter{}
	in := analysis.TokenStream{
		&analysis.Token{Term: []byte("camelCase"), Start: 10, End: 19, Position: 5, Type: analysis.AlphaNumeric},
	}
	out := f.Filter(in)
	if len(out) != 2 {
		t.Fatalf("expected 2 tokens, got %d", len(out))
	}
	if !bytes.Equal(out[0].Term, []byte("camel")) || !bytes.Equal(out[1].Term, []byte("Case")) {
		t.Fatalf("unexpected terms: %q %q", out[0].Term, out[1].Term)
	}
	if out[0].Start != 10 || out[0].End != 15 {
		t.Fatalf("first token offsets wrong: %d-%d", out[0].Start, out[0].End)
	}
	if out[1].Start != 15 || out[1].End != 19 {
		t.Fatalf("second token offsets wrong: %d-%d", out[1].Start, out[1].End)
	}
	if out[0].Position != 5 || out[1].Position != 6 {
		t.Fatalf("positions wrong: %d %d", out[0].Position, out[1].Position)
	}
}

func TestCamelCaseFilter_PassthroughForSimpleTokens(t *testing.T) {
	f := &camelCaseFilter{}
	in := analysis.TokenStream{
		&analysis.Token{Term: []byte("hello"), Start: 0, End: 5, Position: 1},
		&analysis.Token{Term: []byte("HTTP"), Start: 5, End: 9, Position: 2},
	}
	out := f.Filter(in)
	if len(out) != 2 {
		t.Fatalf("expected 2 tokens, got %d", len(out))
	}
	if !bytes.Equal(out[0].Term, []byte("hello")) || !bytes.Equal(out[1].Term, []byte("HTTP")) {
		t.Fatalf("passthrough failed: %q %q", out[0].Term, out[1].Term)
	}
}

func newTempIndex(t *testing.T) Index {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "lex")
	idx, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		_ = idx.Close()
	})
	return idx
}

func TestIndex_UpsertQueryDelete(t *testing.T) {
	idx := newTempIndex(t)
	ctx := context.Background()

	docs := []Doc{
		{ID: "a:0", FilePath: "/p/a.go", Language: "go", Content: "func parseHTMLDoc(input []byte) error { return nil }"},
		{ID: "b:0", FilePath: "/p/b.go", Language: "go", Content: "var camelCase = 1"},
		{ID: "c:0", FilePath: "/p/c.md", Language: "markdown", Content: "# Hello world\nSome prose."},
	}
	if err := idx.Upsert(ctx, docs); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	n, err := idx.Count()
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 3 {
		t.Fatalf("Count = %d, want 3", n)
	}

	// Query by camel-cased identifier; analyzer should split it and match both
	// "camelCase" and "parseHTMLDoc" via shared subtoken "case"/"parse"... but
	// we only expect the literal token match to rank the specific doc highest.
	hits, err := idx.Query(ctx, "camelCase", 10)
	if err != nil {
		t.Fatalf("Query(camelCase): %v", err)
	}
	if len(hits) == 0 {
		t.Fatalf("no hits for 'camelCase'")
	}
	if hits[0].ID != "b:0" {
		t.Fatalf("top hit = %s, want b:0", hits[0].ID)
	}
	if hits[0].Rank != 1 {
		t.Fatalf("top rank = %d, want 1", hits[0].Rank)
	}

	// Acronym split: "HTML" should hit parseHTMLDoc doc.
	hits, err = idx.Query(ctx, "HTML", 10)
	if err != nil {
		t.Fatalf("Query(HTML): %v", err)
	}
	if len(hits) == 0 || hits[0].ID != "a:0" {
		t.Fatalf("HTML hits = %+v, want top a:0", hits)
	}

	// Delete one doc.
	if err := idx.Delete(ctx, []string{"b:0"}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	n, err = idx.Count()
	if err != nil {
		t.Fatalf("Count after delete: %v", err)
	}
	if n != 2 {
		t.Fatalf("Count after delete = %d, want 2", n)
	}
}

func TestIndex_PersistsAcrossOpens(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "lex")
	ctx := context.Background()

	idx, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := idx.Upsert(ctx, []Doc{{ID: "x:0", Content: "persistent content here", FilePath: "/p/x.go", Language: "go"}}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := idx.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	idx2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = idx2.Close() }()

	n, err := idx2.Count()
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 1 {
		t.Fatalf("Count after reopen = %d, want 1", n)
	}
	hits, err := idx2.Query(ctx, "persistent", 5)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(hits) != 1 || hits[0].ID != "x:0" {
		t.Fatalf("hits = %+v", hits)
	}
}

func TestIndex_EmptyQueryReturnsNoHits(t *testing.T) {
	idx := newTempIndex(t)
	ctx := context.Background()
	_ = idx.Upsert(ctx, []Doc{{ID: "a:0", Content: "hello world", FilePath: "/p/a.go"}})
	hits, err := idx.Query(ctx, "", 5)
	if err != nil {
		t.Fatalf("Query(''): %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("empty query hits = %+v, want 0", hits)
	}
}
