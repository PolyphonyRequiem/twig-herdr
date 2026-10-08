package panel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	goruntime "runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	vt "github.com/charmbracelet/x/vt"
)

func detailFixture(t *testing.T) (*runtime, *DetailDocument) {
	t.Helper()
	rt, snapshot := browserFixture(t)
	rt.browser.ensureLayout(rt.benchView, rt.contentCols())
	var body strings.Builder
	for i := 1; i <= 120; i++ {
		body.WriteString("\x1b[1;36mparagraph-" + strconv.Itoa(i) + "\x1b[0m\n")
	}
	document := &DetailDocument{Version: 1, BenchID: snapshot.BenchID, BenchName: snapshot.BenchName,
		BindingID: rt.binding.BindingID, IdentityID: rt.binding.IdentityID, WorkItemID: 1,
		Title: "Long parent", ANSI: body.String()}
	rt.detail = detailState{active: true, id: 1, key: "root/1", view: rt.benchView, benchID: snapshot.BenchID,
		binding: rt.binding, gen: 4, document: document}
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

func TestDetailScrollAndCollapseDoNotChangeBenchSelectionFoldsOrViewport(t *testing.T) {
	for _, closeKey := range []rune{uv.KeyEscape, uv.KeyEnter} {
		rt, _ := detailFixture(t)
		rt.size.rows = panelChromeRows + 3
		rt.browser.selectIndex(2)
		rt.browser.collapsed["root/1"] = true
		rt.browser.dirty = true
		rt.browser.ensureLayout(rt.benchView, rt.contentCols())
		rt.offsets["tree"], rt.offsets["table"] = 1, 7
		rt.detail.benchOffset, rt.detail.offset = 1, 1
		selected, selectedID := rt.browser.selected, rt.browser.selectedID
		folds := make(map[string]bool)
		for key, value := range rt.browser.collapsed {
			folds[key] = value
		}
		var before strings.Builder
		rt.drawBrowserRows(&before, rt.contentCols(), rt.contentVisibleRows())
		captureStdout(t, func() {
			rt.handleDetailKey(uv.KeyPressEvent{Code: 'j', Text: "j"})
			rt.handleDetailMouse(uv.Mouse{Button: uv.MouseWheelDown})
		})
		if rt.detail.offset != 5 {
			t.Fatal("keys and wheel did not scroll the composed surface from its Bench position")
		}
		captureStdout(t, func() { rt.handleDetailKey(uv.KeyPressEvent{Code: uv.KeyEnd}) })
		rows := detailScreen(t, rt)
		if !strings.Contains(strings.Join(rows[contentStartRow:len(rows)-2], "\n"), "Unpublished") {
			t.Fatal("End did not scroll beyond the detail to the following Bench rows")
		}
		captureStdout(t, func() { rt.handleDetailMouse(uv.Mouse{Button: uv.MouseWheelUp}) })
		rows = detailScreen(t, rt)
		if !strings.Contains(strings.Join(rows[contentStartRow:len(rows)-2], "\n"), "paragraph-120") {
			t.Fatal("scrolling back from neighboring rows did not expose the uncapped final detail line")
		}
		captureStdout(t, func() { rt.handleDetailKey(uv.KeyPressEvent{Code: closeKey}) })
		if rt.detail.active || rt.detail.document != nil || rt.browser.selected != selected || rt.browser.selectedID != selectedID || !reflect.DeepEqual(rt.browser.collapsed, folds) || rt.offsets["tree"] != 1 || rt.offsets["table"] != 7 {
			t.Fatal("collapse changed Bench selection, folds or saved viewport, or retained closed detail data")
		}
		var after strings.Builder
		rt.drawBrowserRows(&after, rt.contentCols(), rt.contentVisibleRows())
		if after.String() != before.String() {
			t.Fatal("collapse did not restore the same rendered Bench viewport")
		}
	}
}

func detailScreen(t *testing.T, rt *runtime) []string {
	t.Helper()
	pane := vt.NewEmulator(rt.contentCols(), rt.size.rows)
	_, _ = pane.WriteString(captureStdout(t, rt.draw))
	return strings.Split(pane.String(), "\n")
}

func TestDetailFrameAndWrappedBenchRowsFormOneContinuousSurface(t *testing.T) {
	rt, document := detailFixture(t)
	rt.size.rows = panelChromeRows + 10
	rt.browser.snapshot.Roots = []*BrowserNode{
		{Key: "ancestor", ID: 10, Label: "Ancestor", Children: []*BrowserNode{
			{Key: "captured", ID: 1, Label: "Captured row with a wrapped continuation TAIL"},
			{Key: "following", ID: 11, Label: "Following sibling"},
		}},
		{Key: "neighbor", ID: 12, Label: "Neighbor", Children: []*BrowserNode{{Key: "hidden", ID: 13, Label: "Hidden child"}}},
		{Key: "duplicate", ID: 1, Label: "Other occurrence"},
	}
	rt.browser.collapsed["neighbor"] = true
	rt.browser.dirty = true
	rt.browser.ensureLayout(rt.benchView, rt.contentCols())
	rt.browser.selectIndex(1)
	rt.detail.key = "captured"
	document.ANSI = "\x1b[36m┌──── Status ────\n│ Rich description\n└────────────────\x1b[0m\n"
	rt.detail.cols = 0
	rows := detailScreen(t, rt)
	content := rows[contentStartRow : len(rows)-2]
	if !strings.Contains(content[0], "Ancestor") || !strings.Contains(content[1], "Captured row") || !strings.Contains(content[2], "TAIL") {
		t.Fatalf("ancestor or wrapped captured row disappeared before detail: %q", content)
	}
	for i, text := range []string{"┌──── Status ────", "│ Rich description", "└────────────────"} {
		if content[3+i] != strings.Repeat(" ", 6)+text {
			t.Fatalf("native open-right frame lost its content-depth indentation or sequence: %q", content)
		}
	}
	if !strings.Contains(content[6], "Following sibling") || !strings.Contains(content[7], "Neighbor") || !strings.Contains(content[8], "Other occurrence") || strings.Contains(strings.Join(content, "\n"), "Hidden child") {
		t.Fatalf("following rows or existing fold changed around inline detail: %q", content)
	}
	rt.browser.selectIndex(4)
	rows = detailScreen(t, rt)
	if rows[contentStartRow+3] != strings.Repeat(" ", 6)+"┌──── Status ────" {
		t.Fatal("detail followed hidden selection or another occurrence of the captured item")
	}
	rt.size.rows = panelChromeRows + 3
	captureStdout(t, func() { rt.handleDetailKey(uv.KeyPressEvent{Code: uv.KeyEnd}) })
	rows = detailScreen(t, rt)
	if !strings.Contains(rows[contentStartRow], "Following sibling") || !strings.Contains(rows[contentStartRow+2], "Other occurrence") {
		t.Fatalf("one surface could not scroll past the frame to following Bench rows: %q", rows)
	}
	rt.size.rows = panelChromeRows + 10
	rows = detailScreen(t, rt)
	if !strings.Contains(rows[contentStartRow], "Ancestor") || !strings.Contains(rows[contentStartRow+8], "Other occurrence") {
		t.Fatal("a taller viewport left the composed surface at an invalid scroll offset")
	}
}

func TestDetailLoadingAndRefreshPreserveFollowingBenchViewport(t *testing.T) {
	rt, dir := detailNativeFixture(t, false)
	rt.size.rows = panelChromeRows + 3
	rt.browser.snapshot.Roots = []*BrowserNode{{Key: "captured", ID: 1, Label: "Captured"}}
	for i := 2; i <= 40; i++ {
		rt.browser.snapshot.Roots = append(rt.browser.snapshot.Roots, &BrowserNode{Key: fmt.Sprintf("following/%d", i), ID: i, Label: fmt.Sprintf("Following %d", i)})
	}
	rt.browser.dirty = true
	rt.browser.ensureLayout(rt.benchView, rt.contentCols())
	rt.browser.selectIndex(0)
	rt.offsets[rt.benchView] = 20
	before := detailScreen(t, rt)[contentStartRow]
	captureStdout(t, func() { rt.handleKey(uv.KeyPressEvent{Code: uv.KeyEnter}) })
	if top := detailScreen(t, rt)[contentStartRow]; top != before {
		t.Fatalf("loading placeholder moved following Bench anchor: before %q, after %q", before, top)
	}
	awaitDetailRead(t, rt)
	if top := detailScreen(t, rt)[contentStartRow]; top != before {
		t.Fatalf("loaded detail moved following Bench anchor: before %q, after %q", before, top)
	}
	changed := *rt.detail.document
	changed.ANSI = strings.Repeat("A longer refreshed document line\n", 200)
	data, err := json.Marshal(&changed)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cache.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	captureStdout(t, func() { rt.handleKey(uv.KeyPressEvent{Code: 'r', Text: "r"}) })
	awaitDetailRead(t, rt)
	if top := detailScreen(t, rt)[contentStartRow]; top != before {
		t.Fatalf("refreshed detail moved following Bench anchor: before %q, after %q", before, top)
	}
}

func TestDetailLateResponseCannotReplaceReflowOrReviveClosedActor(t *testing.T) {
	rt, original := detailFixture(t)
	stale := *original
	stale.Title = "stale-response"
	captureStdout(t, func() { rt.handleDetailLoaded(event{token: 3, detail: &stale}) })
	if rt.detail.document != original {
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
	rt.layoutDetail(8)
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

// Use the test executable as a native boundary, including connection admission.
// Cache reads cannot change the remote-pull journal; only --sync can replace the
// fixture's cached document. No real Twig executable or credentials are used.
func init() {
	dir := os.Getenv("TWIG_HERDR_DETAIL_NATIVE_FIXTURE")
	if dir == "" || len(os.Args) < 2 || (os.Args[1] != "connection" && os.Args[1] != "bench") {
		return
	}
	if err := runDetailNativeFixture(dir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func runDetailNativeFixture(dir string) error {
	if os.Args[1] == "connection" {
		output, err := os.ReadFile(filepath.Join(dir, "binding.json"))
		if err == nil {
			_, err = os.Stdout.Write(output)
		}
		return err
	}
	if len(os.Args) < 4 || os.Args[2] != "detail" {
		return errors.New("unexpected native command outside captured detail")
	}
	sync := false
	for _, arg := range os.Args[4:] {
		sync = sync || arg == "--sync"
	}
	if expected, err := os.ReadFile(filepath.Join(dir, "expected-width")); err == nil {
		width := ""
		for i := 4; i+1 < len(os.Args); i++ {
			if os.Args[i] == "--width" {
				width = os.Args[i+1]
				break
			}
		}
		if width != string(expected) {
			return fmt.Errorf("native detail width %s did not fit the inline content width %s", width, expected)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if sync {
		journal, err := os.OpenFile(filepath.Join(dir, "pulls"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			return err
		}
		_, err = journal.WriteString("pull\n")
		journal.Close()
		if err != nil {
			return err
		}
		if _, err := os.Stat(filepath.Join(dir, "hold")); err == nil {
			deadline := time.Now().Add(10 * time.Second)
			for {
				if _, err := os.Stat(filepath.Join(dir, "release")); err == nil {
					break
				}
				if time.Now().After(deadline) {
					return errors.New("fixture sync was never released")
				}
				time.Sleep(5 * time.Millisecond)
			}
		}
		if _, err := os.Stat(filepath.Join(dir, "fail")); err == nil {
			return errors.New("remote unavailable")
		}
		remote, err := os.ReadFile(filepath.Join(dir, "remote.json"))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "cache.json"), remote, 0600); err != nil {
			return err
		}
	}
	output, err := os.ReadFile(filepath.Join(dir, "cache.json"))
	if err != nil {
		return err
	}
	var document DetailDocument
	if err := json.Unmarshal(output, &document); err != nil {
		return err
	}
	if strconv.Itoa(document.WorkItemID) != os.Args[3] {
		return errors.New("requested item is not cached in this fixture")
	}
	_, err = os.Stdout.Write(output)
	return err
}

func detailNativeFixture(t *testing.T, seed bool) (*runtime, string) {
	t.Helper()
	rt, document := detailFixture(t)
	if seed {
		document.WorkItemID, document.Title = -1, "Unpublished"
	}
	rt.cancelDetail()
	rt.cfg.Standalone = true
	dir := t.TempDir()
	binding, err := json.Marshal(rt.binding)
	if err != nil {
		t.Fatal(err)
	}
	rt.binding, err = parseStandaloneBinding(binding, "")
	if err != nil {
		t.Fatal(err)
	}
	remote := *document
	remote.Title = "Remote item"
	remote.ANSI = "Pulled remote description\n" + document.ANSI
	for name, value := range map[string]any{"binding.json": json.RawMessage(binding), "cache.json": document, "remote.json": &remote} {
		output, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), output, 0600); err != nil {
			t.Fatal(err)
		}
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	name := "twig"
	if strings.EqualFold(filepath.Ext(executable), ".exe") {
		name += ".exe"
	}
	rt.nativePath = filepath.Join(dir, name)
	// Windows runner image locks can prevent removing an executable hardlink.
	// Use an independently owned fixture image there; retain cheap Unix links.
	if goruntime.GOOS == "windows" || os.Link(executable, rt.nativePath) != nil {
		source, err := os.Open(executable)
		if err != nil {
			t.Fatal(err)
		}
		defer source.Close()
		target, err := os.OpenFile(rt.nativePath, os.O_CREATE|os.O_WRONLY, 0700)
		if err != nil {
			t.Fatal(err)
		}
		_, copyErr := io.Copy(target, source)
		closeErr := target.Close()
		if copyErr != nil || closeErr != nil {
			t.Fatal(copyErr, closeErr)
		}
	}
	t.Setenv("TWIG_HERDR_DETAIL_NATIVE_FIXTURE", dir)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Cleanup(rt.cancelDetail)
	return rt, dir
}

func awaitDetailRead(t *testing.T, rt *runtime) event {
	t.Helper()
	select {
	case ev := <-rt.events:
		if ev.kind != evDetailLoaded {
			t.Fatalf("unexpected detail event: %v", ev.kind)
		}
		captureStdout(t, func() { rt.handleDetailLoaded(ev) })
		return ev
	case <-time.After(10 * time.Second):
		t.Fatal("captured detail read did not complete")
		return event{}
	}
}

func detailPullCount(t *testing.T, dir string) int {
	t.Helper()
	output, err := os.ReadFile(filepath.Join(dir, "pulls"))
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(output), "pull\n")
}

func TestDetailInputKeepsEntryRefreshAndResizeCachedUntilExplicitSync(t *testing.T) {
	rt, dir := detailNativeFixture(t, false)
	rt.size.rows = panelChromeRows + 3
	rt.offsets["tree"] = 1
	anchor := detailScreen(t, rt)[contentStartRow]
	if err := os.WriteFile(filepath.Join(dir, "expected-width"), []byte("28"), 0600); err != nil {
		t.Fatal(err)
	}
	captureStdout(t, func() { rt.handleKey(uv.KeyPressEvent{Code: uv.KeyEnter}) })
	awaitDetailRead(t, rt)
	if rt.detail.document == nil || rt.detail.document.Title != "Long parent" || detailPullCount(t, dir) != 0 {
		t.Fatal("opening detail fetched remote content instead of presenting the cache")
	}
	if rows := detailScreen(t, rt); rows[contentStartRow] != anchor {
		t.Fatal("expansion moved the existing Bench viewport anchor")
	}
	rt.detail.offset = 17
	for _, key := range []uv.KeyPressEvent{{Code: 'r', Text: "r"}, {Code: 'R', Text: "R"}, {Code: 'r', Mod: uv.ModShift}} {
		captureStdout(t, func() { rt.handleKey(key) })
		awaitDetailRead(t, rt)
		if rt.detail.document.Title != "Long parent" || rt.detail.offset != 17 || detailPullCount(t, dir) != 0 {
			t.Fatal("R changed position or fetched remote content rather than rereading the cache")
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "expected-width"), []byte("41"), 0600); err != nil {
		t.Fatal(err)
	}
	captureStdout(t, func() { rt.handleResize(size{cols: 45, rows: rt.size.rows}) })
	awaitDetailRead(t, rt)
	if rt.detail.document.Title != "Long parent" || detailPullCount(t, dir) != 0 {
		t.Fatal("resize implicitly synced instead of reflowing cached detail")
	}
	rt.browser.ensureLayout(rt.benchView, rt.contentCols())
	rt.browser.selectIndex(2)
	for i, key := range []uv.KeyPressEvent{{Code: 's', Text: "s"}, {Code: 'S', Text: "S"}, {Code: 's', Mod: uv.ModShift}} {
		captureStdout(t, func() { rt.handleKey(key) })
		awaitDetailRead(t, rt)
		if rt.detail.document.WorkItemID != 1 || rt.detail.document.Title != "Remote item" || !strings.Contains(rt.detail.document.ANSI, "Pulled remote description") || rt.detail.offset != 17 || rt.browser.selectedID != 3 || detailPullCount(t, dir) != i+1 {
			t.Fatal("S did not pull the captured item, or followed changed hidden selection")
		}
	}
}

func TestDetailNestedNativeWidthSubtractsIndentationAndClampsToOne(t *testing.T) {
	rt, dir := detailNativeFixture(t, false)
	output, err := os.ReadFile(filepath.Join(dir, "cache.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document DetailDocument
	if err := json.Unmarshal(output, &document); err != nil {
		t.Fatal(err)
	}
	document.WorkItemID, document.Title = 2, "Inherited child"
	output, err = json.Marshal(&document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cache.json"), output, 0600); err != nil {
		t.Fatal(err)
	}
	rt.browser.selectIndex(1)
	if err := os.WriteFile(filepath.Join(dir, "expected-width"), []byte("26"), 0600); err != nil {
		t.Fatal(err)
	}
	captureStdout(t, func() { rt.handleKey(uv.KeyPressEvent{Code: uv.KeyEnter}) })
	awaitDetailRead(t, rt)
	if rt.detail.err != nil || rt.detail.document == nil || rt.detail.document.WorkItemID != 2 {
		t.Fatalf("nested cache-only detail did not fit beneath its captured row: %v", rt.detail.err)
	}
	for _, next := range []struct{ cols, width int }{{20, 14}, {1, 1}} {
		if err := os.WriteFile(filepath.Join(dir, "expected-width"), []byte(strconv.Itoa(next.width)), 0600); err != nil {
			t.Fatal(err)
		}
		captureStdout(t, func() { rt.handleResize(size{cols: next.cols, rows: rt.size.rows}) })
		awaitDetailRead(t, rt)
		if rt.detail.err != nil || rt.detail.document.WorkItemID != 2 || detailPullCount(t, dir) != 0 {
			t.Fatalf("narrow inline reflow failed or fetched remote content: %v", rt.detail.err)
		}
	}
}

func TestDetailBusySyncRefusesReplayAndFailureRetainsContentAndPosition(t *testing.T) {
	rt, dir := detailNativeFixture(t, false)
	captureStdout(t, func() { rt.handleKey(uv.KeyPressEvent{Code: uv.KeyEnter}) })
	awaitDetailRead(t, rt)
	for _, name := range []string{"hold", "fail"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	original := rt.detail.document
	rt.detail.offset = 19
	captureStdout(t, func() { rt.handleKey(uv.KeyPressEvent{Code: 's', Text: "s"}) })
	gen := rt.detail.gen
	deadline := time.Now().Add(10 * time.Second)
	for detailPullCount(t, dir) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("explicit sync never reached the native pull boundary")
		}
		time.Sleep(5 * time.Millisecond)
	}
	captureStdout(t, func() {
		rt.handleKey(uv.KeyPressEvent{Code: 'r', Text: "r"})
		rt.handleKey(uv.KeyPressEvent{Code: 'S', Text: "S"})
		rt.handleResize(size{cols: 46, rows: rt.size.rows})
		rt.handleDetailRefresh()
		rt.handleDetailSync()
	})
	if !rt.detailActionBusy() || rt.detail.gen != gen || rt.detail.document != original || rt.detail.offset != 19 || !strings.Contains(rt.detailActionLabel(), "Syncing") {
		t.Fatal("overlapping keys, mouse actions or resize cancelled/replaced the active sync or its viewport")
	}
	if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	ev := awaitDetailRead(t, rt)
	if ev.err == nil || !strings.Contains(ev.err.Error(), "remote unavailable") || rt.detailActionBusy() || rt.detail.document != original || rt.detail.offset != 19 || !strings.Contains(rt.detailActionLabel(), "Item sync failed") || detailPullCount(t, dir) != 1 || rt.detail.gen != gen {
		t.Fatal("failed sync cancelled/replayed the pull or discarded last successful content/position")
	}
	rows := detailScreen(t, rt)
	if !strings.Contains(rows[len(rows)-2], "Item sync failed") || !strings.Contains(strings.Join(rows[contentStartRow:len(rows)-2], "\n"), "paragraph-") {
		t.Fatal("sync failure did not remain visible alongside the retained scrolled content")
	}
	captureStdout(t, func() { rt.handleKey(uv.KeyPressEvent{Code: 'r', Text: "r"}) })
	awaitDetailRead(t, rt)
	if rt.detail.err != nil || rt.detail.offset != 19 || rt.detail.document.Title != original.Title || detailPullCount(t, dir) != 1 {
		t.Fatal("cache refresh after failed sync could not recover without another remote pull")
	}
}

func TestDetailSeedSyncRefusalKeepsSeedReadableAndRefreshCached(t *testing.T) {
	rt, dir := detailNativeFixture(t, true)
	rt.browser.ensureLayout(rt.benchView, rt.contentCols())
	rt.browser.selectIndex(len(rt.browser.rows) - 1)
	captureStdout(t, func() { rt.handleKey(uv.KeyPressEvent{Code: uv.KeyEnter}) })
	awaitDetailRead(t, rt)
	if rt.detail.document == nil || rt.detail.document.WorkItemID != -1 {
		t.Fatal("seed could not be opened from cache")
	}
	original, gen := rt.detail.document, rt.detail.gen
	rt.detail.offset = 11
	captureStdout(t, func() { rt.handleKey(uv.KeyPressEvent{Code: 's', Mod: uv.ModShift}) })
	if rt.detailActionBusy() || rt.detail.gen != gen || rt.detail.document != original || rt.detail.offset != 11 || !strings.Contains(rt.detailActionLabel(), "unpublished seeds") || detailPullCount(t, dir) != 0 {
		t.Fatal("seed sync was not visibly refused before native/network access, or destroyed cached detail")
	}
	captureStdout(t, func() { rt.handleKey(uv.KeyPressEvent{Code: 'r', Mod: uv.ModShift}) })
	awaitDetailRead(t, rt)
	if rt.detail.err != nil || rt.detail.document.WorkItemID != -1 || rt.detail.offset != 11 || detailPullCount(t, dir) != 0 {
		t.Fatal("seed refusal prevented later cache-only refresh")
	}
}
