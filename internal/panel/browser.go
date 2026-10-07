package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	goruntime "runtime"
	"strconv"
	"strings"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// BrowserSnapshot is native membership and presentation authority. Keys identify
// displayed occurrences, not work-item identities: an item may occur in two roots.
type BrowserSnapshot struct {
	Version       int                 `json:"version"`
	BenchID       string              `json:"benchId"`
	BenchName     string              `json:"benchName"`
	BindingID     string              `json:"bindingId"`
	IdentityID    string              `json:"identityId"`
	WorktreeRoot  string              `json:"worktreeRoot"`
	Configuration *BenchConfiguration `json:"configuration"`
	Roots         []*BrowserNode      `json:"roots"`
}

type BrowserNode struct {
	Key              string         `json:"key"`
	ID               int            `json:"id"`
	Title            string         `json:"title"`
	Type             string         `json:"type"`
	State            string         `json:"state"`
	Label            string         `json:"label"`
	IsSeed           bool           `json:"isSeed"`
	Pins             []string       `json:"pins"`
	OwningSubtreeIDs []int          `json:"owningSubtreeIds"`
	Membership       string         `json:"membership"`
	Children         []*BrowserNode `json:"children"`
}

type browserRow struct {
	node   *BrowserNode
	parent string
	depth  int
	start  int
	end    int
}

type browserLine struct {
	text       string
	row        int
	disclosure int // -1 on continuations and leaf rows
}

type browserModel struct {
	snapshot   *BrowserSnapshot
	selected   string
	selectedID int
	collapsed  map[string]bool
	rows       []browserRow
	lines      []browserLine
	layoutView string
	layoutCols int
	dirty      bool
}

type pinAction struct {
	id             int
	mode           string
	remove         bool
	benchID        string
	binding        HostBinding
	settingsDigest string
}

type hitTarget struct {
	x1, x2, y int
	action    string
	row       int
}

func companionPath() string {
	name := "twig-bench-native"
	if goruntime.GOOS == "windows" {
		name += ".exe"
	}
	if executable, err := os.Executable(); err == nil {
		for _, candidate := range []string{
			filepath.Join(filepath.Dir(executable), "twig-bench-native", name),
			filepath.Join(filepath.Dir(executable), name),
		} {
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate
			}
		}
	}
	return "twig"
}

func nativeOutput(ctx context.Context, executable, cwd string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Dir = cwd
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("semantic bench command failed: %w: %s %s; install a matching twig-bench-native beside this browser or upgrade Twig to support browser/configuration v1, captured-origin/settings guards and workspace untrack --mode single|tree", err, strings.TrimSpace(string(output)), strings.TrimSpace(stderr.String()))
	}
	return output, nil
}

func verifyBrowserOrigin(snapshot *BrowserSnapshot, binding HostBinding) error {
	if snapshot.BindingID != binding.BindingID || snapshot.IdentityID != binding.IdentityID || !sameWorkspace(snapshot.WorktreeRoot, binding.WorktreeRoot) {
		return errors.New("binding-changed/reconnect: semantic browser origin does not match the admitted attachment")
	}
	return nil
}

func parseBrowser(output []byte, binding HostBinding) (*BrowserSnapshot, error) {
	var envelope struct {
		Browser *BrowserSnapshot `json:"browser"`
	}
	if err := json.Unmarshal(output, &envelope); err != nil {
		return nil, fmt.Errorf("invalid semantic workspace JSON: %w; install twig-bench-native beside this browser or upgrade Twig", err)
	}
	snapshot := envelope.Browser
	if snapshot == nil || snapshot.Version != 1 || snapshot.BenchID == "" || snapshot.BindingID == "" || snapshot.IdentityID == "" || snapshot.WorktreeRoot == "" || snapshot.Roots == nil {
		return nil, errors.New("unsupported semantic bench capability: workspace --view tree -o json --include-browser must provide browser v1; install twig-bench-native beside this browser or upgrade Twig")
	}
	if err := verifyBrowserOrigin(snapshot, binding); err != nil {
		return nil, err
	}
	if err := validateConfiguration(snapshot.Configuration); err != nil {
		return nil, err
	}
	keys := make(map[string]bool)
	var validate func([]*BrowserNode) error
	validate = func(nodes []*BrowserNode) error {
		for _, node := range nodes {
			if node == nil || node.Key == "" || keys[node.Key] || node.ID == 0 || node.Label == "" || (!node.IsSeed && node.ID < 0) {
				return errors.New("invalid semantic browser node identity/label; upgrade Twig and this browser together")
			}
			keys[node.Key] = true
			for _, pin := range node.Pins {
				if pin != "single" && pin != "tree" {
					return errors.New("unsupported semantic browser pin kind; upgrade Twig and this browser together")
				}
			}
			for _, id := range node.OwningSubtreeIDs {
				if id <= 0 {
					return errors.New("invalid semantic browser subtree identity")
				}
			}
			if err := validate(node.Children); err != nil {
				return err
			}
		}
		return nil
	}
	if err := validate(snapshot.Roots); err != nil {
		return nil, err
	}
	return snapshot, nil
}

