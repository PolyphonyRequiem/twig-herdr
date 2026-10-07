package panel

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

func managementFixture(t *testing.T) *runtime {
	t.Helper()
	rt, _ := browserFixture(t)
	isolatePinCommands(t, rt)
	rt.size = size{cols: 100, rows: 35}
	rt.configuration = &configurationView{benchID: "bench", section: 3}
	rt.management.binding = rt.binding
	rt.management.snapshot = &benchManagement{Version: 1, CurrentBenchID: "bench", Benches: []managedBench{
		{ID: "default-id", Name: "default", IsDefault: true, ContentsDigest: "empty", Pins: []BenchPin{}, Queries: []string{}},
		{ID: "bench", Name: "My 界 Bench", IsCurrent: true, ContentsDigest: "contents", Pins: []BenchPin{{ID: 41, Mode: "single"}, {ID: 41, Mode: "tree"}}, Queries: []string{"Area Under: Project\\界", "Sprint: @Current+1"}},
	}}
	rt.configuration.selected[3] = 1
	t.Cleanup(func() { rt.cancelManagement(); rt.cancelBenchLaunch(nil) })
	return rt
}

func clickManagementAction(t *testing.T, rt *runtime, action string) {
	t.Helper()
	for _, hit := range rt.hits {
		if hit.action == action {
			rt.handleMouse(uv.Mouse{X: hit.x1, Y: hit.y, Button: uv.MouseLeft})
			return
		}
	}
	t.Fatalf("missing clickable action %s", action)
}

func TestBenchCreationRetainsNameAndDoesNotSelect(t *testing.T) {
	for _, standalone := range []bool{false, true} {
		for _, mouse := range []bool{false, true} {
			rt := managementFixture(t)
			rt.cfg.Standalone = standalone
			original := rt.browser.snapshot
			name := "  Release 界 work  "
			captureStdout(t, func() {
				rt.draw()
				if mouse {
					clickManagementAction(t, rt, "config-add")
				} else {
					rt.handleKey(uv.KeyPressEvent{Code: 'n', Text: "n"})
				}
				rt.handleKey(uv.KeyPressEvent{Code: 'R', Text: name})
				if rt.form == nil || rt.form.editor.text != name || rt.pinCancel != nil {
					t.Fatal("name input lost spaces/Unicode or mutated before submission")
				}
				if mouse {
					clickManagementAction(t, rt, "config-confirm")
				} else {
					rt.handleKey(uv.KeyPressEvent{Code: uv.KeyEnter})
				}
			})
			if rt.benchOperation == nil || rt.benchOperation.kind != "create" || rt.benchOperation.target.Name != name || rt.browser.snapshot != original || rt.configuration.section != 3 {
				t.Fatal("creation selected a Bench, lost the entered name, or left management")
			}
			want := []string{"bench", "create", name, "-o", "json", "--expect-binding", "binding", "--expect-identity", "identity"}
			if !reflect.DeepEqual(rt.benchOperation.args(), want) {
				t.Fatal("creation command lost exact name or origin refusal guards")
			}
			captureStdout(t, func() { rt.handleBenchOperationDone(event{token: rt.pinGen}) })
			if rt.browser.snapshot != original || rt.configuration == nil || rt.management.cancel == nil || rt.bench.launchCancel == nil {
				t.Fatal("successful creation must remain on its current Bench and refresh native management truth")
			}
		}
	}
}

