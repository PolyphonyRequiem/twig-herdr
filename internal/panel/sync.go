package panel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type benchSyncResult struct {
	Kind              string `json:"kind"`
	BenchID           string `json:"benchId"`
	BenchName         string `json:"benchName"`
	ItemCount         int    `json:"itemCount"`
	MemberCount       int    `json:"memberCount"`
	RelationshipCount int    `json:"relationshipCount"`
	ProtectedCount    int    `json:"protectedCount"`
}

func parseBenchSyncResult(output []byte, expectedBench string) (*benchSyncResult, error) {
	// Missing/null counts cannot masquerade as a successful zero-item pull.
	result := benchSyncResult{ItemCount: -1, MemberCount: -1, RelationshipCount: -1, ProtectedCount: -1}
	if err := json.Unmarshal(output, &result); err != nil {
		return nil, fmt.Errorf("invalid Bench-scoped sync JSON: %w; install a matching twig-bench-native companion", err)
	}
	if result.Kind != "benchSync" || result.BenchID == "" || result.BenchID != expectedBench {
		return nil, errors.New("invalid Bench-scoped sync result: native response does not identify the captured Bench; install a matching twig-bench-native companion")
	}
	if result.ItemCount < 0 || result.MemberCount < 0 || result.RelationshipCount < 0 || result.ProtectedCount < 0 {
		return nil, errors.New("invalid Bench-scoped sync result: item, member, relationship and protected counts must be present and nonnegative")
	}
	if result.MemberCount > result.ItemCount || result.ProtectedCount > result.ItemCount || result.RelationshipCount != result.ItemCount-result.MemberCount {
		return nil, errors.New("invalid Bench-scoped sync result: scope counts are inconsistent")
	}
	return &result, nil
}

func (result *benchSyncResult) notice() string {
	return fmt.Sprintf("Sync: %d items · %d Bench members · %d context · %d protected · pull only",
		result.ItemCount, result.MemberCount, result.RelationshipCount, result.ProtectedCount)
}

// Sync only pulls ADO into the cache. Browsing never flushes pending edits.
func (rt *runtime) beginSync() {
	if rt.closing || rt.reconnectRequired {
		return
	}
	if rt.syncCancel != nil {
		return
	}
	if rt.pinCancel != nil {
		rt.setNotice("Wait for the pin change before syncing this Bench.", 0)
		rt.draw()
		return
	}
	rt.cancelManagement()
	rt.bench.launchGen++
	rt.cancelBenchLaunch(errors.New("bench refresh superseded by sync"))
	rt.syncGen++
	gen := rt.syncGen
	binding := rt.binding
	benchID := ""
	if rt.browser.snapshot != nil {
		benchID = rt.browser.snapshot.BenchID
	}
	ctx, cancel := context.WithTimeout(rt.ctx, 2*time.Minute)
	rt.syncCancel = cancel
	rt.setNotice("Syncing this Bench and relationship-rule candidates from ADO (pull only)…", 0)
	rt.draw()
	go func() {
		err := rt.semanticAuthority(ctx, binding)
		var result *benchSyncResult
		if err == nil && benchID == "" {
			// Review can be opened before the initial Bench read completes. Capture
			// its actual native Bench once, then guard that exact ID during sync.
			var output []byte
			output, err = nativeOutput(ctx, rt.nativePath, rt.cfg.Cwd, semanticArgs([]string{"workspace", "--view", "tree", "-o", "json", "--include-browser"}, binding)...)
			if err == nil {
				var snapshot *BrowserSnapshot
				snapshot, err = parseBrowser(output, binding)
				if err == nil {
					benchID = snapshot.BenchID
				}
			}
		}
		if err == nil {
			var output []byte
			output, err = nativeOutput(ctx, rt.nativePath, rt.cfg.Cwd, semanticArgs([]string{"workspace", "sync", "--expect-bench", benchID, "-o", "json"}, binding)...)
			if err == nil {
				result, err = parseBenchSyncResult(output, benchID)
			}
		}
		admissionCtx, admissionCancel := context.WithTimeout(rt.ctx, 15*time.Second)
		_, admissionErr := rt.readBinding(admissionCtx, rt.cfg.Cwd, binding.Snapshot)
		admissionCancel()
		rt.emit(event{kind: evSyncDone, token: gen, err: err, admissionErr: admissionErr, syncResult: result})
	}()
}

func (rt *runtime) cancelSync() {
	rt.syncGen++
	if rt.syncCancel != nil {
		rt.syncCancel()
		rt.syncCancel = nil
	}
}

func (rt *runtime) handleSyncDone(ev event) {
	if rt.closing || ev.token != rt.syncGen || rt.syncCancel == nil {
		return
	}
	rt.syncCancel()
	rt.syncCancel = nil
	if ev.admissionErr != nil {
		rt.stopForReconnect(ev.admissionErr)
		return
	}
	if ev.err == nil && ev.syncResult == nil {
		ev.err = errors.New("missing Bench-scoped sync result; no item counts were confirmed")
	}
	if ev.err != nil {
		if bindingChanged(ev.err.Error()) {
			rt.stopForReconnect(ev.err)
			return
		}
		rt.setError(fmt.Sprintf("Sync failed: %s", safe(ev.err.Error())))
		rt.draw()
		return
	}
	if rt.mode == "review" {
		rt.setNotice(ev.syncResult.notice()+"; review keeps its captured snapshot.", 0)
		rt.draw()
		return
	}
	rt.setNotice(ev.syncResult.notice(), 0)
	rt.beginBenchRefresh(nil, true)
	rt.beginManagementRefresh()
	rt.draw()
}
