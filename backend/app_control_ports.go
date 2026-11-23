package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"
)

type appEventPayload struct {
	At       string `json:"at"`
	Group    string `json:"group"`
	FromNode string `json:"fromNode"`
	ToNode   string `json:"toNode"`
	Trigger  string `json:"trigger"`
	Evidence string `json:"evidence"`
}
type appControlEffects struct {
	a                        *app
	q                        appServiceRequest
	w                        http.ResponseWriter
	hooks                    map[string]func(appServiceEffect) appServiceReply
	owned, responded         bool
	deferRelease             bool
	writerPanic              any
	h                        healthDomainProjection
	haveHealth               bool
	c                        serviceControlState
	china                    chinaIPStatus
	chain                    proxyChain
	chains                   map[string][]string
	resolved, selected, mode string
	lastParams               healthParams
	lastErr                  error
	now                      time.Time
	delays                   map[string]int
	events                   []switchEvent
	eventPayload             []appEventPayload
	cancel                   <-chan struct{}
	pendingCancel            chan struct{}
	ticker                   <-chan time.Time
	stopTicker               func()
	newTicker                func(time.Duration) (<-chan time.Time, func())
	clock                    func() time.Time
	spawn                    func(<-chan struct{}, time.Duration)
	once                     func()
}

func newAppControlEffects(a *app, q appServiceRequest) *appControlEffects {
	return &appControlEffects{a: a, q: q, clock: time.Now, newTicker: func(d time.Duration) (<-chan time.Time, func()) { t := time.NewTicker(d); return t.C, t.Stop }, spawn: func(c <-chan struct{}, d time.Duration) { go a.patrolLoop(c, d) }, once: a.patrolOnce}
}
func (e *appControlEffects) releaseTicker() {
	if f := e.stopTicker; f != nil {
		e.stopTicker = nil
		f()
	}
}
func (e *appControlEffects) Release() {
	if e.deferRelease {
		return
	}
	defer func() {
		if e.owned {
			e.owned = false
			e.a.mu.Unlock()
		}
	}()
	defer e.releaseTicker()
	if c := e.pendingCancel; c != nil {
		e.pendingCancel = nil
		if e.a.patrolCancel == c {
			e.a.patrolCancel = nil
		}
		close(c)
	}
}
func (e *appControlEffects) Call(q appServiceEffect) (r appServiceReply) {
	if q.Kind == "response" {
		defer func() {
			if p := recover(); p != nil {
				if e.responded {
					e.writerPanic = p
					panic(appResponsePanic{p})
				}
				panic(p)
			}
		}()
	}
	if f := e.hooks[q.Kind]; f != nil {
		return f(q)
	}
	switch q.Kind {
	case "lock":
		if e.owned {
			panic("application control double lock")
		}
		e.a.mu.Lock()
		e.owned = true
	case "unlock":
		if e.deferRelease {
			return
		}
		if e.owned {
			e.owned = false
			e.a.mu.Unlock()
		}
	case "state-base":
		e.h = e.a.healthDomainLocked()
		e.haveHealth = true
		e.c = e.a.controlLocked()
		e.china = e.a.chinaIPStatusLocked()
		r.ProxyOn = e.a.proxyOn
		r.WebRTC = e.c.WebRTCProtection
		r.DNS = e.c.DNSProtection
		r.JobNil = e.h.Job == nil
	case "running":
		r.Flag = e.a.kernelRunningLocked()
	case "proxy":
		r.Flag, r.Err = e.a.systemProxyEnabled()
		e.lastErr = r.Err
	case "log":
		switch q.Text {
		case "proxy":
			log.Printf("could not check Windows proxy status: %v", e.lastErr)
		case "stop":
			log.Printf("could not clear the fail-closed selection: %v", e.lastErr)
		case "sweep":
			log.Printf("health sweep could not measure the group (%v); counting it as a failed sweep", e.lastErr)
		}
	case "active-get":
		var v map[string]any
		v, r.Err = e.a.mihomoRequest(http.MethodGet, "/proxies/"+q.Text, nil)
		r.Text, _ = v["now"].(string)
	case "active-region":
		r.Text = e.h.Regions[q.Text].Country
	case "tun-reason":
		r.Text = tunReason(e.a.tunUnavailableLocked())
	case "resolved":
		e.resolved = e.a.mihomoPath()
	case "choice":
		r.ChainEnabled = e.a.chainEnabledLocked()
		if !r.ChainEnabled {
			c := e.a.controlMetaLocked()
			r.SelectionMode = c.SelectionMode
			r.SelectedNode = c.SelectedNode
			r.LockedRegion = c.RuntimeLockedRegion
			r.LockedNode = c.RuntimeLockedNode
		}
	case "mode":
		r.Mode = e.a.controlMetaLocked().ConnectionMode
		r.ModeSupported = connectionModeSupported(r.Mode)
		r.DefaultMode = defaultConnectionMode()
	case "project-choice":
		e.selected = q.Text
		// The response is encoded under the app lock and borrows its resource graph.
		graph := e.a.borrowChainsLocked()
		e.chain, e.chains = graph.Chain, graph.Chains
	case "project-mode":
		e.mode = q.Text
	case "health-facts":
		if !e.haveHealth {
			e.h = e.a.healthDomainLocked()
			e.haveHealth = true
		}
		now := e.clock()
		r.Health = make([]appHealthFact, 0, len(e.h.Health))
		r.Regions = make([]appRegionFact, 0, len(e.h.Regions))
		for _, v := range e.h.Health {
			r.Health = append(r.Health, appHealthFact{v.State, v.cooldownActive(now)})
		}
		for name, v := range e.h.Regions {
			r.Regions = append(r.Regions, appRegionFact{Country: v.Country, Verified: v.Status == regionVerified, Expired: now.Sub(v.VerifiedAt) > regionTTL, Cooldown: e.h.Health[name].cooldownActive(now)})
		}
	case "pin":
		r.Text = e.a.controlMetaLocked().RuntimeLockedRegion
	case "meta":
		h := e.a.healthMetaLocked()
		r.Params = h.Params
		r.Flag = h.Blocked
		e.lastParams = h.Params
	case "commit-params":
		e.a.replaceHealthParamsLocked(q.Params)
	case "store-present":
		r.Flag = e.a.store != nil
	case "save-params":
		r.Err = e.a.store.saveHealthParams(q.Params)
	case "cancel-present":
		r.Flag = e.a.patrolCancel != nil
	case "cancel-close":
		c := e.a.patrolCancel
		e.a.patrolCancel = nil
		if e.pendingCancel == c {
			e.pendingCancel = nil
		}
		if c != nil {
			close(c)
		}
	case "cancel-new":
		c := make(chan struct{})
		e.pendingCancel = c
		e.a.patrolCancel = c
	case "spawn":
		c := e.pendingCancel
		e.pendingCancel = nil
		e.spawn(c, q.Interval)
	case "path-start":
		e.a.startPathMonitorLocked()
	case "job-running":
		r.Flag = e.a.regionJobRunningLocked()
	case "now":
		e.now = e.clock()
	case "delays":
		e.delays, r.Err = e.a.groupDelaysForURLLocked(patrolTargetPrimary, q.Interval)
		e.lastErr = r.Err
	case "sweep":
		if q.Count == 1 {
			e.a.applyFailedSweepLocked(q.Text, e.now)
		} else {
			e.a.applyPatrolLocked(q.Text, e.delays, e.now)
		}
	case "unblock":
		e.a.tryUnblockLocked(e.now)
	case "clear-blocked":
		e.a.setHealthBlockedLocked(false)
	case "clear-reason":
		e.a.setHealthReasonLocked("")
	case "switch":
		r.Err = e.a.switchGroupLocked(q.Text)
		e.lastErr = r.Err
	case "ticker-new":
		e.ticker, e.stopTicker = e.newTicker(q.Interval)
	case "wait":
		select {
		case <-e.cancel:
			r.Flag = false
		case <-e.ticker:
			r.Flag = true
		}
	case "ticker-stop":
		e.releaseTicker()
	case "once":
		e.once()
	case "parse-limit":
		r.Count, _ = strconv.Atoi(q.Text)
	case "events-read":
		e.events, r.Err = e.a.store.recentSwitchEvents(q.Count)
		r.Count = len(e.events)
		e.eventPayload = make([]appEventPayload, 0, len(e.events))
	case "events-project":
		v := e.events[q.Count]
		e.eventPayload = append(e.eventPayload, appEventPayload{v.At.Format(time.RFC3339), v.Group, v.FromNode, v.ToNode, v.Trigger, v.Evidence})
	case "response":
		e.respond(q)
	default:
		panic("unknown application effect: " + q.Kind)
	}
	return
}
func appHealthProjection(h healthDomainProjection, p appServicePlan) map[string]any {
	counts := map[string]int{}
	for _, v := range p.Counts {
		counts[v.Name] = v.Count
	}
	return map[string]any{"counts": counts, "params": h.Params, "lockedRegion": p.LockedRegion, "poolSize": p.PoolSize, "sweepNote": h.SweepNote}
}
func (e *appControlEffects) stateProjection(p appServicePlan) map[string]any {
	h, c := e.h, e.c
	job := h.Job
	if p.ZeroJob {
		job = &regionJob{}
	}
	return map[string]any{
		"connected": p.Running, "subscriptionUrl": c.SubscriptionURL, "mihomoPath": c.MihomoPath, "mihomoResolved": e.resolved,
		"home": e.a.home, "portable": e.a.portable, "proxyEnabled": p.ProxyEnabled, "mixedPort": e.a.mixedPort, "socksPort": c.SocksPort, "startedAt": e.a.started,
		"selectedNode": e.selected, "activeNode": p.ActiveNode, "chain": e.chain, "chains": e.chains, "activeRegion": p.ActiveRegion,
		"lockedRegion": c.RuntimeLockedRegion, "blocked": h.Blocked, "blockReason": h.BlockReason, "health": appHealthProjection(h, p), "regionJob": job,
		"connectionMode": e.mode, "strictPrivacy": p.StrictPrivacy, "domesticWeb": c.DomesticWeb, "gameBypass": c.GameBypass, "webRTCProtection": c.WebRTCProtection,
		"dnsProtection": c.DNSProtection, "wintunPath": c.WintunPath, "elevated": e.a.elevated, "tunAvailable": p.TunReason == "", "tunReason": p.TunReason, "tunActive": p.TunActive, "chinaDirect": e.china,
	}
}
func appControlError(key string) string {
	switch key {
	case "interval":
		return "the patrol interval must be at least 10 seconds"
	case "save-params":
		return "could not save the health parameters"
	case "connect":
		return "connect before running a health sweep"
	case "no-store":
		return "the local database is not available"
	case "events":
		return "could not read the switch history"
	}
	return "health state unavailable"
}
func (e *appControlEffects) respond(q appServiceEffect) {
	if q.Status != 200 {
		e.responded = true
		http.Error(e.w, appControlError(q.Text), q.Status)
		return
	}
	var data map[string]any
	switch q.Text {
	case "state":
		data = e.stateProjection(q.Plan)
	case "params":
		data = map[string]any{"ok": true, "params": e.lastParams}
	case "patrol":
		data = map[string]any{"ok": true, "health": appHealthProjection(e.h, q.Plan)}
	case "events":
		data = map[string]any{"events": e.eventPayload}
	default:
		panic("invalid app response")
	}
	if q.Text == "state" {
		b, err := json.Marshal(data)
		if err != nil {
			e.responded = true
			http.Error(e.w, "could not read service state", 500)
			return
		}
		b = append(b, '\n')
		e.responded = true
		_, _ = e.w.Write(b)
		return
	}
	e.responded = true
	writeJSON(e.w, data)
}
func runAppControlWithEffects(q appServiceRequest, e *appControlEffects) (out appServiceResult) {
	if e.w != nil {
		e.deferRelease = true
	}
	defer func() { e.deferRelease = false; e.Release() }()
	defer func() {
		if p := recover(); p != nil {
			if e.writerPanic != nil {
				panic(e.writerPanic)
			}
			out.Err = fmt.Errorf("application control interrupted: %v", p)
		}
		if out.Err != nil && e.w != nil && !e.responded {
			message := "health state unavailable"
			if q.Op == "state" {
				message = "could not read service state"
			}
			e.responded = true
			http.Error(e.w, message, 500)
		}
	}()
	return runSelectedAppService(q, e)
}
func (a *app) runAppControl(q appServiceRequest) appServiceResult {
	return runAppControlWithEffects(q, newAppControlEffects(a, q))
}
