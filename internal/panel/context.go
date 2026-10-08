package panel

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// gitContext is refreshed independently of Bench and Review, never during rendering.
type gitContext struct {
	worktree string
	branch   string
}

func readGitContext(ctx context.Context, cwd string) gitContext {
	result := gitContext{worktree: cwd, branch: "unavailable"}
	git, err := exec.LookPath("git")
	if err != nil {
		result.branch = "unavailable (git not found)"
		return result
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	// Repository overrides belong to the launching shell, not the captured cwd.
	// Do not let them silently label a different worktree or branch.
	env := os.Environ()
	filtered := env[:0]
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(key) {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE":
			continue
		}
		filtered = append(filtered, entry)
	}
	read := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, git, args...)
		cmd.Dir = cwd
		cmd.Env = filtered
		cmd.WaitDelay = 200 * time.Millisecond
		output, err := cmd.Output()
		return strings.TrimRight(string(output), "\r\n"), err
	}
	root, err := read("rev-parse", "--show-toplevel")
	if err != nil || root == "" {
		if ctx.Err() == nil {
			result.branch = "unavailable (no Git worktree)"
		}
		return result
	}
	result.worktree = filepath.Clean(root)
	branch, branchErr := read("symbolic-ref", "--quiet", "--short", "HEAD")
	commit, commitErr := read("rev-parse", "--verify", "--quiet", "--short", "HEAD")
	if ctx.Err() != nil {
		return result
	}
	if branchErr == nil && branch != "" {
		result.branch = branch
		if commitErr != nil {
			if exit, ok := commitErr.(*exec.ExitError); ok && exit.ExitCode() == 1 {
				result.branch += " (unborn)"
			} else {
				result.branch += " (commit unavailable)"
			}
		}
	} else if commitErr == nil && commit != "" {
		result.branch = "detached " + commit
	}
	return result
}

func (rt *runtime) contextHeaderText() (left, right string) {
	if rt.reconnectRequired {
		return "", ""
	}
	mode := "Table"
	if rt.benchView == "tree" {
		mode = "Tree"
	}
	if rt.mode == "review" {
		mode = "Review"
	}
	if rt.configuration != nil || rt.form != nil {
		mode = "Bench Configuration"
	}
	worktree := rt.gitContext.worktree
	if worktree == "" {
		worktree = rt.cfg.Cwd
	}
	name := "unavailable"
	if worktree != "" {
		name = filepath.Base(filepath.Clean(worktree))
	}
	branch := rt.gitContext.branch
	if branch == "" {
		branch = "unavailable"
	}
	left = mode + " · Worktree: " + safe(name) + " · Branch: " + safe(branch)
	if rt.mode == "review" && rt.reviewFile != "" {
		left += " · " + safe(filepath.Base(rt.reviewFile))
	} else if worktree != "" {
		left += " · " + safe(worktree)
	}
	return left, safe(rt.cfg.HerdrContext)
}

func (rt *runtime) beginGitContextRefresh() {
	if rt.ctx == nil || rt.ctx.Err() != nil || rt.closing || rt.reconnectRequired || rt.gitContextCancel != nil {
		return
	}
	rt.gitContextGen++
	gen, binding, cwd := rt.gitContextGen, rt.binding, rt.cfg.Cwd
	ctx, cancel := context.WithTimeout(rt.ctx, 15*time.Second)
	rt.gitContextCancel = cancel
	go func() {
		defer cancel()
		info := readGitContext(ctx, cwd)
		_, admissionErr := rt.readBinding(ctx, cwd, binding.Snapshot)
		rt.emit(event{kind: evGitContextLoaded, token: gen, binding: binding, gitContext: info, admissionErr: admissionErr})
	}()
}

func (rt *runtime) cancelGitContext() {
	rt.gitContextGen++
	if rt.gitContextCancel != nil {
		rt.gitContextCancel()
		rt.gitContextCancel = nil
	}
	rt.gitContext = gitContext{}
}

func (rt *runtime) handleGitContextLoaded(ev event) {
	if rt.closing || rt.reconnectRequired || ev.token != rt.gitContextGen || rt.gitContextCancel == nil {
		return
	}
	rt.gitContextCancel()
	rt.gitContextCancel = nil
	if !sameManagementOrigin(ev.binding, rt.binding) {
		return
	}
	if ev.admissionErr != nil {
		rt.stopForReconnect(ev.admissionErr)
		return
	}
	rt.gitContext = ev.gitContext
	rt.draw()
}
