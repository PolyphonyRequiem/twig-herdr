package panel

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
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
	snapshot.Configuration = &BenchConfiguration{Version: 1, SettingsDigest: "settings", AssigneeSummary: "canonical self", Areas: []BenchArea{}, Sprints: []BenchSprint{}, Pins: []BenchPin{{ID: 1, Mode: "single", Cached: true, Title: "Long parent"}, {ID: 1, Mode: "tree", Cached: true, Title: "Long parent"}}}
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

// Canceled contexts keep interaction tests isolated from both the installed Twig
// and the user's Bench; they inspect the native command intent before admission.
func isolatePinCommands(t *testing.T, rt *runtime) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rt.ctx = ctx
	t.Cleanup(rt.cancelPins)
}

func TestIndependentPinGesturesChooseGuardedIntentForEveryExplicitState(t *testing.T) {
	for _, standalone := range []bool{false, true} {
		for _, modes := range [][]string{nil, {"single"}, {"tree"}, {"single", "tree"}} {
			for _, mode := range []string{"single", "tree"} {
				for _, mouse := range []bool{false, true} {
					name := strings.Join(modes, "+") + "/" + mode
					if standalone {
						name += "/standalone"
					}
					if mouse {
						name += "/mouse"
					}
					t.Run(name, func(t *testing.T) {
						rt, snapshot := browserFixture(t)
						rt.cfg.Standalone = standalone
						isolatePinCommands(t, rt)
						rt.size = size{cols: 100, rows: 16}
						snapshot.Configuration.Pins = nil
						remove := false
						for _, explicit := range modes {
							snapshot.Configuration.Pins = append(snapshot.Configuration.Pins, BenchPin{ID: 1, Mode: explicit})
							remove = remove || explicit == mode
						}
						if snapshot.Configuration.Pins == nil {
							snapshot.Configuration.Pins = []BenchPin{}
						}
						captureStdout(t, func() {
							rt.draw()
							if mouse {
								clicked := false
								for _, hit := range rt.hits {
									if hit.action == "pin-"+mode {
										rt.handleMouse(uv.Mouse{X: hit.x1, Y: hit.y, Button: uv.MouseLeft})
										clicked = true
										break
									}
								}
								if !clicked {
									t.Fatal("named pin action is not clickable")
								}
							} else if mode == "tree" {
								rt.handleKey(uv.KeyPressEvent{Code: 'P', Text: "P"})
							} else {
								rt.handleKey(uv.KeyPressEvent{Code: 'p', Text: "p"})
							}
						})
						if rt.pinAction == nil {
							t.Fatal("pin gesture did not capture an immediate native intent")
						}
						command := "track"
						if mode == "tree" {
							command = "track-tree"
						}
						want := []string{"workspace", command, "1"}
						if remove {
							want = []string{"workspace", "untrack", "1", "--mode", mode}
						}
						want = append(want, "--expect-bench", "bench", "--expect-settings", "settings", "-o", "json", "--expect-binding", "binding", "--expect-identity", "identity")
						if got := rt.pinAction.args(); !reflect.DeepEqual(got, want) {
							t.Fatalf("pin gesture changed the wrong selector or dropped refusal guards: %v", got)
						}
						gen, pending := rt.pinGen, rt.pinAction
						captureStdout(t, func() {
							rt.handleKey(uv.KeyPressEvent{Code: 'p', Text: "p"})
							rt.handleKey(uv.KeyPressEvent{Code: 'P', Text: "P"})
						})
						if rt.pinGen != gen || rt.pinAction != pending {
							t.Fatal("busy repeat launched another or opposite-kind mutation")
						}
						// A same-kind external change must not turn this intention into a flip.
						snapshot.Configuration.Pins = []BenchPin{{ID: 1, Mode: mode}}
						if got := pending.args(); !reflect.DeepEqual(got, want) {
							t.Fatal("an external change retargeted the captured add/remove intention")
						}
					})
				}
			}
		}
	}
}

func TestHelpDismissalCannotMutateHiddenPinTarget(t *testing.T) {
	for _, code := range []rune{'p', 'P'} {
		rt, _ := browserFixture(t)
		isolatePinCommands(t, rt)
		rt.showBrowserHelp = true
		captureStdout(t, func() { rt.handleKey(uv.KeyPressEvent{Code: code, Text: string(code)}) })
		if rt.showBrowserHelp || rt.pinAction != nil {
			t.Fatal("help dismissal mutated a hidden target or failed to return")
		}
	}
}

