package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"time"
)

const hostCapability = "qualified-attachment-snapshot-v1"

// HostBinding is opaque native authority, not a plugin authentication selector.
// The CLI owns snapshot qualification and every operation's admission check.
type HostBinding struct {
	ProtocolVersion int    `json:"protocolVersion"`
	Capability      string `json:"capability"`
	Snapshot        string `json:"snapshot"`
	BindingID       string `json:"bindingId"`
	IdentityID      string `json:"identityId"`
	WorktreeRoot    string `json:"worktreeRoot"`
}

func ReadHostBinding(ctx context.Context, cwd, expected string) (HostBinding, error) {
	args := []string{"connection", "host-snapshot", "-o", "json"}
	if expected != "" {
		args = boundArgs(args, expected)
	}
	output, err := twigOutput(ctx, cwd, args...)
	if err != nil {
		return HostBinding{}, fmt.Errorf("native host admission refused: %w", err)
	}
	binding, err := parseHostBinding(output)
	if err != nil {
		return HostBinding{}, err
	}
	if expected != "" && binding.Snapshot != expected {
		return HostBinding{}, errors.New("binding-changed/reconnect: native attachment snapshot changed; explicitly reconnect")
	}
	return binding, nil
}

func parseHostBinding(output []byte) (HostBinding, error) {
	var binding HostBinding
	if err := json.Unmarshal(output, &binding); err != nil {
		return HostBinding{}, fmt.Errorf("invalid native host snapshot: %w", err)
	}
	if binding.ProtocolVersion != 1 || binding.Capability != hostCapability || strings.TrimSpace(binding.Snapshot) == "" || binding.BindingID == "" || binding.IdentityID == "" || binding.WorktreeRoot == "" {
		return HostBinding{}, fmt.Errorf("unsupported Twig host protocol: requires %s v1; upgrade CLI/plugin together before opening a panel", hostCapability)
	}
	return binding, nil
}

func boundArgs(args []string, snapshot string) []string {
	return append(args, "--connection-snapshot", snapshot)
}

func twigOutput(ctx context.Context, cwd string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "twig", args...)
	cmd.Dir = cwd
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("twig: %w: %s %s", err, strings.TrimSpace(string(output)), strings.TrimSpace(stderr.String()))
	}
	return output, nil
}

func bindingChanged(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "binding-changed/reconnect") || strings.Contains(lower, "binding-changed:") || strings.Contains(lower, "binding-transition-incomplete:") || strings.Contains(lower, "binding-default-transition-incomplete:") || strings.Contains(lower, "migration-incomplete:")
}

