// twig-bench-tui hosts the shared Twig browser in an ordinary terminal.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/PolyphonyRequiem/twig-herdr/internal/panel"
)

const help = `Twig Bench TUI — standalone workspace and proposal browser

Usage:
  twig-bench-tui [--cwd PATH] [--view tree|table|review] [--file PATH]

Uses the current directory's Twig workspace and current bench by default.
Requires twig on PATH; does not require Herdr or any Herdr environment variables.
The persistent header shows Org/Project, Team, Bench and User on its first row;
mode, worktree and Git branch on its second. Inside Herdr, captured workspace/tab/pane
handles appear at the lower-right when all three environment handles are available.
Semantic Bench reads, pins, configuration and scoped sync prefer twig-bench-native.exe
beside this browser (twig-bench-native on Unix); otherwise Twig must support browser
v1/configuration v1 JSON, --include-browser, --expect-bench, --expect-binding,
--expect-identity, --expect-settings, --expect-contents, bench list --include-management
v1, and workspace untrack ID --mode single|tree.
Normal Twig remains connection-status and proposal-review authority.
--file opens a specific proposal; otherwise Review finds the latest unresolved proposal.
Relative proposal paths resolve against --cwd (the current directory by default).

Keys:
  1 workspace table, 2 bench tree, 3 proposal review
  j/k or up/down select work items locally (not twig set), PgUp/PgDn/Home/End navigate
  left/right collapse/expand, Space toggle; click rows/disclosures; wheel scrolls
  b Bench Configuration (Pins / Areas / Sprints / Benches); Esc returns to the viewer
  up/down sections; Enter/right items; left sections; Tab or 1/2/3/4 sections
  Areas/Sprints use a add, d remove; Enter reviews/confirms
  Benches: arrows/j/k choose, Enter selects, n creates empty, d/Delete reviews deletion
  Yes/Cancel shows exact saved pins/queries; Default is protected; staged work survives
  Current deletion selects default. Stale contents require a fresh list/reconfirmation.
  Mouse controls match keys; selecting resets item selection/folds/scroll, stays in b.
  p toggles only explicit Single pin; Shift+P only Subtree pin, in viewer or Pins
  Both coexist; each removal retains other pin kinds and inherited/query membership
  Manual IDs only in b Bench Configuration > Pins > i, including an empty Bench
  Enter validates/reviews; p adds Single pin, Shift+P adds Subtree pin immediately
  Enter adds the chosen kind. Unknown IDs stay uncached/unverified until scoped sync.
  Area Exact/Under; sprint @Current, @Current±N, or absolute iteration path
  Text fields treat q/c/s/p as text; Esc cancels the field first, Ctrl+C always quits
  Click named Pin/Unpin controls for the same p/Shift+P action; ? discovery help
  s Bench-scoped ADO pull, r refresh; seeds cannot be pinned
  d details, b brief, Esc/c leave review, Ctrl+R reconnect, q/Ctrl+C quit

Sync pulls only Bench members and saved automatic/relationship-rule candidates;
it never substitutes shared workspace area/sprint settings or flushes pending edits.
No sprints disables automatic membership; pins/seeds/pending guards remain additive.
An open review keeps its captured snapshot, never pins, authorizes,
or applies. Fresh observations and operations check the admitted native origin;
local selection/folding uses retained snapshots without waiting for subprocesses.
Selection/folds/viewport survive configuration entry and exit; reconnect clears forms.
Standalone status checks bracket companion operations; native expected-origin,
expected-Bench and settings-digest guards refuse retargeting captured editors.
There is no ANSI identity scraping fallback.

Build: go build -trimpath -o bin/twig-bench-tui.exe ./cmd/twig-bench-tui
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "twig-bench-tui:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("twig-bench-tui", flag.ContinueOnError)
	flags.Usage = func() { fmt.Fprint(flags.Output(), help) }
	cwd := flags.String("cwd", ".", "Twig workspace directory")
	view := flags.String("view", "tree", "initial view: tree, table, review")
	file := flags.String("file", "", "proposal to review")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	if *view != "tree" && *view != "table" && *view != "review" {
		return fmt.Errorf("unknown view %q: use tree, table, or review", *view)
	}
	root, err := filepath.Abs(*cwd)
	if err != nil {
		return fmt.Errorf("resolve workspace: %w", err)
	}
	requests := make(chan panel.Request, 1)
	initialView := *view
	if *file != "" || *view == "review" {
		if *file != "" && !filepath.IsAbs(*file) {
			*file = filepath.Join(root, *file)
		}
		requests <- panel.Request{Command: "review", File: *file}
		initialView = "tree"
	}
	return panel.Run(panel.Config{
		Cwd:          root,
		InitialView:  initialView,
		Standalone:   true,
		HerdrContext: herdrContext(),
	}, requests)
}

func herdrContext() string {
	if os.Getenv("HERDR_ENV") != "1" {
		return ""
	}
	workspace, tab, pane := os.Getenv("HERDR_WORKSPACE_ID"), os.Getenv("HERDR_TAB_ID"), os.Getenv("HERDR_PANE_ID")
	if workspace == "" || tab == "" || pane == "" {
		return ""
	}
	return fmt.Sprintf("Herdr: %s / %s / %s", workspace, tab, pane)
}
