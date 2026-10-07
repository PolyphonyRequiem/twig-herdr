package panel

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	uv "github.com/charmbracelet/ultraviolet"
)

func TestConfigurationEscapeRestoresViewerStateAndDoesNotBecomeReview(t *testing.T) {
	rt, _ := browserFixture(t)
	rt.size = size{cols: 18, rows: 8}
	rt.browser.ensureLayout("tree", rt.contentCols())
	rt.browser.collapsed["root/1"] = true
	rt.browser.dirty = true
	rt.browser.ensureLayout("tree", rt.contentCols())
	rt.browser.selectIndex(1)
	rt.offsets["tree"] = min(2, max(len(rt.browser.lines)-rt.contentVisibleRows(), 0))
	selected, offset := rt.browser.selectedID, rt.offsets["tree"]
	captureStdout(t, func() {
		rt.handleKey(uv.KeyPressEvent{Code: 'b', Text: "b"})
		if rt.configuration == nil || rt.mode != "bench" || !rt.snapshot().Configuring {
			t.Fatal("Bench entry changed transport mode or did not open configuration")
		}
		rt.handleKey(uv.KeyPressEvent{Code: '3', Text: "3"})
		if rt.configuration.section != 2 || rt.review.lookupCancel != nil {
			t.Fatal("configuration section choice launched proposal review")
		}
		rt.handleKey(uv.KeyPressEvent{Code: uv.KeyEscape})
	})
	if rt.configuration != nil || rt.snapshot().Configuring || rt.browser.selectedID != selected || rt.offsets["tree"] != offset || !rt.browser.collapsed["root/1"] {
		t.Fatal("returning from configuration lost the viewer selection, fold or viewport")
	}
}

func TestEmptyBenchManualPinRequiresNumericIDAndExplicitConfirmation(t *testing.T) {
	rt, snapshot := browserFixture(t)
	snapshot.Roots = []*BrowserNode{}
	rt.browser.replace(snapshot)
	captureStdout(t, func() {
		rt.handleKey(uv.KeyPressEvent{Code: 'p', Text: "p"})
		if rt.form == nil || rt.form.kind != "pin" {
			t.Fatal("an empty Bench cannot enter a manual pin")
		}
		for _, letter := range "qcsp" {
			rt.handleKey(uv.KeyPressEvent{Code: letter, Text: string(letter)})
		}
		if rt.form.editor.text != "qcsp" || rt.closing || rt.syncCancel != nil || rt.pinCancel != nil {
			t.Fatal("text field executed a viewer shortcut")
		}
		rt.handleKey(uv.KeyPressEvent{Code: uv.KeyEnter})
		if rt.form.confirm || rt.form.error == "" || rt.pinCancel != nil {
			t.Fatal("nonnumeric work item ID reached confirmation or a mutation")
		}
		for range 4 {
			rt.handleKey(uv.KeyPressEvent{Code: uv.KeyBackspace})
		}
		rt.handleKey(uv.KeyPressEvent{Code: '9', Text: "9001"})
		rt.handleKey(uv.KeyPressEvent{Code: uv.KeyEnter})
		if !rt.form.confirm || rt.form.editor.text != "9001" || rt.pinCancel != nil {
			t.Fatal("valid ID was not reviewed before mutation")
		}
		rt.handleKey(uv.KeyPressEvent{Code: uv.KeyEscape})
	})
	if rt.form != nil || rt.pinCancel != nil || rt.browser.snapshot != snapshot {
		t.Fatal("canceling a manual pin changed native membership")
	}
}

func TestEditorDeletionAndCursorRespectUnicodeGraphemes(t *testing.T) {
	e := newTextEditor("A界e\u0301👩\u200d💻")
	e.handle(uv.KeyPressEvent{Code: uv.KeyBackspace})
	if e.text != "A界e\u0301" {
		t.Fatal("backspace split an emoji grapheme")
	}
	e.handle(uv.KeyPressEvent{Code: uv.KeyBackspace})
	if e.text != "A界" {
		t.Fatal("backspace left a detached combining mark")
	}
	e.handle(uv.KeyPressEvent{Code: uv.KeyLeft})
	e.handle(uv.KeyPressEvent{Code: '🙂', Text: "🙂"})
	e.handle(uv.KeyPressEvent{Code: uv.KeyDelete})
	if e.text != "A🙂" || !utf8.ValidString(e.text) {
		t.Fatal("cursor insertion/delete corrupted a Unicode path")
	}
}