// The companion shares native attachment services, but is not the installed
// Twig used for host admission/proposals. Expected IDs are refusal guards, never
// authentication selectors. Normal Twig checks still bracket every boundary.
func (rt *runtime) semanticAuthority(ctx context.Context, binding HostBinding) error {
	_, err := rt.readBinding(ctx, rt.cfg.Cwd, binding.Snapshot)
	return err
}

func (rt *runtime) postSemanticAdmission(binding HostBinding) error {
	ctx, cancel := context.WithTimeout(rt.ctx, 15*time.Second)
	defer cancel()
	_, err := rt.readBinding(ctx, rt.cfg.Cwd, binding.Snapshot)
	return err
}

func semanticArgs(args []string, binding HostBinding) []string {
	return append(args, "--expect-binding", binding.BindingID, "--expect-identity", binding.IdentityID)
}

func (rt *runtime) launchBrowser(ctx context.Context, gen uint64, binding HostBinding) {
	err := rt.semanticAuthority(ctx, binding)
	var snapshot *BrowserSnapshot
	if err == nil {
		var output []byte
		output, err = nativeOutput(ctx, rt.nativePath, rt.cfg.Cwd, semanticArgs([]string{"workspace", "--view", "tree", "-o", "json", "--include-browser"}, binding)...)
		if err == nil {
			snapshot, err = parseBrowser(output, binding)
		}
	}
	// Failed commands also need a post-boundary check; old actor data must never
	// remain visible merely because a newly bound command returned an error.
	admissionErr := rt.postSemanticAdmission(binding)
	rt.emit(event{kind: evBrowserLoaded, token: gen, browser: snapshot, err: err, admissionErr: admissionErr})
}

func (rt *runtime) handleBrowserLoaded(ev event) {
	if rt.closing || rt.reconnectRequired || ev.token != rt.bench.launchGen {
		return
	}
	if ev.admissionErr != nil {
		rt.stopForReconnect(ev.admissionErr)
		return
	}
	if ev.err != nil {
		rt.handleLaunchFailed("bench", ev.token, ev.err)
		return
	}
	if ev.browser == nil {
		rt.handleLaunchFailed("bench", ev.token, errors.New("missing semantic browser snapshot"))
		return
	}
	if err := verifyBrowserOrigin(ev.browser, rt.binding); err != nil {
		rt.stopForReconnect(err)
		return
	}
	anchorKey, anchorID, anchorPart := "", 0, 0
	headerAnchor := false
	b := &rt.browser
	if b.snapshot != nil && b.snapshot.BenchID == ev.browser.BenchID {
		b.ensureLayout(rt.benchView, rt.contentCols())
		if offset := rt.offsets[rt.benchView]; offset >= 0 && offset < len(b.lines) {
			rowIndex := b.lines[offset].row
			if rowIndex >= 0 && rowIndex < len(b.rows) {
				row := b.rows[rowIndex]
				anchorKey, anchorID, anchorPart = row.node.Key, row.node.ID, offset-row.start
			} else if offset == 0 {
				headerAnchor = true
			}
		}
	} else {
		rt.offsets["tree"], rt.offsets["table"] = 0, 0
	}
	rt.cancelBenchLaunch(nil)
	rt.browser.replace(ev.browser)
	rt.benchSummary = ev.browser.BenchName
	if rt.configuration != nil && rt.configuration.benchID != ev.browser.BenchID {
		rt.leaveConfiguration()
		rt.setNotice("Bench changed; configuration closed without retargeting.", 0)
	}
	if rt.form != nil && rt.form.benchID != ev.browser.BenchID {
		rt.form = nil
		rt.setNotice("Bench changed; captured input canceled.", 0)
	}
	if strings.HasPrefix(rt.notice, "Twig bench refresh failed") {
		rt.notice = ""
	}
	rt.browser.ensureLayout(rt.benchView, rt.contentCols())
	anchored := headerAnchor
	if headerAnchor {
		rt.offsets[rt.benchView] = 0
	}
	for _, row := range b.rows {
		if row.node.Key == anchorKey || (anchorKey != "" && row.node.ID == anchorID) {
			rt.offsets[rt.benchView] = row.start + min(anchorPart, row.end-row.start-1)
			anchored = true
			break
		}
	}
	if !anchored {
		rt.ensureSelectionVisible()
	}
	reply := rt.bench.pendingReply
	rt.bench.pendingReply = nil
	rt.reply(reply, Result{Snapshot: rt.snapshot()})
	rt.draw()
}

