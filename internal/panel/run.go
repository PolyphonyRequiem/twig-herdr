package panel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	pty "github.com/aymanbagabas/go-pty"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/term"
	vt "github.com/charmbracelet/x/vt"
)

const (
	scrollbackLimit  = 10_000
	noticeDuration   = 1200 * time.Millisecond
	benchRefreshRate = 3 * time.Second
	resizePollRate   = 100 * time.Millisecond
	reviewPrompt     = "Review only — Details / Back / Cancel: "
)

type size struct {
	cols int
	rows int
}

type benchSummary struct {
	Current string `json:"current"`
}

type runtime struct {
	cfg            Config
	latestProposal func(context.Context, string) (proposalCandidate, error)

	ctx    context.Context
	cancel context.CancelFunc

	events chan event
	done   chan struct{}

	rawState *term.State
	closing  bool
	closed   bool

	size size

	mode       string
	benchView  string
	reviewFile string
	notice     string
	noticeSeq  uint64

	benchSummary string
	offsets      map[string]int

	bench struct {
		active       *session
		pending      *session
		launchCancel context.CancelFunc
		launchGen    uint64
		pendingReply chan Result
	}

	review struct {
		session           *session
		restoreBench      *session
		launchCancel      context.CancelFunc
		launchGen         uint64
		pendingReply      chan Result
		lookupCancel      context.CancelFunc
		lookupReply       chan Result
		lookupGen         uint64
		pendingResize     size
		hasPendingResize  bool
		pendingDensity    string
		hasPendingDensity bool
	}
}

type session struct {
	gen  uint64
	kind string

	view string
	file string

	cols int
	rows int

	pty  pty.Pty
	cmd  *pty.Cmd
	term *vt.Emulator

	cancel   context.CancelFunc
	readDone chan struct{}
	started  chan struct{}

	ready   bool
	hasData bool
	exited  bool

	promptTail string
	density    string

	pendingDensity    string
	pendingResize     size
	hasPendingResize  bool
	hasPendingDensity bool
}

type eventKind int

const (
	evRequest eventKind = iota
	evKey
	evResize
	evBenchTick
	evSignal
	evSessionStarted
	evSessionData
	evSessionExit
	evLaunchFailed
	evLatestResolved
	evNoticeExpire
)

type event struct {
	kind eventKind

	req Request
	key uv.KeyPressEvent

	size size

	session     *session
	sessionKind string
	summary     string
	data        []byte
	err         error
	token       uint64
	candidate   proposalCandidate
}