func TestEditorDeletionNormalizesJoinedGraphemes(t *testing.T) {
	for _, deletion := range []struct {
		name string
		move []rune
		key  rune
	}{
		{name: "backspace", move: []rune{uv.KeyLeft}, key: uv.KeyBackspace},
		{name: "delete", move: []rune{uv.KeyHome, uv.KeyRight}, key: uv.KeyDelete},
	} {
		t.Run(deletion.name, func(t *testing.T) {
			e := newTextEditor("🇦X🇧")
			for _, move := range deletion.move {
				e.handle(uv.KeyPressEvent{Code: move})
			}
			e.handle(uv.KeyPressEvent{Code: deletion.key})
			if e.text != "🇦🇧" || e.cursor != len(e.text) {
				t.Fatalf("joined flag retained a cursor inside its grapheme: %q at %d", e.text, e.cursor)
			}
			e.handle(uv.KeyPressEvent{Code: uv.KeyDelete})
			if e.text != "🇦🇧" {
				t.Fatal("delete split a joined flag at the end of the field")
			}
			e.handle(uv.KeyPressEvent{Code: uv.KeyBackspace})
			if e.text != "" || e.cursor != 0 {
				t.Fatal("subsequent backspace did not remove the entire joined flag")
			}
		})
	}
}

func TestEditorInsertionNormalizesJoinedGraphemes(t *testing.T) {
	e := newTextEditor("👩💻")
	e.handle(uv.KeyPressEvent{Code: uv.KeyLeft})
	e.handle(uv.KeyPressEvent{Text: "\u200d"})
	if e.text != "👩\u200d💻" || e.cursor != len(e.text) {
		t.Fatal("insertion left the cursor inside a joined emoji")
	}
	e.handle(uv.KeyPressEvent{Code: uv.KeyBackspace})
	if e.text != "" || e.cursor != 0 {
		t.Fatal("backspace split the emoji joined by insertion")
	}
}

func TestPositiveIDRefusesSignsWhitespaceZeroAndNativeOverflow(t *testing.T) {
	for _, value := range []string{"", "0", "-1", "+1", " 1", "1 ", "１２", "2147483648", "99999999999999999999999"} {
		if _, err := positiveID(value); err == nil {
			t.Fatalf("unsafe ID accepted: %q", value)
		}
	}
	id, err := positiveID("2147483647")
	if err != nil || id != 2147483647 {
		t.Fatal("valid native ID boundary was rejected")
	}
}

func TestCapturedConfigurationCannotRetargetOnOriginBenchOrSettingsChange(t *testing.T) {
	for _, change := range []string{"settings", "bench", "identity", "binding"} {
		t.Run(change, func(t *testing.T) {
			rt, snapshot := browserFixture(t)
			captureStdout(t, func() {
				rt.openInput("area", "Project\\Area")
				rt.handleKey(uv.KeyPressEvent{Code: uv.KeyEnter})
				switch change {
				case "settings":
					snapshot.Configuration.SettingsDigest = "new-settings"
				case "bench":
					snapshot.BenchID = "other-bench"
				case "identity":
					rt.binding.IdentityID = "other-identity"
				case "binding":
					rt.binding.BindingID = "other-binding"
				}
				rt.handleKey(uv.KeyPressEvent{Code: uv.KeyEnter})
			})
			if rt.form == nil || rt.form.error == "" || rt.notice == "" || rt.pinCancel != nil {
				t.Fatal("captured form retargeted or hid its stale-capture refusal")
			}
		})
	}
}

func TestEscapeCancelsFieldBeforeLeavingConfigurationAndQueuedKeysCannotApply(t *testing.T) {
	rt, _ := browserFixture(t)
	captureStdout(t, func() {
		rt.openConfiguration()
		rt.chooseSection(2)
		rt.openInput("sprint", "@Current+1")
		rt.handleAdmittedKey(uv.KeyPressEvent{Code: 's', Text: "s"})
		rt.handleAdmittedKey(uv.KeyPressEvent{Code: uv.KeyEnter})
		if rt.syncCancel != nil || rt.pinCancel != nil || rt.form.confirm {
			t.Fatal("queued viewer keys became editor actions")
		}
		rt.handleKey(uv.KeyPressEvent{Code: uv.KeyEscape})
		if rt.form != nil || rt.configuration == nil {
			t.Fatal("first Escape exited configuration instead of canceling input")
		}
		rt.handleKey(uv.KeyPressEvent{Code: uv.KeyEscape})
	})
	if rt.configuration != nil {
		t.Fatal("second Escape did not restore viewer")
	}
}

