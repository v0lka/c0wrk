package embedding

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestChunkFile_GoCode(t *testing.T) {
	content := `package main

import "fmt"


func hello() {
	fmt.Println("hello")
}


func world() {
	fmt.Println("world")
}
`
	chunks, err := ChunkFile("/src/main.go", []byte(content), ChunkerConfig{MaxChunkSize: 1500})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
	}
	for _, c := range chunks {
		if c.Language != "go" {
			t.Errorf("expected language go, got %s", c.Language)
		}
		if c.FileName != "main.go" {
			t.Errorf("expected filename main.go, got %s", c.FileName)
		}
		if c.FilePath != "/src/main.go" {
			t.Errorf("expected filepath /src/main.go, got %s", c.FilePath)
		}
	}
	// First chunk should start at line 1
	if chunks[0].StartLine != 1 {
		t.Errorf("first chunk StartLine: got %d, want 1", chunks[0].StartLine)
	}
	// Verify chunks are ordered and non-overlapping
	for i := 1; i < len(chunks); i++ {
		if chunks[i].StartLine <= chunks[i-1].EndLine {
			t.Errorf("chunk %d StartLine (%d) should be after chunk %d EndLine (%d)",
				i, chunks[i].StartLine, i-1, chunks[i-1].EndLine)
		}
	}
	// Last chunk should contain the closing brace of world()
	lastChunk := chunks[len(chunks)-1]
	if !strings.Contains(lastChunk.Content, "world") {
		t.Errorf("last chunk should contain world(), got: %q", lastChunk.Content)
	}
}

func TestChunkFile_TypeScript(t *testing.T) {
	content := `import { Component } from 'react';

class Foo {
  render() { return null; }
}


class Bar {
  render() { return null; }
}
`
	chunks, err := ChunkFile("/src/app.tsx", []byte(content), ChunkerConfig{MaxChunkSize: 1500})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks for TS, got %d", len(chunks))
	}
	for _, c := range chunks {
		if c.Language != "typescript" {
			t.Errorf("expected language typescript, got %s", c.Language)
		}
	}
}

func TestChunkFile_Markdown(t *testing.T) {
	content := `# Title

Intro paragraph.

## Section One

Content of section one.

## Section Two

Content of section two.
`
	chunks, err := ChunkFile("/docs/readme.md", []byte(content), ChunkerConfig{MaxChunkSize: 1500})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks (preamble + 2 H2), got %d", len(chunks))
	}
	if !strings.HasPrefix(chunks[0].Content, "# Title") {
		t.Errorf("first chunk should start with H1, got: %q", chunks[0].Content[:20])
	}
	if !strings.HasPrefix(chunks[1].Content, "## Section One") {
		t.Errorf("second chunk should start with H2 Section One")
	}
	if !strings.HasPrefix(chunks[2].Content, "## Section Two") {
		t.Errorf("third chunk should start with H2 Section Two")
	}
	if chunks[0].StartLine != 1 {
		t.Errorf("preamble StartLine: got %d, want 1", chunks[0].StartLine)
	}
	if chunks[0].Language != "markdown" {
		t.Errorf("expected language markdown, got %s", chunks[0].Language)
	}
}

func TestChunkFile_YAMLSmall(t *testing.T) {
	content := `name: test
version: 1.0
`
	chunks, err := ChunkFile("/config.yaml", []byte(content), ChunkerConfig{MaxChunkSize: 1500})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk for small YAML, got %d", len(chunks))
	}
	if chunks[0].Language != "yaml" {
		t.Errorf("expected language yaml, got %s", chunks[0].Language)
	}
}

func TestChunkFile_YAMLLarge(t *testing.T) {
	// Build a YAML file larger than MaxChunkSize with top-level keys
	var b strings.Builder
	b.WriteString("key1:\n")
	b.WriteString("  value: " + strings.Repeat("a", 100) + "\n")
	b.WriteString("key2:\n")
	b.WriteString("  value: " + strings.Repeat("b", 100) + "\n")
	b.WriteString("key3:\n")
	b.WriteString("  value: " + strings.Repeat("c", 100) + "\n")

	chunks, err := ChunkFile("/big.yaml", []byte(b.String()), ChunkerConfig{MaxChunkSize: 150})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks for large YAML, got %d", len(chunks))
	}
}

func TestChunkFile_JSONSmall(t *testing.T) {
	content := `{"name": "test", "version": 1}`
	chunks, err := ChunkFile("/config.json", []byte(content), ChunkerConfig{MaxChunkSize: 1500})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk for small JSON, got %d", len(chunks))
	}
	if chunks[0].Language != "json" {
		t.Errorf("expected language json, got %s", chunks[0].Language)
	}
}

