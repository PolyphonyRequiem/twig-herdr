package panel

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
)

// These are the native Bench-local selectors, not workspace area/sprint settings.
type BenchConfiguration struct {
	Version          int           `json:"version"`
	SettingsDigest   string        `json:"settingsDigest"`
	Areas            []BenchArea   `json:"areas"`
	Sprints          []BenchSprint `json:"sprints"`
	AutomaticEnabled bool          `json:"automaticEnabled"`
	AssigneeSummary  string        `json:"assigneeSummary"`
	Pins             []BenchPin    `json:"pins"`
}

type BenchArea struct {
	Path            string `json:"path"`
	IncludeChildren bool   `json:"includeChildren"`
}

type BenchSprint struct {
	Expression string `json:"expression"`
}

type BenchPin struct {
	ID     int    `json:"id"`
	Mode   string `json:"mode"`
	Cached bool   `json:"cached"`
	Title  string `json:"title,omitempty"`
	Type   string `json:"type,omitempty"`
	State  string `json:"state,omitempty"`
}

func validateConfiguration(c *BenchConfiguration) error {
	if c == nil || c.Version != 1 || c.SettingsDigest == "" || c.AssigneeSummary == "" || c.Areas == nil || c.Sprints == nil || c.Pins == nil {
		return errors.New("unsupported Bench configuration: install a matching twig-bench-native companion with configuration v1")
	}
	if c.AutomaticEnabled && len(c.Sprints) == 0 {
		return errors.New("invalid native automatic rule: enabled without sprint selectors")
	}
	for _, area := range c.Areas {
		if strings.TrimSpace(area.Path) == "" {
			return errors.New("invalid native Bench area selector")
		}
	}
	for _, sprint := range c.Sprints {
		if strings.TrimSpace(sprint.Expression) == "" {
			return errors.New("invalid native Bench sprint selector")
		}
	}
	for _, pin := range c.Pins {
		if pin.ID <= 0 || (pin.Mode != "single" && pin.Mode != "tree") {
			return errors.New("invalid native Bench pin selector")
		}
	}
	return nil
}

func configurationDigest(snapshot *BrowserSnapshot) string {
	if snapshot == nil || snapshot.Configuration == nil {
		return ""
	}
	return snapshot.Configuration.SettingsDigest
}

func (rt *runtime) matchesCapture(benchID string, binding HostBinding, digest string) bool {
	return rt.browser.snapshot != nil && rt.browser.snapshot.BenchID == benchID &&
		binding.Snapshot == rt.binding.Snapshot && binding.BindingID == rt.binding.BindingID &&
		binding.IdentityID == rt.binding.IdentityID && sameWorkspace(binding.WorktreeRoot, rt.binding.WorktreeRoot) &&
		digest != "" && configurationDigest(rt.browser.snapshot) == digest
}

type configurationView struct {
	benchID   string
	section   int
	selected  [4]int
	offset    int
	maxOffset int
}

// One editor handles IDs, area paths and iteration expressions. Cursor offsets
// are UTF-8 boundaries; movement and deletion operate on whole grapheme clusters.
type textEditor struct {
	text   string
	cursor int
}

func newTextEditor(value string) textEditor { return textEditor{text: value, cursor: len(value)} }

func (e *textEditor) boundaries() (int, int) {
	previous, next := 0, len(e.text)
	g := uniseg.NewGraphemes(e.text)
	for g.Next() {
		start, end := g.Positions()
		if end <= e.cursor {
			previous = start
		}
		if start >= e.cursor {
			next = end
			break
		}
	}
	return previous, next
}

func (e *textEditor) normalizeCursor() {
	// Editing can join the clusters on either side of the cursor. Keep the
	// cursor after that whole cluster, never between its constituent runes.
	g := uniseg.NewGraphemes(e.text)
	for g.Next() {
		start, end := g.Positions()
		if start < e.cursor && e.cursor < end {
			e.cursor = end
			return
		}
	}
}

