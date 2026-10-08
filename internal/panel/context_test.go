package panel

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadGitContextTracksWorktreeAndHeadTransitions(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("Git is required for real worktree coverage")
	}
	root := t.TempDir()
	contextTestGit(t, git, root, "init", "-b", "main")
	unborn := readGitContext(context.Background(), root)
	assertContextWorktree(t, unborn.worktree, root)
	if unborn.branch != "main (unborn)" {
		t.Fatalf("new repository branch = %q, want unborn main", unborn.branch)
	}
	contextTestGit(t, git, root, "-c", "user.name=Context Test", "-c", "user.email=context@example.invalid", "-c", "commit.gpgsign=false", "-c", "core.hooksPath="+filepath.Join(root, "no-hooks"), "commit", "--allow-empty", "-m", "header fixture")
	nested := filepath.Join(root, "nested", "directory")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	named := readGitContext(context.Background(), nested)
	assertContextWorktree(t, named.worktree, root)
	if named.branch != "main" {
		t.Fatalf("committed repository branch = %q, want main", named.branch)
	}
	worktree := filepath.Join(t.TempDir(), "linked-worktree")
	contextTestGit(t, git, root, "worktree", "add", "-b", "feature/header", worktree, "HEAD")
	linked := readGitContext(context.Background(), worktree)
	assertContextWorktree(t, linked.worktree, worktree)
	if linked.branch != "feature/header" {
		t.Fatalf("linked worktree branch = %q, want feature/header", linked.branch)
	}
	// A shell's repository override must not replace the admitted worktree.
	t.Setenv("GIT_DIR", filepath.Join(root, ".git"))
	t.Setenv("GIT_WORK_TREE", root)
	overridden := readGitContext(context.Background(), worktree)
	assertContextWorktree(t, overridden.worktree, worktree)
	if overridden.branch != "feature/header" {
		t.Fatalf("environment retargeted worktree branch to %q", overridden.branch)
	}
	contextTestGit(t, git, worktree, "checkout", "--detach", "HEAD")
	short := contextTestGit(t, git, worktree, "rev-parse", "--short", "HEAD")
	detached := readGitContext(context.Background(), worktree)
	assertContextWorktree(t, detached.worktree, worktree)
	if detached.branch != "detached "+short {
		t.Fatalf("detached branch = %q, want actual short commit %q", detached.branch, short)
	}
}

func TestReadGitContextReportsNonRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git is required to distinguish a non-repository")
	}
	cwd := t.TempDir()
	got := readGitContext(context.Background(), cwd)
	if got.worktree != cwd || got.branch != "unavailable (no Git worktree)" {
		t.Fatalf("non-repository context invented a repository or branch: %+v", got)
	}
}

func TestReadGitContextReportsMissingGit(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	got := readGitContext(context.Background(), cwd)
	if got.worktree != cwd || got.branch != "unavailable (git not found)" {
		t.Fatalf("missing Git context invented a repository or branch: %+v", got)
	}
}

func TestContextHeaderKeepsBranchBeforeLongOptionalContext(t *testing.T) {
	root := filepath.Join(t.TempDir(), "a-long-workspace-parent", "source-worktree")
	rt := newRuntime(Config{Cwd: filepath.Join(root, "nested"), InitialView: "tree", HerdrContext: "Herdr: w1 / w1:t2 / w1:p3"})
	rt.gitContext = gitContext{worktree: root, branch: "feature/header"}
	left, right := rt.contextHeaderText()
	if !strings.HasPrefix(left, "Tree · Worktree: source-worktree · Branch: feature/header") {
		t.Fatalf("header did not prioritize viewer, actual worktree and branch: %q", left)
	}
	if strings.Index(left, "feature/header") >= strings.Index(left, root) || right != rt.cfg.HerdrContext {
		t.Fatalf("optional context displaced branch or lost captured Herdr handles: %q / %q", left, right)
	}
	rt.mode, rt.reviewFile = "review", filepath.Join(root, "reviews", "captured-proposal.json")
	left, _ = rt.contextHeaderText()
	if !strings.HasPrefix(left, "Review · Worktree:") || !strings.HasSuffix(left, " · captured-proposal.json") || strings.Contains(left, root) {
		t.Fatalf("review did not identify its captured proposal after the Git context: %q", left)
	}
	rt.form = &configurationForm{}
	left, _ = rt.contextHeaderText()
	if !strings.HasPrefix(left, "Bench Configuration · Worktree:") {
		t.Fatalf("configuration form retained the previous viewer label: %q", left)
	}
}

