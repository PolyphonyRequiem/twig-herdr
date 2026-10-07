package panel

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

func browserFixture(t *testing.T) (*runtime, *BrowserSnapshot) {
	t.Helper()
	cwd := t.TempDir()
	rt := newRuntime(Config{Cwd: cwd, InitialView: "tree"})
	rt.ctx = context.Background()
	rt.size = size{cols: 32, rows: 12}
	rt.binding = HostBinding{Snapshot: "origin", BindingID: "binding", IdentityID: "identity", WorktreeRoot: cwd}
	snapshot := &BrowserSnapshot{Version: 1, BenchID: "bench", BenchName: "My Bench", BindingID: "binding", IdentityID: "identity", WorktreeRoot: cwd, Roots: []*BrowserNode{
		{Key: "root/1", ID: 1, Title: "Long parent", Label: "\x1b[38;2;12;34;56mABC界DEF🙂GHIJKLMNOPQRSTUVWXYZ\x1b[0m", Pins: []string{"single", "tree"}, Membership: "bench member", Children: []*BrowserNode{{Key: "root/1/2", ID: 2, Title: "Inherited child", Label: "Inherited child", OwningSubtreeIDs: []int{1}, Membership: "subtree"}}},
		{Key: "root/3", ID: 3, Title: "Other item", Label: "Other item", Membership: "ancestor context"},
		{Key: "seed/-1", ID: -1, Title: "Unpublished", Label: "Unpublished", IsSeed: true, Membership: "seed"},
	}}
	rt.browser.replace(snapshot)
	return rt, snapshot
}

func TestSemanticReadRejectsIncompatibleEnvelopeAndChangedOrigin(t *testing.T) {
	rt, snapshot := browserFixture(t)
	if _, err := parseBrowser([]byte(`{"items":[]}`), rt.binding); err == nil || !strings.Contains(err.Error(), "install twig-bench-native") {
		t.Fatal("legacy workspace JSON must require an actionable capability upgrade")
	}
	encoded, err := json.Marshal(struct {
		Browser *BrowserSnapshot `json:"browser"`
	}{snapshot})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseBrowser(encoded, rt.binding); err != nil {
		t.Fatal(err)
	}
	other := rt.binding
	other.IdentityID = "different-actor"
	if _, err := parseBrowser(encoded, other); err == nil || !bindingChanged(err.Error()) {
		t.Fatal("a companion observation from another actor was admitted")
	}
}

func TestSemanticReadCannotReplaceSnapshotOnFailureOrStaleGeneration(t *testing.T) {
	rt, original := browserFixture(t)
	rt.bench.launchGen = 2
	captureStdout(t, func() {
		rt.handleBrowserLoaded(event{token: 1, browser: &BrowserSnapshot{BenchID: "stale"}})
		rt.handleBrowserLoaded(event{token: 2, err: errors.New("cache unavailable")})
	})
	if rt.browser.snapshot != original || !rt.snapshot().Ready {
		t.Fatal("a stale or failed refresh replaced admitted cached data")
	}
	captureStdout(t, func() {
		rt.stopForReconnect(errors.New("binding-changed: actor changed"))
		rt.handleBrowserLoaded(event{token: 2, browser: original})
	})
	if rt.browser.snapshot != nil || rt.snapshot().Ready {
		t.Fatal("a late browser callback revived the prior actor")
	}
}

