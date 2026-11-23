package main

import (
	"fmt"
	"time"
)

// The original Go use case is retained as the offline reference implementation.
// Its scratch app owns only supplied state, no kernel, DB, files or timers.
type goHealthUseCase struct {
	health                   map[string]nodeHealth
	regions                  map[string]regionRecord
	healthParams             healthParams
	settings                 settings
	lockedRegion, lockedNode string
	blocked                  bool
	blockReason, sweepNote   string
	lastSwitch               time.Time
	effects                  healthServiceEffects
	storeAvailable           bool
	now                      time.Time
}

func (a *goHealthUseCase) snapshot() healthServiceState {
	return healthServiceState{Health: a.health, Regions: a.regions, Params: a.healthParams, LockedRegion: a.lockedRegion, LockedNode: a.lockedNode, SettingsLockedNode: a.settings.LockedNode, ChainEnabled: a.settings.Chain.Enabled, Blocked: a.blocked, BlockReason: a.blockReason, SweepNote: a.sweepNote, LastSwitch: a.lastSwitch, StoreAvailable: a.storeAvailable}
}
func (a *goHealthUseCase) commitControl() {
	if fx, ok := a.effects.(healthAuthorityEffects); ok {
		fx.CommitControl(a.snapshot())
	}
}
func runGoHealthService(state *healthServiceState, request healthServiceRequest, effects healthServiceEffects) {
	service := &goHealthUseCase{health: state.Health, regions: state.Regions, healthParams: state.Params, settings: settings{LockedNode: state.SettingsLockedNode, Chain: proxyChain{Enabled: state.ChainEnabled}}, lockedRegion: state.LockedRegion, lockedNode: state.LockedNode, blocked: state.Blocked, blockReason: state.BlockReason, sweepNote: state.SweepNote, lastSwitch: state.LastSwitch, effects: effects, storeAvailable: state.StoreAvailable, now: request.Now}
	switch request.Kind {
	case "patrol":
		service.applyPatrolLocked(request.Active, request.Delays, request.Now)
	case "failed":
		service.applyFailedSweepLocked(request.Active, request.Now)
	case "unblock":
		service.tryUnblockLocked(request.Now)
	}
	*state = service.snapshot()
	service.commitControl()
}

func (a *goHealthUseCase) applyPatrolLocked(active string, delays map[string]int, now time.Time) {
	watched := append([]string{active}, candidatesForRegion(a.lockedRegion, a.regions, a.health, now)...)
	if a.settings.Chain.Enabled {
		watched = []string{active}
	}
	// A sweep that reached anything has put the measurement machinery back in
	// order, so an anomaly reported by an earlier one no longer describes now.
	for _, name := range watched {
		if delays[name] > 0 {
			a.sweepNote = ""
			a.commitControl()
			break
		}
	}
	a.recordSweepLocked(watched, delays, now)
	a.failoverIfUnhealthyLocked(active, now)
}

func (a *goHealthUseCase) applyFailedSweepLocked(active string, now time.Time) {
	a.recordSweepLocked([]string{active}, map[string]int{active: 0}, now)
	a.failoverIfUnhealthyLocked(active, now)
}

func (a *goHealthUseCase) recordSweepLocked(watched []string, delays map[string]int, now time.Time) {
	seen := map[string]bool{}
	for _, name := range watched {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		delay := delays[name]
		record := a.health[name]
		if record.Name == "" {
			record = newHealth(name)
		}
		previous := record.State
		next := nextHealthStateGo(record, delay > 0, delay, now, a.healthParams)
		a.recordHealthLocked(next)
		if previous != healthUnhealthy && next.State == healthUnhealthy {
			a.effects.Logf("node %s became %s after a failed sweep", name, next.State)
		}
	}
}

func (a *goHealthUseCase) failoverIfUnhealthyLocked(active string, now time.Time) {
	if a.settings.Chain.Enabled {
		return
	}
	if a.health[active].State != healthUnhealthy || a.lockedRegion == "" {
		return
	}
	if !a.dwellElapsedLocked(now) {
		return
	}
	a.failoverLocked(active, now, fmt.Sprintf("%s failed %d consecutive sweeps",
		active, a.health[active].ConsecutiveFailures))
}

func (a *goHealthUseCase) dwellElapsedLocked(now time.Time) bool {
	if a.lastSwitch.IsZero() {
		return true
	}
	return now.Sub(a.lastSwitch) >= time.Duration(a.healthParams.MinDwellSec)*time.Second
}