func (b *browserModel) replace(snapshot *BrowserSnapshot) {
	if b.snapshot != nil && b.snapshot.BenchID != snapshot.BenchID {
		b.selected, b.selectedID, b.collapsed = "", 0, nil
	}
	foldsByID := make(map[int]bool)
	var remember func([]*BrowserNode)
	remember = func(nodes []*BrowserNode) {
		for _, node := range nodes {
			if collapsed, exists := b.collapsed[node.Key]; exists {
				foldsByID[node.ID] = collapsed
			}
			remember(node.Children)
		}
	}
	if b.snapshot != nil && b.snapshot.BenchID == snapshot.BenchID {
		remember(b.snapshot.Roots)
	}
	b.snapshot = snapshot
	if b.collapsed == nil {
		b.collapsed = make(map[string]bool)
	}
	present := make(map[string]bool)
	var byKey, byID *BrowserNode
	var visit func([]*BrowserNode)
	visit = func(nodes []*BrowserNode) {
		for _, node := range nodes {
			present[node.Key] = true
			if _, exists := b.collapsed[node.Key]; !exists {
				if collapsed, known := foldsByID[node.ID]; known {
					b.collapsed[node.Key] = collapsed
				}
			}
			if node.Key == b.selected {
				byKey = node
			}
			if node.ID == b.selectedID && byID == nil {
				byID = node
			}
			visit(node.Children)
		}
	}
	visit(snapshot.Roots)
	for key := range b.collapsed {
		if !present[key] {
			delete(b.collapsed, key)
		}
	}
	if byKey != nil {
		b.selectedID = byKey.ID
	} else if byID != nil {
		b.selected = byID.Key
	} else {
		b.selected, b.selectedID = "", 0
	}
	b.dirty = true
}

func (rt *runtime) scrollOverlayKey(key uv.KeyPressEvent) bool {
	step := max(rt.contentVisibleRows()-2, 1)
	switch {
	case key.MatchString("pgdown"):
		rt.overlayOffset += step
	case key.MatchString("pgup"):
		rt.overlayOffset -= step
	case key.MatchString("home"):
		rt.overlayOffset = 0
	case key.MatchString("end"):
		rt.overlayOffset = rt.overlayMax
	default:
		return false
	}
	rt.overlayOffset = max(0, min(rt.overlayOffset, rt.overlayMax))
	rt.draw()
	return true
}

// Native labels own badge/color choices. Permit SGR only, never cursor/OSC or
// control sequences from work-item text. This does not infer IDs from ANSI.
var labelSGR = regexp.MustCompile(`\x1b\[[0-9;:]*m`)

func terminalLabel(label string) string {
	var out strings.Builder
	for len(label) > 0 {
		if label[0] == 0x1b {
			if loc := labelSGR.FindStringIndex(label); loc != nil && loc[0] == 0 {
				out.WriteString(label[:loc[1]])
				label = label[loc[1]:]
				continue
			}
			// Unsupported escape means invalid native one-row data, not styling.
			label = label[1:]
			continue
		}
		next := strings.IndexByte(label, 0x1b)
		if next < 0 {
			next = len(label)
		}
		out.WriteString(safe(label[:next]))
		label = label[next:]
	}
	return out.String()
}