// Run drives the native panel terminal UI.
func Run(cfg Config, requests <-chan Request) error {
	cfg.Cwd = strings.TrimSpace(cfg.Cwd)
	if cfg.Cwd == "" {
		return errors.New("missing source cwd")
	}
	absCwd, err := filepath.Abs(cfg.Cwd)
	if err != nil {
		return fmt.Errorf("resolve source cwd: %w", err)
	}
	info, err := os.Stat(absCwd)
	if err != nil {
		return fmt.Errorf("validate source cwd: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("source cwd is not a directory: %s", absCwd)
	}
	cfg.Cwd = absCwd
	if cfg.InitialView != "table" {
		cfg.InitialView = "tree"
	}

	rt := newRuntime(cfg)
	return rt.run(requests)
}

func newRuntime(cfg Config) *runtime {
	return &runtime{
		cfg:            cfg,
		latestProposal: latestProposal,
		mode:           "bench",
		benchView:      cfg.InitialView,
		offsets:        map[string]int{"table": 0, "tree": 0, "review": 0},
		benchSummary:   "loading",
		done:           make(chan struct{}),
		events:         make(chan event, 128),
	}
}

func (rt *runtime) run(requests <-chan Request) error {
	if !term.IsTerminal(os.Stdin.Fd()) || !term.IsTerminal(os.Stdout.Fd()) {
		return errors.New("the Twig bench panel requires a terminal")
	}

	rawState, err := term.MakeRaw(os.Stdin.Fd())
	if err != nil {
		return fmt.Errorf("enter raw terminal mode: %w", err)
	}
	rt.rawState = rawState

	ctx, cancel := context.WithCancel(context.Background())
	rt.ctx = ctx
	rt.cancel = cancel

	defer rt.shutdown()

	fmt.Fprint(os.Stdout, "\x1b[?1049h\x1b[?25l")

	rt.size = rt.panelSize()
	rt.draw()

	go rt.watchRequests(requests)
	go rt.watchInput()
	go rt.watchResize()
	go rt.watchBenchTimer()
	go rt.watchInterrupts()

	rt.beginBenchRefresh(nil, true)

	for {
		select {
		case ev := <-rt.events:
			switch ev.kind {
			case evRequest:
				rt.handleRequest(ev.req)
			case evKey:
				rt.handleKey(ev.key)
			case evResize:
				rt.handleResize(ev.size)
			case evBenchTick:
				if rt.mode == "bench" && !rt.closing {
					rt.beginBenchRefresh(nil, false)
				}
			case evSignal:
				rt.shutdown()
			case evSessionStarted:
				rt.handleSessionStarted(ev.session, ev.summary)
			case evSessionData:
				rt.handleSessionData(ev.session, ev.data)
			case evSessionExit:
				rt.handleSessionExit(ev.session, ev.err)
			case evLaunchFailed:
				rt.handleLaunchFailed(ev.sessionKind, ev.token, ev.err)
			case evLatestResolved:
				rt.handleLatestResolved(ev)
			case evNoticeExpire:
				if rt.noticeSeq == ev.token {
					rt.notice = ""
					rt.draw()
				}
			}
		case <-rt.ctx.Done():
			return nil
		}
	}
}

func (rt *runtime) watchRequests(requests <-chan Request) {
	if requests == nil {
		return
	}
	for {
		select {
		case <-rt.ctx.Done():
			return
		case req, ok := <-requests:
			if !ok {
				rt.emit(event{kind: evSignal})
				return
			}
			rt.emit(event{kind: evRequest, req: req})
		}
	}
}

func (rt *runtime) watchInput() {
	reader := uv.NewTerminalReader(os.Stdin, os.Getenv("TERM"))
	events := make(chan uv.Event, 32)
	go func() {
		defer close(events)
		_ = reader.StreamEvents(rt.ctx, events)
	}()
	for {
		select {
		case <-rt.ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			for _, wrapped := range flattenEvent(ev) {
				rt.emit(wrapped)
			}
		}
	}
}

func flattenEvent(ev uv.Event) []event {
	switch k := ev.(type) {
	case uv.KeyPressEvent:
		return []event{{kind: evKey, key: k}}
	case uv.MultiEvent:
		var out []event
		for _, sub := range k {
			out = append(out, flattenEvent(sub)...)
		}
		return out
	default:
		return nil
	}
}

func (rt *runtime) watchResize() {
	ticker := time.NewTicker(resizePollRate)
	previous := rt.panelSize()
	for {
		select {
		case <-rt.ctx.Done():
			return
		case <-ticker.C:
			current := rt.panelSize()
			if current == previous {
				continue
			}
			previous = current
			rt.emit(event{kind: evResize, size: current})
		}
	}
}

func (rt *runtime) watchBenchTimer() {
	ticker := time.NewTicker(benchRefreshRate)
	defer ticker.Stop()
	for {
		select {
		case <-rt.ctx.Done():
			return
		case <-ticker.C:
			rt.emit(event{kind: evBenchTick})
		}
	}
}

func (rt *runtime) watchInterrupts() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt)
	defer signal.Stop(ch)
	for {
		select {
		case <-rt.ctx.Done():
			return
		case <-ch:
			rt.emit(event{kind: evSignal})
		}
	}
}

func (rt *runtime) emit(ev event) {
	select {
	case rt.events <- ev:
	case <-rt.ctx.Done():
	}
}

func (rt *runtime) handleRequest(req Request) {
	switch req.Command {
	case "ping", "status":
		rt.reply(req.Reply, Result{Snapshot: rt.snapshot()})
	case "view":
		switch req.View {
		case "tree", "table":
			rt.benchView = req.View
			if rt.mode == "review" {
				rt.beginExitReview(req.Reply)
				return
			}
			rt.cancelReviewLookup(errors.New("Review superseded by Bench view"))
			rt.beginBenchRefresh(req.Reply, true)
		case "review":
			rt.selectReview(req.Reply)
		default:
			rt.reply(req.Reply, Result{Snapshot: rt.snapshot(), Err: fmt.Errorf("unknown view: %s", req.View)})
		}
	case "review":
		if req.File == "" {
			rt.selectReview(req.Reply)
		} else {
			rt.beginReview(req.File, "", req.Reply, rt.mode == "review")
		}
	case "exit-review":
		if rt.mode == "review" {
			rt.beginExitReview(req.Reply)
			return
		}
		rt.cancelReviewLookup(errors.New("Review exited"))
		rt.reply(req.Reply, Result{Snapshot: rt.snapshot()})
	case "close":
		rt.reply(req.Reply, Result{Snapshot: rt.snapshot()})
		rt.shutdown()
	default:
		rt.reply(req.Reply, Result{Snapshot: rt.snapshot(), Err: fmt.Errorf("unknown command: %s", req.Command)})
	}
}

