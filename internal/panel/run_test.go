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
	goruntime "runtime"
	"strings"
	"syscall"
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

func TestContextBarRightAlignsWithoutCrowdingMode(t *testing.T) {
	for _, tc := range []struct {
		name, left, right string
		width             int
		want              string
	}{
		{name: "wide right label", left: "Tree · work", right: "ws界", width: 20, want: "Tree · work     ws界"},
		{name: "right clipping leaves left half", left: "Tree · Worktree", right: "界界界界界", width: 12, want: "Tree … 界界…"},
		{name: "joined and combining graphemes", left: "Tree", right: "e\u0301👩\u200d💻", width: 10, want: "Tree   e\u0301👩\u200d💻"},
		{name: "no host context", left: "Tree", width: 8, want: "Tree    "},
		{name: "one cell keeps left", left: "Tree", right: "Herdr", width: 1, want: "…"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out strings.Builder
			drawContextBar(&out, tc.left, tc.right, tc.width, "\x1b[48;2;22;40;59m")
			if got := ansi.Strip(out.String()); got != tc.want || ansi.StringWidth(got) != tc.width {
				t.Fatalf("context row = %q (width %d), want %q (width %d)", got, ansi.StringWidth(got), tc.want, tc.width)
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

func TestSharedHeadersRemainSeparateAcrossSurfaces(t *testing.T) {
	for _, tc := range []struct {
		name, mode, content, footer string
	}{
		{name: "tree", mode: "Tree", content: "ABC界DEF", footer: "[b Bench]"},
		{name: "table", mode: "Table", content: "Work item", footer: "[b Bench]"},
		{name: "review", mode: "Review", content: "REVIEW CONTENT", footer: "REVIEW"},
		{name: "help", mode: "Tree", content: "Bench browser", footer: "[Close]"},
		{name: "configuration", mode: "Bench Configuration", content: "[1 Pins]", footer: "Sections:"},
		{name: "form", mode: "Bench Configuration", content: "Add area", footer: "[OK]"},
		{name: "monochrome tree", mode: "Tree", content: "ABC界DEF", footer: "[b Bench]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, snapshot := browserFixture(t)
			rt.size = size{cols: 160, rows: 16}
			rt.binding.Organization, rt.binding.Project, rt.binding.Team = "Org", "Project", "Team"
			rt.binding.Account, rt.benchSummary = "person@example.com", "Bench"
			rt.cfg.HerdrContext = "Herdr ws界/tab/pane"
			rt.notice = "NOTICE"
			switch tc.name {
			case "table":
				rt.benchView = "table"
			case "review":
				rt.mode, rt.reviewFile = "review", "proposal.json"
				rt.review.session = &session{kind: "review", term: vt.NewEmulator(160, 11), hasData: true}
				_, _ = rt.review.session.term.WriteString("REVIEW CONTENT")
			case "help":
				rt.showBrowserHelp = true
			case "configuration":
				rt.configuration = &configurationView{benchID: snapshot.BenchID, sectionsFocused: true}
			case "form":
				rt.form = &configurationForm{kind: "area", benchName: snapshot.BenchName, editor: newTextEditor("Alpha")}
			case "monochrome tree":
				t.Setenv("NO_COLOR", "")
			}
			output := captureStdout(t, rt.draw)
			if tc.name == "monochrome tree" && labelSGR.MatchString(output) {
				t.Fatal("NO_COLOR left styled header or content output")
			}
			pane := vt.NewEmulator(160, 16)
			_, _ = pane.WriteString(output)
			rows := strings.Split(pane.String(), "\n")
			if len(rows) != 16 {
				t.Fatalf("visible screen rows = %d, want 16", len(rows))
			}
			for _, text := range []string{"Org/Project", "Team: Team", "Bench: Bench", "User: person@example.com"} {
				if !strings.Contains(rows[0], text) {
					t.Fatalf("connection row lost %q: %q", text, rows[0])
				}
			}
			if !strings.HasPrefix(rows[1], tc.mode) || !strings.Contains(rows[1], "Worktree:") || !strings.Contains(rows[1], "Branch:") || !strings.HasSuffix(rows[1], rt.cfg.HerdrContext) || ansi.StringWidth(rows[1]) != 160 {
				t.Fatalf("context row was clipped, misplaced or not right aligned: %q", rows[1])
			}
			if rows[2] != strings.Repeat("─", 160) || !strings.Contains(rows[3], tc.content) || rows[14] != "NOTICE" || !strings.Contains(rows[15], tc.footer) {
				t.Fatalf("headers, content, notice or controls collided: %q", pane.String())
			}
			for _, hit := range rt.hits {
				if hit.y < 3 || hit.y == 14 || hit.y >= 16 {
					t.Fatalf("mouse target overlaps chrome or lies outside the panel: %+v", hit)
				}
			}
		})
	}
}

func TestLatestProposalNoCandidateLeavesBench(t *testing.T) {
	rt := newRuntime(Config{Cwd: t.TempDir()})
	rt.size = size{cols: 80, rows: 20}
	rt.ctx = context.Background()
	rt.latestProposal = func(context.Context, string, string) (proposalCandidate, error) {
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
	rt.latestProposal = func(ctx context.Context, cwd, snapshot string) (proposalCandidate, error) {
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
	rt.latestProposal = func(context.Context, string, string) (proposalCandidate, error) {
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
	defer r.Close()
	defer w.Close()
	type captureResult struct {
		data []byte
		err  error
	}
	result := make(chan captureResult, 1)
	go func() {
		data, err := io.ReadAll(r)
		result <- captureResult{data: data, err: err}
	}()
	os.Stdout = w
	defer func() { os.Stdout = old }()
	fn()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	captured := <-result
	if captured.err != nil {
		t.Fatal(captured.err)
	}
	return string(captured.data)
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

	t.Run("symlinked proposal", func(t *testing.T) {
		symlink := filepath.Join(root, "proposal-link.json")
		if err := os.Symlink(manifestProposal, symlink); err != nil {
			// ERROR_PRIVILEGE_NOT_HELD is distinct from ordinary access-denied errors.
			if goruntime.GOOS == "windows" && errors.Is(err, syscall.Errno(1314)) {
				t.Skip("Windows token cannot create symbolic links; ordinary explicit/latest workspace cases still run")
			}
			t.Fatal(err)
		}
		got, err := reviewLaunchCwd(symlink, source, true)
		if err != nil {
			t.Fatal(err)
		}
		if got != wantManifestWorkspace {
			t.Fatalf("symlinked proposal launched from %q, want target workspace %q", got, wantManifestWorkspace)
		}
	})

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
		{name: "one-row terminal only shows connection", rows: 1, wantSessionRows: 1, wantRows: []int{1}, wantMissing: 2},
		{name: "two-row terminal keeps footer separate", rows: 2, wantSessionRows: 1, wantRows: []int{1, 2}, wantMissing: 3},
		{name: "three-row terminal keeps both headers and footer", rows: 3, wantSessionRows: 1, wantRows: []int{1, 2, 3}, wantMissing: 4},
		{name: "four-row terminal replaces divider with notice", rows: 4, wantSessionRows: 1, wantRows: []int{1, 2, 3, 4}, wantMissing: 5},
		{name: "five-row terminal has chrome but no content", rows: 5, wantSessionRows: 1, wantRows: []int{1, 2, 3, 4, 5}, wantMissing: 6},
		{name: "tall view reserves five chrome rows", rows: 7, wantSessionRows: 2, wantRows: []int{1, 2, 3, 4, 5, 6, 7}, wantMissing: 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := newRuntime(Config{Cwd: t.TempDir()})
			rt.ctx = context.Background()
			rt.mode = "review"
			rt.reviewFile = "/tmp/proposal.json"
			rt.size = size{cols: 40, rows: tc.rows}
			rt.review.session = &session{kind: "review", ready: true, hasData: true, cols: 40, rows: 8, term: vt.NewEmulator(40, 8)}
			rt.notice = "NOTICE"
			rt.offsets["review"] = 99
			output := captureStdout(t, func() { rt.handleAdmittedResize(size{cols: 40, rows: tc.rows}) })
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
			pane := vt.NewEmulator(40, tc.rows)
			_, _ = pane.WriteString(output)
			screen := strings.Split(pane.String(), "\n")
			if len(screen) != tc.rows {
				t.Fatalf("visible rows = %d, want %d", len(screen), tc.rows)
			}
			if tc.rows >= 3 && !strings.HasPrefix(screen[1], "Review") {
				t.Fatalf("context header collided with other chrome: %q", screen[1])
			}
			if tc.rows >= 4 && screen[tc.rows-2] != "NOTICE" {
				t.Fatalf("notice collided with divider or content: %q", screen[tc.rows-2])
			}
			if tc.rows >= 2 && !strings.Contains(screen[tc.rows-1], "REVIEW") {
				t.Fatalf("footer collided with header or notice: %q", screen[tc.rows-1])
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
	rt.handleAdmittedKey(uv.KeyPressEvent(uv.Key{Code: uv.KeyPgDown}))
	if got := rt.offsets["review"]; got != 1 {
		t.Fatalf("page down offset = %d, want 1", got)
	}
	rt.handleAdmittedKey(uv.KeyPressEvent(uv.Key{Code: uv.KeyEnd}))
	if got, want := rt.offsets["review"], rt.maxOffsetForCurrentSession(); got != want {
		t.Fatalf("end offset = %d, want %d", got, want)
	}
	rt.size = size{cols: 40, rows: 6}
	rt.offsets["review"] = 0
	rt.handleAdmittedKey(uv.KeyPressEvent(uv.Key{Code: uv.KeyPgDown}))
	if got := rt.offsets["review"]; got != 1 {
		t.Fatalf("single-row viewport page down offset = %d, want 1", got)
	}

	rt.size = size{cols: 40, rows: 5}
	rt.offsets["review"] = 5
	rt.handleAdmittedKey(uv.KeyPressEvent(uv.Key{Code: uv.KeyPgDown}))
	if got := rt.offsets["review"]; got != 0 {
		t.Fatalf("short viewport page down offset = %d, want 0", got)
	}
}