func (e *textEditor) handle(key uv.KeyPressEvent) {
	previous, next := e.boundaries()
	switch {
	case key.MatchString("left"):
		e.cursor = previous
	case key.MatchString("right"):
		e.cursor = next
	case key.MatchString("home", "ctrl+a"):
		e.cursor = 0
	case key.MatchString("end", "ctrl+e"):
		e.cursor = len(e.text)
	case key.MatchString("backspace", "ctrl+h"):
		if e.cursor > 0 {
			e.text = e.text[:previous] + e.text[e.cursor:]
			e.cursor = previous
			e.normalizeCursor()
		}
	case key.MatchString("delete"):
		if e.cursor < len(e.text) {
			e.text = e.text[:e.cursor] + e.text[next:]
			e.normalizeCursor()
		}
	case key.MatchString("ctrl+u"):
		e.text, e.cursor = e.text[e.cursor:], 0
	default:
		text := key.Text
		if text == "" && key.MatchString("space", " ") {
			text = " "
		}
		for _, r := range text {
			if unicode.IsControl(r) {
				return
			}
		}
		e.text = e.text[:e.cursor] + text + e.text[e.cursor:]
		e.cursor += len(text)
		e.normalizeCursor()
	}
}

func positiveID(value string) (int, error) {
	if value == "" {
		return 0, errors.New("Enter a positive numeric work item ID.")
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, errors.New("Work item IDs accept digits only.")
		}
	}
	id, err := strconv.Atoi(value)
	if err != nil || id <= 0 {
		return 0, errors.New("Work item ID must be positive and fit the native ID range.")
	}
	// Native work item IDs are signed 32-bit integers, including on 64-bit Go.
	if uint64(id) > 2147483647 {
		return 0, errors.New("Work item ID exceeds the native ID range.")
	}
	return id, nil
}

type configurationForm struct {
	kind           string // pin, area, sprint, bench
	remove         bool
	editor         textEditor
	confirm        bool
	choice         int // pin: single/tree; area: under/exact
	benchID        string
	benchName      string
	binding        HostBinding
	settingsDigest string
	target         managedBench
	error          string
}

func (rt *runtime) leaveConfiguration() {
	rt.cancelManagement()
	rt.configuration, rt.form = nil, nil
	rt.showBrowserHelp = false
	rt.overlayOffset = 0
}

func (rt *runtime) openConfiguration() {
	if rt.mode != "bench" || rt.reconnectRequired {
		return
	}
	snapshot := rt.browser.snapshot
	if snapshot == nil {
		rt.setNotice("Wait for the Bench to load.", 0)
		rt.draw()
		return
	}
	if err := validateConfiguration(snapshot.Configuration); err != nil {
		rt.setError(err.Error())
		rt.draw()
		return
	}
	rt.cancelQueuedViews()
	rt.cancelReviewLookup(errors.New("Review superseded by Bench configuration"))
	rt.form, rt.showBrowserHelp = nil, false
	rt.configuration = &configurationView{benchID: snapshot.BenchID}
	rt.draw()
}

func (rt *runtime) openInput(kind, value string) {
	if rt.mode != "bench" || rt.reconnectRequired || rt.pinCancel != nil {
		return
	}
	if kind == "bench" {
		if rt.configuration == nil || rt.configuration.section != 3 {
			return
		}
		rt.form = &configurationForm{kind: kind, editor: newTextEditor(value), binding: rt.binding}
		rt.overlayOffset = 0
		rt.draw()
		return
	}
	if kind == "pin" && (rt.configuration == nil || rt.configuration.section != 0) {
		return
	}
	snapshot := rt.browser.snapshot
	if snapshot == nil {
		rt.setNotice("Wait for the Bench to load.", 0)
		rt.draw()
		return
	}
	if err := validateConfiguration(snapshot.Configuration); err != nil {
		rt.setError(err.Error())
		rt.draw()
		return
	}
	rt.cancelQueuedViews()
	rt.cancelReviewLookup(errors.New("Review superseded by a Bench editor"))
	rt.showBrowserHelp = false
	rt.form = &configurationForm{kind: kind, editor: newTextEditor(value), benchID: snapshot.BenchID, benchName: snapshot.BenchName, binding: rt.binding, settingsDigest: snapshot.Configuration.SettingsDigest}
	rt.overlayOffset = 0
	rt.draw()
}