func TestLogicalSelectionFoldAndContinuationMouseHit(t *testing.T) {
	rt, _ := browserFixture(t)
	rt.size = size{cols: 18, rows: 16}
	captureStdout(t, func() { rt.draw(); rt.handleMouse(uv.Mouse{X: 3, Y: 3, Button: uv.MouseLeft}) })
	if rt.browser.selectedID != 1 || rt.browser.collapsed["root/1"] {
		t.Fatal("a continuation click toggled the disclosure or selected another logical item")
	}
	captureStdout(t, func() { rt.handleKey(uv.KeyPressEvent{Code: 'j', Text: "j"}) })
	if rt.browser.selectedID != 2 {
		t.Fatal("keyboard selection moved a physical wrapped line instead of one logical item")
	}
	captureStdout(t, func() { rt.handleKey(uv.KeyPressEvent{Code: uv.KeyLeft}) })
	if rt.browser.selectedID != 1 {
		t.Fatal("left on a leaf did not select its parent")
	}
	captureStdout(t, func() { rt.handleMouse(uv.Mouse{X: 2, Y: 2, Button: uv.MouseLeft}) })
	if !rt.browser.collapsed["root/1"] {
		t.Fatal("the disclosure click did not collapse the same item as keyboard left")
	}
	captureStdout(t, func() { rt.handleKey(uv.KeyPressEvent{Code: 'j', Text: "j"}) })
	if rt.browser.selectedID != 3 {
		t.Fatal("navigation selected a child hidden by a fold")
	}
}

func TestNativeUnicodeLabelSurvivesPhysicalWrapping(t *testing.T) {
	label := "\x1b[38;2;12;34;56mABC界DEF🙂GHI\x1b[0m"
	parts := wrappedLabels(label, 4)
	var text strings.Builder
	for _, part := range parts {
		if ansi.StringWidth(part) > 4 {
			t.Fatal("wrapped native label overflowed a narrow viewport")
		}
		text.WriteString(ansi.Strip(part))
	}
	if text.String() != "ABC界DEF🙂GHI" {
		t.Fatalf("wrapping dropped or duplicated wide graphemes: %q", text.String())
	}
	if !strings.Contains(parts[1], "\x1b[38;2;12;34;56m") {
		t.Fatal("continuation lost native label styling after physical row reset")
	}
}

func TestWheelViewportSurvivesPeriodicRefreshWithoutMovingSelection(t *testing.T) {
	rt, snapshot := browserFixture(t)
	rt.size = size{cols: 18, rows: 8}
	rt.bench.launchGen = 1
	captureStdout(t, func() { rt.draw(); rt.handleMouse(uv.Mouse{Button: uv.MouseWheelDown}) })
	offset := rt.offsets["tree"]
	if offset == 0 || rt.browser.selectedID != 1 {
		t.Fatal("wheel did not independently scroll the viewport")
	}
	captureStdout(t, func() { rt.handleBrowserLoaded(event{token: 1, browser: snapshot}) })
	if rt.offsets["tree"] != offset || rt.browser.selectedID != 1 {
		t.Fatal("periodic refresh snapped the wheel viewport back to selection")
	}
}

func TestRefreshPreservesSelectionAndFoldByWorkItemIdentity(t *testing.T) {
	rt, original := browserFixture(t)
	rt.browser.ensureLayout("tree", 32)
	rt.browser.collapsed["root/1"] = true
	rt.browser.selectIndex(2)
	moved := *original
	parent := *original.Roots[0]
	parent.Key = "moved/1"
	other := *original.Roots[1]
	other.Key = "moved/3"
	moved.Roots = []*BrowserNode{&parent, &other, original.Roots[2]}
	rt.browser.replace(&moved)
	rt.browser.ensureLayout("tree", 32)
	if rt.browser.selectedID != 3 || !rt.browser.collapsed["moved/1"] {
		t.Fatal("refresh lost local selection or folding when occurrence keys moved")
	}
	if rt.browser.selectedIndex() < 0 {
		t.Fatal("preserved selection is not a visible logical row")
	}
}

