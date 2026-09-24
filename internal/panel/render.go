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
	if rt.size.cols <= 0 || rt.size.rows <= 0 {
		rt.size = rt.panelSize()
	}

	cols, rows := rt.size.cols, rt.size.rows
	if cols < 20 {
		cols = 20
	}
	if rows < 1 {
		rows = 1
	}
	contentRows := rt.contentVisibleRows()

	var sess *session
	key := rt.benchView
	placeholder := "Loading bench output…"
	if rt.mode == "review" {
		key = "review"
		sess = rt.review.session
		placeholder = "Waiting for review output…"
	} else {
		sess = rt.bench.active
	}

	offset := rt.offsets[key]
	if offset < 0 {
		offset = 0
	}
	maxStart := maxOffset(sess, contentRows)
	if offset > maxStart {
		offset = maxStart
	}
	rt.offsets[key] = offset

	var out strings.Builder
	out.Grow(cols * rows)
	out.WriteString("\x1b[0m\x1b[H\x1b[2J")
	out.WriteString("\x1b[1;1H\x1b[2K")
	drawBar(&out, rt.headerText(), cols, "\x1b[48;2;22;40;59m\x1b[38;2;177;217;239m")

	if rows >= 3 {
		fmt.Fprintf(&out, "\x1b[%d;1H\x1b[2K", 2)
		drawDivider(&out, cols, "\x1b[48;2;22;40;59m\x1b[38;2;57;84;106m")
	}

	truncated := sess != nil && sess.term != nil && sess.term.ScrollbackLen() >= scrollbackLimit
	if contentRows > 0 {
		if sess == nil || sess.term == nil || !sess.hasData {
			fmt.Fprintf(&out, "\x1b[%d;1H\x1b[2K", 3)
			out.WriteString("\x1b[38;2;129;157;177m")
			out.WriteString(ansi.Truncate(placeholder, cols, "…"))
			out.WriteString("\x1b[0m")
			for row := 1; row < contentRows; row++ {
				fmt.Fprintf(&out, "\x1b[%d;1H\x1b[2K", row+3)
			}
		} else {
			for row := 0; row < contentRows; row++ {
				fmt.Fprintf(&out, "\x1b[%d;1H\x1b[2K", row+3)
				line := sessionLine(sess, offset+row, cols)
				out.WriteString(line.Render())
			}
		}
	}

	if rows >= 4 {
		fmt.Fprintf(&out, "\x1b[%d;1H\x1b[2K", rows-1)
		drawDivider(&out, cols, "\x1b[48;2;27;37;52m\x1b[38;2;57;84;106m")
	}
	if rows >= 2 {
		fmt.Fprintf(&out, "\x1b[%d;1H\x1b[2K", rows)
		drawBar(&out, rt.footerText(truncated), cols, "\x1b[48;2;27;37;52m\x1b[38;2;152;175;195m")
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
	subject := rt.cfg.Cwd
	if rt.reviewFile != "" {
		subject = filepath.Base(rt.reviewFile)
	}
	return fmt.Sprintf("\x1b[1;38;2;154;218;250m%s\x1b[22;38;2;177;217;239m · Bench: \x1b[1m%s\x1b[22m · \x1b[2m%s\x1b[22m",
		safe(view), safe(bench), safe(subject))
}

func (rt *runtime) footerText(truncated bool) string {
	var bits []string
	if truncated {
		bits = append(bits, "\x1b[1;38;2;255;190;105mOUTPUT TRUNCATED — earliest lines discarded\x1b[22;38;2;152;175;195m")
	}
	if rt.notice != "" {
		bits = append(bits, "\x1b[38;2;255;190;105m"+safe(rt.notice)+"\x1b[38;2;152;175;195m")
	}
	mode := rt.benchView
	help := "1 table · 2 tree · 3 review · j/k scroll · PgUp/PgDn/Home/End · r refresh · q close"
	if rt.mode == "review" {
		mode = "review"
		help = "d details · b back · Esc/c exit review · r redraw · 1 table · 2 tree · 3 review · j/k scroll · PgUp/PgDn/Home/End · q close"
	}
	bits = append(bits, "\x1b[1;38;2;154;218;250m"+safe(strings.ToUpper(mode))+"\x1b[22;38;2;152;175;195m", help)
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
