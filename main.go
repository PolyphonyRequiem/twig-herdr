package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/PolyphonyRequiem/twig-herdr/internal/panel"
)

var version = "0.2.0"

const help = `Twig Herdr — native bench and proposal review panel

Usage:
  twig-herdr open [--pane SOURCE_OR_PANEL_ID]
  twig-herdr table [--pane SOURCE_OR_PANEL_ID]
  twig-herdr tree [--pane SOURCE_OR_PANEL_ID]
  twig-herdr review [--file PATH] [--pane SOURCE_OR_PANEL_ID]
  twig-herdr exit-review [--pane SOURCE_OR_PANEL_ID]
  twig-herdr status [--pane SOURCE_OR_PANEL_ID]
  twig-herdr panel
  twig-herdr --version

Requires Herdr 0.9.0+ and Twig 0.94.0+ on PATH. No Node or Go runtime is needed.
New benches open in Tree view; explicit Table choices are preserved.
Inside the panel: 1 Table, 2 Tree, 3 Review (after an explicit file selection),
j/k/arrows scroll, PgUp/PgDn/Home/End navigate, r refreshes the bench (redraw
only during Review), d/b Details/Brief, Esc/c leaves Review, q closes the panel.
Review never authorizes or applies changes.

Review paths are relative to the source pane's working directory; a selected
proposal is remembered only until this panel closes. Without a selected file,
3 reports that no proposal is selected. The control
channel is local, authenticated, and scoped to the Herdr session and tab.
Repeated open reuses the existing pane and preserves its divider and view.
New panels preserve focus, prefer 80-column panes, and use at most 25 rows.

To build from source: go build -trimpath -o bin/twig-herdr .
For isolated terminal checks, set TWIG_PANEL_CWD, HERDR_ENV=1, HERDR_PANE_ID,
HERDR_WORKSPACE_ID, HERDR_TAB_ID and HERDR_SOCKET_PATH. Optional initial state:
TWIG_PANEL_INITIAL_VIEW=table|tree, TWIG_PANEL_INITIAL_REVIEW_FILE=absolute-path.
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "twig-herdr:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" || args[0] == "help" {
		fmt.Print(help)
		return nil
	}
	if args[0] == "--version" || args[0] == "version" {
		fmt.Println(version)
		return nil
	}
	command := args[0]
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	paneID := flags.String("pane", "", "source or panel pane ID")
	file := flags.String("file", "", "proposal file")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Print(help)
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	if command != "review" && *file != "" {
		return errors.New("--file is only valid for review")
	}
	if os.Getenv("HERDR_ENV") != "1" {
		return errors.New("run inside a Herdr pane (HERDR_ENV=1)")
	}
	if os.Getenv("HERDR_SOCKET_PATH") == "" {
		return errors.New("HERDR_SOCKET_PATH is required to identify the caller's session")
	}

	source, err := resolvePane(*paneID)
	if err != nil {
		return err
	}
	if command == "panel" {
		if err := checkTwig(source.Cwd); err != nil {
			return err
		}
		view := os.Getenv("TWIG_PANEL_INITIAL_VIEW")
		if view == "" {
			view = "tree"
		}
		if view != "table" && view != "tree" {
			return fmt.Errorf("unknown initial view %q", view)
		}
		requests := make(chan panel.Request, 8)
		stop, err := servePanel(source, requests)
		if err != nil {
			return err
		}
		defer stop()
		return panel.Run(panel.Config{Cwd: source.Cwd, InitialView: view, InitialReview: os.Getenv("TWIG_PANEL_INITIAL_REVIEW_FILE")}, requests)
	}
	if command != "open" && command != "table" && command != "tree" && command != "review" && command != "exit-review" && command != "status" {
		return fmt.Errorf("unknown command %q (use --help)", command)
	}
	if command == "review" && *file != "" {
		if !filepath.IsAbs(*file) {
			*file = filepath.Join(source.Cwd, *file)
		}
		*file = filepath.Clean(*file)
	}
	result, err := controlPanel(source, command, *file)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}
