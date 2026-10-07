package panel

import (
	"context"
	"errors"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	vt "github.com/charmbracelet/x/vt"
)

func TestBenchSelectionRespondsWhileConnectionCheckIsPending(t *testing.T) {
	for _, standalone := range []bool{false, true} {
		rt := newRuntime(Config{Cwd: t.TempDir(), InitialView: "tree", Standalone: standalone})
		rt.size = size{cols: 80, rows: 8}
		rt.ctx = context.Background()
		rt.admissionCancel = func() {}
		rt.admissionActions = []event{{kind: evBenchTick}}
		rt.browser.replace(&BrowserSnapshot{BenchID: "bench", Roots: []*BrowserNode{{Key: "1", ID: 1, Label: "FIRST ROW"}, {Key: "2", ID: 2, Label: "SECOND ROW"}}})
		captureStdout(t, func() { rt.handleKey(uv.KeyPressEvent{Code: 'j', Text: "j"}) })
		if rt.browser.selectedID != 2 {
			t.Fatal("local selection waited for connection admission")
		}
		captureStdout(t, func() { rt.handleKey(uv.KeyPressEvent{Code: 'p', Text: "p"}) })
		if rt.picker == nil || rt.picker.node.ID != 2 {
			t.Fatal("local pin picker waited for connection admission")
		}
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

func TestBenchPublishesOnlyAfterCompleteNativeOriginVerification(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "admitted", true: "connection changed"}[changed], func(t *testing.T) {
			cwd := t.TempDir()
			rt := newRuntime(Config{Cwd: cwd, InitialView: "tree", Standalone: true})
			rt.ctx = context.Background()
			rt.size = size{cols: 100, rows: 12}
			rt.binding = HostBinding{BindingID: "binding", IdentityID: "identity", WorktreeRoot: cwd}
			rt.benchSummary = "ORIGINAL BENCH"
			rt.bench.launchGen = 1
			snapshot := &BrowserSnapshot{BenchID: "bench", BenchName: "NEW PRIVATE BENCH", BindingID: "binding", IdentityID: "identity", WorktreeRoot: cwd, Roots: []*BrowserNode{{Key: "1", ID: 1, Label: "NEW PRIVATE OUTPUT"}}}
			var admissionErr error
			if changed {
				admissionErr = errors.New("binding-changed/reconnect: actor changed")
			}
			output := captureStdout(t, func() { rt.handleBrowserLoaded(event{token: 1, browser: snapshot, admissionErr: admissionErr}) })
			if changed {
				if strings.Contains(output, "NEW PRIVATE") || !rt.snapshot().ReconnectRequired || rt.snapshot().Ready {
					t.Fatal("changed actor's Bench was published")
				}
			} else if !strings.Contains(output, "NEW PRIVATE BENCH") || !strings.Contains(output, "NEW PRIVATE OUTPUT") || !rt.snapshot().Ready {
				t.Fatal("verified native Bench metadata and rows were not published together")
			}
		})
	}
}