func (rt *runtime) handleKey(key uv.KeyPressEvent) {
	if key.MatchString("q", "ctrl+c") {
		rt.shutdown()
		return
	}

	if rt.mode == "review" {
		if key.MatchString("esc", "c") {
			rt.beginExitReview(nil)
			return
		}
		if key.MatchString("d") {
			rt.queueReviewDensity("d")
			rt.draw()
			return
		}
		if key.MatchString("b") {
			rt.queueReviewDensity("b")
			rt.draw()
			return
		}
		if key.MatchString("r") {
			rt.setNotice("Review redraw only; no new preview was fetched.", noticeDuration)
			rt.draw()
			return
		}
		if key.MatchString("1") {
			rt.benchView = "table"
			rt.beginExitReview(nil)
			return
		}
		if key.MatchString("2") {
			rt.benchView = "tree"
			rt.beginExitReview(nil)
			return
		}
		if key.MatchString("3") {
			return
		}
		if key.MatchString("home") {
			rt.offsets["review"] = 0
			rt.draw()
			return
		}
		if key.MatchString("end") {
			rt.offsets["review"] = rt.maxOffsetForCurrentSession()
			rt.draw()
			return
		}
		rt.handleScroll("review", key)
		return
	}

	if key.MatchString("1") {
		rt.benchView = "table"
		rt.cancelReviewLookup(errors.New("Review superseded by Table view"))
		rt.beginBenchRefresh(nil, true)
		return
	}
	if key.MatchString("2") {
		rt.benchView = "tree"
		rt.cancelReviewLookup(errors.New("Review superseded by Tree view"))
		rt.beginBenchRefresh(nil, true)
		return
	}
	if key.MatchString("3") {
		rt.selectReview(nil)
		return
	}
	if key.MatchString("r") {
		rt.beginBenchRefresh(nil, true)
		return
	}
	if key.MatchString("home") {
		rt.offsets[rt.benchView] = 0
		rt.draw()
		return
	}
	if key.MatchString("end") {
		rt.offsets[rt.benchView] = rt.maxOffsetForCurrentSession()
		rt.draw()
		return
	}
	rt.handleScroll(rt.benchView, key)
}

func (rt *runtime) handleScroll(key string, ev uv.KeyPressEvent) {
	offsetKey := key
	if rt.mode == "review" {
		offsetKey = "review"
	}
	step := 0
	if ev.MatchString("down", "j") {
		step = 1
	} else if ev.MatchString("up", "k") {
		step = -1
	} else if ev.MatchString("pgdown") {
		step = rt.contentRows() - 1
	} else if ev.MatchString("pgup") {
		step = -(rt.contentRows() - 1)
	}
	if step == 0 {
		return
	}
	rt.offsets[offsetKey] += step
	if rt.offsets[offsetKey] < 0 {
		rt.offsets[offsetKey] = 0
	}
	rt.draw()
}

func (rt *runtime) handleResize(next size) {
	rt.size = next
	if rt.mode == "review" {
		next = size{cols: rt.contentCols(), rows: rt.contentRows()}
		if rt.review.session == nil {
			rt.review.pendingResize = next
			rt.review.hasPendingResize = true
			rt.draw()
			return
		}
		rt.reviewResize(next)
		rt.draw()
		return
	}
	if rt.mode == "bench" {
		rt.beginBenchRefresh(nil, true)
	}
}

func (rt *runtime) reviewResize(next size) {
	s := rt.review.session
	if s == nil {
		rt.review.pendingResize = next
		rt.review.hasPendingResize = true
		return
	}
	s.density = s.currentDensity()
	if !s.ready {
		s.pendingResize = next
		s.hasPendingResize = true
		s.pendingDensity = s.density
		s.hasPendingDensity = true
		return
	}
	s.applyResize(next)
	rt.queueReviewDensity(s.density)
}

