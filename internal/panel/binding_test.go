package panel

import (
	"context"
	"errors"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	vt "github.com/charmbracelet/x/vt"
)

// These fixtures exercise the real event callbacks and VT renderer. Native
// store/principal admission is verified separately with the CLI transport fixture.
func TestBindingRefusalErasesVisibleAndHiddenActorData(t *testing.T) {
	rt := newRuntime(Config{Cwd: t.TempDir(), InitialView: "tree"})
	rt.ctx = context.Background()
	rt.size = size{cols: 100, rows: 10}
	rt.binding = HostBinding{Snapshot: "native-origin", BindingID: "original-binding", IdentityID: "original-principal"}
	rt.mode = "review"
	rt.reviewFile = "original-proposal.json"
	bench := &session{kind: "bench", term: vt.NewEmulator(100, 6), hasData: true}
	bench.term.SetScrollbackSize(100)
	_, _ = bench.term.Write([]byte(strings.Repeat("PRIVATE ORIGINAL BENCH\r\n", 12)))
	review := &session{kind: "review", gen: 1, term: vt.NewEmulator(100, 6), hasData: true, ready: true}
	_, _ = review.term.Write([]byte("PRIVATE ORIGINAL PROPOSAL"))
	rt.review.launchGen = 1
	rt.review.session = review
	rt.review.restoreBench = bench
	rt.bench.active = bench
	pane := vt.NewEmulator(100, 10)
	pane.SetScrollbackSize(100)
	_, _ = pane.Write([]byte(strings.Repeat("PRIVATE ORIGINAL PANE\r\n", 20)))

	output := captureStdout(t, func() {
		// Only the native host-admission result establishes reconnect refusal.
		rt.handleAdmissionResolved(event{kind: evAdmissionResolved, token: rt.admissionGen,
			err: errors.New("binding-changed: profile intent changed")})
	})
	finalScreen := output[strings.LastIndex(output, "\x1b[H\x1b[2J"):]
	visible := ansi.Strip(finalScreen)
	if strings.Contains(visible, "PRIVATE ORIGINAL") || !strings.Contains(visible, "reconnect required") {
		t.Fatalf("stopped panel exposed original actor data: %q", visible)
	}
	_, _ = pane.Write([]byte(output))
	if pane.ScrollbackLen() != 0 {
		t.Fatal("containing terminal retained original actor scrollback after refusal")
	}
	if bench.term != nil || review.term != nil {
		t.Fatal("hidden old actor VT buffers survived the refusal")
	}
	status := rt.snapshot()
	if status.Ready || !status.ReconnectRequired || status.IdentityID != "original-principal" || status.ReviewFile != "original-proposal.json" {
		t.Fatalf("stopped status lost original provenance or stayed ready: %+v", status)
	}

	for _, request := range []Request{{Command: "view", View: "table"}, {Command: "review", File: "different.json"}, {Command: "exit-review"}} {
		reply := make(chan Result, 1)
		request.Reply = reply
		rt.handleRequest(request)
		if result := <-reply; result.Err == nil || !result.Snapshot.ReconnectRequired {
			t.Fatalf("ordinary request silently reattached: %+v", result)
		}
	}
	captureStdout(t, func() {
		rt.handleKey(uv.KeyPressEvent(uv.Key{Code: 'r'}))
		rt.handleKey(uv.KeyPressEvent(uv.Key{Code: '1'}))
		rt.handleResize(size{cols: 80, rows: 8})
		rt.beginBenchRefresh(nil, false)
		// Late bytes and an old admission result cannot revive old data.
		rt.handleSessionData(review, []byte("PRIVATE LATE RESULT"))
		rt.handleAdmissionResolved(event{kind: evAdmissionResolved, token: rt.admissionGen - 1, reconnect: true, binding: HostBinding{Snapshot: "late-new-actor"}})
	})
	if !rt.snapshot().ReconnectRequired || rt.binding.Snapshot != "native-origin" || rt.bench.launchCancel != nil || rt.review.launchCancel != nil {
		t.Fatal("refresh, resize, view selection or stale callback silently reconnected")
	}
}

func TestRenderedWorkItemTextIsNotNativeReconnectEvidence(t *testing.T) {
	rt := newRuntime(Config{Cwd: t.TempDir(), InitialView: "tree"})
	rt.ctx = context.Background()
	rt.size = size{cols: 100, rows: 10}
	rt.binding = HostBinding{Snapshot: "native-origin", BindingID: "original-binding", IdentityID: "original-principal"}
	rt.bench.launchGen = 1
	sess := &session{kind: "bench", gen: 1, term: vt.NewEmulator(100, 6)}
	rt.bench.active = sess
	output := captureStdout(t, func() {
		rt.handleSessionData(sess, []byte("PRIVATE binding-changed:"))
		rt.handleSessionData(sess, []byte(" migration-incomplete: design notes"))
	})
	if rt.snapshot().ReconnectRequired || !strings.Contains(ansi.Strip(output), "PRIVATE binding-changed:") {
		t.Fatalf("successful work-item content was treated as a native refusal: %q", ansi.Strip(output))
	}
}

