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
	if rows < 3 {
		rows = 3
	}
	contentRows := rows - 2
	if contentRows < 1 {
		contentRows = 1
	}

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
	out.WriteString(ansi.Truncate(rt.headerText(), cols, "…"))

	truncated := sess != nil && sess.term != nil && sess.term.ScrollbackLen() >= scrollbackLimit
	if sess == nil || sess.term == nil || !sess.hasData {
		out.WriteString("\x1b[2;1H\x1b[2K")
		out.WriteString(ansi.Truncate(placeholder, cols, "…"))
		for row := 1; row < contentRows; row++ {
			fmt.Fprintf(&out, "\x1b[%d;1H\x1b[2K", row+2)
		}
	} else {
		for row := 0; row < contentRows; row++ {
			fmt.Fprintf(&out, "\x1b[%d;1H\x1b[2K", row+2)
			line := sessionLine(sess, offset+row, cols)
			out.WriteString(line.Render())
		}
	}

	fmt.Fprintf(&out, "\x1b[%d;1H\x1b[2K", rows)
	out.WriteString(ansi.Truncate(rt.footerText(truncated), cols, "…"))
	out.WriteString("\x1b[0m\x1b[?25l")
	_, _ = os.Stdout.WriteString(out.String())
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
	return safe(fmt.Sprintf("%s · Bench: %s · %s", view, bench, subject))
}

func (rt *runtime) footerText(truncated bool) string {
	var bits []string
	if truncated {
		bits = append(bits, "OUTPUT TRUNCATED — earliest lines discarded")
	}
	if rt.notice != "" {
		bits = append(bits, rt.notice)
	}
	if rt.mode == "review" {
		bits = append(bits, "Mode: review", "d details · b back · Esc/c exit review · r redraw · 1 table · 2 tree · 3 review · j/k scroll · PgUp/PgDn/Home/End · q close")
	} else {
		bits = append(bits, "Mode: "+rt.benchView, "1 table · 2 tree · 3 review · j/k scroll · PgUp/PgDn/Home/End · r refresh · q close")
	}
	return safe(strings.Join(bits, " · "))
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
