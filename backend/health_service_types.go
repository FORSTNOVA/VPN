package main

import (
	"log"
	"time"
)

// State is supplied by the locked app; clocks and effects are explicit.
// Production enters only through patrol/failed/unblock with a nonzero Now.
// Maps are detached snapshots. Observations commit through narrow authority
// commands before subsequent platform effects.
// Only SaveSettings observes a complete intermediate snapshot. Other effects
// observe their explicit arguments; scalar state is committed after the use case.
// Zero-Now synthetic requests are not equivalent to the legacy event clock
// fallback and must not be used to claim arbitrary-time compatibility.
type healthServiceState struct {
	Health                                       map[string]nodeHealth
	Regions                                      map[string]regionRecord
	Params                                       healthParams
	LockedRegion, LockedNode, SettingsLockedNode string
	ChainEnabled, Blocked, StoreAvailable        bool
	BlockReason, SweepNote                       string
	LastSwitch                                   time.Time
}
type healthServiceRequest struct {
	Kind, Active string
	Delays       map[string]int
	Now          time.Time
}
type healthServiceEffects interface {
	MeasureGroup(time.Duration) (map[string]int, error)
	ConfirmTraffic() bool
	SwitchGroup(string) error
	SaveHealth(nodeHealth) error
	SaveSettings(healthServiceState) error
	AppendSwitchEvent(switchEvent) error
	Logf(string, ...any)
}

type healthAuthorityEffects interface {
	CommitHealth(nodeHealth)
	CommitControl(healthServiceState)
}

func healthStateFromApp(a *app) healthServiceState {
	h := a.healthDomainLocked()
	return healthServiceState{Health: h.Health, Regions: h.Regions, Params: h.Params, LockedRegion: a.controlMetaLocked().RuntimeLockedRegion, LockedNode: a.controlMetaLocked().RuntimeLockedNode, SettingsLockedNode: a.controlMetaLocked().LockedNode, ChainEnabled: a.chainEnabledLocked(), Blocked: h.Blocked, StoreAvailable: a.store != nil, BlockReason: h.BlockReason, SweepNote: h.SweepNote, LastSwitch: h.LastSwitch}
}

// Commit control fields only. Observation maps are never written back from a
// planner snapshot: each observation uses CommitHealth at its original effect.
func applyHealthServiceState(a *app, s healthServiceState) {
	h := a.healthMetaLocked()
	if h.Blocked != s.Blocked {
		a.setHealthBlockedLocked(s.Blocked)
	}
	if h.BlockReason != s.BlockReason {
		a.setHealthReasonLocked(s.BlockReason)
	}
	if h.SweepNote != s.SweepNote {
		a.setHealthNoteLocked(s.SweepNote)
	}
	if h.LastSwitch != s.LastSwitch {
		a.setHealthLastSwitchLocked(s.LastSwitch)
	}
	a.patchControlLocked(controlRuntimeLockedRegion, serviceControlState{RuntimeLockedRegion: s.LockedRegion})
	a.patchControlLocked(controlRuntimeLockedNode, serviceControlState{RuntimeLockedNode: s.LockedNode})
	a.patchControlLocked(controlLockedNode, serviceControlState{LockedNode: s.SettingsLockedNode})
	if c := a.borrowChainsLocked().Chain; c.Enabled != s.ChainEnabled {
		c.Enabled = s.ChainEnabled
		a.setActiveChainLocked(c)
	}
}

type appHealthEffects struct{ a *app }

func (e appHealthEffects) CommitHealth(v nodeHealth)          { e.a.writeHealthRecordLocked(v.Name, v) }
func (e appHealthEffects) CommitControl(s healthServiceState) { applyHealthServiceState(e.a, s) }

func (e appHealthEffects) MeasureGroup(d time.Duration) (map[string]int, error) {
	return e.a.groupDelaysForURLLocked(patrolTargetPrimary, d)
}
func (e appHealthEffects) ConfirmTraffic() bool            { return e.a.confirmTrafficFlowsLocked() }
func (e appHealthEffects) SwitchGroup(target string) error { return e.a.switchGroupLocked(target) }
func (e appHealthEffects) SaveHealth(record nodeHealth) error {
	if e.a.store == nil {
		return nil
	}
	return e.a.store.saveHealth(record)
}
func (e appHealthEffects) SaveSettings(s healthServiceState) error {
	applyHealthServiceState(e.a, s)
	return e.a.saveSettings()
}
func (e appHealthEffects) AppendSwitchEvent(event switchEvent) error {
	if e.a.store == nil {
		return nil
	}
	return e.a.store.appendSwitchEvent(event)
}
func (e appHealthEffects) Logf(f string, v ...any) { log.Printf(f, v...) }
func runAppHealthService(a *app, kind, active string, delays map[string]int, now time.Time) {
	s := healthStateFromApp(a)
	fx := &ownedHealthControlEffects{appHealthEffects: appHealthEffects{a}, previous: s}
	runSelectedHealthService(&s, healthServiceRequest{kind, active, delays, now}, fx)
	fx.CommitControl(s)
}

// Invocation baseline is not live authority: only deliberate planner changes
// are committed, never unrelated fields from an old composite snapshot.
type ownedHealthControlEffects struct {
	appHealthEffects
	previous healthServiceState
}

func (e *ownedHealthControlEffects) CommitControl(s healthServiceState) {
	old := e.previous
	e.previous = s
	var mask uint64
	if old.LockedRegion != s.LockedRegion {
		mask |= controlRuntimeLockedRegion
	}
	if old.LockedNode != s.LockedNode {
		mask |= controlRuntimeLockedNode
	}
	if old.SettingsLockedNode != s.SettingsLockedNode {
		mask |= controlLockedNode
	}
	e.a.patchControlLocked(mask, serviceControlState{RuntimeLockedRegion: s.LockedRegion, RuntimeLockedNode: s.LockedNode, LockedNode: s.SettingsLockedNode})
	// Existing health-field commits remain narrow. Never use the old control pin
	// in this helper to restore a concurrently changed pin or Go chain graph.
	h := e.a.healthMetaLocked()
	if h.Blocked != s.Blocked {
		e.a.setHealthBlockedLocked(s.Blocked)
	}
	if h.BlockReason != s.BlockReason {
		e.a.setHealthReasonLocked(s.BlockReason)
	}
	if h.SweepNote != s.SweepNote {
		e.a.setHealthNoteLocked(s.SweepNote)
	}
	if h.LastSwitch != s.LastSwitch {
		e.a.setHealthLastSwitchLocked(s.LastSwitch)
	}
}
func (e *ownedHealthControlEffects) SaveSettings(s healthServiceState) error {
	e.CommitControl(s)
	return e.a.saveSettings()
}

func (e *ownedHealthControlEffects) CommitPin(s healthServiceState) {
	e.previous.LockedNode = s.LockedNode
	e.previous.SettingsLockedNode = s.SettingsLockedNode
	e.a.patchControlLocked(controlRuntimeLockedNode|controlLockedNode, serviceControlState{RuntimeLockedNode: s.LockedNode, LockedNode: s.SettingsLockedNode})
}
