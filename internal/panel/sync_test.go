package panel

import (
	"context"
	"errors"
	"strings"
	"testing"

	vt "github.com/charmbracelet/x/vt"
)

func TestSyncKeepsCapturedReviewInBothBrowsers(t *testing.T) {
	for _, standalone := range []bool{false, true} {
		label := "Herdr"
		if standalone {
			label = "standalone"
		}
		t.Run(label, func(t *testing.T) {
			rt := newRuntime(Config{Cwd: t.TempDir(), Standalone: standalone})
			rt.ctx = context.Background()
			rt.size = size{cols: 100, rows: 12}
			rt.mode = "review"
			rt.reviewFile = "original-proposal.json"
			sess := &session{kind: "review", term: vt.NewEmulator(100, 20), hasData: true, ready: true}
			_, _ = sess.term.Write([]byte("ORIGINAL CAPTURED REVIEW"))
			rt.review.session = sess
			rt.syncCancel = func() {}
			rt.syncGen = 1
			output := captureStdout(t, func() { rt.handleSyncDone(event{token: 1}) })
			if !strings.Contains(output, "ORIGINAL CAPTURED REVIEW") || rt.review.session != sess || rt.snapshot().ReviewFile != "original-proposal.json" || !rt.snapshot().Ready {
				t.Fatal("sync replaced or invalidated the captured proposal review")
			}
			rt.syncCancel = func() {}
			rt.syncGen = 2
			output = captureStdout(t, func() { rt.handleSyncDone(event{token: 2, err: context.DeadlineExceeded}) })
			if !strings.Contains(output, "ORIGINAL CAPTURED REVIEW") || rt.snapshot().ReconnectRequired || !rt.snapshot().Ready {
				t.Fatal("sync timeout discarded an unchanged connection's captured review")
			}
			rt.syncCancel = func() {}
			rt.syncGen = 3
			output = captureStdout(t, func() {
				rt.handleSyncDone(event{token: 3, admissionErr: errors.New("binding-changed/reconnect: principal changed")})
			})
			if strings.Contains(output, "ORIGINAL CAPTURED REVIEW") || !rt.snapshot().ReconnectRequired || rt.snapshot().Ready {
				t.Fatal("sync retained prior actor data after connection change")
			}
		})
	}
}

func TestCanceledSyncCompletionCannotReviveOldReview(t *testing.T) {
	rt := newRuntime(Config{Cwd: t.TempDir(), Standalone: true})
	rt.ctx = context.Background()
	rt.size = size{cols: 100, rows: 12}
	rt.mode = "review"
	rt.syncGen = 1
	rt.syncCancel = func() {}
	rt.cancelSync()
	output := captureStdout(t, func() { rt.handleSyncDone(event{token: 1}) })
	if output != "" || rt.snapshot().Ready {
		t.Fatal("canceled sync completion changed the browser")
	}
}
