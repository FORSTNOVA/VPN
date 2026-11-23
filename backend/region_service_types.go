package main

import (
	"log"
	"net/http"
	"strings"
	"time"
)

type regionServiceRequest struct{ Op, Country string }
type regionServiceResult struct {
	Status  int
	Message string
	Data    map[string]any
}
type regionServiceState struct {
	Chain, Blocked                                   bool
	LockedRegion, LockedNode, BlockReason, SweepNote string
	LastSwitch                                       time.Time
	Regions                                          map[string]regionRecord
	Health                                           map[string]nodeHealth
	Job                                              regionJob
}
type regionEffect struct {
	Kind, Country, Name, Path, Message string
	Now                                time.Time
	Event                              switchEvent
}
type regionReply struct {
	State *regionServiceState
	Err   error
	Bool  bool
	Value any
	Now   time.Time
}
type regionServiceEffects interface {
	Call(regionEffect) regionReply
	Release()
}

func cloneRegionServiceState(s *regionServiceState) *regionServiceState {
	if s == nil {
		return nil
	}
	out := *s
	if s.Regions != nil {
		out.Regions = make(map[string]regionRecord, len(s.Regions))
		for name, r := range s.Regions {
			if r.Sources != nil {
				r.Sources = append([]string{}, r.Sources...)
			}
			out.Regions[name] = r
		}
	}
	if s.Health != nil {
		out.Health = make(map[string]nodeHealth, len(s.Health))
		for name, h := range s.Health {
			out.Health[name] = h
		}
	}
	return &out
}
func regionStateFromApp(a *app) *regionServiceState {
	h := a.healthDomainLocked()
	s := &regionServiceState{Chain: a.chainEnabledLocked(), Blocked: h.Blocked, LockedRegion: a.controlMetaLocked().RuntimeLockedRegion, LockedNode: a.controlMetaLocked().RuntimeLockedNode, BlockReason: h.BlockReason, SweepNote: h.SweepNote, LastSwitch: h.LastSwitch, Regions: h.Regions, Health: h.Health}
	if h.Job != nil {
		s.Job = *h.Job
	}
	return s
}

type appRegionEffects struct {
	a       *app
	owned   bool
	now     func() time.Time
	save    func() error
	running func() bool
	mihomo  func(string, string, any) (map[string]any, error)
	block   func(string, time.Time, string)
	event   func(switchEvent)
	logger  func(string)
}

func newAppRegionEffects(a *app) *appRegionEffects {
	return &appRegionEffects{a: a, now: time.Now, save: a.saveSettings, running: a.kernelRunningLocked, mihomo: a.mihomoRequest, block: a.blockLocked, event: a.recordSwitchLocked, logger: func(s string) { log.Print(s) }}
}
func (e *appRegionEffects) Release() {
	if e.owned {
		e.owned = false
		e.a.mu.Unlock()
	}
}
func (e *appRegionEffects) Call(q regionEffect) (r regionReply) {
	if q.Kind == "lock" {
		if e.owned {
			panic("region lock already owned")
		}
		e.a.mu.Lock()
		e.owned = true
		r.State = regionStateFromApp(e.a)
		return
	}
	if !e.owned {
		panic("region effect outside lock")
	}
	defer func() { r.State = regionStateFromApp(e.a) }()
	switch q.Kind {
	case "now":
		r.Now = e.now()
	case "set-country":
		e.a.patchControlLocked(controlRuntimeLockedRegion, serviceControlState{RuntimeLockedRegion: q.Country})
		e.a.patchControlLocked(controlLockedRegion, serviceControlState{LockedRegion: q.Country})
	case "set-node":
		e.a.patchControlLocked(controlRuntimeLockedNode, serviceControlState{RuntimeLockedNode: q.Name})
		e.a.patchControlLocked(controlLockedNode, serviceControlState{LockedNode: q.Name})
	case "save":
		r.Err = e.save()
	case "running":
		r.Bool = e.running()
	case "get":
		var group map[string]any
		group, r.Err = e.mihomo(http.MethodGet, q.Path, nil)
		r.Value = group["now"]
	case "put":
		_, r.Err = e.mihomo(http.MethodPut, q.Path, map[string]string{"name": q.Name})
	case "block":
		e.block("", q.Now, q.Message)
	case "event":
		e.event(q.Event)
	case "log":
		e.logger(q.Message)
	default:
		panic("unknown region effect: " + q.Kind)
	}
	return
}
func (a *app) runRegionService(q regionServiceRequest) (result regionServiceResult) {
	defer func() {
		if v := recover(); v != nil {
			log.Printf("region service interrupted: %v", v)
			result = regionServiceResult{Status: 500, Message: "region state unavailable"}
		}
	}()
	if q.Op == "lock" {
		q.Country = strings.ToUpper(strings.TrimSpace(q.Country))
	}
	fx := newAppRegionEffects(a)
	defer fx.Release()
	return runSelectedRegionService(q, fx)
}
func writeRegionServiceResult(w http.ResponseWriter, r regionServiceResult) {
	if r.Status != 200 {
		http.Error(w, r.Message, r.Status)
		return
	}
	writeJSON(w, r.Data)
}

type regionReportNode struct {
	Name       string   `json:"name"`
	Country    string   `json:"country,omitempty"`
	ExitIP     string   `json:"exitIp,omitempty"`
	Status     string   `json:"status"`
	Sources    []string `json:"sources,omitempty"`
	Detail     string   `json:"detail,omitempty"`
	VerifiedAt string   `json:"verifiedAt,omitempty"`
	Stale      bool     `json:"stale"`
}
type regionReportHealth struct {
	State               string `json:"state"`
	LatencyMs           int    `json:"latencyMs"`
	ConsecutiveFailures int    `json:"consecutiveFailures"`
	CooldownUntil       string `json:"cooldownUntil,omitempty"`
	UpdatedAt           string `json:"updatedAt,omitempty"`
}

// Projection only: ordering, stale classification and candidate pool come from the selected planner.
func projectRegionReport(s *regionServiceState, names []string, stale []bool, pool []string) map[string]any {
	payload := make([]regionReportNode, 0, len(names))
	for i, name := range names {
		r := s.Regions[name]
		sources := r.Sources
		if sources != nil {
			sources = append([]string{}, sources...)
		}
		payload = append(payload, regionReportNode{Name: name, Country: r.Country, ExitIP: r.ExitIP, Status: r.Status, Sources: sources, Detail: r.Detail, VerifiedAt: timestamp(r.VerifiedAt), Stale: stale[i]})
	}
	if pool == nil {
		pool = []string{}
	} else {
		pool = append([]string{}, pool...)
	}
	health := make(map[string]regionReportHealth, len(s.Health))
	for name, h := range s.Health {
		health[name] = regionReportHealth{State: h.State, LatencyMs: h.LatencyMs, ConsecutiveFailures: h.ConsecutiveFailures, CooldownUntil: timestamp(h.CooldownUntil), UpdatedAt: timestamp(h.UpdatedAt)}
	}
	job := s.Job
	return map[string]any{"nodes": payload, "job": &job, "lockedRegion": s.LockedRegion, "pool": pool, "health": health, "ttlHours": int(regionTTL.Hours())}
}
