package panel

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

func detailFixture(t *testing.T) (*runtime, *DetailDocument) {
	t.Helper()
	rt, snapshot := browserFixture(t)
	var body strings.Builder
	for i := 1; i <= 120; i++ {
		body.WriteString("\x1b[1;36mparagraph-" + strconv.Itoa(i) + "\x1b[0m\n")
	}
	document := &DetailDocument{Version: 1, BenchID: snapshot.BenchID, BenchName: snapshot.BenchName,
		BindingID: rt.binding.BindingID, IdentityID: rt.binding.IdentityID, WorkItemID: 1,
		Title: "Long parent", ANSI: body.String()}
	rt.detail = detailState{active: true, id: 1, title: document.Title, benchID: snapshot.BenchID,
		bench: snapshot.BenchName, binding: rt.binding, gen: 4, document: document}
	rt.layoutDetail(rt.contentCols())
	return rt, document
}

func TestDetailReadRejectsRetargetingOriginAndExecutableTerminalControls(t *testing.T) {
	rt, document := detailFixture(t)
	for _, change := range []func(*DetailDocument){
		func(d *DetailDocument) { d.WorkItemID = 3 },
		func(d *DetailDocument) { d.BenchID = "different" },
		func(d *DetailDocument) { d.IdentityID = "different-actor" },
		func(d *DetailDocument) { d.ANSI = "unsafe\x1b[2J" },
		func(d *DetailDocument) { d.ANSI = "unsafe\x1b]52;c;secret\a" },
		func(d *DetailDocument) { d.ANSI = "unsafe\u009b2J" },
	} {
		copy := *document
		change(&copy)
		encoded, err := json.Marshal(copy)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := parseDetail(encoded, rt.binding, document.BenchID, 1); err == nil {
			t.Fatalf("unsafe/retargeted detail admitted: %+v", copy)
		}
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseDetail(encoded, rt.binding, document.BenchID, 1)
	if err != nil || !strings.Contains(parsed.ANSI, "\x1b[1;36m") {
		t.Fatal("renderer-owned SGR must retain legitimate rich styling", err)
	}
}

func TestDetailScrollAndEscapeDoNotChangeBenchSelectionFoldsOrViewport(t *testing.T) {
	rt, _ := detailFixture(t)
	rt.size.rows = panelChromeRows + 3
	rt.browser.ensureLayout(rt.benchView, rt.contentCols())
	rt.browser.selectIndex(2)
	rt.browser.collapsed["root/1"] = true
	rt.browser.dirty = true
	rt.browser.ensureLayout(rt.benchView, rt.contentCols())
	rt.offsets["tree"], rt.offsets["table"] = 3, 7
	selected, selectedID := rt.browser.selected, rt.browser.selectedID
	folds := make(map[string]bool)
	for key, value := range rt.browser.collapsed {
		folds[key] = value
	}
	captureStdout(t, func() {
		rt.handleDetailKey(uv.KeyPressEvent{Code: 'j', Text: "j"})
		rt.handleDetailMouse(uv.Mouse{Button: uv.MouseWheelDown})
	})
	if rt.detail.offset != 4 {
		t.Fatal("detail keys and wheel did not scroll the independent detail viewport")
	}
	captureStdout(t, func() { rt.handleDetailKey(uv.KeyPressEvent{Code: uv.KeyEnd}) })
	var out strings.Builder
	rt.drawDetail(&out, rt.contentCols(), rt.contentVisibleRows())
	if !strings.Contains(ansi.Strip(out.String()), "paragraph-120") {
		t.Fatal("End did not expose the final uncapped detail line")
	}
	captureStdout(t, func() { rt.handleDetailKey(uv.KeyPressEvent{Code: uv.KeyEscape}) })
	if rt.detail.active || rt.detail.document != nil || rt.browser.selected != selected || rt.browser.selectedID != selectedID || !reflect.DeepEqual(rt.browser.collapsed, folds) || rt.offsets["tree"] != 3 || rt.offsets["table"] != 7 {
		t.Fatal("Esc changed hidden Bench state or retained closed detail data")
	}
}

func TestDetailLateResponseCannotReplaceReflowOrReviveClosedActor(t *testing.T) {
	rt, original := detailFixture(t)
	stale := *original
	stale.Title = "stale-response"
	captureStdout(t, func() { rt.handleDetailLoaded(event{token: 3, detail: &stale}) })
	if rt.detail.document != original || rt.detail.title != original.Title {
		t.Fatal("stale generation replaced the captured rich detail")
	}
	captureStdout(t, rt.closeDetail)
	captureStdout(t, func() { rt.handleDetailLoaded(event{token: 4, detail: &stale}) })
	if rt.detail.active || rt.detail.document != nil {
		t.Fatal("closed detail was revived by a late callback")
	}
	rt, original = detailFixture(t)
	rt.binding.IdentityID = "replacement-actor"
	captureStdout(t, func() { rt.handleDetailLoaded(event{token: 4, detail: original}) })
	if rt.detail.active || rt.detail.document != nil {
		t.Fatal("prior actor's detail survived an origin change")
	}
}

func TestDetailReconcileClosesChangedBenchWithoutRetargeting(t *testing.T) {
	rt, _ := detailFixture(t)
	rt.browser.snapshot.BenchID = "replacement-bench"
	rt.reconcileDetail()
	if rt.detail.active || rt.detail.document != nil {
		t.Fatal("detail silently followed a replacement Bench")
	}
}

func TestDetailReflowPreservesWideGraphemesAndStylingWithoutLineCap(t *testing.T) {
	rt, document := detailFixture(t)
	document.ANSI = "\x1b[38;2;12;34;56mABC界DEF🙂GHIJKLMNOPQRSTUVWXYZ\x1b[0m\n" + strings.Repeat("tail\n", 11000)
	rt.detail.cols = 0
	rt.layoutDetail(4)
	var joined strings.Builder
	for _, line := range rt.detail.lines {
		if ansi.StringWidth(line) > 4 {
			t.Fatal("detail reflow overflowed the requested terminal width")
		}
		joined.WriteString(ansi.Strip(line))
	}
	if !strings.HasPrefix(joined.String(), "ABC界DEF🙂GHIJKLMNOPQRSTUVWXYZ") || strings.Count(joined.String(), "tail") != 11000 {
		t.Fatal("rich reflow lost graphemes or silently capped long content")
	}
	if !strings.Contains(rt.detail.lines[1], "\x1b[38;2;12;34;56m") {
		t.Fatal("scrolling to a continuation lost native styling")
	}
}

func TestOpeningDetailSupersedesOlderRefreshReviewAndHostView(t *testing.T) {
	rt, snapshot := browserFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rt.ctx = ctx
	rt.bench.launchGen, rt.review.lookupGen, rt.admissionGen = 9, 7, 3
	rt.bench.launchCancel, rt.review.lookupCancel = func() {}, func() {}
	rt.admissionActions = []event{{kind: evRequest, req: Request{Command: "view", View: "table"}}}
	rt.admissionActionCount = 1
	captureStdout(t, rt.openDetail)
	if !rt.detail.active {
		t.Fatal("Enter did not open detail from the retained selected item")
	}
	replacement := *snapshot
	replacement.BenchName = "superseded refresh"
	captureStdout(t, func() {
		rt.handleBrowserLoaded(event{token: 9, browser: &replacement})
		rt.handleLatestResolved(event{token: 7, err: errors.New("superseded Review lookup")})
		rt.handleAdmissionResolved(event{token: 3, binding: rt.binding})
	})
	if !rt.detail.active || rt.browser.snapshot != snapshot || rt.benchView != "tree" {
		t.Fatal("an older refresh, Review lookup or admitted host view replaced the newly selected detail")
	}
	rt.cancelDetail()
}

func TestSyncCompletionCannotRetargetAnOpenedDetail(t *testing.T) {
	rt, snapshot := browserFixture(t)
	rt.syncCancel = func() {}
	captureStdout(t, rt.openDetail)
	if rt.detail.active || rt.browser.snapshot != snapshot {
		t.Fatal("detail opened during a sync that will replace its hidden Bench view")
	}
}