func TestFailedExplicitReconnectKeepsPanelStopped(t *testing.T) {
	rt := newRuntime(Config{Cwd: t.TempDir(), InitialView: "table"})
	rt.size = size{cols: 80, rows: 8}
	rt.binding = HostBinding{Snapshot: "old-native-origin", BindingID: "original-binding"}
	rt.admissionGen = 2
	reply := make(chan Result, 1)
	rt.admissionReply = reply
	captureStdout(t, func() {
		rt.handleAdmissionResolved(event{kind: evAdmissionResolved, token: 2, reconnect: true, err: errors.New("native migration is fenced by an open legacy host")})
	})
	result := <-reply
	if result.Err == nil || result.Snapshot.Ready || !result.Snapshot.ReconnectRequired || rt.binding.Snapshot != "old-native-origin" {
		t.Fatalf("failed reconnect admitted an actor: %+v", result)
	}
}

func TestFailedWorkspaceAdmissionStopsBeforeReportingReady(t *testing.T) {
	for _, operation := range []struct {
		name string
		err error
	}{
		{name: "successful subprocess", err: nil},
		{name: "failed subprocess", err: errors.New("workspace operation failed")},
	} {
		t.Run(operation.name, func(t *testing.T) {
			rt := newRuntime(Config{Cwd: t.TempDir(), InitialView: "table"})
			rt.size = size{cols: 100, rows: 10}
			rt.binding = HostBinding{Snapshot: "native-origin", BindingID: "original-binding", IdentityID: "original-principal"}
			rt.bench.launchGen = 1
			sess := &session{kind: "bench", gen: 1, view: "table", term: vt.NewEmulator(100, 6), hasData: true}
			_, _ = sess.term.Write([]byte("PRIVATE ORIGINAL WORKSPACE"))
			rt.bench.pending = sess
			reply := make(chan Result, 1)
			rt.bench.pendingReply = reply
			output := captureStdout(t, func() {
				rt.handleSessionExit(sess, operation.err, errors.New("binding-changed: native origin is no longer current"))
			})
			result := <-reply
			if result.Err == nil || result.Snapshot.Ready || !result.Snapshot.ReconnectRequired {
				t.Fatalf("workspace became ready despite native reconnect refusal: %+v", result)
			}
			if strings.Contains(ansi.Strip(output), "PRIVATE ORIGINAL WORKSPACE") {
				t.Fatal("operation published its original actor output")
			}
		})
	}
}

func TestLocalReviewActionCannotRedrawBeforeOriginQualification(t *testing.T) {
	for _, action := range []struct {
		name string
		invoke func(*runtime)
	}{
		{name: "redraw", invoke: func(rt *runtime) { rt.handleKey(uv.KeyPressEvent(uv.Key{Code: 'r'})) }},
		{name: "scroll to hidden output", invoke: func(rt *runtime) { rt.handleKey(uv.KeyPressEvent(uv.Key{Code: uv.KeyEnd})) }},
		{name: "resize", invoke: func(rt *runtime) { rt.handleResize(size{cols: 90, rows: 9}) }},
	} {
		t.Run(action.name, func(t *testing.T) {
			rt := newRuntime(Config{Cwd: t.TempDir(), InitialView: "tree"})
			rt.ctx = context.Background()
			rt.size = size{cols: 100, rows: 10}
			rt.mode = "review"
			rt.binding = HostBinding{Snapshot: "native-origin", BindingID: "original-binding", IdentityID: "original-principal"}
			sess := &session{kind: "review", term: vt.NewEmulator(100, 6), ready: true, hasData: true, cols: 100, rows: 6}
			sess.term.SetScrollbackSize(100)
			_, _ = sess.term.Write([]byte(strings.Repeat("PRIVATE ORIGINAL REVIEW\r\n", 12)))
			rt.review.session = sess
			// The action arrives while a native heartbeat result is outstanding.
			// It cannot use that earlier check as authority to redraw retained data.
			rt.admissionCancel = func() {}
			pending := captureStdout(t, func() { action.invoke(rt) })
			if strings.Contains(ansi.Strip(pending), "PRIVATE ORIGINAL REVIEW") || sess.cols != 100 || sess.rows != 6 || rt.offsets["review"] != 0 {
				t.Fatal("unqualified first action changed or exposed the retained review")
			}
			stopped := captureStdout(t, func() {
				rt.handleAdmissionResolved(event{token: rt.admissionGen, err: errors.New("binding-changed: original origin is no longer current")})
			})
			if strings.Contains(ansi.Strip(stopped), "PRIVATE ORIGINAL REVIEW") || !rt.snapshot().ReconnectRequired || rt.snapshot().Ready {
				t.Fatal("first local action did not clear and stop its unavailable actor")
			}
		})
	}
}