func (rt *runtime) configurationCount() int {
	if rt.configuration != nil && rt.configuration.section == 3 {
		if rt.management.snapshot != nil {
			return len(rt.management.snapshot.Benches)
		}
		return 0
	}
	if rt.configuration == nil || rt.browser.snapshot == nil || rt.browser.snapshot.Configuration == nil {
		return 0
	}
	c := rt.browser.snapshot.Configuration
	switch rt.configuration.section {
	case 0:
		return len(c.Pins)
	case 1:
		return len(c.Areas)
	default:
		return len(c.Sprints)
	}
}

func (rt *runtime) chooseSection(section int) {
	if rt.configuration == nil {
		return
	}
	rt.configuration.section, rt.configuration.offset = (section+4)%4, 0
	if rt.configuration.section == 3 {
		rt.beginManagementRefresh()
	}
	rt.draw()
}

func (rt *runtime) removeConfigurationSelection() {
	view := rt.configuration
	if view == nil || rt.configurationCount() == 0 || rt.pinCancel != nil {
		return
	}
	if view.section == 3 {
		rt.openBenchDelete()
		return
	}
	c := rt.browser.snapshot.Configuration
	i := max(0, min(view.selected[view.section], rt.configurationCount()-1))
	if view.section == 0 {
		rt.setNotice("Use p for Single pin or Shift+P for Subtree pin; each toggles only its named kind.", 0)
		rt.draw()
		return
	}
	kind, value := "area", ""
	if view.section == 1 {
		value = c.Areas[i].Path
	} else {
		kind, value = "sprint", c.Sprints[i].Expression
	}
	rt.openInput(kind, value)
	if rt.form != nil {
		rt.form.remove, rt.form.confirm = true, true
		rt.draw()
	}
}

func (rt *runtime) addConfigurationSelection() {
	if rt.configuration == nil {
		return
	}
	switch rt.configuration.section {
	case 0:
		rt.setNotice("Use i in Pins to enter a positive ID, or p / Shift+P to toggle the selected item's pin kind.", 0)
		rt.draw()
	case 1:
		rt.openInput("area", "")
	case 2:
		rt.openInput("sprint", "")
	case 3:
		rt.openInput("bench", "")
	}
}

func (rt *runtime) handleConfigurationKey(key uv.KeyPressEvent) {
	c := rt.configuration
	if c == nil {
		return
	}
	switch {
	case key.MatchString("esc", "b"):
		rt.leaveConfiguration()
		rt.draw()
	case key.MatchString("q"):
		rt.shutdown()
	case key.MatchString("tab", "right"):
		rt.chooseSection(c.section + 1)
	case key.MatchString("shift+tab", "left"):
		rt.chooseSection(c.section + 3)
	case key.MatchString("1", "2", "3", "4"):
		section := 0
		if key.MatchString("2") {
			section = 1
		}
		if key.MatchString("3") {
			section = 2
		}
		if key.MatchString("4") {
			section = 3
		}
		rt.chooseSection(section)
	case key.MatchString("P", "shift+p"):
		rt.toggleSelectedPin("tree")
	case key.MatchString("p"):
		rt.toggleSelectedPin("single")
	case key.MatchString("enter"):
		if c.section == 3 {
			rt.selectManagedBench()
		} else {
			rt.addConfigurationSelection()
		}
	case key.MatchString("a", "n"):
		if !key.MatchString("n") || c.section == 3 {
			rt.addConfigurationSelection()
		}
	case key.MatchString("i"):
		if c.section == 0 {
			rt.openInput("pin", "")
		}
	case key.MatchString("d", "delete"):
		rt.removeConfigurationSelection()
	case key.MatchString("s"):
		rt.beginSync()
	case key.MatchString("r"):
		rt.beginBenchRefresh(nil, true)
		if c.section == 3 {
			rt.beginManagementRefresh()
		}
	case key.MatchString("j", "down", "k", "up", "home", "end"):
		i := c.selected[c.section]
		switch {
		case key.MatchString("j", "down"):
			i++
		case key.MatchString("k", "up"):
			i--
		case key.MatchString("home"):
			i = 0
		case key.MatchString("end"):
			i = rt.configurationCount() - 1
		}
		c.selected[c.section] = max(0, min(i, max(rt.configurationCount()-1, 0)))
		rt.revealConfigurationSelection()
		rt.draw()
	case key.MatchString("pgup", "pgdown"):
		step := max(rt.contentVisibleRows()-1, 1)
		if key.MatchString("pgup") {
			step = -step
		}
		c.offset = max(0, min(c.offset+step, c.maxOffset))
		rt.draw()
	}
}

