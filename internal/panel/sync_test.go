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
			output := captureStdout(t, func() {
				rt.handleSyncDone(event{token: 1, syncResult: &benchSyncResult{Kind: "benchSync", BenchID: "bench", BenchName: "My Bench", ItemCount: 12, MemberCount: 8, RelationshipCount: 4, ProtectedCount: 2}})
			})
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

func TestBenchSyncResultRefusesAnotherBench(t *testing.T) {
	output := []byte(`{"kind":"benchSync","benchId":"other-bench","benchName":"Other","itemCount":3,"memberCount":2,"relationshipCount":1,"protectedCount":0}`)
	if _, err := parseBenchSyncResult(output, "captured-bench"); err == nil {
		t.Fatal("a sync response for another Bench was accepted as the captured Bench's pull")
	}
}

func TestBenchSyncResultRequiresReportedNonnegativeCounts(t *testing.T) {
	for _, output := range []string{
		`{"kind":"benchSync","benchId":"bench","itemCount":3,"memberCount":2,"relationshipCount":1,"protectedCount":-1}`,
		`{"kind":"benchSync","benchId":"bench","itemCount":3,"memberCount":2,"protectedCount":0}`,
		`{"kind":"benchSync","benchId":"bench","itemCount":3,"memberCount":2,"relationshipCount":2,"protectedCount":0}`,
	} {
		if _, err := parseBenchSyncResult([]byte(output), "bench"); err == nil {
			t.Fatal("negative or unreported sync counts were accepted")
		}
	}
}
