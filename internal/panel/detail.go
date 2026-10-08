package panel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// DetailDocument is the native rich presentation, read from cache unless the
// viewer explicitly requests sync. Only SGR styling and line breaks are admitted.
type DetailDocument struct {
	Version    int    `json:"version"`
	BenchID    string `json:"benchId"`
	BenchName  string `json:"benchName"`
	BindingID  string `json:"bindingId"`
	IdentityID string `json:"identityId"`
	WorkItemID int    `json:"workItemId"`
	Title      string `json:"title"`
	ANSI       string `json:"ansi"`
}

type detailAction uint8

const (
	detailRead detailAction = iota
	detailRefresh
	detailSync
	detailReflow
)

type detailState struct {
	active      bool
	id          int
	key         string
	view        string
	benchOffset int
	benchID     string
	binding     HostBinding
	gen         uint64
	cancel      context.CancelFunc
	loading     bool
	action      detailAction
	err         error
	document    *DetailDocument
	lines       []string
	cols        int
	requestCols int
	offset      int
}

func parseDetail(output []byte, binding HostBinding, benchID string, id int) (*DetailDocument, error) {
	var document DetailDocument
	if err := json.Unmarshal(output, &document); err != nil {
		return nil, fmt.Errorf("invalid native detail JSON: %w", err)
	}
	if document.Version != 1 || document.BenchID == "" || document.BindingID == "" || document.IdentityID == "" || document.WorkItemID == 0 {
		return nil, errors.New("unsupported native detail capability; install a matching twig-bench-native or upgrade Twig")
	}
	if document.BenchID != benchID || document.WorkItemID != id {
		return nil, errors.New("native detail returned a different captured Bench or work item")
	}
	if document.BindingID != binding.BindingID || document.IdentityID != binding.IdentityID {
		return nil, errors.New("binding-changed/reconnect: native detail origin changed")
	}
	document.ANSI = strings.ReplaceAll(strings.ReplaceAll(document.ANSI, "\r\n", "\n"), "\r", "\n")
	if !safeDetailANSI(document.ANSI) {
		return nil, errors.New("native detail contained unsupported terminal controls; refusing unsafe presentation")
	}
	return &document, nil
}

func safeDetailANSI(text string) bool {
	for len(text) > 0 {
		if text[0] == 0x1b {
			loc := labelSGR.FindStringIndex(text)
			if loc == nil || loc[0] != 0 {
				return false
			}
			text = text[loc[1]:]
			continue
		}
		next := strings.IndexByte(text, 0x1b)
		if next < 0 {
			next = len(text)
		}
		for _, ch := range text[:next] {
			if (ch < 32 && ch != '\n') || (ch >= 0x7f && ch <= 0x9f) {
				return false
			}
		}
		text = text[next:]
	}
	return true
}

func (rt *runtime) openDetail() {
	if rt.closing || rt.reconnectRequired || rt.mode != "bench" || rt.form != nil || rt.configuration != nil || rt.showBrowserHelp || rt.browser.snapshot == nil {
		return
	}
	if rt.pinCancel != nil || rt.syncCancel != nil {
		rt.setNotice("Wait for the Bench change or sync to finish before opening detail.", 0)
		rt.draw()
		return
	}
	rt.browser.ensureLayout(rt.benchView, rt.contentCols())
	index := rt.browser.selectedIndex()
	if index < 0 {
		return
	}
	node, snapshot := rt.browser.rows[index].node, rt.browser.snapshot
	rt.cancelQueuedViews()
	rt.cancelReviewLookup(errors.New("Detail superseded Review selection"))
	rt.bench.launchGen++
	rt.cancelBenchLaunch(errors.New("Detail superseded Bench refresh"))
	rt.cancelDetail()
	rt.detail.active = true
	rt.detail.id = node.ID
	rt.detail.key, rt.detail.view = node.Key, rt.benchView
	rt.detail.benchOffset = rt.offsets[rt.benchView]
	rt.detail.offset = rt.detail.benchOffset
	if after, _ := rt.detailAnchor(rt.contentCols()); rt.detail.offset >= after {
		rt.detail.offset += rt.detailLineCount()
	}
	rt.detail.benchID = snapshot.BenchID
	rt.detail.binding = rt.binding
	rt.requestDetail()
	rt.draw()
}

func (rt *runtime) requestDetail() {
	action := detailRead
	if rt.detail.document != nil || rt.detail.err != nil {
		action = detailRefresh
	}
	rt.requestDetailAction(action)
}

func (rt *runtime) requestDetailSync() {
	rt.requestDetailAction(detailSync)
}

func (rt *runtime) detailActionBusy() bool {
	return rt.detail.active && rt.detail.loading
}

