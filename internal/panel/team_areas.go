package panel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
)

// TeamAreaCandidates is a native read of the configured team's official paths,
// not workspace defaults and not permission to change the configured team.
type TeamAreaCandidates struct {
	Version        int         `json:"version"`
	BenchID        string      `json:"benchId"`
	BenchName      string      `json:"benchName"`
	BindingID      string      `json:"bindingId"`
	IdentityID     string      `json:"identityId"`
	Team           string      `json:"team"`
	Areas          []BenchArea `json:"areas"`
	SettingsDigest string      `json:"settingsDigest"`
}

type teamAreaState struct {
	open           bool
	snapshot       *TeamAreaCandidates
	benchID        string
	benchName      string
	team           string
	binding        HostBinding
	settingsDigest string
	cancel         context.CancelFunc
	gen            uint64
	error          string
	selected       int
	offset         int
	maxOffset      int
}

func parseTeamAreas(output []byte, binding HostBinding, benchID, digest string) (*TeamAreaCandidates, error) {
	var snapshot TeamAreaCandidates
	if err := json.Unmarshal(output, &snapshot); err != nil {
		return nil, fmt.Errorf("invalid team area candidates JSON: %w", err)
	}
	if snapshot.Version != 1 || snapshot.BenchID == "" || strings.TrimSpace(snapshot.BenchName) == "" || strings.TrimSpace(snapshot.Team) == "" || snapshot.Areas == nil || snapshot.SettingsDigest == "" {
		return nil, errors.New("unsupported team area candidates: install a matching native companion with bench configuration area candidates v1")
	}
	if snapshot.BindingID != binding.BindingID || snapshot.IdentityID != binding.IdentityID {
		return nil, errors.New("binding-changed/reconnect: team area candidates do not match the admitted origin")
	}
	if snapshot.BenchID != benchID || snapshot.SettingsDigest != digest {
		return nil, errors.New("Bench or automatic settings changed while reading team areas. Refresh Bench and retry; no settings were changed.")
	}
	for _, area := range snapshot.Areas {
		if strings.TrimSpace(area.Path) == "" {
			return nil, errors.New("invalid native team area path")
		}
	}
	return &snapshot, nil
}

func (rt *runtime) cancelTeamAreaRead() {
	rt.teamAreas.gen++
	if rt.teamAreas.cancel != nil {
		rt.teamAreas.cancel()
		rt.teamAreas.cancel = nil
	}
}

// Closing can keep a completed read, but only its captured origin/Bench/settings
// may reopen it. Cancellation at an origin/mode boundary discards the capture.
func (rt *runtime) closeTeamAreas() {
	rt.cancelTeamAreaRead()
	rt.teamAreas.open = false
}

func (rt *runtime) cancelTeamAreas() {
	rt.cancelTeamAreaRead()
	rt.teamAreas = teamAreaState{gen: rt.teamAreas.gen}
}

func (rt *runtime) teamAreasCurrent() bool {
	s := &rt.teamAreas
	return !rt.closing && !rt.reconnectRequired && rt.mode == "bench" && rt.configuration != nil &&
		rt.configuration.section == 1 && rt.form == nil && rt.matchesCapture(s.benchID, s.binding, s.settingsDigest)
}

// The browser replacement boundary invalidates a cached read even when closed.
func (rt *runtime) reconcileTeamAreas() {
	if rt.teamAreas.benchID != "" && !rt.matchesCapture(rt.teamAreas.benchID, rt.teamAreas.binding, rt.teamAreas.settingsDigest) {
		rt.cancelTeamAreas()
	}
}

