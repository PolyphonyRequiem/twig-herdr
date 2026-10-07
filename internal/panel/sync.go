package panel

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Sync only pulls ADO into the cache. Browsing never flushes pending edits.
func (rt *runtime) beginSync() {
	if rt.closing || rt.reconnectRequired {
		return
	}
	if rt.syncCancel != nil {
		return
	}
	rt.bench.launchGen++
	rt.cancelBenchLaunch(errors.New("bench refresh superseded by sync"))
	rt.cancelBenchPending(errors.New("bench refresh superseded by sync"))
	rt.syncGen++
	gen := rt.syncGen
	snapshot := rt.binding.Snapshot
	ctx, cancel := context.WithTimeout(rt.ctx, 2*time.Minute)
	rt.syncCancel = cancel
	rt.setNotice("Syncing from ADO (pull only)…", 0)
	rt.draw()
	go func() {
		// Sync has no host-snapshot option in the shipped CLI. It resolves its own
		// connection and only updates that connection's cache; verify our original
		// attachment again before publishing completion or refreshing the view.
		_, err := twigOutput(ctx, rt.cfg.Cwd, "sync", "--pull-only")
		admissionCtx, admissionCancel := context.WithTimeout(rt.ctx, 15*time.Second)
		_, admissionErr := rt.readBinding(admissionCtx, rt.cfg.Cwd, snapshot)
		admissionCancel()
		rt.emit(event{kind: evSyncDone, token: gen, err: err, admissionErr: admissionErr})
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
		rt.setNotice("Synced from ADO; review keeps its captured snapshot.", 0)
		rt.draw()
		return
	}
	rt.setNotice("Synced from ADO (pull only).", 0)
	rt.beginBenchRefresh(nil, true)
	rt.draw()
}
