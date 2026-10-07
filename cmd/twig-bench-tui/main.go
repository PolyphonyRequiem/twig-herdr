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
Semantic Bench reads, pins, and scoped sync prefer twig-bench-native.exe beside this
browser (twig-bench-native on Unix); otherwise Twig must support browser v1 JSON,
--include-browser, --expect-bench, --expect-binding and --expect-identity.
Normal Twig remains connection-status and proposal-review authority.
--file opens a specific proposal; otherwise Review finds the latest unresolved proposal.
Relative proposal paths resolve against --cwd (the current directory by default).

Keys:
  1 workspace table, 2 bench tree, 3 proposal review
  j/k or up/down select work items locally (not twig set), PgUp/PgDn/Home/End navigate
  left/right collapse/expand, Space toggle; click rows/disclosures; wheel scrolls
  p pin type picker: Single item / Whole subtree; Shift+P confirms explicit unpin
  Enter confirms; Esc/c cancels. Seeds/inherited membership explain instead of mutating.
  Click footer Pin/Unpin, ? discovery help; s Bench-scoped ADO pull, r refresh
  d details, b brief, Esc/c leave review, Ctrl+R reconnect, q/Ctrl+C quit

Sync pulls only Bench members and relationship-rule candidates; it never flushes
pending edits. An open review keeps its captured snapshot, never pins, authorizes,
or applies. Fresh observations and operations check the admitted native origin;
local selection/folding uses retained snapshots without waiting for subprocesses.
Selection/folds survive refresh; connection changes clear data and reset them.
Standalone status checks bracket companion operations; native expected-origin and
expected-Bench guards refuse retargeting. There is no ANSI identity scraping fallback.

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
	return panel.Run(panel.Config{Cwd: root, InitialView: initialView, Standalone: true}, requests)
}