func (rt *runtime) openTeamAreas() {
	if rt.closing || rt.reconnectRequired || rt.mode != "bench" || rt.configuration == nil || rt.configuration.section != 1 || rt.form != nil || rt.pinCancel != nil || rt.syncCancel != nil {
		return
	}
	snapshot := rt.browser.snapshot
	if snapshot == nil {
		rt.setNotice("Wait for the Bench to load.", 0)
		rt.draw()
		return
	}
	if err := validateConfiguration(snapshot.Configuration); err != nil {
		rt.setError(err.Error())
		rt.draw()
		return
	}
	rt.cancelQueuedViews()
	rt.cancelReviewLookup(errors.New("Review superseded by team area selection"))
	rt.showBrowserHelp = false
	if rt.matchesCapture(rt.teamAreas.benchID, rt.teamAreas.binding, rt.teamAreas.settingsDigest) && rt.teamAreas.snapshot != nil {
		rt.teamAreas.open = true
		rt.revealTeamAreaSelection()
		rt.draw()
		return
	}
	rt.cancelTeamAreas()
	s := &rt.teamAreas
	s.open, s.benchID, s.benchName = true, snapshot.BenchID, snapshot.BenchName
	s.binding, s.settingsDigest, s.team = rt.binding, snapshot.Configuration.SettingsDigest, snapshot.EffectiveTeam
	rt.beginTeamAreasRead()
}

func (rt *runtime) beginTeamAreasRead() {
	if !rt.teamAreas.open || !rt.teamAreasCurrent() || rt.pinCancel != nil || rt.syncCancel != nil {
		return
	}
	rt.cancelTeamAreaRead()
	s := &rt.teamAreas
	s.snapshot, s.error = nil, ""
	s.offset = 0
	gen, binding, benchID, digest := s.gen, s.binding, s.benchID, s.settingsDigest
	ctx, cancel := context.WithTimeout(rt.ctx, 30*time.Second)
	s.cancel = cancel
	rt.draw()
	go func() {
		err := rt.semanticAuthority(ctx, binding)
		var snapshot *TeamAreaCandidates
		if err == nil {
			var output []byte
			output, err = nativeOutput(ctx, rt.nativePath, rt.cfg.Cwd, semanticArgs([]string{"bench", "configuration", "area", "candidates", "-o", "json", "--expect-bench", benchID}, binding)...)
			if err == nil {
				snapshot, err = parseTeamAreas(output, binding, benchID, digest)
			}
		}
		admissionErr := rt.postSemanticAdmission(binding)
		rt.emit(event{kind: evTeamAreasLoaded, token: gen, teamAreas: snapshot, binding: binding, err: err, admissionErr: admissionErr})
	}()
}

func (rt *runtime) handleTeamAreasLoaded(ev event) {
	s := &rt.teamAreas
	if !s.open || ev.token != s.gen || s.cancel == nil {
		return
	}
	// A superseded callback never draws, retargets a form or revives old data.
	if !rt.teamAreasCurrent() {
		rt.cancelTeamAreas()
		return
	}
	s.cancel()
	s.cancel = nil
	if ev.admissionErr != nil {
		rt.stopForReconnect(ev.admissionErr)
		return
	}
	if !sameManagementOrigin(ev.binding, s.binding) {
		rt.stopForReconnect(errors.New("binding-changed/reconnect: team area read origin changed"))
		return
	}
	if ev.err == nil && ev.teamAreas == nil {
		ev.err = errors.New("missing native team area candidates")
	}
	if ev.err == nil {
		// Also validate the event payload at the publication boundary, so a
		// wrong-origin or changed-settings observation cannot become selectable.
		candidate := ev.teamAreas
		if candidate.BindingID != s.binding.BindingID || candidate.IdentityID != s.binding.IdentityID {
			ev.err = errors.New("binding-changed/reconnect: team area candidates changed origin")
		} else if candidate.BenchID != s.benchID || candidate.SettingsDigest != s.settingsDigest {
			ev.err = errors.New("Bench or automatic settings changed while reading team areas. Refresh Bench and retry; no settings were changed.")
		}
	}
	if ev.err != nil {
		if bindingChanged(ev.err.Error()) {
			rt.stopForReconnect(ev.err)
			return
		}
		s.snapshot, s.error = nil, ev.err.Error()
	} else {
		s.snapshot, s.team, s.error = ev.teamAreas, ev.teamAreas.Team, ""
		s.selected = max(0, min(s.selected, max(len(s.snapshot.Areas)-1, 0)))
		rt.revealTeamAreaSelection()
	}
	rt.draw()
}