func TestPinPickerExplainsInheritedAndSeedTargetsAndCapturesExplicitPins(t *testing.T) {
	rt, _ := browserFixture(t)
	rt.browser.ensureLayout("tree", 32)
	rt.browser.selectIndex(1)
	captureStdout(t, func() { rt.openPicker(true) })
	if rt.picker == nil || !strings.Contains(rt.picker.explanation, "#1") {
		t.Fatal("inherited unpin did not explain its authoritative subtree source")
	}
	captureStdout(t, func() { rt.handleKey(uv.KeyPressEvent{Code: uv.KeyEnter}) })
	if rt.picker != nil || rt.pinCancel != nil {
		t.Fatal("inherited-only Enter attempted a mutation")
	}
	rt.browser.selectIndex(3)
	captureStdout(t, func() { rt.openPicker(false) })
	if rt.picker == nil || !strings.Contains(rt.picker.explanation, "Publish") {
		t.Fatal("seed picker did not explain why pinning is disabled")
	}
	captureStdout(t, func() { rt.handleKey(uv.KeyPressEvent{Code: uv.KeyEscape}) })
	rt.browser.selectIndex(0)
	output := captureStdout(t, func() { rt.handleKey(uv.KeyPressEvent{Code: 'P', Text: "P"}) })
	if rt.picker == nil || !rt.picker.remove || !strings.Contains(ansi.Strip(output), "single + tree") {
		t.Fatal("Shift+P did not enumerate both explicit pin kinds for confirmation")
	}
	captureStdout(t, func() { rt.handleKey(uv.KeyPressEvent{Code: uv.KeyEscape}) })
	if rt.pinCancel != nil {
		t.Fatal("canceling explicit unpin launched a mutation")
	}
}

func TestPickerCannotRetargetAfterBenchChangeAndLatePinReplyCannotReviveIt(t *testing.T) {
	rt, snapshot := browserFixture(t)
	rt.bench.launchGen = 1
	captureStdout(t, func() { rt.openPicker(false) })
	changed := *snapshot
	changed.BenchID = "other-bench"
	captureStdout(t, func() { rt.handleBrowserLoaded(event{token: 1, browser: &changed}) })
	if rt.picker != nil {
		t.Fatal("an open pin picker silently retargeted to a changed Bench")
	}
	rt.pinCancel = func() {}
	rt.pinGen = 2
	captureStdout(t, func() {
		rt.stopForReconnect(errors.New("binding-changed: actor changed"))
		rt.handlePinDone(event{token: 2})
	})
	if rt.pinCancel != nil || rt.picker != nil || rt.browser.snapshot != nil {
		t.Fatal("late mutation completion revived old actor state")
	}
}

func TestLocalTreeChoiceSupersedesQueuedReviewKeyAndRequest(t *testing.T) {
	rt, _ := browserFixture(t)
	rt.admissionCancel = func() {}
	reply := make(chan Result, 1)
	captureStdout(t, func() {
		rt.handleKey(uv.KeyPressEvent{Code: '3', Text: "3"})
		rt.handleRequest(Request{Command: "review", Reply: reply})
		rt.admissionActionCount = len(rt.admissionActions)
		rt.handleKey(uv.KeyPressEvent{Code: '2', Text: "2"})
	})
	select {
	case result := <-reply:
		if result.Err == nil {
			t.Fatal("superseded Review request was not canceled")
		}
	default:
		t.Fatal("superseded Review request was left waiting")
	}
	captureStdout(t, func() { rt.handleAdmissionResolved(event{token: rt.admissionGen}) })
	if rt.review.lookupCancel != nil || rt.mode != "bench" || rt.benchView != "tree" {
		t.Fatal("an older queued Review choice started after the newer Tree choice")
	}
}

func TestTableRefreshPreservesHeaderViewportAndItemSelection(t *testing.T) {
	rt, snapshot := browserFixture(t)
	rt.size = size{cols: 80, rows: 12}
	rt.benchView = "table"
	rt.browser.ensureLayout("table", 80)
	rt.browser.selectIndex(1)
	selected := rt.browser.selectedID
	rt.offsets["table"] = 0
	output := captureStdout(t, func() { rt.handleBrowserLoaded(event{token: rt.bench.launchGen, browser: snapshot}) })
	if !rt.snapshot().Ready || rt.browser.selectedID != selected || rt.offsets["table"] != 0 || !strings.Contains(ansi.Strip(output), "ABC界DEF") {
		t.Fatal("table refresh lost its selected item, top viewport or native row content")
	}
}
