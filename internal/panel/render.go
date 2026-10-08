package panel

import (
	"fmt"
	"os"
	"strings"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// Content uses zero-based terminal coordinates; ANSI cursor rows add one.
const contentStartRow = 3
const panelChromeRows = contentStartRow + 2

func (rt *runtime) contentSize() (int, int) {
	return rt.contentCols(), rt.contentRows()
}

func (s *session) currentDensity() string {
	if s != nil && s.density == "d" {
		return "d"
	}
	return "b"
}

func (s *session) applyResize(next size) {
	if s == nil {
		return
	}
	if next.cols < 20 {
		next.cols = 20
	}
	if next.rows < 1 {
		next.rows = 1
	}
	s.cols, s.rows = next.cols, next.rows
	if s.pty != nil {
		_ = s.pty.Resize(next.cols, next.rows)
	}
	if s.term != nil {
		s.term.Resize(next.cols, next.rows)
	}
}

func (rt *runtime) draw() {
	if rt.closing {
		return
	}
	// Review observations still require their native publication boundary.
	if rt.mode == "review" && !rt.cfg.Standalone && len(rt.admissionActions) != 0 {
		return
	}
	if rt.size.cols <= 0 || rt.size.rows <= 0 {
		rt.size = rt.panelSize()
	}
	cols, rows := max(rt.size.cols, 1), max(rt.size.rows, 1)
	contentRows := rt.contentVisibleRows()
	rt.hits = rt.hits[:0]
	var out strings.Builder
	out.Grow(cols * rows)
	out.WriteString("\x1b[0m\x1b[H\x1b[2J")
	if rt.reconnectRequired {
		out.WriteString("\x1b[3J")
	}
	out.WriteString("\x1b[1;1H\x1b[2K")
	barStyle, dividerStyle, footerStyle := rt.chromeStyles()
	drawBar(&out, rt.headerText(), cols, barStyle)
	if rows >= 3 {
		out.WriteString("\x1b[2;1H\x1b[2K")
		left, right := rt.contextHeaderText()
		drawContextBar(&out, left, right, cols, barStyle)
	}
	if rows >= panelChromeRows {
		out.WriteString("\x1b[3;1H\x1b[2K")
		drawDivider(&out, cols, dividerStyle)
	}
	truncated := false
	if rt.mode == "review" && !rt.reconnectRequired {
		sess := rt.review.session
		truncated = sess != nil && sess.term != nil && sess.term.ScrollbackLen() >= scrollbackLimit
		offset := max(0, min(rt.offsets["review"], maxOffset(sess, contentRows)))
		rt.offsets["review"] = offset
		for row := range contentRows {
			fmt.Fprintf(&out, "\x1b[%d;1H\x1b[2K", row+contentStartRow+1)
			if sess != nil && sess.term != nil && sess.hasData {
				out.WriteString(sessionLine(sess, offset+row, cols).Render())
			} else if row == 0 {
				out.WriteString(ansi.Truncate("Waiting for review output…", cols, "…"))
			}
		}
	} else if rt.reconnectRequired {
		if contentRows > 0 {
			fmt.Fprintf(&out, "\x1b[%d;1H", contentStartRow+1)
			out.WriteString(ansi.Truncate("Prior binding data unavailable. Ctrl+R explicitly reconnects.", cols, "…"))
		}
	} else {
		rt.browser.ensureLayout(rt.benchView, cols)
		if rt.form != nil {
			rt.drawConfigurationForm(&out, cols, contentRows)
		} else if rt.showBrowserHelp {
			rt.drawBrowserHelp(&out, cols, contentRows)
		} else if rt.configuration != nil {
			rt.drawConfiguration(&out, cols, contentRows)
		} else {
			rt.drawBrowserRows(&out, cols, contentRows)
		}
	}
	if rows >= 4 {
		fmt.Fprintf(&out, "\x1b[%d;1H\x1b[2K", rows-1)
		if rt.notice != "" {
			drawBar(&out, safe(rt.notice), cols, "\x1b[48;2;27;37;52m\x1b[38;2;255;190;105m")
		} else {
			drawDivider(&out, cols, dividerStyle)
		}
	}
	if rows >= 2 {
		fmt.Fprintf(&out, "\x1b[%d;1H\x1b[2K", rows)
		drawBar(&out, rt.footerText(truncated), cols, footerStyle)
		rt.addFooterHits(cols, rows-1, truncated)
	}
	out.WriteString("\x1b[0m\x1b[?25l")
	if colorDisabled() {
		_, _ = os.Stdout.WriteString(labelSGR.ReplaceAllString(out.String(), ""))
		return
	}
	_, _ = os.Stdout.WriteString(out.String())
}

// drawBar fills the entire terminal row so the chrome stays distinct even when
// the label is short. The caller supplies trusted styles; text is sanitized by
// headerText/contextHeaderText/footerText before reaching this function.
func drawBar(out *strings.Builder, text string, width int, style string) {
	out.WriteString(style)
	clipped := ansi.Truncate(text, width, "…")
	out.WriteString(clipped)
	// Inline emphasis may have changed the background as well as the foreground.
	out.WriteString(style)
	for n := ansi.StringWidth(clipped); n < width; n++ {
		out.WriteByte(' ')
	}
	out.WriteString("\x1b[0m")
}

// Herdr context occupies at most half the row, leaving mode and worktree first.
func drawContextBar(out *strings.Builder, left, right string, width int, style string) {
	if right == "" || width < 2 {
		drawBar(out, left, width, style)
		return
	}
	right = ansi.Truncate(right, width/2, "…")
	rightWidth := ansi.StringWidth(right)
	left = ansi.Truncate(left, width-rightWidth-1, "…")
	out.WriteString(style)
	out.WriteString(left)
	out.WriteString(style)
	for n := ansi.StringWidth(left); n < width-rightWidth; n++ {
		out.WriteByte(' ')
	}
	out.WriteString(right)
	out.WriteString("\x1b[0m")
}

func drawDivider(out *strings.Builder, width int, style string) {
	out.WriteString(style)
	for range width {
		out.WriteString("─")
	}
	out.WriteString("\x1b[0m")
}

func colorDisabled() bool {
	_, noColor := os.LookupEnv("NO_COLOR")
	return noColor || os.Getenv("TERM") == "dumb"
}

func (rt *runtime) chromeStyles() (string, string, string) {
	if rt.configuration != nil || rt.form != nil {
		// Fixed foreground/background pairs remain readable in light and dark
		// terminal themes; the text label also distinguishes monochrome mode.
		return "\x1b[48;2;57;35;65m\x1b[38;2;245;227;248m", "\x1b[48;2;57;35;65m\x1b[38;2;229;163;222m", "\x1b[48;2;57;35;65m\x1b[38;2;245;227;248m"
	}
	return "\x1b[48;2;22;40;59m\x1b[38;2;177;217;239m", "\x1b[48;2;22;40;59m\x1b[38;2;57;84;106m", "\x1b[48;2;27;37;52m\x1b[38;2;152;175;195m"
}

func (rt *runtime) addFooterHits(cols, y int, truncated bool) {
	if rt.mode != "bench" || rt.reconnectRequired {
		return
	}
	type button struct{ text, action string }
	var buttons []button
	switch {
	case rt.form != nil:
		buttons = []button{{"[OK]", "config-confirm"}, {"[Cancel]", "config-cancel"}}
		if rt.form.kind == "pin" && rt.form.confirm {
			buttons = append(buttons, button{rt.manualPinButton(false), "config-pin-single"}, button{rt.manualPinButton(true), "config-pin-tree"})
		}
		if rt.form.kind == "bench" {
			label := "[Create]"
			if rt.form.remove {
				label = "[Yes, delete]"
			}
			buttons = []button{{label, "config-confirm"}, {"[Cancel]", "config-cancel"}}
		}
	case rt.showBrowserHelp:
		buttons = []button{{"[Close]", "cancel"}}
	case rt.configuration != nil:
		buttons = []button{{"[a Add]", "config-add"}, {"[d Remove]", "config-remove"}, {"[i ID]", "config-manual-id"}, {"[Esc Back]", "config-back"}}
		if rt.configuration.section == 0 {
			buttons = []button{{rt.pinButton("single"), "config-pin-single"}, {rt.pinButton("tree"), "config-pin-tree"}, {"[i ID]", "config-manual-id"}, {"[Esc Back]", "config-back"}}
		}
		if rt.configuration.section == 3 {
			buttons = []button{{"[Esc Back]", "config-back"}, {"[Enter Items]", "config-items"}}
			if rt.pinCancel == nil && rt.syncCancel == nil {
				buttons = append(buttons, button{"[n Create]", "config-add"})
				if target := rt.selectedManagedBench(); target != nil {
					label := "[Enter Select]"
					if rt.configuration.sectionsFocused {
						label = "[Select]"
					}
					buttons = append(buttons, button{label, "config-bench-select"})
					if !target.IsDefault {
						buttons = append(buttons, button{"[d Delete]", "config-remove"})
					}
				}
			}
		}
		if cols < 55 {
			buttons = append(buttons, button{"[a]", "config-add"}, button{"[d]", "config-remove"}, button{"[i]", "config-manual-id"}, button{"[Esc]", "config-back"})
		}
	default:
		buttons = []button{{rt.pinButton("single"), "pin-single"}, {rt.pinButton("tree"), "pin-tree"}, {"[b Bench]", "configure"}, {"[? Help]", "help"}, {"[b]", "configure"}, {"[?]", "help"}}
	}
	plain := ansi.Strip(rt.footerText(truncated))
	for _, button := range buttons {
		if button.text == "" {
			continue
		}
		index := strings.Index(plain, button.text)
		if index < 0 {
			continue
		}
		x := ansi.StringWidth(plain[:index])
		end := x + ansi.StringWidth(button.text)
		if end <= cols {
			rt.hits = append(rt.hits, hitTarget{x1: x, x2: end, y: y, action: button.action})
		}
	}
}

func (rt *runtime) headerText() string {
	if rt.reconnectRequired {
		return "\x1b[1;38;2;255;151;151mStopped — reconnect required\x1b[0m"
	}
	bench := rt.benchSummary
	if bench == "" {
		bench = "loading"
	}
	account := strings.TrimSpace(rt.binding.Account)
	if account == "" {
		account = strings.TrimSpace(rt.binding.IdentityName)
	}
	if account == "" {
		account = "unavailable"
	}
	connection := safe(strings.TrimSpace(rt.binding.Organization))
	if project := safe(strings.TrimSpace(rt.binding.Project)); project != "" {
		if connection != "" {
			connection += "/"
		}
		connection += project
	}
	if connection == "" {
		connection = "Connection unavailable"
	}
	// Reserve room for connection, Team and current Bench before account text.
	// Never expose storage/attachment IDs as labels.
	width := max(rt.size.cols, 1)
	labelWidth := max((width-36)/3, 1)
	connection = ansi.Truncate(connection, labelWidth, "…")
	bench = ansi.Truncate(safe(bench), labelWidth, "…")
	team := strings.TrimSpace(rt.binding.Team)
	if team == "" {
		team = strings.TrimSpace(rt.binding.EffectiveTeam)
	}
	if team == "" && rt.browser.snapshot != nil {
		team = strings.TrimSpace(rt.browser.snapshot.EffectiveTeam)
	}
	if team == "" {
		team = "not configured"
	}
	team = ansi.Truncate(safe(team), labelWidth, "…")
	return fmt.Sprintf("\x1b[1;38;2;154;218;250m%s\x1b[22;38;2;177;217;239m · Team: %s · Bench: \x1b[1m%s\x1b[22m · User: %s", connection, team, bench, safe(account))
}

func (rt *runtime) footerText(truncated bool) string {
	var bits []string
	if truncated {
		bits = append(bits, "\x1b[1;38;2;255;190;105mOUTPUT TRUNCATED — earliest lines discarded\x1b[22;38;2;152;175;195m")
	}
	if rt.notice != "" && rt.size.rows < 4 {
		bits = append(bits, "\x1b[38;2;255;190;105m"+safe(rt.notice)+"\x1b[38;2;152;175;195m")
	}
	mode := rt.benchView
	buttons := rt.pinButton("single") + " " + rt.pinButton("tree") + " [b Bench] [? Help]"
	if rt.size.cols < 55 {
		buttons = rt.pinButton("single") + " " + rt.pinButton("tree") + " [b] [?]"
	}
	help := buttons + " · 1 table · 2 tree · 3 review · s sync · r refresh · j/k select · ←/→/Space fold · wheel scroll · q close"
	if rt.mode == "review" {
		mode = "review"
		help = "d details · b brief · s sync · Esc/c exit review · r redraw · 1 table · 2 tree · 3 review · j/k scroll · PgUp/PgDn/Home/End · q close"
	}
	if rt.reconnectRequired {
		mode = "reconnect-required"
		help = "Ctrl+R acknowledge/reconnect · q close; sync/refresh/view/review disabled"
	}
	if rt.form != nil {
		if rt.form.kind == "bench" {
			if rt.form.remove {
				return "[Yes, delete] [Cancel] · Tab/←/→ choose · Enter chosen action · y yes · Esc/c cancel"
			}
			return "[Create] [Cancel] · Enter creates empty Bench · Esc cancels · Ctrl+C quits"
		}
		if rt.form.kind == "pin" && rt.form.confirm {
			return rt.manualPinButton(false) + " " + rt.manualPinButton(true) + " [OK] [Cancel] · p/P choose and add · Enter adds chosen kind · Esc cancel"
		}
		return "[OK] [Cancel] · Enter review/confirm · Esc cancel · Ctrl+C quit"
	}
	if rt.showBrowserHelp {
		return "[Close] · PgUp/PgDn/wheel scroll · other key returns"
	}
	if rt.configuration != nil {
		navigation := "Items: ↑/↓ · ← sections"
		if rt.configuration.sectionsFocused {
			navigation = "Sections: ↑/↓ · Enter/→ items"
		}
		if rt.configuration.section == 3 {
			buttons := ""
			if rt.configuration.sectionsFocused {
				buttons = "[Enter Items] "
			}
			if rt.pinCancel == nil && rt.syncCancel == nil {
				buttons += "[n Create]"
				if target := rt.selectedManagedBench(); target != nil {
					label := " [Enter Select]"
					if rt.configuration.sectionsFocused {
						label = " [Select]"
					}
					buttons += label
					if !target.IsDefault {
						buttons += " [d Delete]"
					}
				}
			} else {
				buttons += "Changing Bench; wait for completion"
			}
			return navigation + " · " + buttons + " [Esc Back] · Tab/1/2/3/4 sections · r refresh list"
		}
		if rt.configuration.section == 0 {
			return navigation + " · " + rt.pinButton("single") + " " + rt.pinButton("tree") + " [i ID] [Esc Back] · s sync · r refresh"
		}
		if rt.size.cols < 55 {
			return navigation + " · [a] [d] [Esc]"
		}
		return navigation + " · [a Add] [d Remove] [Esc Back] · Tab/1/2/3/4 sections · s sync · r refresh"
	}
	bits = append(bits, "\x1b[1;38;2;154;218;250m"+safe(strings.ToUpper(mode))+"\x1b[22;38;2;152;175;195m", help)
	if rt.mode == "bench" && !rt.reconnectRequired && !rt.showBrowserHelp {
		return buttons + " · " + strings.Join(bits[:len(bits)-2], " · ") + strings.TrimPrefix(help, buttons) + " · " + safe(strings.ToUpper(mode))
	}
	return strings.Join(bits, "  ·  ")
}

func (rt *runtime) pinButton(mode string) string {
	verb := "Pin"
	if node := rt.selectedPinNode(); node != nil {
		if node.IsSeed || node.ID <= 0 {
			return ""
		} else if explicitPin(rt.browser.snapshot, node.ID, mode) {
			verb = "Unpin"
		}
	}
	key, label := "p", "single"
	if mode == "tree" {
		key, label = "P", "subtree"
		if rt.size.cols < 55 {
			label = "tree"
		}
	}
	if rt.size.cols < 55 {
		state := "+"
		if verb == "Unpin" {
			state = "-"
		}
		return fmt.Sprintf("[%s %s%s]", key, state, label)
	}
	return fmt.Sprintf("[%s %s %s]", key, verb, label)
}

func (rt *runtime) manualPinButton(tree bool) string {
	if tree {
		return "[P Pin subtree]"
	}
	return "[p Pin single]"
}

func sessionLine(sess *session, index, width int) uv.Line {
	line := uv.NewLine(width)
	if sess == nil || sess.term == nil || index < 0 {
		return line
	}
	scrollback := sess.term.ScrollbackLen()
	for x := 0; x < width; {
		var cell *uv.Cell
		if index < scrollback {
			cell = sess.term.ScrollbackCellAt(x, index)
		} else {
			cell = sess.term.CellAt(x, index-scrollback)
		}
		if cell == nil {
			x++
			continue
		}
		if cell.IsZero() {
			x++
			continue
		}
		line.Set(x, cell)
		step := cell.Width
		if step < 1 {
			step = 1
		}
		x += step
	}
	return line
}

func (rt *runtime) drawBrowserRows(out *strings.Builder, cols, visible int) {
	b := &rt.browser
	offset := max(0, min(rt.offsets[rt.benchView], max(len(b.lines)-visible, 0)))
	rt.offsets[rt.benchView] = offset
	for y := range visible {
		fmt.Fprintf(out, "\x1b[%d;1H\x1b[2K", y+contentStartRow+1)
		index := offset + y
		if index >= len(b.lines) {
			if y == 0 {
				if b.snapshot == nil {
					out.WriteString(ansi.Truncate("Loading semantic Bench…", cols, "…"))
				} else {
					out.WriteString(ansi.Truncate("No displayed work items. b Bench configuration > Pins > i adds an ID.", cols, "…"))
				}
			}
			continue
		}
		line := b.lines[index]
		if line.row < 0 {
			out.WriteString("  " + ansi.Truncate(line.text, max(cols-2, 0), ""))
			continue
		}
		selected := b.rows[line.row].node.Key == b.selected
		marker := "  "
		if selected {
			out.WriteString("\x1b[1;38;2;154;218;250m")
			marker = "› "
		}
		out.WriteString(ansi.Truncate(marker, cols, ""))
		out.WriteString(ansi.Truncate(line.text, max(cols-2, 0), ""))
		out.WriteString("\x1b[0m")
		if line.disclosure >= 0 {
			x := line.disclosure + 2
			rt.hits = append(rt.hits, hitTarget{x1: x, x2: min(x+2, cols), y: y + contentStartRow, action: "fold", row: line.row})
		}
		rt.hits = append(rt.hits, hitTarget{x1: 0, x2: cols, y: y + contentStartRow, action: "select", row: line.row})
	}
}

func (rt *runtime) drawBrowserHelp(out *strings.Builder, cols, visible int) {
	lines := []string{
		"Bench browser — local selection, not twig set",
		"j/k or ↑/↓ select visible work items; PgUp/PgDn/Home/End navigate. ← collapses or selects parent; → expands or selects child; Space toggles.",
		"Click a row to select, click its disclosure to fold. Mouse wheel scrolls the viewport independently of selection.",
		"p toggles the selected item's explicit Single pin; Shift+P toggles its explicit Subtree pin. Both can coexist. Each gesture changes only that kind; other pins, inherited membership, query rules and protected work remain. Inherited-only rows gain their own pin; ancestor pins are never removed. Seeds must be published first.",
		"The footer shows Pin/Unpin for each kind from native cached explicit pins. Click those named controls for the same action as p/Shift+P. A busy pin action cannot be repeated; native origin, Bench and settings guards refuse stale captures. Membership refreshes from native truth afterward.",
		"b opens Bench Configuration with Pins / Areas / Sprints / Benches. Tab or 1/2/3/4 chooses a section. Pins uses the same p/Shift+P toggles, including uncached IDs. Areas/Sprints use a add and d remove with review; p/P never pin those entries. Esc returns to the same viewer, selection, folds and viewport.",
		"Manual IDs exist only in b Bench configuration > Pins > i. Enter validates a positive ID and opens review; p adds Single pin, Shift+P adds Subtree pin immediately. Enter adds the chosen kind. These are add intents, not toggles. Unknown IDs stay uncached/unverified until scoped sync. Text-entry q/c/p/P are text; Esc cancels input first.",
		"Benches uses ↑/↓ to choose sections while section labels have focus; Enter/→ focuses the list, then arrows/j/k choose a Bench and Enter selects it. n creates an empty named Bench and highlights its native returned name after refresh without selecting it; press Enter on the list to select explicitly. Names retain spaces and Unicode. Pending list reads coalesce; the last admitted list stays usable for exact guarded actions while refreshing. A failed same-origin read remains visible with a stale-list warning; r retries the list only. Every deletion, even an empty Bench, shows its exact name, saved pins and queries, then Yes/Cancel. Default is protected. Staged work survives; deleting the current Bench falls back to default. A stale target or contents digest requires a fresh list and new confirmation; nothing is retried silently. Click the named controls for the same actions. Switching resets old item selection, folds and viewport while keeping configuration open.",
		"s syncs only this Bench; r refreshes cached membership. 3 opens Review; review never pins or applies. Ctrl+R reconnects after an origin change. Ctrl+C always quits.",
	}
	rt.drawOverlay(out, cols, visible, lines, []struct{ text, action string }{{"[Close help]", "cancel"}})
}

func (rt *runtime) drawOverlay(out *strings.Builder, cols, visible int, paragraphs []string, buttons []struct{ text, action string }) {
	var lines []string
	for _, paragraph := range paragraphs {
		lines = append(lines, strings.Split(ansi.Hardwrap(paragraph, max(cols, 1), true), "\n")...)
	}
	textRows := max(visible-len(buttons), 0)
	rt.overlayMax = max(len(lines)-max(textRows, 1), 0)
	rt.overlayOffset = max(0, min(rt.overlayOffset, rt.overlayMax))
	for y := range min(textRows, len(lines)-rt.overlayOffset) {
		fmt.Fprintf(out, "\x1b[%d;1H\x1b[2K", y+contentStartRow+1)
		out.WriteString(ansi.Truncate(lines[rt.overlayOffset+y], cols, ""))
	}
	for i, button := range buttons {
		y := textRows + i
		if y >= visible {
			break
		}
		fmt.Fprintf(out, "\x1b[%d;1H\x1b[2K\x1b[1;38;2;154;218;250m", y+contentStartRow+1)
		out.WriteString(ansi.Truncate(button.text, cols, "…"))
		out.WriteString("\x1b[0m")
		rt.hits = append(rt.hits, hitTarget{x1: 0, x2: cols, y: y + contentStartRow, action: button.action})
	}
}
