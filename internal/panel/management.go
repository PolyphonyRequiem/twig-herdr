package panel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
)

// The management snapshot is native storage authority, separate from displayed
// membership. A deletion captures the stable ID and the complete contents digest.
type benchManagement struct {
	Version        int            `json:"version"`
	CurrentBenchID string         `json:"currentBenchId"`
	Benches        []managedBench `json:"benches"`
}

type managedBench struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	IsCurrent      bool       `json:"isCurrent"`
	IsDefault      bool       `json:"isDefault"`
	ContentsDigest string     `json:"contentsDigest"`
	Pins           []BenchPin `json:"pins"`
	Queries        []string   `json:"queries"`
}

type managementState struct {
	snapshot *benchManagement
	binding  HostBinding
	cancel   context.CancelFunc
	gen      uint64
	error    string
}

type benchOperation struct {
	kind    string
	target  managedBench
	binding HostBinding
	form    *configurationForm
}

func parseManagement(output []byte) (*benchManagement, error) {
	var snapshot benchManagement
	if err := json.Unmarshal(output, &snapshot); err != nil {
		return nil, fmt.Errorf("invalid Bench management JSON: %w", err)
	}
	if snapshot.Version != 1 || snapshot.CurrentBenchID == "" || snapshot.Benches == nil {
		return nil, errors.New("unsupported Bench management: install a matching native companion with bench list --include-management v1")
	}
	ids := make(map[string]bool)
	current, defaults := 0, 0
	for _, bench := range snapshot.Benches {
		if bench.ID == "" || strings.TrimSpace(bench.Name) == "" || bench.ContentsDigest == "" || bench.Pins == nil || bench.Queries == nil || ids[bench.ID] {
			return nil, errors.New("invalid native Bench management identity or contents")
		}
		ids[bench.ID] = true
		if bench.IsCurrent {
			current++
			if bench.ID != snapshot.CurrentBenchID {
				return nil, errors.New("invalid native current Bench identity")
			}
		}
		if bench.IsDefault {
			defaults++
		}
		for _, pin := range bench.Pins {
			if pin.ID <= 0 || (pin.Mode != "single" && pin.Mode != "tree") {
				return nil, errors.New("invalid native Bench management pin")
			}
		}
	}
	if current != 1 || defaults != 1 {
		return nil, errors.New("invalid native current/default Bench management state")
	}
	return &snapshot, nil
}

func sameManagementOrigin(left, right HostBinding) bool {
	return left.Snapshot == right.Snapshot && left.BindingID == right.BindingID && left.IdentityID == right.IdentityID && sameWorkspace(left.WorktreeRoot, right.WorktreeRoot)
}

func (rt *runtime) cancelManagement() {
	rt.management.gen++
	if rt.management.cancel != nil {
		rt.management.cancel()
		rt.management.cancel = nil
	}
}

func (rt *runtime) beginManagementRefresh() {
	if rt.closing || rt.reconnectRequired || rt.pinCancel != nil || rt.syncCancel != nil || rt.configuration == nil || rt.configuration.section != 3 {
		return
	}
	rt.cancelManagement()
	gen, binding := rt.management.gen, rt.binding
	ctx, cancel := context.WithTimeout(rt.ctx, 30*time.Second)
	rt.management.cancel, rt.management.error = cancel, ""
	rt.draw()
	go func() {
		err := rt.semanticAuthority(ctx, binding)
		var snapshot *benchManagement
		if err == nil {
			var output []byte
			output, err = nativeOutput(ctx, rt.nativePath, rt.cfg.Cwd, semanticArgs([]string{"bench", "list", "--include-management", "-o", "json"}, binding)...)
			if err == nil {
				snapshot, err = parseManagement(output)
			}
		}
		admissionErr := rt.postSemanticAdmission(binding)
		rt.emit(event{kind: evManagementLoaded, token: gen, management: snapshot, binding: binding, err: err, admissionErr: admissionErr})
	}()
}

func (rt *runtime) handleManagementLoaded(ev event) {
	if rt.closing || rt.reconnectRequired || ev.token != rt.management.gen || rt.management.cancel == nil {
		return
	}
	rt.management.cancel()
	rt.management.cancel = nil
	if ev.admissionErr != nil {
		rt.stopForReconnect(ev.admissionErr)
		return
	}
	if !sameManagementOrigin(ev.binding, rt.binding) {
		rt.stopForReconnect(errors.New("binding-changed/reconnect: Bench management origin changed"))
		return
	}
	if ev.err == nil && ev.management == nil {
		ev.err = errors.New("missing native Bench management snapshot")
	}
	if ev.err != nil {
		if bindingChanged(ev.err.Error()) {
			rt.stopForReconnect(ev.err)
			return
		}
		rt.management.snapshot = nil
		rt.management.error = ev.err.Error()
	} else if ev.management != nil {
		selectedID := ""
		if selected := rt.selectedManagedBench(); selected != nil {
			selectedID = selected.ID
		}
		rt.management.snapshot, rt.management.binding = ev.management, ev.binding
		if rt.configuration != nil {
			rt.configuration.selected[3] = 0
			for i, bench := range ev.management.Benches {
				if bench.ID == selectedID || (selectedID == "" && bench.IsCurrent) {
					rt.configuration.selected[3] = i
				}
			}
		}
	}
	rt.draw()
}