func (rt *runtime) beginBenchRefresh(reply chan Result, force bool) {
	if rt.closing || rt.mode == "review" {
		return
	}
	if !force && (rt.bench.launchCancel != nil || rt.bench.pending != nil) {
		return
	}
	rt.cancelBenchLaunch(errors.New("superseded by a newer refresh"))
	rt.cancelBenchPending(errors.New("superseded by a newer refresh"))
	rt.bench.pendingReply = reply

	gen := rt.nextBenchGen()
	rt.bench.launchGen = gen
	cols, rows := rt.contentSize()
	ctx, cancel := context.WithCancel(rt.ctx)
	rt.bench.launchCancel = cancel
	go rt.launchBench(ctx, cancel, gen, rt.benchView, cols, rows)

	if rt.bench.active == nil {
		rt.draw()
	}
}

func (rt *runtime) selectReview(reply chan Result) {
	if rt.mode == "review" {
		rt.reply(reply, Result{Snapshot: rt.snapshot()})
		return
	}
	rt.cancelReviewLookup(errors.New("newer Review selection"))
	ctx := rt.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	rt.review.lookupCancel = cancel
	rt.review.lookupReply = reply
	rt.review.lookupGen++
	gen := rt.review.lookupGen
	go func() {
		candidate, err := rt.latestProposal(lookupCtx, rt.cfg.Cwd)
		rt.emit(event{kind: evLatestResolved, candidate: candidate, err: err, token: gen})
	}()
}

func (rt *runtime) handleLatestResolved(ev event) {
	if rt.closing || ev.token != rt.review.lookupGen {
		return
	}
	if rt.review.lookupCancel != nil {
		rt.review.lookupCancel()
		rt.review.lookupCancel = nil
	}
	reply := rt.review.lookupReply
	rt.review.lookupReply = nil
	if ev.err != nil {
		rt.setError(fmt.Sprintf("Review unavailable: %s", safe(ev.err.Error())))
		rt.draw()
		rt.reply(reply, Result{Snapshot: rt.snapshot(), Err: ev.err})
		return
	}
	rt.beginReview(ev.candidate.File, ev.candidate.Digest, reply, false)
}

func (rt *runtime) beginReview(file, digest string, reply chan Result, replacing bool) {
	if rt.closing {
		rt.reply(reply, Result{Snapshot: rt.snapshot(), Err: errors.New("panel is closing")})
		return
	}
	rt.cancelReviewLookup(errors.New("explicit Review superseded latest lookup"))
	abs, err := rt.resolveFile(file)
	if err == nil && digest != "" {
		_, err = validateProposalCandidate(proposalCandidate{Found: true, File: abs, Digest: digest})
	}
	if err != nil {
		rt.setError(fmt.Sprintf("Review failed to start: %s", safe(err.Error())))
		rt.draw()
		rt.reply(reply, Result{Snapshot: rt.snapshot(), Err: err})
		return
	}

	rt.review.restoreBench = rt.bench.active
	rt.bench.launchGen++
	rt.cancelBenchLaunch(errors.New("review opened"))
	rt.cancelBenchPending(errors.New("review opened"))
	rt.cancelReviewLaunch(errors.New("review reopened"))
	rt.cancelReviewSession(errors.New("review reopened"))

	rt.bench.active = nil
	rt.bench.pending = nil
	rt.mode = "review"
	rt.reviewFile = abs
	rt.review.hasPendingResize = false
	rt.review.pendingResize = size{}
	rt.review.hasPendingDensity = false
	rt.review.pendingDensity = ""
	rt.review.pendingReply = reply

	if replacing {
		rt.setNotice(fmt.Sprintf("Replacing review with %s", safe(filepath.Base(abs))), noticeDuration)
	} else {
		rt.setNotice(fmt.Sprintf("Reviewing %s", safe(filepath.Base(abs))), noticeDuration)
	}

	gen := rt.nextReviewGen()
	rt.review.launchGen = gen
	cols, rows := rt.contentSize()
	ctx, cancel := context.WithCancel(rt.ctx)
	rt.review.launchCancel = cancel
	go rt.launchReview(ctx, cancel, gen, abs, digest, cols, rows)
	rt.draw()
}

func (rt *runtime) beginExitReview(reply chan Result) {
	if rt.closing {
		rt.reply(reply, Result{Snapshot: rt.snapshot(), Err: errors.New("panel is closing")})
		return
	}
	rt.cancelReviewLookup(errors.New("Review exited"))
	rt.review.launchGen++
	rt.cancelReviewLaunch(errors.New("returning to bench"))
	rt.cancelReviewSession(errors.New("returning to bench"))
	rt.review.hasPendingResize = false
	rt.review.pendingResize = size{}
	rt.review.hasPendingDensity = false
	rt.review.restoreBench = nil
	rt.reviewFile = ""
	rt.mode = "bench"
	rt.setNotice("Returned to bench view", 1000)
	rt.beginBenchRefresh(reply, true)
}