func wrappedLabels(label string, width int) []string {
	lines := strings.Split(ansi.Hardwrap(label, width, true), "\n")
	carry := ""
	for i, line := range lines {
		lines[i] = carry + line
		for _, code := range labelSGR.FindAllString(line, -1) {
			if code == "\x1b[0m" || code == "\x1b[m" {
				carry = ""
			} else {
				carry += code
			}
		}
	}
	return lines
}

func (b *browserModel) ensureLayout(view string, cols int) {
	if !b.dirty && b.layoutView == view && b.layoutCols == cols {
		return
	}
	b.rows, b.lines = b.rows[:0], b.lines[:0]
	b.layoutView, b.layoutCols, b.dirty = view, cols, false
	if b.snapshot == nil {
		return
	}
	tableWidth := 0
	if view == "table" && cols >= 40 {
		tableWidth = cols - 2 - 3 - 18
		b.lines = append(b.lines, browserLine{text: "Work item" + strings.Repeat(" ", max(tableWidth-9, 0)) + " │ Pins / membership", row: -1, disclosure: -1})
	}
	var visit func([]*BrowserNode, string, int)
	visit = func(nodes []*BrowserNode, parent string, depth int) {
		for _, node := range nodes {
			index := len(b.rows)
			row := browserRow{node: node, parent: parent, depth: depth, start: len(b.lines)}
			indent := 0
			if view == "tree" {
				indent = min(depth*2, max(cols/3, 0))
			}
			prefix := strings.Repeat(" ", indent)
			disclosure := -1
			if view == "tree" && len(node.Children) > 0 {
				disclosure = indent
				if b.collapsed[node.Key] {
					prefix += "▸ "
				} else {
					prefix += "▾ "
				}
			} else {
				prefix += "  "
			}
			label := terminalLabel(node.Label)
			if node.Membership != "" && tableWidth == 0 {
				label += "  · " + safe(node.Membership)
			}
			if len(node.Pins) > 0 && tableWidth == 0 {
				label += "  [pin: " + strings.Join(node.Pins, "+") + "]"
			}
			width := max(cols-ansi.StringWidth(prefix)-2, 1)
			if tableWidth > 0 {
				width, prefix = tableWidth, ""
			}
			// Hardwrap keeps graphemes whole; replay native SGR on continuation
			// lines because each physical terminal row resets its styles.
			for part, text := range wrappedLabels(label, width) {
				p := prefix
				d := disclosure
				if part > 0 {
					p = strings.Repeat(" ", ansi.StringWidth(prefix))
					d = -1
				}
				if tableWidth > 0 {
					membership := ""
					if part == 0 {
						membership = safe(node.Membership)
						if len(node.Pins) > 0 {
							membership = strings.Join(node.Pins, "+")
						}
					}
					text += strings.Repeat(" ", max(tableWidth-ansi.StringWidth(text), 0)) + " │ " + ansi.Truncate(membership, 18, "…")
				}
				b.lines = append(b.lines, browserLine{text: p + text, row: index, disclosure: d})
			}
			row.end = len(b.lines)
			b.rows = append(b.rows, row)
			if view != "tree" || !b.collapsed[node.Key] {
				visit(node.Children, node.Key, depth+1)
			}
		}
	}
	visit(b.snapshot.Roots, "", 0)
	if b.selectedIndex() < 0 && len(b.rows) > 0 {
		// A Table-selected descendant may be hidden by an existing Tree fold.
		// Fall back to its nearest displayed ancestor, not an unrelated first row.
		parents := make(map[string]string)
		var ancestry func([]*BrowserNode, string)
		ancestry = func(nodes []*BrowserNode, parent string) {
			for _, node := range nodes {
				parents[node.Key] = parent
				ancestry(node.Children, node.Key)
			}
		}
		ancestry(b.snapshot.Roots, "")
		key := b.selected
		for key != "" && b.selectedIndex() < 0 {
			key = parents[key]
			b.selected = key
		}
		if index := b.selectedIndex(); index >= 0 {
			b.selectIndex(index)
		} else {
			b.selectIndex(0)
		}
	}
}

