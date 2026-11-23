package main

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type appPathEffects struct {
	a                       *app
	check                   pathChecker
	hooks                   map[string]func(pathServiceEffect) pathServiceReply
	locked, round, recovery bool
	initialLock             bool
	lease                   uint64
	proxy                   string
	now                     func() time.Time
}

func newAppPathEffects(a *app, check pathChecker, q pathServiceRequest) *appPathEffects {
	return &appPathEffects{a: a, check: check, recovery: q.Op == "recovery", now: time.Now}
}
func (e *appPathEffects) snapshot() *pathServiceState {
	a := e.a
	p := a.pathStateLocked()
	return clonePathServiceState(&pathServiceState{Busy: p.Busy, Enabled: p.Config.Enabled, Chain: a.chainEnabledLocked(), LockedRegion: a.controlMetaLocked().RuntimeLockedRegion, MixedPort: a.mixedPort, Targets: p.Config.Targets, Status: a.pathStatusLocked()})
}
func (e *appPathEffects) Release() {
	if e.locked {
		e.locked = false
		e.a.mu.Unlock()
	}
	if e.round {
		e.round = false
		e.a.mu.Lock()
		defer e.a.mu.Unlock()
		e.a.applyPathCommandLocked(servicePathCommand{Action: pathStateFinishRound, LeaseID: e.lease, Flag: e.recovery})
		e.lease = 0
	}
}
func (e *appPathEffects) Call(q pathServiceEffect) (r pathServiceReply) {
	if h := e.hooks[q.Kind]; h != nil {
		return h(q)
	}
	a := e.a
	switch q.Kind {
	case "lock":
		if e.initialLock {
			e.initialLock = false
		} else {
			a.mu.Lock()
		}
		e.locked = true
		r.State = e.snapshot()
	case "unlock":
		e.locked = false
		a.mu.Unlock()
	case "running":
		r.Bool = a.kernelRunningLocked()
	case "get":
		var g map[string]any
		g, r.Err = a.mihomoRequest(http.MethodGet, "/proxies/"+q.Name, nil)
		r.Text, _ = g["now"].(string)
	case "put":
		r.Err = a.switchGroupLocked(q.Name)
	case "proxy":
		e.proxy = net.JoinHostPort("127.0.0.1", strconv.Itoa(a.mixedPort))
	case "check":
		if e.check != nil {
			out := e.check(e.proxy, []pathTarget{q.Target})
			if len(out) > 0 {
				r.Result = out[0]
			}
		} else {
			r.Result = checkSite(&url.URL{Scheme: "http", Host: e.proxy}, q.Target.Label, q.Target.URL)
		}
	case "now":
		r.Now = e.now()
	case "region":
		r.Record, r.Found = a.regionRecordLocked(q.Name)
	case "age":
		r.Bool = e.now().Sub(a.regionValueLocked(q.Name).VerifiedAt) > regionTTL
	case "candidates":
		now := e.now()
		domain := a.healthDomainLocked()
		r.Candidates = make([]pathCandidateFact, 0, len(domain.Regions))
		for n, v := range domain.Regions {
			r.Candidates = append(r.Candidates, pathCandidateFact{Name: n, Country: v.Country, Verified: v.Status == regionVerified, Age: now.Sub(v.VerifiedAt), Cooldown: domain.Health[n].cooldownActive(now)})
		}
	case "save":
		r.Err = a.saveSettings()
	case "event":
		a.recordSwitchLocked(q.Event)
	case "set":
		switch q.Name {
		case "busy":
			if q.Flag {
				a.applyPathCommandLocked(servicePathCommand{Action: pathStateBeginRound})
				e.lease = a.pathStateLocked().LeaseID
			} else if e.lease != 0 {
				a.applyPathCommandLocked(servicePathCommand{Action: pathStateFinishRound, LeaseID: e.lease})
				e.lease = 0
			}
			e.round = q.Flag
		case "pin":
			a.patchControlLocked(controlRuntimeLockedNode, serviceControlState{RuntimeLockedNode: q.Text})
			a.patchControlLocked(controlLockedNode, serviceControlState{LockedNode: q.Text})
		case "last-switch":
			a.setHealthLastSwitchLocked(q.Time)
		case "at":
			a.pathClockCommandLocked(pathStateAt, q.Time, "", 0)
		default:
			strings := q.Strings
			results := q.Results
			if q.Name == "switched" {
				strings = append([]string(nil), q.Strings...)
			}
			if q.Name == "failures" {
				strings = append([]string{}, q.Strings...)
			}
			if q.Name == "results" {
				results = append([]siteCheckResult(nil), q.Results...)
			}
			a.applyPathCommandLocked(servicePathCommand{Action: servicePathFieldAction(q.Name), Flag: q.Flag, Text: q.Text, Strings: strings, Results: results})
		}

	}
	return
}
func runAppPathService(a *app, check pathChecker, q pathServiceRequest) (r pathServiceResult) {
	defer func() {
		if recover() != nil {
			r = pathServiceResult{Err: errors.New("path operation interrupted")}
		}
	}()
	e := newAppPathEffects(a, check, q)
	defer e.Release()
	return runSelectedPathService(q, e)
}
