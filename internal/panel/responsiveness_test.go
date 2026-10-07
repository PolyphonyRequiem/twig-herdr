package panel

import (
	"context"
	"errors"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	vt "github.com/charmbracelet/x/vt"
)

func TestStandaloneScrollRespondsWhileConnectionCheckIsPending(t *testing.T) {
	rt := newRuntime(Config{Cwd: t.TempDir(), InitialView: "tree", Standalone: true})
	rt.size = size{cols: 80, rows: 8}
	rt.ctx = context.Background()
	rt.admissionCancel = func() {}
	rt.admissionActions = []event{{kind: evBenchTick}}
	sess := &session{kind: "bench", view: "tree", term: vt.NewEmulator(80, 40), hasData: true}
	_, _ = sess.term.Write([]byte("FIRST ROW\r\nSECOND ROW\r\nTHIRD ROW\r\nFOURTH ROW\r\n"))
	rt.bench.active = sess
	output := captureStdout(t, func() { rt.handleKey(uv.KeyPressEvent{Code: 'j', Text: "j"}) })
	if rt.offsets["tree"] != 1 || !strings.Contains(output, "SECOND ROW") || strings.Contains(output, "FIRST ROW") {
		t.Fatal("local scrolling waited for connection admission instead of showing the next row")
	}
}

func TestStandaloneReviewPublishesOnlyAfterCompleteObservationAdmission(t *testing.T) {
	rt := newRuntime(Config{Cwd: t.TempDir(), InitialView: "tree", Standalone: true})
	rt.ctx = context.Background()
	rt.size = size{cols: 100, rows: 12}
	rt.mode = "review"
	rt.admissionCancel = func() {}
	sess := &session{kind: "review", term: vt.NewEmulator(100, 20)}
	rt.review.session = sess
	output := captureStdout(t, func() {
		rt.handleSessionData(sess, []byte("PRIVATE REVIEW\r\nReview only — Details / "))
		rt.draw()
	})
	if strings.Contains(output, "PRIVATE REVIEW") || rt.snapshot().Ready {
		t.Fatal("incomplete unqualified observation was displayed")
	}
	output = captureStdout(t, func() {
		rt.handleSessionData(sess, []byte("Back / Cancel: "))
		rt.draw()
	})
	if strings.Contains(output, "PRIVATE REVIEW") || rt.snapshot().Ready {
		t.Fatal("completed observation was displayed before its status check")
	}
	rt.admissionActionCount = len(rt.admissionActions)
	output = captureStdout(t, func() { rt.handleAdmissionResolved(event{token: rt.admissionGen}) })
	if !strings.Contains(output, "PRIVATE REVIEW") || !rt.snapshot().Ready {
		t.Fatal("admitted complete observation was not displayed")
	}
	// Density output belongs to the same native observation and stays responsive
	// even while its periodic connection-change check is outstanding.
	rt.admissionCancel = func() {}
	output = captureStdout(t, func() {
		rt.handleSessionData(sess, []byte("\r\nEXPANDED DETAILS\r\n"+reviewPrompt))
	})
	if !strings.Contains(output, "EXPANDED DETAILS") {
		t.Fatal("retained details waited for another subprocess admission")
	}
}

func TestStandaloneBenchHidesNewSummaryAndOutputUntilExitAdmission(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "admitted", true: "connection changed"}[changed], func(t *testing.T) {
			rt := newRuntime(Config{Cwd: t.TempDir(), InitialView: "tree", Standalone: true})
			rt.ctx = context.Background()
			rt.size = size{cols: 100, rows: 12}
			rt.benchSummary = "ORIGINAL BENCH"
			sess := &session{kind: "bench", view: "tree", term: vt.NewEmulator(100, 20), started: make(chan struct{})}
			output := captureStdout(t, func() {
				rt.handleSessionStarted(sess, "NEW PRIVATE BENCH")
				rt.handleSessionData(sess, []byte("NEW PRIVATE OUTPUT\r\n"))
			})
			if strings.Contains(output, "NEW PRIVATE") || rt.snapshot().Ready {
				t.Fatal("pending bench data or metadata was published before admission")
			}
			var admissionErr error
			if changed {
				admissionErr = errors.New("binding-changed/reconnect: actor changed")
			}
			output = captureStdout(t, func() { rt.handleSessionExit(sess, nil, admissionErr) })
			if changed {
				if strings.Contains(output, "NEW PRIVATE") || !rt.snapshot().ReconnectRequired || rt.snapshot().Ready {
					t.Fatal("changed actor's bench was published instead of clearing retained data")
				}
			} else if !strings.Contains(output, "NEW PRIVATE BENCH") || !strings.Contains(output, "NEW PRIVATE OUTPUT") || !rt.snapshot().Ready {
				t.Fatal("qualified bench summary and output were not published together")
			}
		})
	}
}