func TestContextHeaderDoesNotInventHerdrContextOrRunGit(t *testing.T) {
	cwd := filepath.Join(t.TempDir(), "standalone-worktree")
	rt := newRuntime(Config{Cwd: cwd, InitialView: "table", Standalone: true})
	rt.gitContext = gitContext{worktree: cwd, branch: "retained-branch"}
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "uncaptured-workspace")
	left, right := rt.contextHeaderText()
	if !strings.HasPrefix(left, "Table · Worktree:") || !strings.Contains(left, "Branch: retained-branch") || right != "" {
		t.Fatalf("draw replaced retained Git metadata or invented ambient Herdr context: %q / %q", left, right)
	}
}

func TestContextHeaderSanitizesAndHidesPreviousContextOnRefusal(t *testing.T) {
	rt := newRuntime(Config{Cwd: "private-cwd", InitialView: "tree", HerdrContext: "Herdr: private-workspace\r / private-tab\t / private-pane\x1b[2J"})
	rt.gitContext = gitContext{worktree: filepath.Join("private-parent", "private-worktree\n"), branch: "private-branch\x1b[2J"}
	rt.mode, rt.reviewFile = "review", filepath.Join("private-parent", "private-proposal\n.json")
	left, right := rt.contextHeaderText()
	for _, text := range []string{left, right} {
		for _, r := range text {
			if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
				t.Fatalf("header allowed terminal controls from contextual data: %q", text)
			}
		}
	}
	rt.reconnectRequired = true
	left, right = rt.contextHeaderText()
	if left != "" || right != "" {
		t.Fatalf("refused connection still exposed previous context: %q / %q", left, right)
	}
}

func TestContextCompletionUpdatesReviewWithoutBenchData(t *testing.T) {
	rt, _ := browserFixture(t)
	rt.mode, rt.browser = "review", browserModel{}
	rt.gitContextGen, rt.gitContextCancel = 7, func() {}
	captureStdout(t, func() {
		rt.handleGitContextLoaded(event{token: 7, binding: rt.binding, gitContext: gitContext{worktree: rt.cfg.Cwd, branch: "review-current"}})
	})
	left, _ := rt.contextHeaderText()
	if !strings.HasPrefix(left, "Review ·") || !strings.Contains(left, "review-current") || rt.browser.snapshot != nil {
		t.Fatal("Review context required a completed Bench refresh")
	}
}

func TestRefusalErasesContextAndRejectsItsLateCompletion(t *testing.T) {
	rt, _ := browserFixture(t)
	rt.gitContext = gitContext{worktree: "PRIVATE WORKTREE", branch: "PRIVATE BRANCH"}
	rt.gitContextGen, rt.gitContextCancel = 7, func() {}
	captureStdout(t, func() { rt.stopForReconnect(errors.New("binding-changed: actor changed")) })
	if rt.gitContext != (gitContext{}) {
		t.Fatal("refusal retained hidden previous-actor Git metadata")
	}
	// Once a new actor is admitted, the old completion must still be fenced.
	rt.reconnectRequired = false
	captureStdout(t, func() {
		rt.handleGitContextLoaded(event{token: 7, binding: rt.binding, gitContext: gitContext{worktree: "PRIVATE WORKTREE", branch: "PRIVATE BRANCH"}})
	})
	left, _ := rt.contextHeaderText()
	if strings.Contains(left, "PRIVATE") {
		t.Fatal("reconnect revived previous-actor worktree metadata")
	}
}

func contextTestGit(t *testing.T, git, cwd string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, git, args...)
	cmd.Dir = cwd
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(key) {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE":
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func assertContextWorktree(t *testing.T, got, want string) {
	t.Helper()
	gotPath, err := filepath.EvalSymlinks(got)
	if err != nil {
		t.Fatal(err)
	}
	wantPath, err := filepath.EvalSymlinks(want)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != wantPath {
		t.Fatalf("worktree = %q, want actual root %q", gotPath, wantPath)
	}
}