func (b *browserModel) selectedIndex() int {
	for i := range b.rows {
		if b.rows[i].node.Key == b.selected {
			return i
		}
	}
	return -1
}
func (b *browserModel) selectIndex(i int) {
	if len(b.rows) == 0 {
		return
	}
	i = max(0, min(i, len(b.rows)-1))
	b.selected, b.selectedID = b.rows[i].node.Key, b.rows[i].node.ID
}
func (rt *runtime) ensureSelectionVisible() {
	b := &rt.browser
	i := b.selectedIndex()
	if i < 0 {
		return
	}
	row := b.rows[i]
	offset := rt.offsets[rt.benchView]
	visible := max(rt.contentVisibleRows(), 1)
	if row.start < offset {
		offset = row.start
	} else if row.start >= offset+visible {
		offset = max(row.start-visible+1, 0)
	}
	rt.offsets[rt.benchView] = min(offset, max(len(b.lines)-visible, 0))
}
func (rt *runtime) toggleFold(index int) {
	b := &rt.browser
	if rt.benchView != "tree" || index < 0 || index >= len(b.rows) || len(b.rows[index].node.Children) == 0 {
		return
	}
	b.selectIndex(index)
	key := b.selected
	b.collapsed[key] = !b.collapsed[key]
	b.dirty = true
	b.ensureLayout(rt.benchView, rt.contentCols())
	rt.ensureSelectionVisible()
}

func (rt *runtime) handleBrowserKey(key uv.KeyPressEvent) bool {
	if rt.showBrowserHelp {
		if rt.scrollOverlayKey(key) {
			return true
		}
		rt.showBrowserHelp = false
		rt.draw()
		return true
	}
	if key.MatchString("b") {
		rt.openConfiguration()
		return true
	}
	if key.MatchString("P", "shift+p") {
		rt.toggleSelectedPin("tree")
		return true
	}
	if key.MatchString("p") {
		rt.toggleSelectedPin("single")
		return true
	}
	if key.MatchString("?") {
		rt.showBrowserHelp = !rt.showBrowserHelp
		rt.overlayOffset = 0
		rt.draw()
		return true
	}
	if !key.MatchString("j", "k", "up", "down", "left", "right", "space", " ", "pgup", "pgdown", "home", "end") {
		return false
	}
	b := &rt.browser
	b.ensureLayout(rt.benchView, rt.contentCols())
	i := b.selectedIndex()
	if i < 0 {
		return true
	}
	switch {
	case key.MatchString("j", "down"):
		b.selectIndex(i + 1)
	case key.MatchString("k", "up"):
		b.selectIndex(i - 1)
	case key.MatchString("home"):
		b.selectIndex(0)
	case key.MatchString("end"):
		b.selectIndex(len(b.rows) - 1)
	case key.MatchString("pgup", "pgdown"):
		step := max(rt.contentVisibleRows()-1, 1)
		if key.MatchString("pgup") {
			step = -step
		}
		target := max(0, b.rows[i].start+step)
		j := i
		if step > 0 {
			for j+1 < len(b.rows) && b.rows[j].start < target {
				j++
			}
		} else {
			for j > 0 && b.rows[j].start > target {
				j--
			}
		}
		b.selectIndex(j)
	case key.MatchString("space", " "):
		rt.toggleFold(i)
	case key.MatchString("right"):
		if rt.benchView == "tree" && len(b.rows[i].node.Children) > 0 {
			if b.collapsed[b.selected] {
				rt.toggleFold(i)
			} else {
				b.selectIndex(i + 1)
			}
		}
	case key.MatchString("left"):
		if rt.benchView == "tree" {
			if len(b.rows[i].node.Children) > 0 && !b.collapsed[b.selected] {
				rt.toggleFold(i)
			} else {
				parent := b.rows[i].parent
				for j := i - 1; j >= 0; j-- {
					if b.rows[j].node.Key == parent {
						b.selectIndex(j)
						break
					}
				}
			}
		}
	}
	rt.ensureSelectionVisible()
	rt.draw()
	return true
}