func (rt *runtime) requestDetailAction(action detailAction) {
	detail := &rt.detail
	if !detail.active || rt.closing || rt.reconnectRequired {
		return
	}
	rt.reconcileDetail()
	if !detail.active || rt.detailActionBusy() {
		// Gestures during a read are refused, not queued. In particular, resize
		// or R must never cancel or replay an admitted network sync.
		return
	}
	if action == detailSync && detail.id < 0 {
		detail.action = action
		detail.err = errors.New("unpublished seeds have no remote item to sync; R refreshes their cached detail")
		return
	}
	detail.gen++
	gen, binding, benchID, id, cols := detail.gen, detail.binding, detail.benchID, detail.id, rt.detailWidth(rt.contentCols())
	ctx, cancel := context.WithTimeout(rt.ctx, 30*time.Second)
	detail.cancel, detail.loading, detail.err = cancel, true, nil
	detail.action, detail.requestCols = action, cols
	args := []string{"bench", "detail", strconv.Itoa(id), "--width", strconv.Itoa(cols), "-o", "json", "--expect-bench", benchID}
	if action == detailSync {
		args = append(args, "--sync")
	}
	go func() {
		err := rt.semanticAuthority(ctx, binding)
		var document *DetailDocument
		if err == nil {
			var output []byte
			output, err = nativeOutput(ctx, rt.nativePath, rt.cfg.Cwd, semanticArgs(args, binding)...)
			if err == nil {
				document, err = parseDetail(output, binding, benchID, id)
			}
		}
		admissionErr := rt.postSemanticAdmission(binding)
		rt.emit(event{kind: evDetailLoaded, token: gen, detail: document, err: err, admissionErr: admissionErr})
	}()
}

func (rt *runtime) handleDetailSync() bool {
	if !rt.detail.active {
		return false
	}
	rt.requestDetailSync()
	rt.draw()
	return true
}

func (rt *runtime) handleDetailRefresh() bool {
	if !rt.detail.active {
		return false
	}
	rt.requestDetail()
	rt.draw()
	return true
}

// cancelDetail invalidates callbacks and erases the prior actor's content. It
// deliberately does not touch tree/table selection, folds or viewport offsets.
func (rt *runtime) cancelDetail() {
	gen := rt.detail.gen + 1
	if rt.detail.cancel != nil {
		rt.detail.cancel()
	}
	rt.detail = detailState{gen: gen}
}

func (rt *runtime) closeDetail() {
	if rt.detail.active {
		rt.offsets[rt.detail.view] = rt.detail.benchOffset
	}
	rt.cancelDetail()
	rt.draw()
}

func (rt *runtime) handleDetailLoaded(ev event) {
	detail := &rt.detail
	if rt.closing || rt.reconnectRequired || !detail.active || ev.token != detail.gen {
		return
	}
	if !sameManagementOrigin(detail.binding, rt.binding) || rt.browser.snapshot == nil || rt.browser.snapshot.BenchID != detail.benchID {
		rt.cancelDetail()
		rt.draw()
		return
	}
	if ev.admissionErr != nil {
		rt.stopForReconnect(ev.admissionErr)
		return
	}
	if detail.cancel != nil {
		detail.cancel()
		detail.cancel = nil
	}
	detail.loading = false
	if ev.err != nil {
		detail.err = ev.err
		rt.draw()
		return
	}
	if ev.detail == nil || ev.detail.Version != 1 || ev.detail.WorkItemID != detail.id || ev.detail.BenchID != detail.benchID || ev.detail.BindingID != detail.binding.BindingID || ev.detail.IdentityID != detail.binding.IdentityID || !safeDetailANSI(ev.detail.ANSI) {
		detail.err = errors.New("native detail did not match the captured item/origin or contained unsafe controls")
		rt.draw()
		return
	}
	detail.document = ev.detail
	detail.cols = 0
	rt.layoutDetail(rt.contentCols())
	rt.draw()
}

func (rt *runtime) layoutDetail(cols int) {
	detail := &rt.detail
	after, _ := rt.detailAnchor(cols)
	cols = rt.detailWidth(cols)
	oldCount := rt.detailLineCount()
	if detail.cols != cols && detail.document != nil {
		detail.lines = wrappedLabels(strings.TrimRight(detail.document.ANSI, "\n"), cols)
		detail.cols = cols
	}
	if after >= 0 && detail.offset >= after+oldCount {
		detail.offset += rt.detailLineCount() - oldCount
	}
	detail.offset = max(0, min(detail.offset, rt.detailMaxOffset()))
}

// resizeDetail reflows the captured item, never whichever row is now selected.
// Immediate ANSI wrapping keeps all values visible. A busy read is left alone;
// otherwise native cache-only layout is regenerated at the new width.
func (rt *runtime) resizeDetail() {
	if !rt.detail.active {
		return
	}
	cols := rt.detailWidth(rt.contentCols())
	rt.layoutDetail(rt.contentCols())
	rt.detail.offset = max(0, min(rt.detail.offset, rt.detailMaxOffset()))
	if rt.detail.requestCols != cols {
		rt.requestDetailAction(detailReflow)
	}
}

// detailAnchor identifies the captured occurrence, not a subsequently changed
// selection. Its end includes every wrapped physical line of the Bench row.
func (rt *runtime) detailAnchor(cols int) (after, indent int) {
	rt.browser.ensureLayout(rt.benchView, cols)
	for _, row := range rt.browser.rows {
		if row.node.Key != rt.detail.key {
			continue
		}
		indent = 4
		if rt.benchView == "tree" {
			indent += min(row.depth*2, max(cols/3, 0))
		} else if cols >= 40 {
			indent = 2
		}
		return row.end, min(indent, max(cols-1, 0))
	}
	return -1, 0
}