func (rt *runtime) handleFormKey(key uv.KeyPressEvent) {
	f := rt.form
	if f == nil {
		return
	}
	if key.MatchString("esc") {
		rt.form = nil
		rt.draw()
		return
	}
	if f.kind == "bench" {
		if f.remove && key.MatchString("c", "n") {
			rt.form = nil
			rt.draw()
			return
		}
		rt.handleBenchFormKey(key)
		return
	}
	if f.confirm && rt.scrollOverlayKey(key) {
		return
	}
	if !f.confirm {
		if key.MatchString("enter") {
			value := f.editor.text
			if f.kind == "pin" {
				if _, err := positiveID(value); err != nil {
					rt.refuseForm(err.Error())
					return
				}
			} else if strings.TrimSpace(value) == "" {
				rt.refuseForm("Enter a nonempty path or sprint expression.")
				return
			}
			f.confirm, f.error = true, ""
		} else {
			f.editor.handle(key)
			f.error = ""
		}
		rt.draw()
		return
	}
	if f.kind == "pin" && key.MatchString("p", "P", "shift+p") {
		rt.confirmManualPin(key.MatchString("P", "shift+p"))
		return
	}
	if key.MatchString("c") {
		rt.form = nil
		rt.draw()
		return
	}
	if !f.remove && (f.kind == "pin" || f.kind == "area") && key.MatchString("tab", "shift+tab", "up", "down", "left", "right", "1", "2") {
		if key.MatchString("1") {
			f.choice = 0
		} else if key.MatchString("2") {
			f.choice = 1
		} else {
			f.choice = 1 - f.choice
		}
		rt.draw()
		return
	}
	if key.MatchString("e", "backspace") && !f.remove {
		f.confirm = false
		rt.draw()
		return
	}
	if key.MatchString("enter") {
		rt.beginConfigurationMutation()
	}
}

func (rt *runtime) confirmManualPin(tree bool) {
	if rt.form == nil || rt.form.kind != "pin" || !rt.form.confirm {
		return
	}
	rt.form.choice = 0
	if tree {
		rt.form.choice = 1
	}
	rt.beginConfigurationMutation()
}

func (rt *runtime) beginConfigurationMutation() {
	f := rt.form
	if f == nil || !f.confirm || rt.pinCancel != nil || rt.reconnectRequired || rt.closing {
		return
	}
	if rt.syncCancel != nil {
		rt.refuseForm("Wait for Bench sync to finish before confirming.")
		return
	}
	if !rt.matchesCapture(f.benchID, f.binding, f.settingsDigest) {
		rt.refuseForm("Bench, origin or settings changed. Cancel and reopen the form.")
		return
	}
	args := []string{}
	if f.kind == "pin" {
		id, err := positiveID(f.editor.text)
		if err != nil {
			rt.refuseForm(err.Error())
			return
		}
		mode := "single"
		if f.choice == 1 {
			mode = "tree"
		}
		rt.beginPinMutation(pinAction{id: id, mode: mode, benchID: f.benchID, binding: f.binding, settingsDigest: f.settingsDigest})
		return
	} else {
		action := "add"
		if f.remove {
			action = "remove"
		}
		args = []string{"bench", "configuration", f.kind, action, f.editor.text}
		if f.kind == "area" && !f.remove && f.choice == 1 {
			args = append(args, "--exact")
		}
	}
	args = semanticArgs(append(args, "--expect-bench", f.benchID, "--expect-settings", f.settingsDigest, "-o", "json"), f.binding)
	rt.form = nil
	rt.cancelManagement()
	rt.bench.launchGen++
	rt.cancelBenchLaunch(errors.New("Bench configuration change superseded refresh"))
	rt.pinGen++
	gen := rt.pinGen
	ctx, cancel := context.WithTimeout(rt.ctx, 30*time.Second)
	rt.pinCancel = cancel
	rt.pinAction = nil
	rt.mutationLabel = "Configuration"
	rt.setNotice("Changing local Bench configuration…", 0)
	rt.draw()
	go func() {
		err := rt.semanticAuthority(ctx, f.binding)
		if err == nil {
			_, err = nativeOutput(ctx, rt.nativePath, rt.cfg.Cwd, args...)
		}
		admissionErr := rt.postSemanticAdmission(f.binding)
		rt.emit(event{kind: evPinDone, token: gen, err: err, admissionErr: admissionErr})
	}()
}