func TestBenchSelectRefreshesAndResetsOldItemStateWithoutLeavingManager(t *testing.T) {
	for _, mouse := range []bool{false, true} {
		rt := managementFixture(t)
		rt.browser.selected, rt.browser.selectedID = "root/1", 1
		rt.browser.collapsed["root/1"] = true
		rt.offsets["tree"], rt.offsets["table"] = 4, 3
		captureStdout(t, func() {
			rt.draw()
			rt.handleKey(uv.KeyPressEvent{Code: 'k', Text: "k"})
			if rt.configuration.selected[3] != 0 {
				t.Fatal("keyboard selection did not choose the preceding Bench")
			}
			if mouse {
				clickManagementAction(t, rt, "config-bench-select")
			} else {
				rt.handleKey(uv.KeyPressEvent{Code: uv.KeyEnter})
			}
		})
		if rt.benchOperation == nil || rt.benchOperation.kind != "switch" || rt.benchOperation.target.ID != "default-id" {
			t.Fatal("select did not capture the displayed Bench storage identity")
		}
		oldGen := rt.bench.launchGen - 1
		captureStdout(t, func() {
			rt.handleBenchOperationDone(event{token: rt.pinGen})
			rt.handleBrowserLoaded(event{token: oldGen, browser: &BrowserSnapshot{BenchID: "old"}})
		})
		if rt.configuration == nil || rt.configuration.section != 3 || rt.browser.snapshot != nil || rt.browser.selectedID != 0 || len(rt.browser.collapsed) != 0 || rt.offsets["tree"] != 0 || rt.offsets["table"] != 0 {
			t.Fatal("switch restored old item state or exited management")
		}
		snapshot := &BrowserSnapshot{BenchID: "default-id", BenchName: "default", BindingID: rt.binding.BindingID, IdentityID: rt.binding.IdentityID, WorktreeRoot: rt.cfg.Cwd, Roots: []*BrowserNode{}, Configuration: &BenchConfiguration{}}
		captureStdout(t, func() { rt.handleBrowserLoaded(event{token: rt.bench.launchGen, browser: snapshot}) })
		if rt.configuration == nil || rt.configuration.section != 3 || rt.configuration.benchID != "default-id" {
			t.Fatal("new current Bench closed management rather than updating its origin")
		}
	}
}

func TestBenchDeletePreviewCancelAndConfirmParity(t *testing.T) {
	for _, empty := range []bool{false, true} {
		for _, mouse := range []bool{false, true} {
			rt := managementFixture(t)
			if empty {
				rt.management.snapshot.Benches[1].Pins = []BenchPin{}
				rt.management.snapshot.Benches[1].Queries = []string{}
			}
			output := captureStdout(t, func() {
				rt.draw()
				if mouse {
					clickManagementAction(t, rt, "config-remove")
				} else {
					rt.handleKey(uv.KeyPressEvent{Code: uv.KeyDelete})
				}
			})
			if rt.form == nil || !rt.form.remove || rt.pinCancel != nil || !strings.Contains(ansi.Strip(output), "My 界 Bench") || !strings.Contains(output, "Staged work is preserved") {
				t.Fatal("delete preview failed to show exact name/preserved work or deleted before confirmation")
			}
			if !empty && (!strings.Contains(output, "#41 · Single pin") || !strings.Contains(output, "#41 · Subtree pin") || !strings.Contains(output, "Sprint: @Current+1")) {
				t.Fatal("delete confirmation omitted saved pin kinds or query descriptions")
			}
			captureStdout(t, func() {
				if mouse {
					clickManagementAction(t, rt, "config-cancel")
				} else {
					// Enter defaults to Cancel; an accidental repeat cannot delete.
					rt.handleKey(uv.KeyPressEvent{Code: uv.KeyEnter})
				}
			})
			if rt.form != nil || rt.pinCancel != nil || rt.configuration == nil {
				t.Fatal("Cancel mutated or exited management")
			}
			captureStdout(t, func() {
				rt.openBenchDelete()
				if mouse {
					clickManagementAction(t, rt, "config-confirm")
				} else {
					rt.handleKey(uv.KeyPressEvent{Code: uv.KeyTab})
					rt.handleKey(uv.KeyPressEvent{Code: uv.KeyEnter})
				}
			})
			want := []string{"bench", "delete", "My 界 Bench", "--expect-bench", "bench", "--confirm", "My 界 Bench", "--expect-contents", "contents", "-o", "json", "--expect-binding", "binding", "--expect-identity", "identity"}
			if rt.benchOperation == nil || !reflect.DeepEqual(rt.benchOperation.args(), want) {
				t.Fatal("confirmed deletion lost captured name, ID, digest or origin")
			}
			captureStdout(t, func() { rt.handleBenchOperationDone(event{token: rt.pinGen}) })
			if rt.browser.snapshot != nil || rt.configuration == nil || rt.offsets["tree"] != 0 {
				t.Fatal("current deletion did not reset old Bench viewer for default fallback")
			}
		}
	}
}

