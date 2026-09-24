package panel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	vt "github.com/charmbracelet/x/vt"
)

func TestDrawBarFillsAndClipsToViewportWidth(t *testing.T) {
	for _, tc := range []struct {
		name  string
		text  string
		width int
		want  string
	}{
		{name: "fills short label", text: "Tree", width: 8, want: "Tree    "},
		{name: "clips long label", text: "Tree workspace", width: 8, want: "Tree wo…"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out strings.Builder
			drawBar(&out, tc.text, tc.width, "\x1b[48;2;22;40;59m")
			visible := ansi.Strip(out.String())
			if visible != tc.want {
				t.Fatalf("visible bar = %q, want %q", visible, tc.want)
			}
			if width := ansi.StringWidth(out.String()); width != tc.width {
				t.Fatalf("bar width = %d, want %d", width, tc.width)
			}
		})
	}
}

func TestDrawDividerFillsViewportWidth(t *testing.T) {
	var out strings.Builder
	drawDivider(&out, 8, "\x1b[48;2;22;40;59m")
	visible := ansi.Strip(out.String())
	if visible != "────────" {
		t.Fatalf("visible divider = %q, want full-width rule", visible)
	}
	if width := ansi.StringWidth(out.String()); width != 8 {
		t.Fatalf("divider width = %d, want 8", width)
	}
}

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
	gotCwd := ""
	rt.latestProposal = func(ctx context.Context, cwd string) (proposalCandidate, error) {
		gotCwd = cwd
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
	if gotCwd != rt.cfg.Cwd {
		t.Fatalf("latest review lookup used %q, want panel cwd %q", gotCwd, rt.cfg.Cwd)
	}
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

func TestReviewPreviewArgsGuardOnlyLatestSelection(t *testing.T) {
	file := "/workspace/proposal.json"
	digest := strings.Repeat("a", 64)
	if got, want := reviewPreviewArgs(file, digest), []string{"proposal", "preview", "--file", file, "--expect-digest", digest, "--color", "always", "--interactive"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("latest preview arguments = %v, want %v", got, want)
	}
	if got, want := reviewPreviewArgs(file, ""), []string{"proposal", "preview", "--file", file, "--color", "always", "--interactive"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("manual preview arguments = %v, want %v", got, want)
	}
}

func TestConsumeReviewEchoDropsSplitInputLine(t *testing.T) {
	sess := &session{reviewEcho: 'b'}
	var got []byte
	for _, chunk := range [][]byte{[]byte("b"), []byte("\r"), []byte("\n\x1b[Hbody")} {
		got = append(got, consumeReviewEcho(sess, chunk)...)
	}
	if string(got) != "\x1b[Hbody" {
		t.Fatalf("filtered Review output = %q, want child redraw only", got)
	}
	if sess.reviewEcho != 0 || len(sess.reviewEchoBuffer) != 0 {
		t.Fatalf("Review echo state remained after filtering: key=%q buffer=%q", sess.reviewEcho, sess.reviewEchoBuffer)
	}
}

func TestConsumeReviewEchoLeavesUnechoedChildOutput(t *testing.T) {
	sess := &session{reviewEcho: 'd'}
	input := []byte("\x1b[Hdetails")
	if got := consumeReviewEcho(sess, input); string(got) != string(input) {
		t.Fatalf("unechoed child output = %q, want %q", got, input)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()
	fn()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func rowMarker(row int) string {
	return fmt.Sprintf("\x1b[%d;1H", row)
}

func TestReviewLaunchCwdUsesProposalWorkspaceForExplicitFilesAndSourceForLatest(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	stateWorkspace := filepath.Join(root, "state-workspace")
	stateProposal := filepath.Join(stateWorkspace, ".twig", "org", "project", "proposal.json")
	if err := os.MkdirAll(filepath.Dir(stateProposal), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stateProposal, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := reviewLaunchCwd(stateProposal, source, true)
	if err != nil {
		t.Fatal(err)
	}
	wantStateWorkspace, err := filepath.EvalSymlinks(stateWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	if got != wantStateWorkspace {
		t.Fatalf("explicit proposal launched from %q, want state workspace %q", got, wantStateWorkspace)
	}

	manifestWorkspace := filepath.Join(root, "manifest-workspace")
	manifestProposal := filepath.Join(manifestWorkspace, "reviews", "proposal.json")
	if err := os.MkdirAll(filepath.Dir(manifestProposal), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manifestWorkspace, "twig.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestProposal, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = reviewLaunchCwd(manifestProposal, source, true)
	if err != nil {
		t.Fatal(err)
	}
	wantManifestWorkspace, err := filepath.EvalSymlinks(manifestWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	if got != wantManifestWorkspace {
		t.Fatalf("explicit proposal launched from %q, want manifest workspace %q", got, wantManifestWorkspace)
	}

	symlink := filepath.Join(root, "proposal-link.json")
	if err := os.Symlink(manifestProposal, symlink); err != nil {
		t.Fatal(err)
	}
	got, err = reviewLaunchCwd(symlink, source, true)
	if err != nil {
		t.Fatal(err)
	}
	if got != wantManifestWorkspace {
		t.Fatalf("symlinked proposal launched from %q, want target workspace %q", got, wantManifestWorkspace)
	}

	got, err = reviewLaunchCwd(stateProposal, source, false)
	if err != nil {
		t.Fatal(err)
	}
	if got != source {
		t.Fatalf("latest proposal launched from %q, want source cwd %q", got, source)
	}
}

func TestResolveFileUsesSourceCwdForRelativePaths(t *testing.T) {
	cwd := t.TempDir()
	rt := newRuntime(Config{Cwd: cwd})
	want := filepath.Join(cwd, "proposal.json")
	if err := os.WriteFile(want, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := rt.resolveFile("proposal.json")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("relative review file resolved to %q, want %q", got, want)
	}
}

func TestReviewResizeClampsChromeAndChildViewport(t *testing.T) {
	for _, tc := range []struct {
		name            string
		rows            int
		wantSessionRows int
		wantRows        []int
		wantMissing     int
	}{
		{name: "three-row terminal keeps header and footer visible", rows: 3, wantSessionRows: 1, wantRows: []int{1, 2, 3}, wantMissing: 4},
		{name: "tall view keeps four chrome rows and content visible", rows: 7, wantSessionRows: 3, wantRows: []int{1, 2, 3, 4, 5, 6, 7}, wantMissing: 8},
		{name: "short view keeps header and footer visible without overflow", rows: 4, wantSessionRows: 1, wantRows: []int{1, 2, 3, 4}, wantMissing: 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := newRuntime(Config{Cwd: t.TempDir()})
			rt.ctx = context.Background()
			rt.mode = "review"
			rt.reviewFile = "/tmp/proposal.json"
			rt.size = size{cols: 40, rows: tc.rows}
			rt.review.session = &session{kind: "review", ready: true, hasData: true, cols: 40, rows: 8, term: vt.NewEmulator(40, 8)}
			rt.offsets["review"] = 99
			output := captureStdout(t, func() { rt.handleResize(size{cols: 40, rows: tc.rows}) })
			if got := rt.review.session.rows; got != tc.wantSessionRows {
				t.Fatalf("child viewport rows = %d, want %d", got, tc.wantSessionRows)
			}
			for _, row := range tc.wantRows {
				if !strings.Contains(output, rowMarker(row)) {
					t.Fatalf("output missed row %d: %q", row, output)
				}
			}
			if strings.Contains(output, rowMarker(tc.wantMissing)) {
				t.Fatalf("output wrote row %d off-screen: %q", tc.wantMissing, output)
			}
		})
	}
}

func TestReviewScrollUsesVisibleViewportRows(t *testing.T) {
	rt := newRuntime(Config{Cwd: t.TempDir()})
	rt.ctx = context.Background()
	rt.mode = "review"
	rt.reviewFile = "/tmp/proposal.json"
	rt.size = size{cols: 40, rows: 7}
	rt.review.session = &session{kind: "review", ready: true, hasData: true, cols: 40, rows: 8, term: vt.NewEmulator(40, 8)}
	rt.offsets["review"] = 0
	rt.handleKey(uv.KeyPressEvent(uv.Key{Code: uv.KeyPgDown}))
	if got := rt.offsets["review"]; got != 2 {
		t.Fatalf("page down offset = %d, want 2", got)
	}
	rt.handleKey(uv.KeyPressEvent(uv.Key{Code: uv.KeyEnd}))
	if got, want := rt.offsets["review"], rt.maxOffsetForCurrentSession(); got != want {
		t.Fatalf("end offset = %d, want %d", got, want)
	}
	rt.size = size{cols: 40, rows: 5}
	rt.offsets["review"] = 0
	rt.handleKey(uv.KeyPressEvent(uv.Key{Code: uv.KeyPgDown}))
	if got := rt.offsets["review"]; got != 1 {
		t.Fatalf("single-row viewport page down offset = %d, want 1", got)
	}

	rt.size = size{cols: 40, rows: 4}
	rt.offsets["review"] = 5
	rt.handleKey(uv.KeyPressEvent(uv.Key{Code: uv.KeyPgDown}))
	if got := rt.offsets["review"]; got != 0 {
		t.Fatalf("short viewport page down offset = %d, want 0", got)
	}
}