func (rt *runtime) refuseForm(message string) {
	if rt.form == nil {
		return
	}
	rt.form.error = message
	rt.setError(message)
	rt.draw()
}

type configurationLine struct {
	text         string
	action       string
	row          int
	fieldCursors []int // rendered byte offsets to editor grapheme boundaries
	cursorOffset int   // rendered cursor marker byte offset; -1 on other field rows
}

func (e *textEditor) fieldLine() configurationLine {
	var text strings.Builder
	text.Grow(len(e.text) + len("> ▏"))
	text.WriteString("> ")
	line := configurationLine{action: "field", row: -1, fieldCursors: make([]int, 2, len(e.text)+len("> ▏")+1), cursorOffset: -1}
	appendText := func(value string, cursor int) {
		value = safe(value)
		text.WriteString(value)
		for range len(value) {
			line.fieldCursors = append(line.fieldCursors, cursor)
		}
	}
	g := uniseg.NewGraphemes(e.text)
	for g.Next() {
		start, _ := g.Positions()
		if start == e.cursor {
			line.cursorOffset = text.Len()
			appendText("▏", e.cursor)
		}
		appendText(g.Str(), start)
	}
	if e.cursor == len(e.text) {
		line.cursorOffset = text.Len()
		appendText("▏", e.cursor)
	}
	line.fieldCursors = append(line.fieldCursors, len(e.text))
	line.text = text.String()
	return line
}

func pinDescription(pin BenchPin) string {
	text := fmt.Sprintf("#%d · %s", pin.ID, pin.Mode)
	if !pin.Cached {
		return text + " · uncached / unverified"
	}
	return text + " · " + safe(strings.TrimSpace(pin.Type+" "+pin.State+" "+pin.Title))
}