func (rt *runtime) selectedManagedBench() *managedBench {
	if rt.configuration == nil || rt.configuration.section != 3 || rt.management.snapshot == nil {
		return nil
	}
	i := rt.configuration.selected[3]
	if i < 0 || i >= len(rt.management.snapshot.Benches) {
		return nil
	}
	return &rt.management.snapshot.Benches[i]
}

func (rt *runtime) openBenchDelete() {
	target := rt.selectedManagedBench()
	if target == nil || target.IsDefault || rt.pinCancel != nil || rt.management.cancel != nil {
		return
	}
	rt.form = &configurationForm{kind: "bench", remove: true, confirm: true, choice: 1, benchName: target.Name, benchID: target.ID, binding: rt.management.binding, target: *target}
	rt.overlayOffset = 0
	rt.draw()
}

func (op benchOperation) args() []string {
	args := []string{"bench", op.kind, op.target.Name}
	if op.kind != "create" {
		args = append(args, "--expect-bench", op.target.ID)
	}
	if op.kind == "delete" {
		args = append(args, "--confirm", op.target.Name, "--expect-contents", op.target.ContentsDigest)
	}
	return semanticArgs(append(args, "-o", "json"), op.binding)
}

func (rt *runtime) beginBenchOperation(op benchOperation) {
	if rt.closing || rt.reconnectRequired || rt.pinCancel != nil {
		return
	}
	if rt.syncCancel != nil {
		rt.setError("Wait for Bench sync to finish before changing benches.")
		rt.draw()
		return
	}
	if !sameManagementOrigin(op.binding, rt.binding) {
		message := "Origin changed. Cancel and reopen the form or reconnect before selecting."
		if rt.form != nil {
			rt.form.error = message
		}
		rt.setError(message)
		rt.draw()
		return
	}
	if op.kind != "create" {
		matched := false
		if rt.management.snapshot != nil && rt.management.cancel == nil && sameManagementOrigin(rt.management.binding, op.binding) {
			for _, bench := range rt.management.snapshot.Benches {
				if bench.ID == op.target.ID && bench.Name == op.target.Name && (op.kind != "delete" || (!bench.IsDefault && bench.ContentsDigest == op.target.ContentsDigest)) {
					matched = true
				}
			}
		}
		if !matched {
			rt.form = nil
			rt.setError("Bench or contents changed. Refresh the list and review a new confirmation; nothing was deleted.")
			rt.beginManagementRefresh()
			rt.draw()
			return
		}
	}
	rt.form = nil
	rt.cancelQueuedViews()
	rt.cancelReviewLookup(errors.New("Review superseded by Bench management"))
	rt.bench.launchGen++
	rt.cancelBenchLaunch(errors.New("Bench management superseded refresh"))
	rt.cancelManagement()
	rt.pinGen++
	gen := rt.pinGen
	ctx, cancel := context.WithTimeout(rt.ctx, 30*time.Second)
	rt.pinCancel, rt.benchOperation = cancel, &op
	rt.mutationLabel = "Bench"
	rt.setNotice("Changing local benches…", 0)
	rt.draw()
	go func() {
		err := rt.semanticAuthority(ctx, op.binding)
		if err == nil {
			_, err = nativeOutput(ctx, rt.nativePath, rt.cfg.Cwd, op.args()...)
		}
		admissionErr := rt.postSemanticAdmission(op.binding)
		rt.emit(event{kind: evBenchOperationDone, token: gen, err: err, admissionErr: admissionErr})
	}()
}

func (rt *runtime) selectManagedBench() {
	if target := rt.selectedManagedBench(); target != nil && rt.management.cancel == nil {
		rt.beginBenchOperation(benchOperation{kind: "switch", target: *target, binding: rt.management.binding})
	}
}

