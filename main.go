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

var version = "0.4.0"

const help = `Twig Herdr — native bench and proposal review panel

Usage:
  twig-herdr open [--pane SOURCE_OR_PANEL_ID]
  twig-herdr table [--pane SOURCE_OR_PANEL_ID]
  twig-herdr tree [--pane SOURCE_OR_PANEL_ID]
  twig-herdr review [--file PATH] [--pane SOURCE_OR_PANEL_ID]
  twig-herdr exit-review [--pane SOURCE_OR_PANEL_ID]
  twig-herdr status [--pane SOURCE_OR_PANEL_ID]
  twig-herdr reconnect [--pane SOURCE_OR_PANEL_ID]
  twig-herdr panel
  twig-herdr --version

Requires Herdr 0.9.0+ and Twig on PATH for digest-guarded proposal review.
Install the private twig-bench-native companion bundle beside this browser for
semantic Bench reads, guarded local pin/configuration changes, scoped sync, and host admission.
Companion operations hold native connection admission and expected-origin guards;
status fingerprints detect host changes but are not atomic native snapshot tokens.
Without the bundle, normal Twig must provide qualified-attachment-snapshot-v1
host admission plus --include-browser, --expect-bench, --expect-binding,
--expect-identity, --expect-settings, --expect-contents, configuration v1 in browser JSON,
bench list --include-management v1, and workspace untrack ID --mode single|tree.
No ANSI identity scraping or auth fallback is used.
New benches open in Tree view; explicit Table choices are preserved.
The persistent header has two rows: Org/Project, Team, Bench and User above mode,
worktree and Git branch. Herdr workspace/tab/pane handles sit at the lower-right.
Inside the panel: 1 Table, 2 Tree, 3 Review (latest unresolved proposal),
j/k or up/down select local work items (not twig set), left/right collapse/expand,
Space toggles, PgUp/PgDn/Home/End navigate. Click rows/disclosures; wheel scrolls.
Enter opens full cached detail using Twig's Show renderer; no pre-sync. HTML field
headings, lists, code and tables render safely. In detail, S pulls only that item and
its links (never pending edits/related targets); R reloads cache; Esc restores selection.
b opens Bench Configuration (Pins / Areas / Sprints / Benches); Esc returns to the viewer.
Up/down chooses sections; Enter/right focuses items; left returns to sections.
Tab or 1/2/3/4 also chooses a section. Pins uses p/Shift+P independent toggles, including
uncached rows; Areas/Sprints use a add and d remove with review, never p/P pins.
In Areas, t chooses an official configured-team path; Enter reviews its Exact/Under
semantics before adding it. Candidate reads never import workspace area defaults.
Benches: arrows/j/k choose, Enter selects, n creates an empty named Bench, d/Delete
reviews exact saved pins/queries before Yes/Cancel. Default is protected. Deletion
preserves staged work and falls back to default if current. Stale contents require
a fresh list and a new confirmation. Click the named controls for keyboard parity.
Selecting a Bench resets item selection/folds/scroll but keeps configuration open.
p toggles only the selected item's explicit Single pin; Shift+P only Subtree pin.
Both can coexist; removing one retains the other and inherited/query membership.
Manual IDs are only in b Bench Configuration > Pins > i, including an empty Bench.
Enter validates the ID and reviews; p adds Single pin, Shift+P adds Subtree pin.
Enter adds the chosen kind. Unknown IDs stay uncached/unverified until scoped sync.
Seeds explain instead of mutating. Click each named pin control for its key action;
footer Pin/Unpin labels reflect each explicit kind. ? opens discovery help.
Areas distinguish Exact/Under; sprints accept @Current, @Current±N or absolute paths.
No sprints disables automatic membership; areas alone never enable a project-wide rule.
Text fields treat q/c/s/p as text. Esc cancels input first; Ctrl+C always quits.
s pulls only this Bench and its saved automatic/relationship rules from ADO; never
flushes pending edits or substitutes shared workspace area/sprint settings.
r refreshes cached membership (redraw only during Review).
d/b Details/Brief, Esc/c leaves Review, q closes. Review keeps its captured snapshot
after sync and never pins, authorizes, or applies changes.
Ctrl+R acknowledges reconnect; binding/principal changes clear all previous actor data.
Selection/folds/viewport survive configuration entry and exit. Native origin/Bench
and settings-digest guards refuse stale mutations without retargeting captured forms.

Relative --file paths resolve from the source pane's working directory. File-free
Review keeps using that cwd to find the latest proposal; an explicit absolute
file launches against that proposal's Twig workspace.
The control channel is local, authenticated, and scoped to the Herdr session and tab.
Repeated open reuses the existing pane and preserves its divider and view.
New panels preserve focus, prefer 80-column panes, and use at most 25 rows.

To build from source: go build -trimpath -o bin/twig-herdr .
For isolated terminal checks, set TWIG_PANEL_CWD, HERDR_ENV=1, HERDR_PANE_ID,
TWIG_PANEL_INITIAL_VIEW=table|tree.
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
		return panel.Run(panel.Config{
			Cwd:          source.Cwd,
			InitialView:  view,
			HerdrContext: fmt.Sprintf("Herdr: %s / %s / %s", source.WorkspaceID, source.TabID, source.PaneID),
		}, requests)
	}
	if command != "open" && command != "table" && command != "tree" && command != "review" && command != "exit-review" && command != "status" && command != "reconnect" {
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
		if result.ReconnectRequired {
			if encodeErr := json.NewEncoder(os.Stdout).Encode(result); encodeErr != nil {
				return errors.Join(err, encodeErr)
			}
		}
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}