// Both viewer and configuration gestures capture an explicit add/remove intent.
// Native operations are idempotent: a same-kind race never flips the new state.
func (rt *runtime) selectedPinNode() *BrowserNode {
	if rt.configuration != nil {
		if rt.configuration.section != 0 || rt.configurationCount() == 0 {
			return nil
		}
		pins := rt.browser.snapshot.Configuration.Pins
		i := max(0, min(rt.configuration.selected[0], len(pins)-1))
		pin := pins[i]
		return &BrowserNode{ID: pin.ID, Title: pin.Title, Type: pin.Type, State: pin.State}
	}
	rt.browser.ensureLayout(rt.benchView, rt.contentCols())
	if i := rt.browser.selectedIndex(); i >= 0 {
		return rt.browser.rows[i].node
	}
	return nil
}

func explicitPin(snapshot *BrowserSnapshot, id int, mode string) bool {
	if snapshot == nil || snapshot.Configuration == nil {
		return false
	}
	for _, pin := range snapshot.Configuration.Pins {
		if pin.ID == id && pin.Mode == mode {
			return true
		}
	}
	return false
}

func (rt *runtime) toggleSelectedPin(mode string) {
	if rt.mode != "bench" || rt.form != nil || rt.reconnectRequired || rt.closing {
		return
	}
	if rt.configuration != nil && rt.configuration.section != 0 {
		rt.setNotice("Pin gestures apply only to work items in the Pins section.", 0)
		rt.draw()
		return
	}
	if rt.pinCancel != nil {
		rt.setNotice("Pin change is still in progress.", 0)
		rt.draw()
		return
	}
	snapshot := rt.browser.snapshot
	if snapshot == nil {
		rt.setNotice("Wait for the Bench to load before changing pins.", 0)
		rt.draw()
		return
	}
	node := rt.selectedPinNode()
	if node == nil {
		rt.setNotice("Select an item or use b Bench configuration > Pins > i to enter an ID.", 0)
		rt.draw()
		return
	}
	if node.IsSeed || node.ID <= 0 {
		rt.setNotice("Seeds cannot be pinned. Publish this seed first; no selectors were changed.", 0)
		rt.draw()
		return
	}
	if err := validateConfiguration(snapshot.Configuration); err != nil {
		rt.setError(err.Error())
		rt.draw()
		return
	}
	action := pinAction{id: node.ID, mode: mode, remove: explicitPin(snapshot, node.ID, mode), benchID: snapshot.BenchID, binding: rt.binding, settingsDigest: configurationDigest(snapshot)}
	rt.beginPinMutation(action)
}

func (p pinAction) args() []string {
	command := "track"
	if p.mode == "tree" {
		command = "track-tree"
	}
	args := []string{"workspace", command, strconv.Itoa(p.id)}
	if p.remove {
		args = []string{"workspace", "untrack", strconv.Itoa(p.id), "--mode", p.mode}
	}
	return semanticArgs(append(args, "--expect-bench", p.benchID, "--expect-settings", p.settingsDigest, "-o", "json"), p.binding)
}

func pinKind(mode string) string {
	if mode == "tree" {
		return "Subtree pin"
	}
	return "Single pin"
}

func (rt *runtime) beginPinMutation(p pinAction) {
	if rt.pinCancel != nil || rt.reconnectRequired || rt.closing {
		return
	}
	if rt.syncCancel != nil {
		rt.setNotice("Wait for Bench sync before changing pins.", 0)
		rt.draw()
		return
	}
	if !rt.matchesCapture(p.benchID, p.binding, p.settingsDigest) {
		rt.setNotice("Bench, origin or settings changed; pin action refused without retargeting.", 0)
		rt.draw()
		return
	}
	rt.form = nil
	rt.cancelQueuedViews()
	rt.cancelReviewLookup(errors.New("Review superseded by pin change"))
	rt.bench.launchGen++
	rt.cancelBenchLaunch(errors.New("pin change superseded refresh"))
	rt.pinGen++
	gen := rt.pinGen
	ctx, cancel := context.WithTimeout(rt.ctx, 30*time.Second)
	rt.pinCancel, rt.pinAction = cancel, &p
	verb := "Adding"
	if p.remove {
		verb = "Removing"
	}
	rt.setNotice(fmt.Sprintf("%s %s for #%d…", verb, strings.ToLower(pinKind(p.mode)), p.id), 0)
	rt.mutationLabel = pinKind(p.mode)
	rt.draw()
	go func() {
		err := rt.semanticAuthority(ctx, p.binding)
		if err == nil {
			_, err = nativeOutput(ctx, rt.nativePath, rt.cfg.Cwd, p.args()...)
		}
		admissionErr := rt.postSemanticAdmission(p.binding)
		rt.emit(event{kind: evPinDone, token: gen, err: err, admissionErr: admissionErr})
	}()
}