func (rt *runtime) configurationLines(cols int) []configurationLine {
	view := rt.configuration
	c := &BenchConfiguration{}
	if rt.browser.snapshot != nil && rt.browser.snapshot.Configuration != nil {
		c = rt.browser.snapshot.Configuration
	}
	var logical []configurationLine
	for i, label := range []string{"[1 Pins]", "[2 Areas]", "[3 Sprints]", "[4 Benches]"} {
		prefix := "  "
		if i == view.section {
			prefix = "› "
		}
		logical = append(logical, configurationLine{text: prefix + label, action: "section", row: i})
	}
	status := "Automatic off (no sprints) · Ownership: " + safe(c.AssigneeSummary)
	if c.AutomaticEnabled {
		status = "Automatic ownership: " + safe(c.AssigneeSummary)
	}
	if view.section == 3 {
		status = "Named local benches · create empty, then select explicitly"
		if rt.management.cancel != nil {
			status = "Loading native benches…"
		} else if rt.management.error != "" {
			status = "Refused: " + safe(rt.management.error) + " · r retries the list only"
		}
	}
	logical = append(logical, configurationLine{text: status, row: -1})
	count := rt.configurationCount()
	view.selected[view.section] = max(0, min(view.selected[view.section], max(count-1, 0)))
	for i := range count {
		label := ""
		switch view.section {
		case 0:
			label = pinDescription(c.Pins[i])
		case 1:
			scope := "Exact"
			if c.Areas[i].IncludeChildren {
				scope = "Under"
			}
			label = scope + " · " + safe(c.Areas[i].Path)
		case 2:
			label = safe(c.Sprints[i].Expression)
		case 3:
			bench := rt.management.snapshot.Benches[i]
			label = safe(bench.Name)
			if bench.IsCurrent {
				label += " · current"
			}
			if bench.IsDefault {
				label += " · default (protected)"
			}
		}
		prefix := "  "
		if i == view.selected[view.section] {
			prefix = "› "
		}
		logical = append(logical, configurationLine{text: prefix + label, action: "entry", row: i})
	}
	if count == 0 {
		message := "No saved selectors in this section. Add one below."
		if view.section == 3 {
			message = "No admitted Bench list. r refreshes; n creates an empty Bench."
		}
		logical = append(logical, configurationLine{text: message, row: -1})
	}
	if view.section == 0 {
		if node := rt.selectedPinNode(); node != nil {
			for _, mode := range []string{"single", "tree"} {
				logical = append(logical, configurationLine{text: rt.pinButton(mode), action: "pin-" + mode, row: -1})
			}
		}
		logical = append(logical, configurationLine{text: "[i Enter any ID]", action: "manual-id", row: -1})
	} else if view.section == 3 {
		logical = append(logical, configurationLine{text: "[n Create empty Bench]", action: "add", row: -1})
		if selected := rt.selectedManagedBench(); selected != nil {
			logical = append(logical, configurationLine{text: "[Enter Select Bench]", action: "bench-select", row: -1})
			if !selected.IsDefault {
				logical = append(logical, configurationLine{text: "[d Delete selected Bench…]", action: "remove", row: -1})
			}
		}
	} else {
		logical = append(logical, configurationLine{text: "[a Add]", action: "add", row: -1})
		if count > 0 {
			logical = append(logical, configurationLine{text: "[d Remove selected]", action: "remove", row: -1})
		}
	}
	if view.section == 1 {
		logical = append(logical, configurationLine{text: "Areas OR together, then AND chosen sprints. No areas means no area restriction; it does not enable automatic membership.", row: -1})
	} else if view.section == 2 {
		logical = append(logical, configurationLine{text: "Sprints OR together: @Current, @Current+1, @Current-1, or absolute iteration paths. Saved rules drive cached reads and scoped sync.", row: -1})
	} else if view.section == 3 {
		logical = append(logical, configurationLine{text: "↑/↓ or j/k choose · Enter selects · n creates · d/Delete reviews deletion. Default cannot be deleted. Deletion preserves staged work; deleting the current Bench selects default.", row: -1})
	} else {
		logical = append(logical, configurationLine{text: "p toggles only Single pin; Shift+P only Subtree pin. Both can coexist. Other pin kinds, inherited membership, automatic rules and protected work remain. Uncached IDs stay unverified until scoped sync.", row: -1})
	}
	return wrapConfigurationLines(logical, cols)
}

func wrapConfigurationLines(logical []configurationLine, cols int) []configurationLine {
	var physical []configurationLine
	for _, line := range logical {
		offset := 0
		for _, part := range strings.Split(ansi.Hardwrap(line.text, max(cols, 1), true), "\n") {
			wrapped := line
			wrapped.text = part
			if line.fieldCursors != nil {
				wrapped.fieldCursors = line.fieldCursors[offset : offset+len(part)+1]
				wrapped.cursorOffset = -1
				if line.cursorOffset >= offset && line.cursorOffset < offset+len(part) {
					wrapped.cursorOffset = line.cursorOffset - offset
				}
				offset += len(part)
			}
			physical = append(physical, wrapped)
		}
	}
	return physical
}

func (rt *runtime) revealConfigurationSelection() {
	view := rt.configuration
	lines := rt.configurationLines(rt.contentCols())
	start, end := -1, -1
	for i, line := range lines {
		if line.action == "entry" && line.row == view.selected[view.section] {
			if start < 0 {
				start = i
			}
			end = i + 1
		}
	}
	visible := rt.contentVisibleRows()
	if start >= 0 && start < view.offset {
		view.offset = start
	} else if end > view.offset+visible {
		view.offset = max(start, end-visible)
	}
}