func (s *teamAreaState) selectedPick() (BenchArea, bool) {
	if s.snapshot == nil || s.selected < 0 || s.selected >= len(s.snapshot.Areas) {
		return BenchArea{}, false
	}
	return s.snapshot.Areas[s.selected], true
}

func (rt *runtime) selectTeamArea(index int) {
	s := &rt.teamAreas
	if !s.open || !rt.teamAreasCurrent() || rt.pinCancel != nil || rt.syncCancel != nil {
		return
	}
	s.selected = index
	pick, ok := s.selectedPick()
	if !ok {
		return
	}
	benchID, binding, digest := s.benchID, s.binding, s.settingsDigest
	rt.closeTeamAreas()
	rt.openInput("area", pick.Path)
	if rt.form != nil && rt.form.kind == "area" && rt.matchesCapture(benchID, binding, digest) {
		rt.form.benchID, rt.form.binding, rt.form.settingsDigest = benchID, binding, digest
		rt.form.choice = 0
		if !pick.IncludeChildren {
			rt.form.choice = 1
		}
		rt.form.confirm = true
		rt.draw()
	}
}

func (rt *runtime) handleTeamAreasKey(key uv.KeyPressEvent) bool {
	if !rt.teamAreas.open {
		return false
	}
	if !rt.teamAreasCurrent() {
		rt.cancelTeamAreas()
		return true
	}
	s := &rt.teamAreas
	switch {
	case key.MatchString("esc", "b", "c"):
		rt.closeTeamAreas()
		rt.draw()
	case key.MatchString("q"):
		rt.shutdown()
	case key.MatchString("r"):
		rt.beginTeamAreasRead()
	case key.MatchString("enter"):
		rt.selectTeamArea(s.selected)
	case key.MatchString("j", "down", "k", "up", "home", "end"):
		count := 0
		if s.snapshot != nil {
			count = len(s.snapshot.Areas)
		}
		switch {
		case key.MatchString("j", "down"):
			s.selected++
		case key.MatchString("k", "up"):
			s.selected--
		case key.MatchString("home"):
			s.selected = 0
			s.offset = 0
		case key.MatchString("end"):
			s.selected = count - 1
		}
		s.selected = max(0, min(s.selected, max(count-1, 0)))
		rt.revealTeamAreaSelection()
		rt.draw()
	case key.MatchString("pgup", "pgdown"):
		step := max(rt.contentVisibleRows()-1, 1)
		if key.MatchString("pgup") {
			step = -step
		}
		s.offset = max(0, min(s.offset+step, s.maxOffset))
		rt.draw()
	}
	return true
}

func (rt *runtime) teamAreaLines(cols int) []configurationLine {
	s := &rt.teamAreas
	team := safe(s.team)
	if team == "" {
		team = "native lookup pending"
	}
	lines := []configurationLine{
		{text: "Team areas · " + safe(s.benchName), row: -1},
		{text: "Configured team: " + team, row: -1},
		{text: "Select one official team path for this Bench. This never changes the team or workspace area defaults.", row: -1},
		{text: "↑/↓ select · PgUp/PgDn scroll · Enter review · r retry · Esc cancel", row: -1},
		{text: "", row: -1},
	}
	switch {
	case s.cancel != nil:
		lines = append(lines, configurationLine{text: "Loading configured team area paths…", row: -1})
	case s.error != "":
		lines = append(lines, configurationLine{text: "Refused: " + safe(s.error), row: -1})
	case s.snapshot != nil && len(s.snapshot.Areas) == 0:
		lines = append(lines, configurationLine{text: "The configured team has no area paths. Nothing was imported. Use manual area input if needed.", row: -1})
	case s.snapshot != nil:
		for i, area := range s.snapshot.Areas {
			prefix, semantics := "  ", "Exact · this area only"
			if i == s.selected {
				prefix = "› "
			}
			if area.IncludeChildren {
				semantics = "Under · include descendants"
			}
			status := "not in this Bench"
			if rt.browser.snapshot != nil && rt.browser.snapshot.Configuration != nil {
				for _, present := range rt.browser.snapshot.Configuration.Areas {
					if strings.EqualFold(present.Path, area.Path) {
						status = "already present"
						if present.IncludeChildren != area.IncludeChildren {
							status = "present as Exact; review changes to Under"
							if present.IncludeChildren {
								status = "present as Under; review changes to Exact"
							}
						}
						break
					}
				}
			}
			lines = append(lines, configurationLine{text: prefix + safe(area.Path), action: "pick", row: i}, configurationLine{text: "  " + semantics + " · " + status, action: "pick", row: i})
		}
	}
	lines = append(lines, configurationLine{text: "", row: -1}, configurationLine{text: "[r Retry team lookup]", action: "retry", row: -1}, configurationLine{text: "[Esc Cancel]", action: "cancel", row: -1})
	return wrapConfigurationLines(lines, cols)
}