func TestUncachedPinCanBeRemovedWithoutDisplayedWorkItem(t *testing.T) {
	rt, snapshot := browserFixture(t)
	snapshot.Roots = []*BrowserNode{}
	snapshot.Configuration.Pins = []BenchPin{{ID: 991, Mode: "single"}, {ID: 991, Mode: "tree"}}
	rt.browser.replace(snapshot)
	captureStdout(t, func() { rt.openConfiguration(); rt.removeConfigurationSelection() })
	if rt.picker == nil || rt.picker.node.ID != 991 || len(rt.picker.node.Pins) != 2 || rt.picker.explanation != "" || rt.picker.settingsDigest != snapshot.Configuration.SettingsDigest {
		t.Fatal("uncached pin could not capture both explicit modes for guarded removal")
	}
	captureStdout(t, func() { rt.handleKey(uv.KeyPressEvent{Code: uv.KeyEscape}) })
	if rt.picker != nil || rt.configuration == nil || rt.pinCancel != nil {
		t.Fatal("canceling uncached removal did not return to configuration")
	}
}

func TestWrappedSectionAndSelectorClicksKeepTheirLogicalTargets(t *testing.T) {
	rt, snapshot := browserFixture(t)
	rt.size = size{cols: 8, rows: 20}
	snapshot.Configuration.Areas = []BenchArea{{Path: "Project\\Long Unicode 界 Area", IncludeChildren: true}, {Path: "Project\\Other"}}
	captureStdout(t, func() {
		rt.openConfiguration()
		var sectionHit *hitTarget
		for _, hit := range rt.hits {
			if hit.action == "config-section" && hit.row == 1 {
				copy := hit
				sectionHit = &copy
			}
		}
		if sectionHit == nil {
			t.Fatal("wrapped Areas tab was not clickable")
		}
		rt.handleMouse(uv.Mouse{X: sectionHit.x1, Y: sectionHit.y, Button: uv.MouseLeft})
		if rt.configuration.section != 1 {
			t.Fatal("wrapped tab click selected another section")
		}
		rt.configuration.selected[1] = 1
		rt.revealConfigurationSelection()
		rt.draw()
		var entryHit *hitTarget
		for _, hit := range rt.hits {
			if hit.action == "config-entry" && hit.row == 1 {
				copy := hit
				entryHit = &copy
			}
		}
		if entryHit == nil {
			t.Fatal("wrapped selected area could not be reached in a narrow viewport")
		}
		rt.configuration.selected[1] = 0
		rt.handleMouse(uv.Mouse{X: entryHit.x1, Y: entryHit.y, Button: uv.MouseLeft})
		if rt.configuration.selected[1] != 1 {
			t.Fatal("continuation click selected a different area")
		}
		rt.removeConfigurationSelection()
	})
	if rt.form == nil || !rt.form.remove || rt.form.editor.text != "Project\\Other" {
		t.Fatal("wrapped selection removal retargeted the area path")
	}
}