func (rt *runtime) detailWidth(cols int) int {
	_, indent := rt.detailAnchor(cols)
	return max(cols-indent, 1)
}

func (rt *runtime) detailBodyRows() int {
	return max(rt.contentVisibleRows(), 1)
}

func (rt *runtime) detailLineCount() int {
	return max(len(rt.detail.lines), 1)
}

func (rt *runtime) detailMaxOffset() int {
	return max(len(rt.browser.lines)+rt.detailLineCount()-rt.detailBodyRows(), 0)
}

func (rt *runtime) scrollDetail(delta int) {
	if !rt.detail.active {
		return
	}
	rt.layoutDetail(rt.contentCols())
	rt.detail.offset = max(0, min(rt.detail.offset+delta, rt.detailMaxOffset()))
	rt.draw()
}

func (rt *runtime) handleDetailKey(key uv.KeyPressEvent) bool {
	if !rt.detail.active {
		return false
	}
	switch {
	case key.MatchString("enter", "esc"):
		rt.closeDetail()
	case key.MatchString("s", "S", "shift+s"):
		rt.handleDetailSync()
	case key.MatchString("r", "R", "shift+r"):
		rt.handleDetailRefresh()
	case key.MatchString("j", "down"):
		rt.scrollDetail(1)
	case key.MatchString("k", "up"):
		rt.scrollDetail(-1)
	case key.MatchString("pgdown", "space", " "):
		rt.scrollDetail(max(rt.detailBodyRows()-1, 1))
	case key.MatchString("pgup"):
		rt.scrollDetail(-max(rt.detailBodyRows()-1, 1))
	case key.MatchString("home"):
		rt.scrollDetail(-rt.detail.offset)
	case key.MatchString("end"):
		rt.scrollDetail(rt.detailMaxOffset())
	}
	// Selection and folds stay fixed while keys scroll the composed surface.
	return true
}

func (rt *runtime) handleDetailMouse(mouse uv.Mouse) bool {
	if !rt.detail.active {
		return false
	}
	switch mouse.Button {
	case uv.MouseWheelDown:
		rt.scrollDetail(3)
	case uv.MouseWheelUp:
		rt.scrollDetail(-3)
	}
	return true
}

func (rt *runtime) reconcileDetail() {
	if !rt.detail.active {
		return
	}
	if rt.browser.snapshot == nil || rt.browser.snapshot.BenchID != rt.detail.benchID || rt.benchView != rt.detail.view || !sameManagementOrigin(rt.detail.binding, rt.binding) {
		rt.cancelDetail()
		return
	}
	if after, _ := rt.detailAnchor(rt.contentCols()); after < 0 {
		rt.cancelDetail()
	}
}

func (rt *runtime) detailPosition() string {
	total := len(rt.browser.lines) + rt.detailLineCount()
	end := min(rt.detail.offset+rt.detailBodyRows(), total)
	return fmt.Sprintf("Read-only Bench · lines %d–%d / %d", min(rt.detail.offset+1, end), end, total)
}

func (rt *runtime) detailActionLabel() string {
	detail := &rt.detail
	action, busy, ready := "Cached read", "Reading cached detail…", "Cached detail"
	switch detail.action {
	case detailRefresh:
		action, busy, ready = "Cache refresh", "Refreshing cached detail…", "Cache refreshed"
	case detailSync:
		action, busy, ready = "Item sync", fmt.Sprintf("Syncing #%d and links…", detail.id), "Item and links synced"
	case detailReflow:
		action, busy, ready = "Cached reflow", "Reflowing cached detail…", "Cached detail reflowed"
	}
	if detail.loading {
		return busy
	}
	if detail.err != nil {
		return action + " failed: " + safe(detail.err.Error())
	}
	return ready
}

func (rt *runtime) drawDetail(out *strings.Builder, cols, rows int) {
	rt.reconcileDetail()
	if !rt.detail.active {
		rt.drawBrowserRows(out, cols, rows)
		return
	}
	rt.layoutDetail(cols)
	after, indent := rt.detailAnchor(cols)
	detail := &rt.detail
	prefix := strings.Repeat(" ", indent)
	count := rt.detailLineCount()
	for y := range rows {
		fmt.Fprintf(out, "\x1b[%d;1H\x1b[2K", y+contentStartRow+1)
		index := detail.offset + y
		if index >= after && index < after+count {
			out.WriteString(prefix)
			if detail.document != nil && index-after < len(detail.lines) {
				out.WriteString(detail.lines[index-after])
			} else {
				out.WriteString(ansi.Truncate(rt.detailActionLabel(), max(cols-indent, 1), "…"))
			}
			out.WriteString("\x1b[0m")
			continue
		}
		if index >= after+count {
			index -= count
		}
		if index < len(rt.browser.lines) {
			rt.drawBrowserLine(out, cols, y, rt.browser.lines[index], false)
		}
	}
}