func (rt *runtime) handlePinDone(ev event) {
	if rt.closing || rt.reconnectRequired || ev.token != rt.pinGen || rt.pinCancel == nil {
		return
	}
	rt.pinCancel()
	rt.pinCancel = nil
	p := rt.pinAction
	rt.pinAction = nil
	if ev.admissionErr != nil {
		rt.stopForReconnect(ev.admissionErr)
		return
	}
	if ev.err != nil {
		if bindingChanged(ev.err.Error()) {
			rt.stopForReconnect(ev.err)
			return
		}
		rt.setError(rt.mutationLabel + " change refused: " + safe(ev.err.Error()))
	} else if p != nil {
		state := "present"
		if p.remove {
			state = "absent"
		}
		rt.setNotice(fmt.Sprintf("%s for #%d ensured %s; other pin kinds retained. Refreshing native membership.", pinKind(p.mode), p.id, state), 0)
	} else {
		rt.setNotice("Local Bench settings updated; refreshing membership.", 0)
	}
	// A refusal can mean the captured Bench or settings changed before admission.
	// Fetch native truth on either outcome without retargeting the mutation.
	rt.beginBenchRefresh(nil, true)
	rt.draw()
}

func (rt *runtime) cancelPins() {
	rt.pinGen++
	if rt.pinCancel != nil {
		rt.pinCancel()
		rt.pinCancel = nil
	}
	rt.pinAction = nil
	rt.leaveConfiguration()
}

func (rt *runtime) handleMouse(mouse uv.Mouse) {
	if rt.mode == "review" && !rt.cfg.Standalone && (mouse.Button == uv.MouseWheelUp || mouse.Button == uv.MouseWheelDown) {
		if rt.closing || rt.reconnectRequired {
			return
		}
		rt.admissionActions = append(rt.admissionActions, event{kind: evMouse, mouse: mouse})
		rt.checkAdmission(nil, false)
		return
	}
	rt.handleAdmittedMouse(mouse)
}

func (rt *runtime) handleAdmittedMouse(mouse uv.Mouse) {
	if rt.closing || rt.reconnectRequired {
		return
	}
	if rt.form != nil || rt.configuration != nil {
		rt.handleConfigurationMouse(mouse)
		return
	}
	if mouse.Button == uv.MouseWheelUp || mouse.Button == uv.MouseWheelDown {
		if rt.showBrowserHelp {
			step := 3
			if mouse.Button == uv.MouseWheelUp {
				step = -step
			}
			rt.overlayOffset = max(0, min(rt.overlayOffset+step, rt.overlayMax))
			rt.draw()
			return
		}
		key := rt.benchView
		limit := 0
		if rt.mode == "review" {
			key = "review"
			limit = maxOffset(rt.review.session, rt.contentVisibleRows())
		} else {
			rt.browser.ensureLayout(rt.benchView, rt.contentCols())
			limit = max(len(rt.browser.lines)-rt.contentVisibleRows(), 0)
		}
		step := 3
		if mouse.Button == uv.MouseWheelUp {
			step = -step
		}
		rt.offsets[key] = max(0, min(rt.offsets[key]+step, limit))
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
		switch hit.action {
		case "pin-single":
			rt.toggleSelectedPin("single")
		case "pin-tree":
			rt.toggleSelectedPin("tree")
		case "configure":
			rt.openConfiguration()
		case "help":
			rt.showBrowserHelp = !rt.showBrowserHelp
			rt.overlayOffset = 0
			rt.draw()
		case "cancel":
			rt.showBrowserHelp = false
			rt.draw()
		case "select":
			rt.browser.selectIndex(hit.row)
			rt.draw()
		case "fold":
			rt.toggleFold(hit.row)
			rt.draw()
		}
		return
	}
}
