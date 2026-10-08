package panel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
)

func teamAreasFixture(t *testing.T) (*runtime, *TeamAreaCandidates) {
	t.Helper()
	rt, browser := browserFixture(t)
	rt.configuration = &configurationView{benchID: browser.BenchID, section: 1}
	snapshot := &TeamAreaCandidates{
		Version: 1, BenchID: browser.BenchID, BenchName: browser.BenchName,
		BindingID: rt.binding.BindingID, IdentityID: rt.binding.IdentityID,
		Team: "Configured Team", SettingsDigest: browser.Configuration.SettingsDigest,
		Areas: []BenchArea{{Path: "Project\\Exact", IncludeChildren: false}, {Path: "Project\\Under", IncludeChildren: true}},
	}
	rt.teamAreas = teamAreaState{
		open: true, snapshot: snapshot, benchID: browser.BenchID, benchName: browser.BenchName,
		binding: rt.binding, settingsDigest: browser.Configuration.SettingsDigest, team: snapshot.Team, gen: 4,
	}
	return rt, snapshot
}

func TestTeamAreaReadCannotBecomeSelectableAcrossOriginOrSettings(t *testing.T) {
	rt, snapshot := teamAreasFixture(t)
	for _, change := range []struct {
		name   string
		mutate func(*TeamAreaCandidates)
	}{
		{name: "other actor", mutate: func(s *TeamAreaCandidates) { s.IdentityID = "other-actor" }},
		{name: "other connection", mutate: func(s *TeamAreaCandidates) { s.BindingID = "other-binding" }},
		{name: "other Bench", mutate: func(s *TeamAreaCandidates) { s.BenchID = "other-Bench" }},
		{name: "changed settings", mutate: func(s *TeamAreaCandidates) { s.SettingsDigest = "new-settings" }},
	} {
		t.Run(change.name, func(t *testing.T) {
			candidate := *snapshot
			change.mutate(&candidate)
			encoded, err := json.Marshal(candidate)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := parseTeamAreas(encoded, rt.binding, snapshot.BenchID, snapshot.SettingsDigest); err == nil {
				t.Fatal("a read from outside the captured origin/Bench/settings was selectable")
			}
		})
	}
}

func TestSupersededTeamAreaResponseDoesNotRepaintOrOpenAnEditor(t *testing.T) {
	for _, change := range []struct {
		name   string
		mutate func(*runtime)
	}{
		{name: "settings changed", mutate: func(rt *runtime) { rt.browser.snapshot.Configuration.SettingsDigest = "new-settings" }},
		{name: "Bench changed", mutate: func(rt *runtime) { rt.browser.snapshot.BenchID = "other-Bench" }},
		{name: "origin changed", mutate: func(rt *runtime) { rt.binding.Snapshot = "new-origin" }},
		{name: "review opened", mutate: func(rt *runtime) { rt.mode = "review" }},
		{name: "left Areas", mutate: func(rt *runtime) { rt.configuration.section = 2 }},
		{name: "picker canceled", mutate: func(rt *runtime) { rt.closeTeamAreas() }},
	} {
		t.Run(change.name, func(t *testing.T) {
			rt, snapshot := teamAreasFixture(t)
			_, cancel := context.WithCancel(rt.ctx)
			rt.teamAreas.cancel = cancel
			ev := event{token: rt.teamAreas.gen, teamAreas: snapshot, binding: rt.binding}
			change.mutate(rt)
			paint := captureStdout(t, func() {
				rt.handleTeamAreasLoaded(ev)
				rt.handleTeamAreasLoaded(event{token: ev.token, err: errors.New("old failure"), binding: ev.binding})
			})
			if paint != "" || rt.form != nil || rt.pinCancel != nil {
				t.Fatal("a superseded response repainted the new view or opened/mutated a form")
			}
		})
	}
}

func TestTeamAreaEnterReviewsNativeSemanticsAndChangedSettingsRefuseConfirmation(t *testing.T) {
	for _, pick := range []struct {
		name   string
		index  int
		choice int
	}{
		{name: "exact", index: 0, choice: 1},
		{name: "under", index: 1, choice: 0},
	} {
		t.Run(pick.name, func(t *testing.T) {
			rt, snapshot := teamAreasFixture(t)
			rt.teamAreas.selected = pick.index
			captureStdout(t, func() { rt.handleTeamAreasKey(uv.KeyPressEvent{Code: uv.KeyEnter}) })
			f := rt.form
			if f == nil || f.kind != "area" || !f.confirm || f.choice != pick.choice || f.editor.text != snapshot.Areas[pick.index].Path || rt.pinCancel != nil || rt.teamAreas.open {
				t.Fatal("Enter did not review exactly the selected team path and its native semantics without applying")
			}
			rt.browser.snapshot.Configuration.SettingsDigest = "concurrent-settings"
			captureStdout(t, func() { rt.handleFormKey(uv.KeyPressEvent{Code: uv.KeyEnter}) })
			if rt.form != f || f.error == "" || rt.pinCancel != nil || len(rt.browser.snapshot.Configuration.Areas) != 0 {
				t.Fatal("changed settings were not refused by the existing captured review boundary")
			}
		})
	}
}