func TestChunkFile_Empty(t *testing.T) {
	chunks, err := ChunkFile("/empty.go", []byte{}, ChunkerConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks for empty file, got %d", len(chunks))
	}
}

func TestChunkFile_Binary(t *testing.T) {
	content := []byte("ELF\x00\x01\x02binary stuff")
	chunks, err := ChunkFile("/bin/prog", content, ChunkerConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks for binary file, got %d", len(chunks))
	}
}

func TestChunkFile_LargeFunction(t *testing.T) {
	// A single large "function" that exceeds MaxChunkSize
	var b strings.Builder
	b.WriteString("func big() {\n")
	for i := 0; i < 50; i++ {
		b.WriteString("  line " + strings.Repeat("x", 40) + "\n")
	}
	b.WriteString("}\n")

	chunks, err := ChunkFile("/big.go", []byte(b.String()), ChunkerConfig{MaxChunkSize: 200, Overlap: 50})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) < 2 {
		t.Fatalf("expected large function to be split into multiple chunks, got %d", len(chunks))
	}
	// All chunks should be code
	for _, c := range chunks {
		if c.Language != "go" {
			t.Errorf("expected language go, got %s", c.Language)
		}
	}
}

func TestChunkFile_SingleLine(t *testing.T) {
	content := "single line content"
	chunks, err := ChunkFile("/single.txt", []byte(content), ChunkerConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk for single-line, got %d", len(chunks))
	}
	if chunks[0].StartLine != 1 || chunks[0].EndLine != 1 {
		t.Errorf("single-line chunk lines: got %d-%d, want 1-1", chunks[0].StartLine, chunks[0].EndLine)
	}
}

func TestComputeFileHash(t *testing.T) {
	input := []byte("hello world")
	expected := sha256.Sum256(input)
	expectedHex := hex.EncodeToString(expected[:])

	got := ComputeFileHash(input)
	if got != expectedHex {
		t.Errorf("ComputeFileHash: got %s, want %s", got, expectedHex)
	}

	// Known value: sha256("hello world") = b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9
	if got != "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9" {
		t.Errorf("ComputeFileHash known value mismatch: got %s", got)
	}
}

func TestChunkFile_DefaultConfig(t *testing.T) {
	// Verify defaults are applied
	content := "some content"
	chunks, err := ChunkFile("/test.txt", []byte(content), ChunkerConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
}

func TestChunkFile_FixedSplitOther(t *testing.T) {
	// .sql file should use fixed-size split
	content := strings.Repeat("SELECT * FROM t;\n", 100)
	chunks, err := ChunkFile("/query.sql", []byte(content), ChunkerConfig{MaxChunkSize: 200, Overlap: 50})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks for large SQL, got %d", len(chunks))
	}
	for _, c := range chunks {
		if c.Language != "sql" {
			t.Errorf("expected language sql, got %s", c.Language)
		}
	}
}

func TestChunkFile_LineNumbersWithBlankLines(t *testing.T) {
	// Fabricated TSX-like file with blank lines to verify line numbers
	// match the frontend's split('\n') counting.
	content := `import { useState } from 'react'

const SIDEBAR_DEFAULT = 300

export function AppLayout() {
  const sidebarCollapsed = false

  const showFileViewer = true

  return (
    <div>
      {/* Sidebar */}
      <Sidebar />

      {/* Resize handle */}
      <ResizeHandle />

      {/* Main content */}
      <ChatArea />
    </div>
  )
}
`
	chunks, err := ChunkFile("/AppLayout.tsx", []byte(content), ChunkerConfig{MaxChunkSize: 1500})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Find the chunk containing the ResizeHandle comment and verify its line range.
	var found bool
	for _, c := range chunks {
		if strings.Contains(c.Content, "Resize handle") {
			found = true
			if c.StartLine != 15 {
				t.Errorf("ResizeHandle chunk StartLine: got %d, want 15", c.StartLine)
			}
			if c.EndLine != 17 {
				t.Errorf("ResizeHandle chunk EndLine: got %d, want 17", c.EndLine)
			}
		}
	}
	if !found {
		t.Fatalf("expected a chunk containing 'Resize handle'")
	}

	// Verify cumulative line count equals split length of original content.
	lastChunk := chunks[len(chunks)-1]
	expectedTotal := len(strings.Split(content, "\n"))
	if lastChunk.EndLine != expectedTotal {
		t.Errorf("last chunk EndLine: got %d, want %d (total lines)", lastChunk.EndLine, expectedTotal)
	}
}