func (rt *runtime) launchBench(ctx context.Context, cancel context.CancelFunc, gen uint64, view string, cols, rows int) {
	summary, err := rt.loadBenchSummary(ctx)
	if err != nil {
		cancel()
		rt.emit(event{kind: evLaunchFailed, sessionKind: "bench", token: gen, err: err})
		return

	}
	sess, err := rt.startSession(ctx, cancel, gen, "bench", []string{"workspace", "--view", view}, "", cols, rows)
	if err != nil {
		cancel()
		rt.emit(event{kind: evLaunchFailed, sessionKind: "bench", token: gen, err: err})
		return
	}
	sess.view = view
	rt.emit(event{kind: evSessionStarted, session: sess, summary: summary})
}

func (rt *runtime) launchReview(ctx context.Context, cancel context.CancelFunc, gen uint64, file, digest string, cols, rows int) {
	args := reviewPreviewArgs(file, digest)
	sess, err := rt.startSession(ctx, cancel, gen, "review", args, file, cols, rows)
	if err != nil {
		cancel()
		rt.emit(event{kind: evLaunchFailed, sessionKind: "review", token: gen, err: err})
		return
	}
	rt.emit(event{kind: evSessionStarted, session: sess})
}

func (rt *runtime) startSession(ctx context.Context, cancel context.CancelFunc, gen uint64, kind string, args []string, file string, cols, rows int) (*session, error) {
	ptyHandle, err := pty.New()
	if err != nil {
		return nil, err
	}
	if err := ptyHandle.Resize(cols, rows); err != nil {
		_ = ptyHandle.Close()
		return nil, err
	}

	cmd := ptyHandle.CommandContext(ctx, "twig", args...)
	cmd.Dir = rt.cfg.Cwd
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "COLORTERM=truecolor")
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		_ = cmd.Process.Signal(os.Interrupt)
		go func(p *os.Process) {
			time.Sleep(150 * time.Millisecond)
			_ = p.Kill()
		}(cmd.Process)
		return nil
	}

	if err := cmd.Start(); err != nil {
		_ = ptyHandle.Close()
		return nil, err
	}

	sess := &session{
		gen:      gen,
		kind:     kind,
		file:     file,
		cols:     cols,
		rows:     rows,
		pty:      ptyHandle,
		cmd:      cmd,
		term:     vt.NewEmulator(cols, rows),
		density:  "b",
		readDone: make(chan struct{}),
		started:  make(chan struct{}),
		cancel: func() {
			cancel()
			_ = ptyHandle.Close()
		},
	}
	sess.term.SetScrollbackSize(scrollbackLimit)

	go rt.readSession(ctx, sess)
	go rt.waitSession(ctx, sess)

	return sess, nil
}

func (rt *runtime) readSession(ctx context.Context, sess *session) {
	defer close(sess.readDone)
	buf := make([]byte, 4096)
	for {
		n, err := sess.pty.Read(buf)
		if n > 0 {
			select {
			case <-sess.started:
			case <-ctx.Done():
				return
			}
			data := append([]byte(nil), buf[:n]...)
			rt.emit(event{kind: evSessionData, session: sess, data: data})
		}
		if err != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		default:
		}
	}
}

func (rt *runtime) waitSession(ctx context.Context, sess *session) {
	err := sess.cmd.Wait()
	select {
	case <-ctx.Done():
		return
	case <-sess.started:
	}
	select {
	case <-sess.readDone:
	case <-time.After(100 * time.Millisecond):
	}
	rt.emit(event{kind: evSessionExit, session: sess, err: err})
}

func (rt *runtime) handleSessionStarted(sess *session, summary string) {
	if sess == nil || rt.closing {
		return
	}
	switch sess.kind {
	case "bench":
		if sess.gen != rt.bench.launchGen {
			close(sess.started)
			if sess.cancel != nil {
				sess.cancel()
			}
			return
		}
		rt.bench.pending = sess
		close(sess.started)
		rt.bench.launchCancel = nil
		if summary != "" {
			rt.benchSummary = summary
		}
		rt.draw()
	case "review":
		if sess.gen != rt.review.launchGen {
			close(sess.started)
			if sess.cancel != nil {
				sess.cancel()
			}
			return
		}
		rt.review.launchCancel = nil
		rt.review.restoreBench = nil
		rt.review.session = sess
		if rt.review.hasPendingResize {
			sess.pendingResize = rt.review.pendingResize
			sess.hasPendingResize = true
			sess.pendingDensity = sess.currentDensity()
			sess.hasPendingDensity = true
			rt.review.hasPendingResize = false
			rt.review.pendingResize = size{}
		}
		if rt.review.hasPendingDensity {
			sess.pendingDensity = rt.review.pendingDensity
			sess.hasPendingDensity = true
			rt.review.hasPendingDensity = false
			rt.review.pendingDensity = ""
		}
		close(sess.started)
		rt.mode = "review"
		rt.reviewFile = sess.file
		rt.draw()
	}
}