func TestDeletionResetsViewerWhenCapturedCurrentFlagIsStale(t *testing.T) {
	rt := managementFixture(t)
	rt.management.snapshot.Benches[1].IsCurrent = false
	rt.offsets["tree"] = 4
	captureStdout(t, func() {
		rt.openBenchDelete()
		rt.handleKey(uv.KeyPressEvent{Code: 'y', Text: "y"})
		rt.handleBenchOperationDone(event{token: rt.pinGen})
	})
	if rt.browser.snapshot != nil || rt.offsets["tree"] != 0 || rt.configuration == nil {
		t.Fatal("captured current flag preserved old viewer after deletion")
	}
}

func TestDefaultBenchHasNoDeleteControlOrDeleteGesture(t *testing.T) {
	rt := managementFixture(t)
	rt.configuration.selected[3] = 0
	captureStdout(t, func() {
		rt.draw()
		for _, hit := range rt.hits {
			if hit.action == "config-remove" {
				t.Fatal("default Bench exposes a delete hit target")
			}
		}
		rt.handleKey(uv.KeyPressEvent{Code: 'd', Text: "d"})
		rt.handleKey(uv.KeyPressEvent{Code: uv.KeyDelete})
	})
	if rt.form != nil || rt.pinCancel != nil {
		t.Fatal("default Bench deletion was allowed")
	}
}

func TestStaleDeletionRequiresFreshDisplayAndNewConfirmation(t *testing.T) {
	for _, changed := range []string{"contents", "recreated", "origin"} {
		rt := managementFixture(t)
		captureStdout(t, func() {
			rt.openBenchDelete()
			switch changed {
			case "contents":
				rt.management.snapshot.Benches[1].ContentsDigest = "changed"
			case "recreated":
				rt.management.snapshot.Benches[1].ID = "replacement"
			case "origin":
				rt.binding.IdentityID = "replacement"
			}
			rt.handleKey(uv.KeyPressEvent{Code: 'y', Text: "y"})
		})
		if rt.pinCancel != nil || rt.benchOperation != nil || rt.notice == "" {
			t.Fatal("stale displayed deletion was retargeted or retried")
		}
		if changed != "origin" && (rt.form != nil || rt.management.cancel == nil) {
			t.Fatal("stale contents must discard confirmation and fetch a fresh list")
		}
	}
}

func TestNativeBenchErrorsStayVisibleWithoutRetryingDeletion(t *testing.T) {
	for _, kind := range []string{"create", "delete"} {
		rt := managementFixture(t)
		captureStdout(t, func() {
			if kind == "create" {
				rt.openInput("bench", "My 界 Bench")
				rt.handleKey(uv.KeyPressEvent{Code: uv.KeyEnter})
			} else {
				rt.openBenchDelete()
				rt.handleKey(uv.KeyPressEvent{Code: 'y', Text: "y"})
			}
			rt.handleBenchOperationDone(event{token: rt.pinGen, err: errors.New("native duplicate/stale contents refusal")})
		})
		if rt.pinCancel != nil || rt.benchOperation != nil || !strings.Contains(rt.notice, "refusal") || rt.management.cancel == nil {
			t.Fatal("native refusal hidden or operation silently retried")
		}
		if kind == "create" && (rt.form == nil || rt.form.confirm || rt.form.editor.text != "My 界 Bench" || rt.form.error == "") {
			t.Fatal("native name error did not retain editable exact input")
		}
		if kind == "delete" && rt.form != nil {
			t.Fatal("native stale deletion retained a reusable confirmation")
		}
	}
}