func TestChangedCaptureCannotRetargetSelectedTeamArea(t *testing.T) {
	rt, _ := teamAreasFixture(t)
	rt.browser.snapshot.Configuration.SettingsDigest = "concurrent-settings"
	captureStdout(t, func() { rt.selectTeamArea(0) })
	if rt.form != nil || rt.pinCancel != nil {
		t.Fatal("the selected official path was retargeted to newly captured settings")
	}
	rt.reconcileTeamAreas()
	if rt.teamAreas.open || rt.teamAreas.snapshot != nil {
		t.Fatal("an outdated closed/open picker retained its reusable capture")
	}
}

func TestTeamAreaFailedRefreshAndEmptyResponseCannotGuessPaths(t *testing.T) {
	rt, _ := teamAreasFixture(t)
	_, cancel := context.WithCancel(rt.ctx)
	rt.teamAreas.cancel = cancel
	captureStdout(t, func() {
		rt.handleTeamAreasLoaded(event{token: rt.teamAreas.gen, binding: rt.binding, err: errors.New("team unavailable")})
		rt.handleTeamAreasKey(uv.KeyPressEvent{Code: uv.KeyEnter})
	})
	if rt.teamAreas.error == "" || rt.teamAreas.snapshot != nil || rt.form != nil {
		t.Fatal("a failed team lookup fell back to old or inferred paths")
	}
	_, cancel = context.WithCancel(rt.ctx)
	rt.teamAreas.cancel = cancel
	empty := &TeamAreaCandidates{Version: 1, BenchID: rt.teamAreas.benchID, BenchName: rt.teamAreas.benchName, BindingID: rt.binding.BindingID, IdentityID: rt.binding.IdentityID, SettingsDigest: rt.teamAreas.settingsDigest, Team: "Configured Team", Areas: []BenchArea{}}
	captureStdout(t, func() {
		rt.handleTeamAreasLoaded(event{token: rt.teamAreas.gen, binding: rt.binding, teamAreas: empty})
		rt.handleTeamAreasKey(uv.KeyPressEvent{Code: uv.KeyEnter})
	})
	if rt.teamAreas.snapshot == nil || rt.teamAreas.error != "" || rt.form != nil || rt.pinCancel != nil {
		t.Fatal("an official empty read was treated as a failure or supplied a guessed path")
	}
}

func TestTeamAreaSelectionRevealsLongWrappedPathsAndCancelKeepsBench(t *testing.T) {
	rt, snapshot := teamAreasFixture(t)
	rt.size = size{cols: 18, rows: 10}
	snapshot.Areas = nil
	for i := range 12 {
		snapshot.Areas = append(snapshot.Areas, BenchArea{Path: fmt.Sprintf("Project\\Official\\Area %d with a long name", i), IncludeChildren: i%2 == 0})
	}
	captureStdout(t, func() { rt.handleTeamAreasKey(uv.KeyPressEvent{Code: uv.KeyEnd}) })
	if rt.teamAreas.selected != 11 || rt.teamAreas.offset == 0 {
		t.Fatal("End did not move the logical selection into the narrow scrolling viewport")
	}
	var view strings.Builder
	rt.hits = nil
	rt.drawTeamAreas(&view, rt.contentCols(), rt.contentVisibleRows())
	visibleSelection := false
	for _, hit := range rt.hits {
		if hit.action == "team-area-pick" && hit.row == 11 {
			visibleSelection = true
		}
	}
	if !visibleSelection {
		t.Fatal("a deeply wrapped selected path was left outside the visible viewport")
	}
	before := rt.browser.snapshot
	captureStdout(t, func() { rt.handleTeamAreasKey(uv.KeyPressEvent{Code: uv.KeyEscape}) })
	if rt.teamAreas.open || rt.form != nil || rt.browser.snapshot != before || rt.configuration == nil {
		t.Fatal("cancel changed membership or discarded the Bench Areas view")
	}
}