func (rt *runtime) handleSessionData(sess *session, data []byte) {
	if sess == nil || rt.closing {
		return
	}
	if !rt.sessionMatches(sess) {
		return
	}
	if sess.term == nil {
		return
	}
	sess.hasData = true
	_, _ = sess.term.Write(data)
	if sess.kind == "review" {
		sess.promptTail += string(data)
		if len(sess.promptTail) > 512 {
			sess.promptTail = sess.promptTail[len(sess.promptTail)-512:]
		}
		if !sess.ready && strings.Contains(sess.promptTail, reviewPrompt) {
			sess.ready = true
			sess.promptTail = ""
			rt.applyReviewPending(sess)
			if sess.ready && rt.review.pendingReply != nil {
				reply := rt.review.pendingReply
				rt.review.pendingReply = nil
				rt.reply(reply, Result{Snapshot: rt.snapshot()})
			}
		}
	}
	rt.draw()
}
func (rt *runtime) handleSessionExit(sess *session, err error) {
	if sess == nil || rt.closing || sess.exited {
		return
	}
	if !rt.sessionMatches(sess) && rt.review.session != sess && rt.bench.pending != sess {
		return
	}
	sess.exited = true
	if sess.cancel != nil {
		sess.cancel()
	}
	sess.pty = nil
	sess.cmd = nil
	sess.cancel = nil
	if sess.kind == "bench" {
		if rt.bench.pending != sess {
			return
		}
		rt.bench.active = sess
		rt.bench.pending = nil
		if err != nil {
			rt.setError(fmt.Sprintf("Twig workspace exited with code %s", exitReason(err)))
		} else if strings.HasPrefix(rt.notice, "Twig bench refresh failed") || strings.HasPrefix(rt.notice, "Twig workspace exited") {
			rt.notice = ""
		}
		if rt.bench.pendingReply != nil {
			reply := rt.bench.pendingReply
			rt.bench.pendingReply = nil
			rt.reply(reply, Result{Snapshot: rt.snapshot(), Err: err})
		}
		rt.draw()
		return
	}
	if rt.review.session != sess {
		return
	}
	if rt.mode == "review" {
		if err == nil {
			err = errors.New("Review exited unexpectedly")
		}
		rt.restoreBenchAfterReviewFailure(err)
	}
}

func (rt *runtime) handleLaunchFailed(kind string, gen uint64, err error) {
	if rt.closing {
		return
	}
	if kind == "bench" {
		if gen != rt.bench.launchGen {
			return
		}
		rt.bench.launchCancel = nil
		if rt.bench.pendingReply != nil {
			reply := rt.bench.pendingReply
			rt.bench.pendingReply = nil
			rt.reply(reply, Result{Snapshot: rt.snapshot(), Err: err})
		}
		rt.setError(fmt.Sprintf("Twig bench refresh failed: %s", safe(err.Error())))
		rt.draw()
		return
	}
	if kind == "review" {
		if gen != rt.review.launchGen {
			return
		}
		rt.restoreBenchAfterReviewFailure(err)
	}
}

func (rt *runtime) restoreBenchAfterReviewFailure(err error) {
	rt.review.launchCancel = nil
	rt.review.session = nil
	rt.reviewFile = ""
	rt.mode = "bench"
	reply := rt.review.pendingReply
	rt.review.pendingReply = nil
	if rt.review.restoreBench != nil {
		rt.bench.active = rt.review.restoreBench
		rt.review.restoreBench = nil
		rt.setError(fmt.Sprintf("Review failed to start: %s", safe(err.Error())))
		rt.draw()
		rt.reply(reply, Result{Snapshot: rt.snapshot(), Err: err})
		return
	}
	rt.review.restoreBench = nil
	rt.setError(fmt.Sprintf("Review failed to start: %s", safe(err.Error())))
	rt.draw()
	rt.reply(reply, Result{Snapshot: rt.snapshot(), Err: err})
	rt.beginBenchRefresh(nil, true)
}

