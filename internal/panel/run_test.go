package panel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	vt "github.com/charmbracelet/x/vt"
)

func TestLatestProposalNoCandidateLeavesBench(t *testing.T) {
	rt := newRuntime(Config{Cwd: t.TempDir()})
	rt.size = size{cols: 80, rows: 20}
	rt.ctx = context.Background()
	rt.latestProposal = func(context.Context, string) (proposalCandidate, error) {
		return proposalCandidate{}, errNoUnresolvedProposal
	}
	if _, err := resolveLatestProposalResponse([]byte(`{"found":false}`)); !errors.Is(err, errNoUnresolvedProposal) {
		t.Fatalf("no-candidate response should be distinct from failures, got %v", err)
	}
	reply := make(chan Result, 1)

	rt.selectReview(reply)
	select {
	case ev := <-rt.events:
		rt.handleLatestResolved(ev)
	case <-time.After(time.Second):
		t.Fatal("latest proposal lookup did not return")
	}

	result := <-reply
	if result.Err == nil || !strings.Contains(result.Err.Error(), "no unresolved proposal") {
		t.Fatalf("missing latest proposal should be explained, got %v", result.Err)
	}
	if rt.mode != "bench" || rt.reviewFile != "" {
		t.Fatal("missing proposal changed the Bench or opened Review")
	}
	if rt.notice != "" {
		t.Fatalf("no candidate should leave the Bench without a notice, got %q", rt.notice)
	}
}

func TestLatestProposalLookupFailureStillReportsError(t *testing.T) {
	rt := newRuntime(Config{Cwd: t.TempDir()})
	rt.size = size{cols: 80, rows: 20}
	rt.review.lookupGen = 1
	reply := make(chan Result, 1)
	rt.review.lookupReply = reply

	rt.handleLatestResolved(event{kind: evLatestResolved, token: 1, err: errors.New("selected file changed")})
	if !strings.Contains(rt.notice, "selected file changed") {
		t.Fatalf("real lookup failure should remain visible, got %q", rt.notice)
	}
	if result := <-reply; result.Err == nil {
		t.Fatal("real lookup failure was not returned to caller")
	}
}

func TestLatestLookupKeepsTerminalResponsiveAndCancelsWhenReviewExits(t *testing.T) {
	rt := newRuntime(Config{Cwd: t.TempDir()})
	rt.ctx = context.Background()
	rt.size = size{cols: 80, rows: 20}
	started := make(chan struct{})
	rt.latestProposal = func(ctx context.Context, _ string) (proposalCandidate, error) {
		close(started)
		<-ctx.Done()
		return proposalCandidate{}, ctx.Err()
	}
	lookupReply := make(chan Result, 1)
	returned := make(chan struct{})
	go func() { rt.selectReview(lookupReply); close(returned) }()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("latest lookup blocked the terminal event loop")
	}
	<-started
	statusReply := make(chan Result, 1)
	rt.handleRequest(Request{Command: "status", Reply: statusReply})
	if result := <-statusReply; result.Snapshot.Mode != "bench" {
		t.Fatalf("status could not report the current Bench: %+v", result)
	}
	exitReply := make(chan Result, 1)
	rt.handleRequest(Request{Command: "exit-review", Reply: exitReply})
	if result := <-lookupReply; result.Err == nil {
		t.Fatal("superseded review lookup was not canceled")
	}
	<-exitReply
	select {
	case ev := <-rt.events:
		rt.handleLatestResolved(ev)
	case <-time.After(time.Second):
		t.Fatal("canceled child lookup did not terminate")
	}
	if rt.mode != "bench" || rt.reviewFile != "" {
		t.Fatal("canceled lookup opened Review after returning to the Bench")
	}
}

func TestLatestProposalUsesAuthoritativeCandidate(t *testing.T) {
	cwd := t.TempDir()
	olderFile := filepath.Join(cwd, "older.json")
	latestFile := filepath.Join(cwd, "latest.json")
	if err := os.WriteFile(olderFile, []byte("older"), 0o600); err != nil {
		t.Fatal(err)
	}
	content := []byte(`{"proposal":"latest"}`)
	if err := os.WriteFile(latestFile, content, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	response, err := json.Marshal(proposalCandidate{
		Found: true, File: latestFile, Digest: hex.EncodeToString(digest[:]), State: "Failed",
	})
	if err != nil {
		t.Fatal(err)
	}

	candidate, err := resolveLatestProposalResponse(response)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.File != latestFile || candidate.Digest != hex.EncodeToString(digest[:]) {
		t.Fatalf("used candidate other than Twig's latest response: %+v", candidate)
	}
}

func TestLatestProposalAcceptsCanonicalDigestAndRejectsMissingFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "proposal.json")
	if err := os.WriteFile(file, []byte("{\n  \"proposal\": \"formatted\"\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Twig hashes canonical JSON, not the raw formatting of the file.
	candidate := proposalCandidate{Found: true, File: file, Digest: strings.Repeat("a", 64)}
	if _, err := validateProposalCandidate(candidate); err != nil {
		t.Fatalf("panel rejected an existing file before Twig's canonical digest guard: %v", err)
	}

	candidate.File = filepath.Join(dir, "missing.json")
	if _, err := validateProposalCandidate(candidate); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("missing latest file should fail visibly, got %v", err)
	}
}

func TestReviewEntryWhileOpenKeepsCurrentSnapshot(t *testing.T) {
	rt := newRuntime(Config{Cwd: t.TempDir()})
	rt.size = size{cols: 80, rows: 20}
	rt.mode, rt.benchView, rt.reviewFile = "review", "table", "/current/proposal.json"
	rt.review.session = &session{kind: "review", file: rt.reviewFile, ready: true, term: vt.NewEmulator(80, 18)}
	latestCalls := 0
	rt.latestProposal = func(context.Context, string) (proposalCandidate, error) {
		latestCalls++
		return proposalCandidate{Found: true, File: "/new/proposal.json", Digest: strings.Repeat("a", 64)}, nil
	}
	reply := make(chan Result, 1)

	rt.selectReview(reply)

	result := <-reply
	if result.Err != nil || result.Snapshot.ReviewFile != "/current/proposal.json" || latestCalls != 0 {
		t.Fatalf("open Review changed or re-resolved its snapshot: %+v, latest calls %d", result, latestCalls)
	}
}

func TestReviewPreviewArgsUsesNativeColorWithOptOut(t *testing.T) {
	file := "/workspace/proposal.json"
	digest := strings.Repeat("a", 64)
	if got, want := reviewPreviewArgs(file, digest, ""), []string{"proposal", "preview", "--file", file, "--expect-digest", digest, "--color", "always", "--interactive"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("color-enabled preview arguments = %v, want %v", got, want)
	}
	if got, want := reviewPreviewArgs(file, "", "1"), []string{"proposal", "preview", "--file", file, "--color", "never", "--interactive"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("NO_COLOR preview arguments = %v, want %v", got, want)
	}
}

func TestSuppressReviewEchoOnlyConsumesSelectedInputPrefix(t *testing.T) {
	filtered, ok := suppressReviewEcho([]byte("d\r\nReview only"), 'd')
	if !ok || string(filtered) != "Review only" {
		t.Fatalf("filtered echo = %q, matched = %v", filtered, ok)
	}
	unchanged, ok := suppressReviewEcho([]byte("Details"), 'd')
	if ok || string(unchanged) != "Details" {
		t.Fatalf("unrelated output changed: %q, matched = %v", unchanged, ok)
	}
}
