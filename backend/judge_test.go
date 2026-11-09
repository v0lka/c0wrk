package backend

import "testing"

// TestBeginJudgeEvaluation_ClaimsAndReleases pins the marker mechanics: the
// first claim wins, a claim while held is refused, and finishing the
// evaluation makes the slot claimable again.
func TestJudgeService_ClaimsAndReleases(t *testing.T) {
	svc := &JudgeService{}

	if !svc.Begin("cid-1") {
		t.Fatal("first claim on a fresh confirm_id should win")
	}
	if svc.Begin("cid-1") {
		t.Fatal("a claim while an evaluation is in flight should be refused")
	}
	// A different confirm_id is never blocked by another one's marker.
	if !svc.Begin("cid-2") {
		t.Fatal("a different confirm_id should claim independently")
	}

	svc.Finish("cid-1")
	if !svc.Begin("cid-1") {
		t.Fatal("claim after finish should win again")
	}
}