func (rt *runtime) applyReviewPending(sess *session) {
	if sess == nil || sess.kind != "review" {
		return
	}
	if sess.hasPendingResize {
		sess.applyResize(sess.pendingResize)
		sess.hasPendingResize = false
		sess.pendingResize = size{}
	}
	if sess.hasPendingDensity {
		density := sess.pendingDensity
		sess.hasPendingDensity = false
		sess.pendingDensity = ""
		rt.sendReviewDensity(sess, density)
	}
}

func (rt *runtime) queueReviewDensity(density string) {
	if density != "d" && density != "b" {
		return
	}
	sess := rt.review.session
	if sess == nil {
		rt.review.pendingDensity = density
		rt.review.hasPendingDensity = true
		rt.offsets["review"] = 0
		return
	}
	sess.density = density
	rt.offsets["review"] = 0
	if !sess.ready {
		sess.pendingDensity = density
		sess.hasPendingDensity = true
		return
	}
	rt.sendReviewDensity(sess, density)
}

func (rt *runtime) sendReviewDensity(sess *session, density string) {
	if sess == nil || sess.pty == nil {
		return
	}
	sess.ready = false
	sess.hasData = false
	sess.promptTail = ""
	sess.term = vt.NewEmulator(sess.cols, sess.rows)
	sess.term.SetScrollbackSize(scrollbackLimit)
	_, _ = sess.pty.Write([]byte{density[0], '\r'})
	rt.draw()
}

func (rt *runtime) sessionMatches(sess *session) bool {
	if sess == nil || sess.exited {
		return false
	}
	switch sess.kind {
	case "bench":
		if rt.bench.pending == sess {
			return sess.gen == rt.bench.launchGen
		}
		return rt.bench.pending == nil && rt.bench.launchCancel == nil && rt.bench.active == sess && sess.gen == rt.bench.launchGen
	case "review":
		return rt.review.session == sess && sess.gen == rt.review.launchGen
	default:
		return false
	}
}

func (rt *runtime) loadBenchSummary(ctx context.Context) (string, error) {
	loadCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(loadCtx, "twig", "bench", "list", "-o", "json")
	cmd.Dir = rt.cfg.Cwd
	cmd.Env = os.Environ()
	output, err := cmd.CombinedOutput()
	if err != nil {
		if loadCtx.Err() != nil {
			return "", loadCtx.Err()
		}
		return "", fmt.Errorf("twig bench list -o json: %w: %s", err, strings.TrimSpace(string(output)))
	}
	var summary benchSummary
	if err := json.Unmarshal(output, &summary); err != nil {
		return "", fmt.Errorf("parse bench summary: %w", err)
	}
	if summary.Current == "" {
		return "loading", nil
	}
	return summary.Current, nil
}

func (rt *runtime) resolveFile(file string) (string, error) {
	if strings.TrimSpace(file) == "" {
		return "", errors.New("missing review file")
	}
	abs := file
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(rt.cfg.Cwd, abs)
	}
	abs, err := filepath.Abs(abs)
	if err != nil {
		return "", fmt.Errorf("resolve review file: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("review file %q: %w", abs, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("review file is a directory: %s", abs)
	}
	return abs, nil

}
func (rt *runtime) panelSize() size {
	cols, rows, err := term.GetSize(os.Stdout.Fd())
	if err != nil || cols <= 0 || rows <= 0 {
		return size{cols: 80, rows: 24}
	}
	if cols < 20 {
		cols = 20
	}
	if rows < 3 {
		rows = 3
	}
	return size{cols: cols, rows: rows}
}

func (rt *runtime) contentRows() int {
	rows := rt.size.rows - 2
	if rows < 1 {
		rows = 1
	}
	return rows
}

func (rt *runtime) contentCols() int {
	cols := rt.size.cols
	if cols < 20 {
		cols = 20
	}
	return cols
}

func (rt *runtime) snapshot() Snapshot {
	ready := false
	if rt.mode == "review" {
		ready = rt.review.session != nil && rt.review.session.ready
	} else {
		ready = rt.bench.active != nil && rt.bench.active.view == rt.benchView
	}
	return Snapshot{
		Mode:       rt.mode,
		View:       rt.benchView,
		ReviewFile: rt.reviewFile,
		Ready:      ready,
	}
}

