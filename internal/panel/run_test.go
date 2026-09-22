package panel

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	vt "github.com/charmbracelet/x/vt"
)

func TestReviewWithoutExplicitSelectionLeavesBench(t *testing.T) {
	rt := newRuntime(Config{Cwd: t.TempDir()})
	rt.size = size{cols: 80, rows: 20}
	reply := make(chan Result, 1)

	rt.selectReview(reply)

	result := <-reply
	if result.Err == nil || !strings.Contains(result.Err.Error(), "no proposal selected") {
		t.Fatalf("missing selection should be explained, got %v", result.Err)
	}
	if rt.mode != "bench" || rt.selectedReviewFile != "" {
		t.Fatal("unbound Review selection changed the Bench or invented a proposal")
	}
}

func TestMissingReplacementPreservesPreviouslySelectedProposal(t *testing.T) {
	cwd := t.TempDir()
	file := filepath.Join(cwd, "selected.json")
	if err := os.WriteFile(file, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	rt := newRuntime(Config{Cwd: cwd})
	rt.size = size{cols: 80, rows: 20}
	rt.selectedReviewFile = file
	reply := make(chan Result, 1)

	rt.beginReview("missing.json", reply, false)

	if result := <-reply; result.Err == nil {
		t.Fatal("missing proposal was accepted")
	}
	if rt.mode != "bench" || rt.selectedReviewFile != file {
		t.Fatal("failed replacement discarded the prior selection or Bench")
	}
}

func TestSuccessfulReplacementCommitsOnlyAfterReviewIsVisible(t *testing.T) {
	rt := newRuntime(Config{Cwd: t.TempDir()})
	rt.size = size{cols: 80, rows: 20}
	rt.mode, rt.benchView = "review", "table"
	rt.selectedReviewFile = "first.json"
	rt.reviewFile = "second.json"
	sess := &session{kind: "review", file: "second.json", gen: 1, term: vt.NewEmulator(80, 18)}
	rt.review.session, rt.review.launchGen = sess, 1
	reply := make(chan Result, 1)
	rt.review.pendingReply = reply

	mid := len(reviewPrompt) / 2
	rt.handleSessionData(sess, []byte(reviewPrompt[:mid]))
	if rt.selectedReviewFile != "first.json" || len(reply) != 0 {
		t.Fatal("replacement was acknowledged before its Review was visible")
	}
	rt.handleSessionData(sess, []byte(reviewPrompt[mid:]))
	result := <-reply
	if result.Err != nil || !result.Snapshot.Ready || result.Snapshot.SelectedReviewFile != "second.json" || rt.benchView != "table" {
		t.Fatalf("visible Review did not commit the replacement: %+v", result)
	}
}

func TestFailedPreviewKeepsPreviousSelectionAndBench(t *testing.T) {
	rt := newRuntime(Config{Cwd: t.TempDir()})
	rt.size = size{cols: 80, rows: 20}
	rt.mode, rt.benchView = "review", "table"
	rt.selectedReviewFile, rt.reviewFile = "first.json", "invalid.json"
	sess := &session{kind: "review", file: "invalid.json", gen: 1}
	rt.review.session, rt.review.launchGen = sess, 1
	bench := &session{kind: "bench", view: "table"}
	rt.review.restoreBench = bench
	reply := make(chan Result, 1)
	rt.review.pendingReply = reply

	rt.handleSessionExit(sess, errors.New("invalid proposal"))
	result := <-reply
	if result.Err == nil || rt.mode != "bench" || rt.bench.active != bench || rt.selectedReviewFile != "first.json" {
		t.Fatalf("failed preview lost the last usable selection or Bench: %+v", result)
	}
}