func (rt *runtime) drawConfiguration(out *strings.Builder, cols, visible int) {
	lines := rt.configurationLines(cols)
	c := rt.configuration
	c.maxOffset = max(len(lines)-visible, 0)
	c.offset = max(0, min(c.offset, c.maxOffset))
	rt.drawConfigurationLines(out, cols, visible, lines, c.offset)
}

func (rt *runtime) drawConfigurationLines(out *strings.Builder, cols, visible int, lines []configurationLine, offset int) {
	for y := range visible {
		fmt.Fprintf(out, "\x1b[%d;1H\x1b[2K", y+3)
		if offset+y >= len(lines) {
			continue
		}
		line := lines[offset+y]
		if line.action != "" {
			out.WriteString("\x1b[1m")
		}
		out.WriteString(ansi.Truncate(line.text, cols, ""))
		out.WriteString("\x1b[0m")
		if line.fieldCursors != nil {
			// Use the renderer's wrapped text and its display-width convention,
			// not a separate mouse-side wrapping calculation. Both cells of a
			// wide grapheme target its start; row padding targets its end.
			text, column, offset := line.text, 0, 0
			for len(text) > 0 && column < cols {
				cluster, width := ansi.FirstGraphemeCluster(text, ansi.GraphemeWidth)
				if width > 0 {
					rt.hits = append(rt.hits, hitTarget{x1: column, x2: min(column+width, cols), y: y + 2, action: "config-field", row: line.fieldCursors[offset]})
				}
				column += width
				offset += len(cluster)
				text = text[len(cluster):]
			}
			if column < cols {
				rt.hits = append(rt.hits, hitTarget{x1: column, x2: cols, y: y + 2, action: "config-field", row: line.fieldCursors[len(line.text)]})
			}
		} else if line.action != "" {
			rt.hits = append(rt.hits, hitTarget{x1: 0, x2: cols, y: y + 2, action: "config-" + line.action, row: line.row})
		}
	}
}

func (rt *runtime) drawConfigurationForm(out *strings.Builder, cols, visible int) {
	f := rt.form
	if f.kind == "bench" {
		rt.drawBenchForm(out, cols, visible)
		return
	}
	verb := "Add"
	if f.remove {
		verb = "Remove"
	}
	lines := []configurationLine{{text: verb + " " + f.kind + " · Bench: " + safe(f.benchName) + " (" + safe(f.benchID) + ")", row: -1}}
	if !f.confirm {
		prompt := "Area path"
		if f.kind == "pin" {
			prompt = "Positive work item ID (digits only)"
		}
		if f.kind == "sprint" {
			prompt = "Sprint expression: @Current±N or absolute iteration path"
		}
		lines = append(lines, configurationLine{text: prompt, row: -1}, f.editor.fieldLine(), configurationLine{text: "Type text · ←/→ Home/End edit · Enter review · Esc cancel", row: -1})
	} else {
		lines = append(lines, configurationLine{text: verb + ": " + safe(f.editor.text), row: -1})
		if f.kind == "pin" {
			preview := "Entered ID: uncached / unverified. No remote fetch or title is inferred. This only adds a local durable pin."
			if id, err := positiveID(f.editor.text); err == nil {
				var visit func([]*BrowserNode)
				visit = func(nodes []*BrowserNode) {
					for _, node := range nodes {
						if node.ID == id {
							preview = fmt.Sprintf("Cached #%d: %s · %s · %s. No remote verification was requested.", id, safe(node.Type), safe(node.State), safe(node.Title))
							return
						}
						visit(node.Children)
					}
				}
				visit(rt.browser.snapshot.Roots)
			}
			lines = append(lines, configurationLine{text: preview, row: -1})
		}
		if !f.remove && (f.kind == "pin" || f.kind == "area") {
			choices := []string{rt.manualPinButton(false), rt.manualPinButton(true)}
			if f.kind == "area" {
				choices = []string{"Under (include descendants)", "Exact (this area only)"}
			}
			for i, choice := range choices {
				prefix := "  "
				if i == f.choice {
					prefix = "› "
				}
				action := "choice"
				if f.kind == "pin" {
					action = "pin-single"
					if i == 1 {
						action = "pin-tree"
					}
				}
				lines = append(lines, configurationLine{text: prefix + choice, action: action, row: i})
			}
		}
		if !f.remove {
			lines = append(lines, configurationLine{text: "[e Edit value]", action: "edit", row: -1})
		}
		lines = append(lines, configurationLine{text: "[Enter Confirm]", action: "confirm", row: -1})
	}
	if f.error != "" {
		lines = append(lines, configurationLine{text: "Refused: " + safe(f.error), row: -1})
	}
	lines = append(lines, configurationLine{text: "[Esc Cancel]", action: "cancel", row: -1})
	rt.drawFormLines(out, cols, visible, lines)
}

