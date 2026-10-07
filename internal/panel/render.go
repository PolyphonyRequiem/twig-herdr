package panel

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

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
	drawBar(&out, rt.headerText(), cols, "\x1b[48;2;22;40;59m\x1b[38;2;177;217;239m")
	if rows >= 3 {
		out.WriteString("\x1b[2;1H\x1b[2K")
		drawDivider(&out, cols, "\x1b[48;2;22;40;59m\x1b[38;2;57;84;106m")
	}
	truncated := false
	if rt.mode == "review" && !rt.reconnectRequired {
		sess := rt.review.session
		truncated = sess != nil && sess.term != nil && sess.term.ScrollbackLen() >= scrollbackLimit
		offset := max(0, min(rt.offsets["review"], maxOffset(sess, contentRows)))
		rt.offsets["review"] = offset
		for row := range contentRows {
			fmt.Fprintf(&out, "\x1b[%d;1H\x1b[2K", row+3)
			if sess != nil && sess.term != nil && sess.hasData {
				out.WriteString(sessionLine(sess, offset+row, cols).Render())
			} else if row == 0 {
				out.WriteString(ansi.Truncate("Waiting for review output…", cols, "…"))
			}
		}
	} else if rt.reconnectRequired {
		if contentRows > 0 {
			out.WriteString("\x1b[3;1H")
			out.WriteString(ansi.Truncate("Prior binding data unavailable. Ctrl+R explicitly reconnects.", cols, "…"))
		}
	} else {
		rt.browser.ensureLayout(rt.benchView, cols)
		if rt.picker != nil {
			rt.drawPicker(&out, cols, contentRows)
		} else if rt.showBrowserHelp {
			rt.drawBrowserHelp(&out, cols, contentRows)
		} else {
			rt.drawBrowserRows(&out, cols, contentRows)
		}
	}
	if rows >= 4 {
		fmt.Fprintf(&out, "\x1b[%d;1H\x1b[2K", rows-1)
		if rt.notice != "" {
			drawBar(&out, safe(rt.notice), cols, "\x1b[48;2;27;37;52m\x1b[38;2;255;190;105m")
		} else {
			drawDivider(&out, cols, "\x1b[48;2;27;37;52m\x1b[38;2;57;84;106m")
		}
	}
	if rows >= 2 {
		fmt.Fprintf(&out, "\x1b[%d;1H\x1b[2K", rows)
		drawBar(&out, rt.footerText(truncated), cols, "\x1b[48;2;27;37;52m\x1b[38;2;152;175;195m")
		if rt.mode != "review" && !rt.reconnectRequired && rt.picker == nil && !rt.showBrowserHelp {
			for _, button := range []struct{ text, action string }{{"[p Pin]", "pin"}, {"[P Unpin]", "unpin"}, {"[? Help]", "help"}} {
				if cols < 40 {
					if button.action == "pin" {
						button.text = "[p]"
					} else if button.action == "unpin" {
						button.text = "[P]"
					} else {
						button.text = "[?]"
					}
				}
				plain := ansi.Strip(rt.footerText(truncated))
				if index := strings.Index(plain, button.text); index >= 0 {
					x := ansi.StringWidth(plain[:index])
					end := x + ansi.StringWidth(button.text)
					if end <= cols {
						rt.hits = append(rt.hits, hitTarget{x1: x, x2: end, y: rows - 1, action: button.action})
					}
				}
			}
		}
		if rt.mode == "bench" && !rt.reconnectRequired && (rt.picker != nil || rt.showBrowserHelp) {
			plain := ansi.Strip(rt.footerText(truncated))
			for _, button := range []struct{ text, action string }{{"[OK]", "confirm"}, {"[Cancel]", "cancel"}, {"[Close]", "cancel"}} {
				if index := strings.Index(plain, button.text); index >= 0 {
					x := ansi.StringWidth(plain[:index])
					end := x + ansi.StringWidth(button.text)
					if end <= cols {
						rt.hits = append(rt.hits, hitTarget{x1: x, x2: end, y: rows - 1, action: button.action})
					}
				}
			}
		}
	}
	out.WriteString("\x1b[0m\x1b[?25l")
	_, _ = os.Stdout.WriteString(out.String())
}

// drawBar fills the entire terminal row so the chrome stays distinct even when
// the label is short. The caller supplies trusted styles; text is sanitized by
// headerText/footerText before reaching this function.
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

func drawDivider(out *strings.Builder, width int, style string) {
	out.WriteString(style)
	for range width {
		out.WriteString("─")
	}
	out.WriteString("\x1b[0m")
}