func (a *goHealthUseCase) failoverLocked(failed string, now time.Time, evidence string) {
	target, delay := a.pickFailoverTargetLocked(now)
	if target == "" {
		a.blockLocked(failed, now, "同地区已无可用节点")
		return
	}
	if err := a.effects.SwitchGroup(target); err != nil {
		a.blockLocked(failed, now, "无法切换到同地区备用节点")
		return
	}
	next := a.health[failed]
	next.Name = failed
	next.State = healthCooldown
	next.CooldownUntil = now.Add(time.Duration(a.healthParams.CooldownSec) * time.Second)
	next.ConsecutiveSuccesses = 0
	next.UpdatedAt = now
	a.recordHealthLocked(next)

	if err := a.effects.SaveSettings(a.snapshot()); err != nil {
		a.effects.Logf("could not persist the failover selection: %v", err)
	}
	a.lastSwitch = now
	a.blocked = false
	a.blockReason = ""
	a.lockedNode = target
	a.settings.LockedNode = target
	if fx, ok := a.effects.(interface{ CommitPin(healthServiceState) }); ok {
		fx.CommitPin(a.snapshot())
	}
	a.recordSwitchLocked(switchEvent{
		At: now, Group: "SmartVPN", FromNode: failed, ToNode: target,
		Trigger:  "unhealthy",
		Evidence: fmt.Sprintf("%s; %s answered in %d ms", evidence, target, delay),
	})
}

func (a *goHealthUseCase) pickFailoverTargetLocked(now time.Time) (string, int) {
	pool := candidatesForRegion(a.lockedRegion, a.regions, a.health, now)
	if len(pool) == 0 {
		return "", 0
	}
	timeout := time.Duration(a.healthParams.ConnectTimeoutMs) * time.Millisecond
	nodes := make([]proxyNode, 0, len(pool))
	delays := map[string]int{}
	for _, name := range pool {
		if health, ok := a.health[name]; ok && health.State == healthUnhealthy {
			continue
		}
		nodes = append(nodes, proxyNode{Name: name})
		if health, ok := a.health[name]; ok && health.LatencyMs > 0 {
			delays[name] = health.LatencyMs
		}
	}
	if best, delay := chooseReachableNodeGo(nodes, delays, ""); best != "" {
		return best, delay
	}
	// Nothing has a fresh measurement; ask Mihomo for the whole group once.
	fresh, err := a.effects.MeasureGroup(timeout)
	if err != nil {
		return "", 0
	}
	return chooseReachableNodeGo(nodes, fresh, "")
}

func (a *goHealthUseCase) noteSweepAnomalyLocked(failed string) {
	if a.sweepNote == "" {
		a.effects.Logf("the locked region measured as exhausted while a request through %s still got through; not blocking",
			failed)
	}
	a.sweepNote = "同地区节点在延迟测试里全部不通，但经当前节点的实际请求仍然通行，因此没有阻断；以实测为准。"
	a.commitControl()
}

func (a *goHealthUseCase) blockLocked(failed string, now time.Time, reason string) {
	if a.effects.ConfirmTraffic() {
		a.noteSweepAnomalyLocked(failed)
		return
	}
	if err := a.effects.SwitchGroup(blockedChoice); err != nil {
		a.effects.Logf("could not block traffic: %v", err)
		return
	}
	a.blocked = true
	a.blockReason = reason
	a.lastSwitch = now
	a.sweepNote = ""
	a.recordSwitchLocked(switchEvent{
		At: now, Group: "SmartVPN", FromNode: failed, ToNode: blockedChoice,
		Trigger: "region_exhausted", Evidence: reason,
	})
}

func (a *goHealthUseCase) tryUnblockLocked(now time.Time) {
	target, delay := a.pickFailoverTargetLocked(now)
	if target == "" {
		return
	}
	if err := a.effects.SwitchGroup(target); err != nil {
		return
	}
	a.blocked = false
	a.blockReason = ""
	a.sweepNote = ""
	a.lastSwitch = now
	a.lockedNode = target
	a.settings.LockedNode = target
	if fx, ok := a.effects.(interface{ CommitPin(healthServiceState) }); ok {
		fx.CommitPin(a.snapshot())
	}
	_ = a.effects.SaveSettings(a.snapshot())
	a.recordSwitchLocked(switchEvent{
		At: now, Group: "SmartVPN", FromNode: blockedChoice, ToNode: target,
		Trigger:  "region_recovered",
		Evidence: fmt.Sprintf("%s answered in %d ms", target, delay),
	})
}

func (a *goHealthUseCase) recordHealthLocked(record nodeHealth) {
	a.health[record.Name] = record
	if fx, ok := a.effects.(healthAuthorityEffects); ok {
		fx.CommitHealth(record)
	}
	if a.storeAvailable {
		if err := a.effects.SaveHealth(record); err != nil {
			a.effects.Logf("could not persist node health: %v", err)
		}
	}
}

func (a *goHealthUseCase) recordSwitchLocked(event switchEvent) {
	a.commitControl()
	if event.At.IsZero() {
		event.At = a.now
	}
	a.effects.Logf("node switch: %s -> %s (%s) %s", event.FromNode, event.ToNode, event.Trigger, event.Evidence)
	if a.storeAvailable {
		if err := a.effects.AppendSwitchEvent(event); err != nil {
			a.effects.Logf("could not persist switch event: %v", err)
		}
	}
}