func (rt *runtime) drawFormLines(out *strings.Builder, cols, visible int, lines []configurationLine) {
	physical := wrapConfigurationLines(lines, cols)
	rt.overlayMax = max(len(physical)-visible, 0)
	rt.overlayOffset = max(0, min(rt.overlayOffset, rt.overlayMax))
	// Keep the edited cursor visible even when entered text wraps deeply.
	if !rt.form.confirm {
		for i, line := range physical {
			if line.action == "field" && line.cursorOffset >= 0 {
				if i < rt.overlayOffset {
					rt.overlayOffset = i
				}
				if i >= rt.overlayOffset+visible {
					rt.overlayOffset = max(i-visible+1, 0)
				}
			}
		}
	}
	rt.drawConfigurationLines(out, cols, visible, physical, rt.overlayOffset)
}

func (rt *runtime) handleConfigurationMouse(mouse uv.Mouse) {
	if mouse.Button == uv.MouseWheelUp || mouse.Button == uv.MouseWheelDown {
		step := 3
		if mouse.Button == uv.MouseWheelUp {
			step = -step
		}
		if rt.form != nil {
			rt.overlayOffset = max(0, min(rt.overlayOffset+step, rt.overlayMax))
		} else if rt.configuration != nil {
			c := rt.configuration
			c.offset = max(0, min(c.offset+step, c.maxOffset))
		}
		rt.draw()
		return
	}
	if mouse.Button != uv.MouseLeft {
		return
	}
	for _, hit := range rt.hits {
		if mouse.Y != hit.y || mouse.X < hit.x1 || mouse.X >= hit.x2 {
			continue
		}
		if rt.form != nil {
			switch hit.action {
			case "config-field":
				if !rt.form.confirm {
					rt.form.editor.cursor = hit.row
					rt.draw()
				}
			case "config-confirm":
				if rt.form.kind == "bench" {
					rt.handleBenchConfirmMouse()
				} else if rt.form.confirm {
					rt.beginConfigurationMutation()
				} else {
					rt.handleFormKey(uv.KeyPressEvent{Code: uv.KeyEnter})
				}
			case "config-cancel":
				rt.form = nil
				rt.draw()
			case "config-choice":
				rt.form.choice = hit.row
				rt.draw()
			case "config-pin-single":
				rt.confirmManualPin(false)
			case "config-pin-tree":
				rt.confirmManualPin(true)
			case "config-edit":
				rt.form.confirm = false
				rt.draw()
			}
		} else if rt.configuration != nil {
			switch hit.action {
			case "config-section":
				rt.chooseSection(hit.row)
			case "config-entry":
				rt.configuration.selected[rt.configuration.section] = hit.row
				rt.draw()
			case "config-bench-select":
				rt.selectManagedBench()
			case "config-add":
				rt.addConfigurationSelection()
			case "config-manual-id":
				rt.openInput("pin", "")
			case "config-remove":
				rt.removeConfigurationSelection()
			case "config-pin-single":
				rt.toggleSelectedPin("single")
			case "config-pin-tree":
				rt.toggleSelectedPin("tree")
			case "config-back":
				rt.leaveConfiguration()
				rt.draw()
			}
		}
		return
	}
}