func TestNarrowViewerKeepsBothPinMouseTargets(t *testing.T) {
	rt, snapshot := browserFixture(t)
	rt.size = size{cols: 30, rows: 16}
	snapshot.Configuration.Pins = []BenchPin{{ID: 1, Mode: "single"}, {ID: 1, Mode: "tree"}}
	captureStdout(t, func() { rt.draw() })
	for _, action := range []string{"pin-single", "pin-tree"} {
		found := false
		for _, hit := range rt.hits {
			found = found || hit.action == action
		}
		if !found {
			t.Fatalf("narrow viewer lost mouse control %s", action)
		}
	}
}

func TestInheritedOnlyGesturesAddOwnPinAndSeedsRefuse(t *testing.T) {
	for _, mode := range []string{"single", "tree"} {
		rt, _ := browserFixture(t)
		isolatePinCommands(t, rt)
		rt.browser.ensureLayout("tree", 32)
		rt.browser.selectIndex(1)
		captureStdout(t, func() { rt.toggleSelectedPin(mode) })
		if rt.pinAction == nil || rt.pinAction.id != 2 || rt.pinAction.remove || rt.pinAction.mode != mode {
			t.Fatal("inherited-only action targeted an ancestor or removed inherited membership")
		}
		rt.cancelPins()
		rt.browser.selectIndex(3)
		captureStdout(t, func() { rt.toggleSelectedPin(mode) })
		if rt.pinCancel != nil || !strings.Contains(rt.notice, "Publish") {
			t.Fatal("seed pin action did not refuse with a publish explanation")
		}
	}
}

func TestCapturedPinIntentRefusesChangedBenchOriginOrSettings(t *testing.T) {
	for _, change := range []string{"bench", "settings", "identity", "binding", "worktree", "snapshot"} {
		t.Run(change, func(t *testing.T) {
			rt, snapshot := browserFixture(t)
			action := pinAction{id: 1, mode: "single", remove: true, benchID: snapshot.BenchID, binding: rt.binding, settingsDigest: configurationDigest(snapshot)}
			switch change {
			case "bench":
				snapshot.BenchID = "other-bench"
			case "settings":
				snapshot.Configuration.SettingsDigest = "other-settings"
			case "identity":
				rt.binding.IdentityID = "other-identity"
			case "binding":
				rt.binding.BindingID = "other-binding"
			case "worktree":
				rt.binding.WorktreeRoot = t.TempDir()
			case "snapshot":
				rt.binding.Snapshot = "other-snapshot"
			}
			captureStdout(t, func() { rt.beginPinMutation(action) })
			if rt.pinCancel != nil || rt.pinAction != nil || rt.notice == "" {
				t.Fatal("stale captured pin intent was retargeted or silently ignored")
			}
		})
	}
}

func TestLatePinReplyCannotRevivePriorActor(t *testing.T) {
	rt, _ := browserFixture(t)
	rt.pinCancel = func() {}
	rt.pinAction = &pinAction{id: 1, mode: "single"}
	rt.pinGen = 2
	captureStdout(t, func() {
		rt.stopForReconnect(errors.New("binding-changed: actor changed"))
		rt.handlePinDone(event{token: 2})
	})
	if rt.pinCancel != nil || rt.pinAction != nil || rt.browser.snapshot != nil {
		t.Fatal("late mutation completion revived old actor state")
	}
}

func TestPinCompletionEnsuresNamedStateWithoutInventingAChange(t *testing.T) {
	for _, remove := range []bool{false, true} {
		rt, snapshot := browserFixture(t)
		isolatePinCommands(t, rt)
		pending := &pinAction{id: 1, mode: "tree", remove: remove}
		rt.pinAction, rt.pinCancel, rt.pinGen = pending, func() {}, 4
		captureStdout(t, func() { rt.handlePinDone(event{token: 3}) })
		if rt.pinCancel == nil || rt.pinAction != pending || rt.bench.launchCancel != nil {
			t.Fatal("stale completion released an in-flight pin or refreshed another intent")
		}
		captureStdout(t, func() { rt.handlePinDone(event{token: 4}) })
		state := "present"
		if remove {
			state = "absent"
		}
		// Native success can be a same-kind race/no-op; only the ensured state is known.
		if !strings.Contains(rt.notice, "Subtree pin for #1 ensured "+state) || strings.Contains(rt.notice, "Removed") || strings.Contains(rt.notice, "Added") {
			t.Fatal("success invented a changed result or misidentified the pin kind/state")
		}
		if rt.pinCancel != nil || rt.pinAction != nil || rt.bench.launchCancel == nil || rt.browser.snapshot != snapshot {
			t.Fatal("successful pin intent failed to refresh native truth or replaced it speculatively")
		}
		rt.cancelBenchLaunch(nil)
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