func TestConfigurationFieldClicksUseWrappedGraphemeGeometry(t *testing.T) {
	for _, click := range []struct {
		name, kind, value  string
		cursor, cols, rows int
		row, column, want  int
	}{
		{name: "wide second cell", kind: "area", value: "AB界e\u0301👩\u200d💻Z", cursor: -1, cols: 8, rows: 40, row: 0, column: 5, want: len("AB")},
		{name: "wrapped emoji second cell", kind: "sprint", value: "AB界e\u0301👩\u200d💻Z", cursor: -1, cols: 8, rows: 60, row: 1, column: 1, want: len("AB界e\u0301")},
		{name: "wrap padding before wide cluster", kind: "area", value: "AB界e\u0301👩\u200d💻Z", cursor: -1, cols: 8, rows: 40, row: 0, column: 7, want: len("AB界e\u0301")},
		{name: "cursor marker shifts columns", kind: "area", value: "AB界e\u0301👩\u200d💻Z", cursor: len("AB"), cols: 8, rows: 40, row: 0, column: 6, want: len("AB")},
		{name: "combining cluster", kind: "area", value: "AB界e\u0301👩\u200d💻Z", cursor: -1, cols: 8, rows: 40, row: 0, column: 6, want: len("AB界")},
		{name: "continuation trailing space", kind: "area", value: "AB界e\u0301👩\u200d💻Z", cursor: 0, cols: 8, rows: 40, row: 1, column: 7, want: len("AB界e\u0301👩\u200d💻Z")},
		{name: "field prefix", kind: "pin", value: "123456789", cursor: -1, cols: 8, rows: 40, row: 0, column: 1, want: 0},
		{name: "wrapped ID", kind: "pin", value: "123456789", cursor: -1, cols: 8, rows: 40, row: 1, column: 1, want: len("1234567")},
		{name: "empty field", kind: "area", value: "", cursor: -1, cols: 8, rows: 40, row: 0, column: 7, want: 0},
		{name: "scrolled wrapped emoji", kind: "area", value: strings.Repeat("界", 20) + "e\u0301👩\u200d💻Z", cursor: -1, cols: 8, rows: 8, row: -1, column: 4, want: len(strings.Repeat("界", 20) + "e\u0301")},
	} {
		t.Run(click.name, func(t *testing.T) {
			rt, snapshot := browserFixture(t)
			rt.size = size{cols: click.cols, rows: click.rows}
			snapshot.BenchName = "Wrapped 界 Bench"
			captureStdout(t, func() {
				rt.openInput(click.kind, click.value)
				if click.cursor >= 0 {
					rt.form.editor.cursor = click.cursor
					rt.draw()
				}
				captured := rt.form
				benchID, binding, digest := captured.benchID, captured.binding, captured.settingsDigest
				first, last := rt.size.rows, -1
				for _, hit := range rt.hits {
					if hit.action == "config-field" {
						first, last = min(first, hit.y), max(last, hit.y)
					}
				}
				if last < 0 {
					t.Fatal("visible editor has no mouse field targets")
				}
				y := first + click.row
				if click.row < 0 {
					if rt.overlayOffset == 0 {
						t.Fatal("long wrapped field did not exercise a scrolled viewport")
					}
					y = last
				}
				rt.handleMouse(uv.Mouse{X: click.column, Y: y, Button: uv.MouseLeft})
				if rt.form != captured || captured.editor.text != click.value || captured.editor.cursor != click.want || captured.confirm || rt.pinCancel != nil || rt.browser.snapshot != snapshot {
					t.Fatalf("field click did not move only the grapheme cursor: %+v", rt.form)
				}
				for _, letter := range "qcbs" {
					rt.handleKey(uv.KeyPressEvent{Code: letter, Text: string(letter)})
				}
				want := click.value[:click.want] + "qcbs" + click.value[click.want:]
				if captured.editor.text != want || rt.closing || rt.configuration != nil || rt.syncCancel != nil || rt.pinCancel != nil {
					t.Fatal("click-to-edit ran viewer shortcuts or inserted at the wrong grapheme")
				}
				if captured.benchID != benchID || captured.binding != binding || captured.settingsDigest != digest {
					t.Fatal("field click or editing changed the captured mutation authority")
				}
				rt.handleKey(uv.KeyPressEvent{Code: uv.KeyEscape})
				if rt.form != nil || rt.browser.snapshot != snapshot || rt.pinCancel != nil {
					t.Fatal("Escape after click-to-edit changed Bench membership")
				}
			})
		})
	}
}