func (rt *runtime) headerText() string {
	if rt.picker != nil {
		verb := "Pin"
		if rt.picker.remove {
			verb = "Unpin"
		}
		return fmt.Sprintf("%s #%d · %s · Bench: %s", verb, rt.picker.node.ID, safe(rt.picker.node.Title), safe(rt.picker.benchID))
	}
	bench := rt.benchSummary
	if bench == "" {
		bench = "loading"
	}
	view := "Table"
	if rt.benchView == "tree" {
		view = "Tree"
	}
	if rt.mode == "review" {
		view = "Review (snapshot)"
	}
	if rt.reconnectRequired {
		view = "Stopped — reconnect required"
	}
	subject := rt.cfg.Cwd
	if rt.reviewFile != "" {
		subject = filepath.Base(rt.reviewFile)
	}
	return fmt.Sprintf("\x1b[1;38;2;154;218;250m%s\x1b[22;38;2;177;217;239m · Identity: %s (cached) · Bench: \x1b[1m%s\x1b[22m · Binding: %s · \x1b[2m%s\x1b[22m",
		safe(view), safe(rt.binding.IdentityID), safe(bench), safe(rt.binding.BindingID), safe(subject))
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
	buttons := "[p Pin] [P Unpin] [? Help]"
	if rt.size.cols < 40 {
		buttons = "[p] [P] [?]"
	}
	help := buttons + " · 1 table · 2 tree · 3 review · s sync · r refresh · j/k select · ←/→/Space fold · wheel scroll · q close"
	if rt.mode == "review" {
		mode = "review"
		help = "d details · b back · s sync · Esc/c exit review · r redraw · 1 table · 2 tree · 3 review · j/k scroll · PgUp/PgDn/Home/End · q close"
	}
	if rt.reconnectRequired {
		mode = "reconnect-required"
		help = "Ctrl+R acknowledge/reconnect · q close; sync/refresh/view/review disabled"
	}
	if rt.picker != nil {
		if rt.syncCancel != nil {
			return "[Cancel] · Sync in progress; pin confirmation waits"
		}
		if rt.picker.explanation != "" {
			return "[Close] · Enter/Esc close · wheel/PgUp/PgDn scroll"
		}
		return "[OK] [Cancel] · Enter confirm · ↑/↓ choose · Esc cancel · wheel/PgUp/PgDn scroll"
	}
	if rt.showBrowserHelp {
		return "[Close] · PgUp/PgDn/wheel scroll · other key returns"
	}
	bits = append(bits, "\x1b[1;38;2;154;218;250m"+safe(strings.ToUpper(mode))+"\x1b[22;38;2;152;175;195m", help)
	if rt.mode == "bench" && !rt.reconnectRequired && rt.picker == nil && !rt.showBrowserHelp {
		return buttons + " · " + strings.Join(bits[:len(bits)-2], " · ") + strings.TrimPrefix(help, buttons) + " · " + safe(strings.ToUpper(mode))
	}
	return strings.Join(bits, "  ·  ")
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
		fmt.Fprintf(out, "\x1b[%d;1H\x1b[2K", y+3)
		index := offset + y
		if index >= len(b.lines) {
			if y == 0 {
				if b.snapshot == nil {
					out.WriteString(ansi.Truncate("Loading semantic Bench…", cols, "…"))
				} else {
					out.WriteString(ansi.Truncate("This Bench has no displayed work items. Sync or add a native Bench pin.", cols, "…"))
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
			rt.hits = append(rt.hits, hitTarget{x1: x, x2: min(x+2, cols), y: y + 2, action: "fold", row: line.row})
		}
		rt.hits = append(rt.hits, hitTarget{x1: 0, x2: cols, y: y + 2, action: "select", row: line.row})
	}
}

func (rt *runtime) drawPicker(out *strings.Builder, cols, visible int) {
	p := rt.picker
	title := fmt.Sprintf("Pin %s #%d — %s", safe(p.node.Type), p.node.ID, safe(p.node.Title))
	if p.remove {
		title = fmt.Sprintf("Remove explicit pins from %s #%d — %s", safe(p.node.Type), p.node.ID, safe(p.node.Title))
	}
	var lines []string
	if p.explanation != "" {
		lines = append(lines, p.explanation)
	} else if p.remove {
		lines = append(lines, "Remove: "+strings.Join(p.node.Pins, " + ")+" explicit pin(s). Inherited membership is not removed.")
	} else {
		lines = append(lines, "Choose a local durable selector. This does not change the active Twig work item or write to ADO.")
	}
	bench := safe(p.benchName)
	if bench == "" {
		bench = safe(p.benchID)
	}
	lines = append(lines, title, "Bench: "+bench+" ("+safe(p.benchID)+")")
	// Reserve the final viewport lines for controls, even on narrow terminals.
	buttons := []struct{ text, action string }{}
	if p.explanation == "" {
		first, second := "1 Single item", "2 Whole subtree"
		if p.remove {
			first, second = "Remove explicit pins", "Keep pins (cancel)"
		}
		if p.choice == 0 {
			first = "› " + first
		} else {
			first = "  " + first
		}
		if p.choice == 1 {
			second = "› " + second
		} else {
			second = "  " + second
		}
		buttons = append(buttons, struct{ text, action string }{first, "choice0"}, struct{ text, action string }{second, "choice1"})
	}
	rt.drawOverlay(out, cols, visible, lines, buttons)
}

func (rt *runtime) drawBrowserHelp(out *strings.Builder, cols, visible int) {
	lines := []string{"Bench browser — local selection, not twig set", "j/k or ↑/↓ select visible work items; PgUp/PgDn/Home/End navigate. ← collapses or selects parent; → expands or selects child; Space toggles.", "Click a row to select, click its disclosure to fold. Mouse wheel scrolls the viewport independently of selection.", "p opens Single item / Whole subtree pin picker. Shift+P removes both explicit pin kinds after confirmation. Inherited membership and seeds cannot be unpinned here.", "1/2 switch cached Table/Tree; r fetches native membership; s pulls only this Bench and relationship-rule candidates from ADO. 3 opens the latest unresolved proposal snapshot. Review never pins, authorizes, or applies.", "Ctrl+R acknowledges connection changes. Selection and folds survive refresh, but reset on reconnect. q quits."}
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
		fmt.Fprintf(out, "\x1b[%d;1H\x1b[2K", y+3)
		out.WriteString(ansi.Truncate(lines[rt.overlayOffset+y], cols, ""))
	}
	for i, button := range buttons {
		y := textRows + i
		if y >= visible {
			break
		}
		fmt.Fprintf(out, "\x1b[%d;1H\x1b[2K\x1b[1;38;2;154;218;250m", y+3)
		out.WriteString(ansi.Truncate(button.text, cols, "…"))
		out.WriteString("\x1b[0m")
		rt.hits = append(rt.hits, hitTarget{x1: 0, x2: cols, y: y + 2, action: button.action})
	}
}
