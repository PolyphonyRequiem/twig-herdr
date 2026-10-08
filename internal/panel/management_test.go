package panel

import (
	"encoding/json"
	"errors"
	"fmt"
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

func TestBenchBoundaryNavigationDoesNotBlockSelection(t *testing.T) {
	rt := managementFixture(t)
	rt.configuration.sectionsFocused = true
	captureStdout(t, func() {
		rt.handleKey(uv.KeyPressEvent{Code: uv.KeyDown})
		rt.handleKey(uv.KeyPressEvent{Code: uv.KeyDown})
		rt.handleKey(uv.KeyPressEvent{Code: uv.KeyRight})
		rt.handleKey(uv.KeyPressEvent{Code: uv.KeyEnter})
	})
	if rt.benchOperation == nil || rt.benchOperation.kind != "switch" || rt.benchOperation.target.ID != "bench" {
		t.Fatal("navigation at the last section blocked selection of the displayed Bench")
	}
}

func TestAdmittedBenchActionsStayAvailableDuringBackgroundRefresh(t *testing.T) {
	for _, deletion := range []bool{false, true} {
		rt := managementFixture(t)
		rt.management.cancel = func() {}
		captureStdout(t, func() {
			if deletion {
				rt.openBenchDelete()
				if rt.form == nil {
					t.Fatal("background refresh hid the displayed Bench's deletion review")
				}
				rt.handleKey(uv.KeyPressEvent{Code: 'y', Text: "y"})
			} else {
				rt.handleKey(uv.KeyPressEvent{Code: uv.KeyEnter})
			}
		})
		if rt.benchOperation == nil || rt.benchOperation.target.ID != "bench" {
			t.Fatal("background refresh swallowed an explicit guarded Bench action")
		}
		if deletion && (rt.benchOperation.kind != "delete" || rt.benchOperation.target.ContentsDigest != "contents") {
			t.Fatal("responsive deletion lost its reviewed contents guard")
		}
	}
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
			receiptName := "Native 界 Name"
			captureStdout(t, func() {
				rt.handleBenchOperationDone(event{token: rt.pinGen, data: []byte(`{"name":"Native 界 Name"}`)})
			})
			if rt.browser.snapshot != original || rt.configuration == nil || rt.management.cancel == nil || rt.bench.launchCancel == nil {
				t.Fatal("successful creation must remain on its current Bench and refresh native management truth")
			}
			refreshed := &benchManagement{Version: 1, CurrentBenchID: "bench", Benches: append([]managedBench{}, rt.management.snapshot.Benches...)}
			refreshed.Benches = append(refreshed.Benches, managedBench{ID: "created-id", Name: receiptName, ContentsDigest: "created-empty", Pins: []BenchPin{}, Queries: []string{}})
			captureStdout(t, func() {
				rt.handleManagementLoaded(event{token: rt.management.gen, binding: rt.binding, management: refreshed})
			})
			if rt.selectedManagedBench().ID != "created-id" || rt.selectedManagedBench().Name != receiptName || rt.management.snapshot.CurrentBenchID != "bench" || rt.browser.snapshot != original || rt.benchOperation != nil || rt.configuration.sectionsFocused {
				t.Fatal("creation did not highlight the native receipt name without switching the current Bench")
			}
			captureStdout(t, func() { rt.handleKey(uv.KeyPressEvent{Code: uv.KeyEnter}) })
			if rt.benchOperation == nil || rt.benchOperation.kind != "switch" || rt.benchOperation.target.ID != "created-id" || rt.benchOperation.target.Name != receiptName {
				t.Fatal("explicit Enter did not select the refreshed native creation identity")
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
			rt.handleBenchOperationDone(event{token: rt.pinGen, data: []byte(`{"name":"default"}`)})
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
			captureStdout(t, func() { rt.handleBenchOperationDone(event{token: rt.pinGen, data: []byte(`{"name":"My 界 Bench"}`)}) })
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
		rt.handleBenchOperationDone(event{token: rt.pinGen, data: []byte(`{"name":"My 界 Bench"}`)})
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
		if kind == "delete" {
			captureStdout(t, func() {
				rt.openBenchDelete()
				rt.handleKey(uv.KeyPressEvent{Code: 'y', Text: "y"})
			})
			if rt.form != nil || rt.benchOperation != nil {
				t.Fatal("native-refused deletion was resubmitted before fresh contents arrived")
			}
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
	if strings.Contains(header, rt.cfg.Cwd) || strings.Contains(header, "Tree") || strings.Contains(header, "Table") {
		t.Fatal("connection row mixed navigation or worktree context into native metadata")
	}
	rt.binding.Team = ""
	rt.size.cols = 32
	narrow := ansi.Truncate(ansi.Strip(rt.headerText()), 32, "…")
	if !strings.Contains(narrow, "Bench:") || strings.Contains(narrow, rt.cfg.Cwd) {
		t.Fatal("narrow header prioritized cwd over connection/current Bench")
	}
	rt.binding.Organization, rt.binding.Project = "", ""
	if strings.Contains(strings.Split(ansi.Strip(rt.headerText()), " · ")[0], "/") {
		t.Fatal("missing connection metadata produced an invented org/project")
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

func TestManagementRefreshCoalescesNavigationAndRepeatedRefreshGestures(t *testing.T) {
	rt := managementFixture(t)
	canceled := 0
	rt.management.gen, rt.management.cancel = 9, func() { canceled++ }
	rt.configuration.sectionsFocused = true
	browserGen := rt.bench.launchGen
	captureStdout(t, func() {
		rt.handleKey(uv.KeyPressEvent{Code: uv.KeyDown})
		rt.handleKey(uv.KeyPressEvent{Code: '4', Text: "4"})
		rt.handleKey(uv.KeyPressEvent{Code: 'r', Text: "r"})
		rt.handleKey(uv.KeyPressEvent{Code: 'r', Text: "r"})
		rt.beginManagementRefresh()
		rt.draw()
		clicked := false
		for _, hit := range rt.hits {
			if hit.action == "config-section" && hit.row == 3 {
				rt.handleMouse(uv.Mouse{X: hit.x1, Y: hit.y, Button: uv.MouseLeft})
				clicked = true
				break
			}
		}
		if !clicked {
			t.Fatal("Bench section has no mouse navigation target")
		}
		rt.handleKey(uv.KeyPressEvent{Code: uv.KeyTab, Mod: uv.ModShift})
		rt.handleKey(uv.KeyPressEvent{Code: uv.KeyTab})
	})
	if rt.management.gen != 9 || rt.management.cancel == nil || canceled != 0 || rt.bench.launchGen != browserGen || rt.configuration.selected[3] != 1 {
		t.Fatal("navigation or repeated refresh replaced the admitted list's pending read or restarted membership")
	}
	captureStdout(t, func() {
		rt.handleManagementLoaded(event{token: 9, binding: rt.binding, management: rt.management.snapshot})
	})
	if rt.management.cancel != nil || canceled != 1 || rt.selectedManagedBench().ID != "bench" {
		t.Fatal("coalesced read did not complete normally with its displayed selection")
	}
}

func TestGuardedActionCancelsPendingReadAndIgnoresLateCompletion(t *testing.T) {
	for _, deletion := range []bool{false, true} {
		rt := managementFixture(t)
		oldSnapshot := rt.management.snapshot
		canceled := 0
		rt.management.gen, rt.management.cancel = 7, func() { canceled++ }
		captureStdout(t, func() {
			if deletion {
				rt.openBenchDelete()
				rt.handleKey(uv.KeyPressEvent{Code: 'y', Text: "y"})
			} else {
				rt.selectManagedBench()
			}
		})
		pending, token := rt.benchOperation, rt.pinGen
		if pending == nil || pending.target.ID != "bench" || canceled != 1 || rt.management.cancel != nil || rt.management.gen == 7 {
			t.Fatal("explicit guarded action did not cancel its obsolete read and capture the displayed identity")
		}
		captureStdout(t, func() {
			rt.handleManagementLoaded(event{token: 7, binding: rt.binding, management: &benchManagement{}, admissionErr: errors.New("obsolete origin result")})
			rt.handleBenchOperationDone(event{token: token - 1, data: []byte(`{"name":"old"}`)})
			rt.handleKey(uv.KeyPressEvent{Code: uv.KeyEnter})
			rt.handleKey(uv.KeyPressEvent{Code: 'n', Text: "n"})
			rt.handleKey(uv.KeyPressEvent{Code: 'd', Text: "d"})
			rt.handleKey(uv.KeyPressEvent{Code: 'r', Text: "r"})
		})
		if rt.reconnectRequired || rt.management.snapshot != oldSnapshot || rt.benchOperation != pending || rt.pinGen != token || rt.pinCancel == nil || rt.form != nil || rt.management.cancel != nil {
			t.Fatal("late read/completion or busy gesture retargeted, released or repeated the active mutation")
		}
		captureStdout(t, func() { rt.handleBenchOperationDone(event{token: token, data: []byte(`{"name":"My 界 Bench"}`)}) })
		refreshGen := rt.management.gen
		captureStdout(t, func() {
			rt.handleManagementLoaded(event{token: 7, binding: rt.binding, err: errors.New("obsolete failed read")})
		})
		if rt.management.cancel == nil || rt.management.gen != refreshGen || rt.management.error != "" || rt.pinCancel != nil || rt.benchOperation != nil {
			t.Fatal("obsolete read changed the fresh post-mutation generation")
		}
	}
}

func TestRefreshedDeletionContentsRequireNewConfirmation(t *testing.T) {
	rt := managementFixture(t)
	rt.management.gen, rt.management.cancel = 5, func() {}
	fresh := &benchManagement{Version: 1, CurrentBenchID: "bench", Benches: append([]managedBench{}, rt.management.snapshot.Benches...)}
	fresh.Benches[1].ContentsDigest = "new-contents"
	captureStdout(t, func() {
		rt.openBenchDelete()
		rt.handleManagementLoaded(event{token: 5, binding: rt.binding, management: fresh})
		rt.handleKey(uv.KeyPressEvent{Code: 'y', Text: "y"})
	})
	if rt.form != nil || rt.benchOperation != nil || rt.pinCancel != nil || rt.management.cancel == nil {
		t.Fatal("refresh silently upgraded a reviewed deletion digest or retained reusable confirmation")
	}
	captureStdout(t, func() {
		rt.handleManagementLoaded(event{token: rt.management.gen, binding: rt.binding, management: fresh})
		rt.openBenchDelete()
		rt.handleKey(uv.KeyPressEvent{Code: 'y', Text: "y"})
	})
	if rt.benchOperation == nil || rt.benchOperation.kind != "delete" || rt.benchOperation.target.ContentsDigest != "new-contents" {
		t.Fatal("a new deletion review did not capture the refreshed exact contents")
	}
}

func TestTransientManagementReadRetainsAdmittedListAndVisibleError(t *testing.T) {
	rt := managementFixture(t)
	snapshot := rt.management.snapshot
	rt.management.gen, rt.management.cancel = 3, func() {}
	output := captureStdout(t, func() {
		rt.handleManagementLoaded(event{token: 3, binding: rt.binding, err: errors.New("network unavailable")})
	})
	if rt.management.snapshot != snapshot || rt.selectedManagedBench().ID != "bench" || rt.management.error == "" || !strings.Contains(ansi.Strip(output), "Stale admitted list") || !strings.Contains(ansi.Strip(output), "network unavailable") {
		t.Fatal("transient same-origin failure erased admitted targets or hid the stale/error state")
	}
	captureStdout(t, func() { rt.beginManagementRefresh() })
	if rt.management.error == "" || rt.management.snapshot != snapshot {
		t.Fatal("retry hid the real failed read before a successful replacement arrived")
	}
	captureStdout(t, func() { rt.selectManagedBench() })
	if rt.benchOperation == nil || rt.benchOperation.target.ID != "bench" || rt.management.cancel != nil {
		t.Fatal("stale admitted target could not submit an exact guarded action during retry")
	}
}

func TestScrolledManagementRefreshErrorRemainsVisible(t *testing.T) {
	rt := managementFixture(t)
	rt.size.rows = 12
	for i := range 24 {
		rt.management.snapshot.Benches = append(rt.management.snapshot.Benches, managedBench{ID: fmt.Sprint(i + 100), Name: fmt.Sprintf("Other Bench %02d", i), ContentsDigest: "digest", Pins: []BenchPin{}, Queries: []string{}})
	}
	rt.configuration.offset = 15
	rt.management.gen, rt.management.cancel = 3, func() {}
	output := captureStdout(t, func() {
		rt.handleManagementLoaded(event{token: 3, binding: rt.binding, err: errors.New("network unavailable")})
	})
	if !strings.Contains(ansi.Strip(output), "network unavailable") || rt.management.snapshot == nil {
		t.Fatal("scrolled cached Bench list hid its refresh failure")
	}
}

func TestManagementReadCannotRetainAnotherActorsCachedList(t *testing.T) {
	for _, changed := range []string{"post-admission", "event-origin", "cached-origin"} {
		rt := managementFixture(t)
		rt.management.gen, rt.management.cancel = 3, func() {}
		ev := event{token: 3, binding: rt.binding, err: errors.New("native read failed")}
		switch changed {
		case "post-admission":
			ev.admissionErr = errors.New("binding-changed: account changed")
		case "event-origin":
			ev.binding.IdentityID = "another-actor"
		case "cached-origin":
			rt.management.binding.IdentityID = "another-actor"
		}
		captureStdout(t, func() { rt.handleManagementLoaded(ev) })
		if rt.management.snapshot != nil || rt.management.cancel != nil || rt.benchOperation != nil {
			t.Fatal("failed read retained another actor's admitted management rows")
		}
		if changed != "cached-origin" && (!rt.reconnectRequired || rt.configuration != nil || rt.browser.snapshot != nil) {
			t.Fatal("changed admission did not erase the prior actor's viewer and editor")
		}
	}
}

func TestBenchEnterControlFollowsFocusAndBusyActionsDisappear(t *testing.T) {
	for _, mouse := range []bool{false, true} {
		rt := managementFixture(t)
		rt.size.cols = 160
		rt.configuration.sectionsFocused = true
		captureStdout(t, func() {
			rt.draw()
			if !strings.Contains(rt.footerText(false), "[Enter Items]") || strings.Contains(rt.footerText(false), "[Enter Select]") {
				t.Fatal("section-focused Enter control falsely advertises selection")
			}
			if mouse {
				clickManagementAction(t, rt, "config-items")
			} else {
				rt.handleKey(uv.KeyPressEvent{Code: uv.KeyEnter})
			}
			if rt.configuration.sectionsFocused || rt.benchOperation != nil || rt.pinCancel != nil {
				t.Fatal("Enter-items control selected a Bench rather than focusing the list")
			}
			if !strings.Contains(rt.footerText(false), "[Enter Select]") || strings.Contains(rt.footerText(false), "[Enter Items]") {
				t.Fatal("list-focused Enter control does not advertise its actual action")
			}
			if mouse {
				clickManagementAction(t, rt, "config-bench-select")
			} else {
				rt.handleKey(uv.KeyPressEvent{Code: uv.KeyEnter})
			}
			for _, hit := range rt.hits {
				if hit.action == "config-add" || hit.action == "config-remove" || hit.action == "config-bench-select" {
					t.Fatal("busy mutation left a clickable control that cannot perform its advertised action")
				}
			}
		})
		if rt.benchOperation == nil || rt.benchOperation.target.ID != "bench" || !strings.Contains(rt.notice, "My 界 Bench") {
			t.Fatal("explicit list selection lost its target or named operation progress")
		}
	}
}

func TestSuccessfulMutationWithUnreadableReceiptIsNotRetriedOrGuessed(t *testing.T) {
	for _, output := range []string{`{"message":"created"}`, `not JSON`} {
		rt := managementFixture(t)
		original := rt.browser.snapshot
		captureStdout(t, func() {
			rt.openInput("bench", "  guessed-name  ")
			rt.handleKey(uv.KeyPressEvent{Code: uv.KeyEnter})
			rt.handleBenchOperationDone(event{token: rt.pinGen, data: []byte(output)})
		})
		if rt.form != nil || rt.pinCancel != nil || rt.benchOperation != nil || rt.management.highlightName != "" || rt.management.cancel == nil || rt.browser.snapshot != original || !strings.Contains(rt.notice, "receipt") {
			t.Fatal("successful mutation with bad receipt invited a retry, guessed native naming or hid receipt failure")
		}
		warning := rt.notice
		captureStdout(t, func() {
			rt.handleManagementLoaded(event{token: rt.management.gen, binding: rt.binding, err: errors.New("replacement read unavailable")})
		})
		if !strings.Contains(rt.notice, warning) || !strings.Contains(rt.notice, "replacement read unavailable") || rt.form != nil {
			t.Fatal("refresh failure hid the mutation outcome or invited repeating it")
		}
	}
}

func TestManagementRefreshPreservesInactiveSectionSelectionAndFallsBackToNativeCurrent(t *testing.T) {
	rt := managementFixture(t)
	reordered := &benchManagement{Version: 1, CurrentBenchID: "bench", Benches: []managedBench{rt.management.snapshot.Benches[1], rt.management.snapshot.Benches[0]}}
	reordered.Benches = append(reordered.Benches, managedBench{ID: "removed-id", Name: "Removed Bench", ContentsDigest: "empty", Pins: []BenchPin{}, Queries: []string{}})
	rt.configuration.section = 2
	rt.management.gen, rt.management.cancel = 3, func() {}
	captureStdout(t, func() { rt.handleManagementLoaded(event{token: 3, binding: rt.binding, management: reordered}) })
	if rt.configuration.section != 2 || rt.configuration.selected[3] != 0 || rt.management.snapshot.CurrentBenchID != "bench" {
		t.Fatal("background refresh changed the active section or lost the inactive Bench target")
	}
	rt.configuration.section, rt.configuration.selected[3] = 3, 2
	removed := &benchManagement{Version: 1, CurrentBenchID: "bench", Benches: []managedBench{reordered.Benches[1], reordered.Benches[0]}}
	rt.management.gen, rt.management.cancel = 4, func() {}
	captureStdout(t, func() { rt.handleManagementLoaded(event{token: 4, binding: rt.binding, management: removed}) })
	if rt.selectedManagedBench().ID != "bench" || rt.management.snapshot.CurrentBenchID != "bench" || rt.benchOperation != nil {
		t.Fatal("removed selection did not fall back to native current without switching")
	}
}

func TestCreationHighlightWaitsForSuccessfulRefreshWithoutSelecting(t *testing.T) {
	rt := managementFixture(t)
	original := rt.browser.snapshot
	captureStdout(t, func() {
		rt.openInput("bench", "  Entered Name  ")
		rt.handleKey(uv.KeyPressEvent{Code: uv.KeyEnter})
		rt.handleBenchOperationDone(event{token: rt.pinGen, data: []byte(`{"name":"Stored 界 Name"}`)})
		rt.handleManagementLoaded(event{token: rt.management.gen, binding: rt.binding, err: errors.New("temporary list failure")})
	})
	if rt.management.highlightName != "Stored 界 Name" || rt.selectedManagedBench().ID != "bench" || rt.management.error == "" || rt.benchOperation != nil || rt.browser.snapshot != original {
		t.Fatal("failed creation refresh guessed a highlight, selected a Bench or lost the native receipt")
	}
	refreshed := &benchManagement{Version: 1, CurrentBenchID: "bench", Benches: []managedBench{{ID: "new-id", Name: "Stored 界 Name", ContentsDigest: "new-empty", Pins: []BenchPin{}, Queries: []string{}}}}
	refreshed.Benches = append(refreshed.Benches, rt.management.snapshot.Benches...)
	captureStdout(t, func() {
		rt.handleKey(uv.KeyPressEvent{Code: 'r', Text: "r"})
		rt.handleManagementLoaded(event{token: rt.management.gen, binding: rt.binding, management: refreshed})
	})
	if rt.selectedManagedBench().ID != "new-id" || rt.management.snapshot.CurrentBenchID != "bench" || rt.management.error != "" || rt.management.highlightName != "" || rt.benchOperation != nil || rt.pinCancel != nil || rt.browser.snapshot != original {
		t.Fatal("successful retry did not highlight the real created Bench while preserving the current pointer")
	}
}

func TestBenchOperationCompletionCannotCrossActorChange(t *testing.T) {
	rt := managementFixture(t)
	captureStdout(t, func() {
		rt.selectManagedBench()
		rt.binding.IdentityID = "another-actor"
		rt.handleBenchOperationDone(event{token: rt.pinGen, data: []byte(`{"name":"My 界 Bench"}`)})
	})
	if !rt.reconnectRequired || rt.browser.snapshot != nil || rt.management.snapshot != nil || rt.configuration != nil || rt.form != nil || rt.pinCancel != nil || rt.management.cancel != nil {
		t.Fatal("mutation completion crossed an actor change or resurrected prior actor data")
	}
}