func (rt *runtime) handleBenchOperationDone(ev event) {
	if rt.closing || rt.reconnectRequired || ev.token != rt.pinGen || rt.pinCancel == nil || rt.benchOperation == nil {
		return
	}
	op := rt.benchOperation
	rt.pinCancel()
	rt.pinCancel, rt.benchOperation = nil, nil
	if ev.admissionErr != nil {
		rt.stopForReconnect(ev.admissionErr)
		return
	}
	if ev.err != nil {
		if bindingChanged(ev.err.Error()) {
			rt.stopForReconnect(ev.err)
			return
		}
		rt.setError("Bench " + op.kind + " refused: " + safe(ev.err.Error()))
		// Creation errors remain editable. Deletion errors always require a fresh
		// list and a newly opened confirmation; never retry a captured digest.
		if op.kind == "create" && rt.configuration != nil && rt.configuration.section == 3 && rt.form == nil {
			rt.form = op.form
			if rt.form != nil {
				rt.form.confirm, rt.form.error = false, ev.err.Error()
			}
		}
	} else {
		rt.setNotice("Bench "+op.kind+" completed; refreshing native truth. Staged work is preserved.", 0)
	}
	// Even refusals can mean another process switched/deleted the current Bench.
	// Keep configuration open, but let the next native read reset item state.
	if ev.err == nil && (op.kind == "switch" || (op.kind == "delete" && (op.target.IsCurrent || rt.browser.snapshot != nil && rt.browser.snapshot.BenchID == op.target.ID))) {
		rt.browser = browserModel{}
		rt.offsets["tree"], rt.offsets["table"] = 0, 0
		rt.benchSummary = "loading"
		if rt.configuration != nil {
			rt.configuration.benchID = ""
			rt.configuration.selected[0], rt.configuration.selected[1], rt.configuration.selected[2] = 0, 0, 0
		}
	}
	rt.beginManagementRefresh()
	rt.beginBenchRefresh(nil, true)
	rt.draw()
}

func (rt *runtime) handleBenchFormKey(key uv.KeyPressEvent) {
	f := rt.form
	if f.remove {
		if key.MatchString("tab", "shift+tab", "left", "right", "up", "down") {
			f.choice = 1 - f.choice
			rt.draw()
			return
		}
		if key.MatchString("enter") {
			if f.choice == 0 {
				rt.beginBenchOperation(benchOperation{kind: "delete", target: f.target, binding: f.binding})
			} else {
				rt.form = nil
				rt.draw()
			}
			return
		}
		if rt.scrollOverlayKey(key) {
			return
		}
		if key.MatchString("y") {
			rt.beginBenchOperation(benchOperation{kind: "delete", target: f.target, binding: f.binding})
		}
		return
	}
	if key.MatchString("enter") {
		// Native create owns validation (including Unicode and duplicate names).
		// Preserve exactly what the editor captured, rather than trimming it here.
		rt.beginBenchOperation(benchOperation{kind: "create", target: managedBench{Name: f.editor.text}, binding: f.binding, form: f})
	} else {
		f.editor.handle(key)
		f.error = ""
		rt.draw()
	}
}

func (rt *runtime) handleBenchConfirmMouse() {
	f := rt.form
	if f.remove {
		rt.beginBenchOperation(benchOperation{kind: "delete", target: f.target, binding: f.binding})
	} else {
		rt.handleBenchFormKey(uv.KeyPressEvent{Code: uv.KeyEnter})
	}
}

func (rt *runtime) drawBenchForm(out *strings.Builder, cols, visible int) {
	f := rt.form
	var lines []configurationLine
	if !f.remove {
		lines = []configurationLine{{text: "Create empty Bench · select it separately afterward", row: -1}, {text: "Bench name (spaces and Unicode allowed)", row: -1}, f.editor.fieldLine(), {text: "Type text · ←/→ Home/End edit · Enter creates · Esc cancels", row: -1}, {text: "[Create]", action: "confirm", row: -1}}
	} else {
		lines = []configurationLine{{text: "Delete Bench: " + safe(f.target.Name), row: -1}, {text: "Saved pin selectors:", row: -1}}
		if len(f.target.Pins) == 0 {
			lines = append(lines, configurationLine{text: "None", row: -1})
		}
		for _, pin := range f.target.Pins {
			lines = append(lines, configurationLine{text: fmt.Sprintf("#%d · %s", pin.ID, pinKind(pin.Mode)), row: -1})
		}
		lines = append(lines, configurationLine{text: "Saved queries / automatic selectors:", row: -1})
		if len(f.target.Queries) == 0 {
			lines = append(lines, configurationLine{text: "None", row: -1})
		}
		for _, query := range f.target.Queries {
			lines = append(lines, configurationLine{text: safe(query), row: -1})
		}
		lines = append(lines, configurationLine{text: "Staged work is preserved. Only this Bench and its selectors are deleted.", row: -1})
		if f.target.IsCurrent {
			lines = append(lines, configurationLine{text: "This is the current Bench. Deletion selects default and resets this viewer's item selection, folds and scroll.", row: -1})
		}
		prefix := "  "
		if f.choice == 0 {
			prefix = "› "
		}
		lines = append(lines, configurationLine{text: prefix + "[Yes, delete]", action: "confirm", row: -1})
	}
	if f.error != "" {
		lines = append(lines, configurationLine{text: "Refused: " + safe(f.error), row: -1})
	}
	prefix := "  "
	if f.remove && f.choice == 1 {
		prefix = "› "
	}
	lines = append(lines, configurationLine{text: prefix + "[Cancel]", action: "cancel", row: -1})
	rt.drawFormLines(out, cols, visible, lines)
}