func proposalDigest(ctx context.Context, cwd, file, snapshot string) (string, error) {
	output, err := twigOutput(ctx, cwd, boundArgs([]string{"proposal", "validate", "--file", file, "-o", "json"}, snapshot)...)
	if err != nil {
		return "", err
	}
	var result struct {
		Valid  bool   `json:"valid"`
		Digest string `json:"digest"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return "", fmt.Errorf("invalid native proposal validation: %w", err)
	}
	if !result.Valid || result.Digest == "" {
		return "", errors.New("proposal did not validate; no review was opened")
	}
	if _, err := validateProposalCandidate(proposalCandidate{Found: true, File: file, Digest: result.Digest}); err != nil {
		return "", err
	}
	return result.Digest, nil
}

func sameWorkspace(left, right string) bool {
	l, err := filepath.EvalSymlinks(left)
	if err != nil {
		return false
	}
	r, err := filepath.EvalSymlinks(right)
	if err != nil {
		return false
	}
	if goruntime.GOOS == "windows" {
		return strings.EqualFold(l, r)
	}
	return l == r
}

func (rt *runtime) stopForReconnect(err error) {
	// Drop all prior actor's VT nodes, including scrollback and hidden restore data.
	// Native proposal files, digests, authorizers and journals remain untouched.
	rt.reconnectRequired = true
	rt.reconnectReason = "binding-changed/reconnect: " + safe(err.Error()) + "; Ctrl+R or twig-herdr reconnect acknowledges a fresh runtime"
	rt.bench.launchGen++
	rt.review.launchGen++
	rt.admissionGen++
	if rt.admissionCancel != nil {
		rt.admissionCancel()
		rt.admissionCancel = nil
	}
	admissionReply := rt.admissionReply
	rt.admissionReply = nil
	rt.reply(admissionReply, Result{Snapshot: rt.snapshot(), Err: err})
	for _, action := range rt.admissionActions {
		if action.kind == evRequest {
			rt.reply(action.req.Reply, Result{Snapshot: rt.snapshot(), Err: err})
		}
	}
	rt.admissionActions = nil
	rt.admissionActionCount = 0
	for _, sess := range []*session{rt.bench.active, rt.bench.pending, rt.review.session, rt.review.restoreBench} {
		if sess != nil {
			sess.term = nil
			sess.hasData = false
			sess.ready = false
			sess.promptTail = ""
			sess.reviewEchoBuffer = nil
		}
	}
	rt.cancelReviewLookup(err)
	rt.cancelBenchLaunch(err)
	rt.cancelBenchPending(err)
	rt.cancelReviewLaunch(err)
	rt.cancelReviewSession(err)
	for _, sess := range []*session{rt.bench.active, rt.review.restoreBench} {
		if sess != nil && sess.cancel != nil {
			sess.cancel()
		}
	}
	rt.bench.active = nil
	rt.review.restoreBench = nil
	rt.review.hasPendingResize = false
	rt.review.hasPendingDensity = false
	rt.benchSummary = "unavailable (original binding " + rt.binding.BindingID + ")"
	rt.offsets = map[string]int{"table": 0, "tree": 0, "review": 0}
	rt.setError(rt.reconnectReason)
	rt.draw()
}

func (rt *runtime) checkAdmission(reply chan Result, reconnect bool) {
	if rt.closing {
		rt.reply(reply, Result{Snapshot: rt.snapshot(), Err: errors.New("panel is closing")})
		return
	}
	if rt.admissionCancel != nil {
		if !reconnect {
			return
		}
		rt.admissionCancel()
		rt.reply(rt.admissionReply, Result{Snapshot: rt.snapshot(), Err: errors.New("reconnect superseded")})
		rt.admissionReply = nil
	}
	rt.admissionActionCount = len(rt.admissionActions)
	if reconnect {
		rt.stopForReconnect(errors.New("explicit reconnect acknowledged; checking fresh native admission"))
	}
	rt.admissionGen++
	gen := rt.admissionGen
	ctx, cancel := context.WithTimeout(rt.ctx, 15*time.Second)
	rt.admissionCancel = cancel
	rt.admissionReply = reply
	expected := rt.binding.Snapshot
	if reconnect {
		expected = ""
	}
	go func() {
		binding, err := ReadHostBinding(ctx, rt.cfg.Cwd, expected)
		rt.emit(event{kind: evAdmissionResolved, token: gen, binding: binding, err: err, reconnect: reconnect})
	}()
}

func (rt *runtime) handleAdmissionResolved(ev event) {
	if rt.closing || ev.token != rt.admissionGen {
		return
	}
	if rt.admissionCancel != nil {
		rt.admissionCancel()
		rt.admissionCancel = nil
	}
	reply := rt.admissionReply
	rt.admissionReply = nil
	if ev.err != nil {
		rt.stopForReconnect(ev.err)
		rt.reply(reply, Result{Snapshot: rt.snapshot(), Err: ev.err})
		return
	}
	if !ev.reconnect {
		// A heartbeat started before an action cannot admit that newer action.
		// Only replay the prefix present when this native check began.
		actions := rt.admissionActions[:rt.admissionActionCount]
		rt.admissionActions = rt.admissionActions[rt.admissionActionCount:]
		rt.admissionActionCount = 0
		for _, action := range actions {
			if rt.closing || rt.reconnectRequired {
				break
			}
			switch action.kind {
			case evKey:
				rt.handleAdmittedKey(action.key)
			case evResize:
				rt.handleAdmittedResize(action.size)
			case evRequest:
				rt.handleAdmittedRequest(action.req)
			case evSessionData:
				rt.handleAdmittedSessionData(action.session, action.data)
			case evNoticeExpire:
				if rt.noticeSeq == action.token {
					rt.notice = ""
					rt.draw()
				}
			}
		}
		if len(rt.admissionActions) != 0 && !rt.closing && !rt.reconnectRequired {
			rt.checkAdmission(nil, false)
		}
		return
	}
	rt.binding = ev.binding
	rt.reconnectRequired = false
	rt.benchSummary = "loading"
	rt.reconnectReason = ""
	rt.reviewFile = ""
	rt.mode = "bench"
	rt.setNotice("Reconnected; fetching under the newly admitted binding", noticeDuration)
	rt.beginBenchRefresh(reply, true)
}