func (rt *runtime) revealTeamAreaSelection() {
	s := &rt.teamAreas
	lines := rt.teamAreaLines(rt.contentCols())
	visible := rt.contentVisibleRows()
	s.maxOffset = max(len(lines)-visible, 0)
	start, end := -1, -1
	for i, line := range lines {
		if line.action == "pick" && line.row == s.selected {
			if start < 0 {
				start = i
			}
			end = i + 1
		}
	}
	if start >= 0 && start < s.offset {
		s.offset = start
	} else if end > s.offset+visible {
		s.offset = max(start, end-visible)
	}
	s.offset = max(0, min(s.offset, s.maxOffset))
}

func (rt *runtime) drawTeamAreas(out *strings.Builder, cols, visible int) {
	s := &rt.teamAreas
	lines := rt.teamAreaLines(cols)
	s.maxOffset = max(len(lines)-visible, 0)
	s.offset = max(0, min(s.offset, s.maxOffset))
	for y := range visible {
		fmt.Fprintf(out, "\x1b[%d;1H\x1b[2K", y+contentStartRow+1)
		if s.offset+y >= len(lines) {
			continue
		}
		line := lines[s.offset+y]
		style := rt.configurationLineStyle(line)
		if line.action == "pick" && line.row == s.selected {
			style = "\x1b[1;48;2;70;49;91m\x1b[38;2;255;246;255m"
		}
		drawBar(out, line.text, cols, style)
		if line.action != "" {
			rt.hits = append(rt.hits, hitTarget{x1: 0, x2: cols, y: y + contentStartRow, action: "team-area-" + line.action, row: line.row})
		}
	}
}

func (rt *runtime) handleTeamAreasMouse(mouse uv.Mouse) {
	if !rt.teamAreas.open || !rt.teamAreasCurrent() {
		return
	}
	if mouse.Button == uv.MouseWheelUp || mouse.Button == uv.MouseWheelDown {
		step := 3
		if mouse.Button == uv.MouseWheelUp {
			step = -step
		}
		rt.teamAreas.offset = max(0, min(rt.teamAreas.offset+step, rt.teamAreas.maxOffset))
		rt.draw()
		return
	}
	if mouse.Button != uv.MouseLeft {
		return
	}
	for _, hit := range rt.hits {
		if mouse.X < hit.x1 || mouse.X >= hit.x2 || mouse.Y != hit.y {
			continue
		}
		switch hit.action {
		case "team-area-confirm":
			rt.selectTeamArea(rt.teamAreas.selected)
		case "team-area-pick":
			rt.teamAreas.selected = hit.row
			rt.revealTeamAreaSelection()
			rt.draw()
		case "team-area-retry":
			rt.beginTeamAreasRead()
		case "team-area-cancel":
			rt.closeTeamAreas()
			rt.draw()
		}
		return
	}
}