func TestConfigurationLabelClickAndReviewEditPreserveKeyboardTransitions(t *testing.T) {
	rt, snapshot := browserFixture(t)
	rt.size = size{cols: 8, rows: 40}
	captureStdout(t, func() {
		rt.openConfiguration()
		rt.openInput("area", "AB界e\u0301👩\u200d💻Z")
		captured := rt.form
		rt.handleMouse(uv.Mouse{X: 1, Y: 2, Button: uv.MouseLeft})
		if rt.form != captured || captured.editor.cursor != len(captured.editor.text) || captured.confirm {
			t.Fatal("clicking the wrapped Bench label edited or submitted the field")
		}
		rt.handleKey(uv.KeyPressEvent{Code: uv.KeyEnter})
		if !captured.confirm || rt.pinCancel != nil {
			t.Fatal("Enter did not review without applying the value")
		}
		for _, hit := range rt.hits {
			if hit.action == "config-edit" {
				rt.handleMouse(uv.Mouse{X: hit.x1, Y: hit.y, Button: uv.MouseLeft})
				break
			}
		}
		if captured.confirm {
			t.Fatal("wrapped Edit value click did not return to the field")
		}
		rt.handleKey(uv.KeyPressEvent{Code: uv.KeyHome})
		rt.handleKey(uv.KeyPressEvent{Code: 'b', Text: "b"})
		rt.handleKey(uv.KeyPressEvent{Code: uv.KeyEnd})
		rt.handleKey(uv.KeyPressEvent{Code: uv.KeyBackspace})
		if captured.editor.text != "bAB界e\u0301👩\u200d💻" || rt.configuration == nil {
			t.Fatal("mouse editing broke Home/End/backspace or treated b as Back")
		}
		rt.handleKey(uv.KeyPressEvent{Code: 'c', Mod: uv.ModCtrl})
	})
	if !rt.closing || rt.form != nil || rt.browser.snapshot != snapshot || rt.pinCancel != nil {
		t.Fatal("Ctrl+C after mouse editing did not shut down without mutating the Bench")
	}
}

func TestReconnectClearsEditorsAndIgnoresLateMutationReplies(t *testing.T) {
	rt, _ := browserFixture(t)
	captureStdout(t, func() { rt.openConfiguration(); rt.openInput("sprint", "@Current") })
	rt.pinGen, rt.pinCancel = 9, func() {}
	captureStdout(t, func() {
		rt.stopForReconnect(errors.New("binding-changed: identity changed"))
		rt.handlePinDone(event{token: 9})
	})
	if rt.form != nil || rt.configuration != nil || rt.pinCancel != nil || rt.browser.snapshot != nil {
		t.Fatal("late mutation reply revived the old configuration or actor")
	}
}

func TestCtrlCQuitsEvenWhenQIsFieldText(t *testing.T) {
	rt, _ := browserFixture(t)
	captureStdout(t, func() {
		rt.openInput("sprint", "")
		rt.handleKey(uv.KeyPressEvent{Code: 'q', Text: "q"})
		if rt.closing || rt.form.editor.text != "q" {
			t.Fatal("q quit rather than entering field text")
		}
		rt.handleKey(uv.KeyPressEvent{Code: 'c', Mod: uv.ModCtrl})
	})
	if !rt.closing || rt.form != nil {
		t.Fatal("Ctrl+C did not quit and clear input")
	}
}

func TestSemanticConfigurationRefusesMissingOrMalformedSelectors(t *testing.T) {
	for _, malformed := range []string{"missing", "pin", "area", "sprint", "unbounded"} {
		t.Run(malformed, func(t *testing.T) {
			rt, snapshot := browserFixture(t)
			switch malformed {
			case "missing":
				snapshot.Configuration = nil
			case "pin":
				snapshot.Configuration.Pins = []BenchPin{{ID: 3, Mode: "unknown"}}
			case "area":
				snapshot.Configuration.Areas = []BenchArea{{Path: " "}}
			case "sprint":
				snapshot.Configuration.Sprints = []BenchSprint{{Expression: ""}}
			case "unbounded":
				snapshot.Configuration.AutomaticEnabled = true
			}
			encoded, err := json.Marshal(struct {
				Browser *BrowserSnapshot `json:"browser"`
			}{snapshot})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := parseBrowser(encoded, rt.binding); err == nil {
				t.Fatal("malformed native configuration was admitted")
			}
		})
	}
}

func TestConfigurationOwnershipScopeIsRetainedWhenAutomaticMembershipDisabled(t *testing.T) {
	rt, snapshot := browserFixture(t)
	snapshot.Configuration.AssigneeSummary = "All assignees (authored)"
	captureStdout(t, func() { rt.openConfiguration() })
	var text strings.Builder
	for _, line := range rt.configurationLines(80) {
		text.WriteString(line.text)
	}
	if !strings.Contains(text.String(), snapshot.Configuration.AssigneeSummary) {
		t.Fatal("disabled automatic membership concealed its authored ownership scope")
	}
}
