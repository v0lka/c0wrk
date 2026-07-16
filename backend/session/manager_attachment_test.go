package session

import (
	"reflect"
	"testing"
	"time"

	"github.com/v0lka/sp4rk/orchestration"
)

// TestToAttachmentInfos_MapsFields verifies that orchestration.Attachment
// records are correctly mapped to the metadata-only AttachmentInfo view.
func TestToAttachmentInfos_MapsFields(t *testing.T) {
	attachments := []orchestration.Attachment{
		{
			ID:              "att-1",
			OriginalName:    "report.pdf",
			OriginalPath:    "/tmp/report.pdf",
			Format:          "pdf",
			SizeBytes:       1024,
			MarkdownContent: "# Report",
			AttachedAt:      time.Now(),
		},
		{
			ID:              "att-2",
			OriginalName:    "notes.md",
			OriginalPath:    "/tmp/notes.md",
			Format:          "md",
			SizeBytes:       256,
			MarkdownContent: "# Notes",
			AttachedAt:      time.Now(),
		},
	}

	got := toAttachmentInfos(attachments)
	if len(got) != 2 {
		t.Fatalf("expected 2 AttachmentInfo, got %d", len(got))
	}

	// MarkdownContent must NOT leak into the metadata-only view.
	for _, info := range got {
		if reflect.DeepEqual(info, attachments[0]) {
			t.Error("AttachmentInfo should not contain MarkdownContent")
		}
	}

	if got[0].ID != "att-1" || got[0].OriginalName != "report.pdf" {
		t.Errorf("got[0] = %+v, want ID=att-1 OriginalName=report.pdf", got[0])
	}
	if got[1].Format != "md" || got[1].SizeBytes != 256 {
		t.Errorf("got[1] = %+v, want Format=md SizeBytes=256", got[1])
	}
}

// TestToAttachmentInfos_DefensiveCopy verifies that mutating the returned
// slice does not affect a subsequent call (each call returns a fresh slice).
func TestToAttachmentInfos_DefensiveCopy(t *testing.T) {
	attachments := []orchestration.Attachment{
		{ID: "a1", OriginalName: "a.txt", Format: "txt", SizeBytes: 1},
	}
	first := toAttachmentInfos(attachments)
	first[0].ID = "MUTATED"

	second := toAttachmentInfos(attachments)
	if second[0].ID != "a1" {
		t.Errorf("defensive copy violated: got ID=%s, want a1", second[0].ID)
	}
}

// TestToAttachmentInfos_Empty returns an empty (non-nil) slice for empty input.
func TestToAttachmentInfos_Empty(t *testing.T) {
	got := toAttachmentInfos(nil)
	if got == nil {
		t.Error("expected non-nil empty slice for nil input")
	}
	if len(got) != 0 {
		t.Errorf("expected length 0, got %d", len(got))
	}
}