func TestLateReviewStreamRefusalDoesNotPublishPromptReady(t *testing.T) {
	rt := newRuntime(Config{Cwd: t.TempDir(), InitialView: "tree"})
	rt.ctx = context.Background()
	rt.size = size{cols: 100, rows: 10}
	rt.mode = "review"
	rt.binding = HostBinding{Snapshot: "native-origin", BindingID: "original-binding", IdentityID: "original-principal"}
	sess := &session{kind: "review", term: vt.NewEmulator(100, 6), cols: 100, rows: 6, pendingDensity: "d", hasPendingDensity: true, pendingResize: size{cols: 90, rows: 5}, hasPendingResize: true}
	rt.review.session = sess
	reply := make(chan Result, 1)
	rt.review.pendingReply = reply
	rt.admissionCancel = func() {}
	pending := captureStdout(t, func() {
		rt.handleSessionData(sess, []byte("PRIVATE RETIRED REVIEW MODEL\r\n"))
		rt.handleSessionData(sess, []byte(reviewPrompt))
	})
	if strings.Contains(ansi.Strip(pending), "PRIVATE RETIRED REVIEW MODEL") || rt.snapshot().Ready || sess.cols != 100 || sess.rows != 6 {
		t.Fatal("unqualified review stream rendered, became ready or consumed a deferred resize")
	}
	select {
	case result := <-reply:
		t.Fatalf("unqualified prompt published a reply: %+v", result)
	default:
	}
	stopped := captureStdout(t, func() {
		rt.handleAdmissionResolved(event{token: rt.admissionGen, err: errors.New("binding-changed: model origin changed after proposal retirement")})
	})
	result := <-reply
	if result.Err == nil || result.Snapshot.Ready || !result.Snapshot.ReconnectRequired || strings.Contains(ansi.Strip(stopped), "PRIVATE RETIRED REVIEW MODEL") {
		t.Fatal("late review prompt bypassed original-origin refusal")
	}
}

func TestAdmittedReviewStreamPublishesCompletePrompt(t *testing.T) {
	rt := newRuntime(Config{Cwd: t.TempDir(), InitialView: "tree"})
	rt.ctx = context.Background()
	rt.size = size{cols: 100, rows: 10}
	rt.mode = "review"
	sess := &session{kind: "review", term: vt.NewEmulator(100, 6), cols: 100, rows: 6}
	rt.review.session = sess
	reply := make(chan Result, 1)
	rt.review.pendingReply = reply
	rt.admissionCancel = func() {}
	captureStdout(t, func() {
		rt.handleSessionData(sess, []byte("ADMITTED REVIEW MODEL\r\n"))
		rt.handleSessionData(sess, []byte(reviewPrompt))
	})
	// Complete the native qualification that covers both queued stream chunks.
	rt.admissionActionCount = len(rt.admissionActions)
	output := captureStdout(t, func() {
		rt.handleAdmissionResolved(event{token: rt.admissionGen})
	})
	result := <-reply
	if result.Err != nil || !result.Snapshot.Ready || result.Snapshot.ReconnectRequired || !strings.Contains(ansi.Strip(output), "ADMITTED REVIEW MODEL") {
		t.Fatalf("qualified multi-chunk review failed: result=%+v, frame=%q", result, ansi.Strip(output))
	}
}

func TestUnsupportedOrIncompleteNativeHostProtocolRefuses(t *testing.T) {
	for _, response := range []string{
		`{"protocolVersion":0,"capability":"qualified-attachment-snapshot-v1","snapshot":"opaque","bindingId":"b","identityId":"p","worktreeRoot":"root"}`,
		`{"protocolVersion":1,"capability":"legacy","snapshot":"opaque","bindingId":"b","identityId":"p","worktreeRoot":"root"}`,
		`{"protocolVersion":1,"capability":"qualified-attachment-snapshot-v1","snapshot":"","bindingId":"b","identityId":"p","worktreeRoot":"root"}`,
	} {
		if _, err := parseHostBinding([]byte(response)); err == nil {
			t.Fatalf("unsupported/incomplete helper was admitted: %s", response)
		}
	}
}