func TestManagementBusyAndOldCompletionCannotResurrectState(t *testing.T) {
	rt := managementFixture(t)
	captureStdout(t, func() {
		rt.syncCancel = func() {}
		rt.selectManagedBench()
		if rt.pinCancel != nil {
			t.Fatal("Bench switch overlapped sync")
		}
		rt.syncCancel = nil
		rt.selectManagedBench()
		pending, token := rt.benchOperation, rt.pinGen
		rt.selectManagedBench()
		rt.handleBenchOperationDone(event{token: token - 1})
		if rt.benchOperation != pending || rt.pinCancel == nil {
			t.Fatal("repeat gesture or stale completion replaced/released active mutation")
		}
		rt.management.gen, rt.management.cancel = 8, func() {}
		rt.stopForReconnect(errors.New("binding-changed: changed"))
		rt.handleBenchOperationDone(event{token: token})
		rt.handleManagementLoaded(event{token: 8, binding: rt.binding, management: &benchManagement{}})
	})
	if rt.configuration != nil || rt.form != nil || rt.browser.snapshot != nil || rt.management.snapshot != nil || rt.pinCancel != nil || rt.management.cancel != nil {
		t.Fatal("late management data revived old actor state")
	}
}

func TestManagementReadRetainsSelectedIdentityAndRejectsIncompleteContents(t *testing.T) {
	rt := managementFixture(t)
	encoded, err := json.Marshal(rt.management.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseManagement(encoded)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Benches[0], parsed.Benches[1] = parsed.Benches[1], parsed.Benches[0]
	rt.management.gen, rt.management.cancel = 3, func() {}
	captureStdout(t, func() { rt.handleManagementLoaded(event{token: 3, binding: rt.binding, management: parsed}) })
	if rt.selectedManagedBench().ID != "bench" {
		t.Fatal("refreshed management order changed the selected target identity")
	}
	for _, payload := range []string{`{"version":1,"currentBenchId":"bench","benches":[]}`, `{"version":1,"currentBenchId":"bench","benches":[{"id":"bench","name":"default","isCurrent":true,"isDefault":true,"contentsDigest":"x","pins":[],"queries":null}]}`} {
		if _, err := parseManagement([]byte(payload)); err == nil {
			t.Fatal("incomplete destructive contents were admitted for confirmation")
		}
	}
}

func TestConnectionHeaderKeepsNativeContextWithoutAuthorityIDs(t *testing.T) {
	rt := managementFixture(t)
	rt.binding.Organization, rt.binding.Project, rt.binding.Team = "My Org", "My Project", "Team 界"
	rt.binding.Account = "person@example.com"
	rt.benchSummary = "My Bench"
	rt.size.cols = 200
	header := ansi.Strip(rt.headerText())
	for _, text := range []string{"My Org/My Project", "Team: Team 界", "User: person@example.com", "Bench: My Bench"} {
		if !strings.Contains(header, text) {
			t.Fatalf("native context missing: %s", text)
		}
	}
	if strings.Contains(header, "default-id") || strings.Contains(header, "(cached)") {
		t.Fatal("header leaked internal identity/cache qualification")
	}
	rt.binding.Team = ""
	rt.size.cols = 32
	narrow := ansi.Truncate(ansi.Strip(rt.headerText()), 32, "…")
	if !strings.Contains(narrow, "Bench:") || strings.Contains(narrow, rt.cfg.Cwd) {
		t.Fatal("narrow header prioritized cwd over connection/current Bench")
	}
	rt.binding.Organization, rt.binding.Project = "", ""
	if strings.Contains(rt.headerText(), "/") {
		// A directory may contain slashes; inspect the connection segment only.
		if strings.Contains(strings.Split(ansi.Strip(rt.headerText()), " · ")[0], "/") {
			t.Fatal("missing connection metadata produced an invented org/project")
		}
	}
}

func TestHeaderShowsNativeEffectiveTeamWhenRawTeamIsEmpty(t *testing.T) {
	rt := managementFixture(t)
	binding, err := parseStandaloneBinding([]byte(`{"bindingId":"binding","identityId":"identity","worktreeRoot":"/work","organization":"Org","project":"Project","team":"","effectiveTeam":"Project Team"}`), "")
	if err != nil {
		t.Fatal(err)
	}
	rt.binding = binding
	rt.size.cols = 160
	if !strings.Contains(ansi.Strip(rt.headerText()), "Team: Project Team") {
		t.Fatal("header hides the native default team")
	}
	rt.binding.EffectiveTeam = ""
	rt.browser.snapshot.EffectiveTeam = "Project Team"
	if !strings.Contains(ansi.Strip(rt.headerText()), "Team: Project Team") {
		t.Fatal("older standalone status concealed the guarded browser's effective team")
	}
}