func (rt *runtime) setNotice(text string, ttl time.Duration) {
	rt.notice = safe(text)
	rt.noticeSeq++
	seq := rt.noticeSeq
	if ttl > 0 {
		go func() {
			time.Sleep(ttl)
			rt.emit(event{kind: evNoticeExpire, token: seq})
		}()
	}
}

func (rt *runtime) setError(text string) {
	rt.notice = safe(text)
	rt.noticeSeq++
}

func (rt *runtime) reply(ch chan Result, res Result) {
	if ch == nil {
		return
	}
	select {
	case ch <- res:
	default:
		ch <- res
	}
}

func (rt *runtime) nextBenchGen() uint64 {
	rt.bench.launchGen++
	return rt.bench.launchGen
}

func (rt *runtime) nextReviewGen() uint64 {
	rt.review.launchGen++
	return rt.review.launchGen
}

func (rt *runtime) cancelBenchLaunch(err error) {
	if rt.bench.launchCancel != nil {
		rt.bench.launchCancel()
		rt.bench.launchCancel = nil
	}
	if err != nil && rt.bench.pendingReply != nil {
		reply := rt.bench.pendingReply
		rt.bench.pendingReply = nil
		rt.reply(reply, Result{Snapshot: rt.snapshot(), Err: err})
	}
}

func (rt *runtime) cancelBenchPending(err error) {
	if rt.bench.pending != nil {
		if rt.bench.pending.cancel != nil {
			rt.bench.pending.cancel()
		}
		rt.bench.pending = nil
	}
	if err != nil && rt.bench.pendingReply != nil {
		reply := rt.bench.pendingReply
		rt.bench.pendingReply = nil
		rt.reply(reply, Result{Snapshot: rt.snapshot(), Err: err})
	}
}

func (rt *runtime) cancelReviewLookup(err error) {
	if rt.review.lookupCancel != nil {
		rt.review.lookupCancel()
		rt.review.lookupCancel = nil
		rt.review.lookupGen++
	}
	if rt.review.lookupReply != nil {
		reply := rt.review.lookupReply
		rt.review.lookupReply = nil
		rt.reply(reply, Result{Snapshot: rt.snapshot(), Err: err})
	}
}

func (rt *runtime) cancelReviewLaunch(err error) {
	if rt.review.launchCancel != nil {
		rt.review.launchCancel()
		rt.review.launchCancel = nil
	}
	if err != nil && rt.review.pendingReply != nil {
		reply := rt.review.pendingReply
		rt.review.pendingReply = nil
		rt.reply(reply, Result{Snapshot: rt.snapshot(), Err: err})
	}
}

func (rt *runtime) cancelReviewSession(err error) {
	if rt.review.session != nil {
		if rt.review.session.cancel != nil {
			rt.review.session.cancel()
		}
		rt.review.session = nil
	}
	if err != nil && rt.review.pendingReply != nil {
		reply := rt.review.pendingReply
		rt.review.pendingReply = nil
		rt.reply(reply, Result{Snapshot: rt.snapshot(), Err: err})
	}
}

func (rt *runtime) maxOffsetForCurrentSession() int {
	if rt.mode == "review" {
		return maxOffset(rt.review.session, rt.contentRows())
	}
	return maxOffset(rt.bench.active, rt.contentRows())
}

func maxOffset(sess *session, visibleRows int) int {
	if sess == nil || sess.term == nil {
		return 0
	}
	total := sess.term.Height()
	if sb := sess.term.Scrollback(); sb != nil {
		total += sb.Len()
	}
	if total <= visibleRows {
		return 0
	}
	return total - visibleRows
}

func exitReason(err error) string {
	if err == nil {
		return "0"
	}
	return safe(err.Error())
}

func safe(text string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, text)
}

func (rt *runtime) shutdown() {
	rt.onceShutdown(func() {
		rt.closing = true
		rt.cancelReviewLookup(errors.New("panel closed"))
		rt.cancelBenchLaunch(nil)
		rt.cancelBenchPending(nil)
		rt.cancelReviewLaunch(nil)
		rt.cancelReviewSession(nil)
		if rt.cancel != nil {
			rt.cancel()
		}
		if rt.rawState != nil {
			_ = term.Restore(os.Stdin.Fd(), rt.rawState)
			rt.rawState = nil
		}
		fmt.Fprint(os.Stdout, "\x1b[0m\x1b[?25h\x1b[?1049l")
		close(rt.done)
	})
}

func (rt *runtime) onceShutdown(fn func()) {
	if rt.closed {
		return
	}
	rt.closed = true
	fn()
}
