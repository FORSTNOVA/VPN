package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

type goPathMonitorService struct {
	q      pathMonitorServiceRequest
	fx     pathMonitorServiceEffects
	s      *pathMonitorServiceState
	locked bool
	stale  bool
}

func (a *goPathMonitorService) call(q pathMonitorServiceEffect) pathMonitorServiceReply {
	return a.fx.Call(q)
}
func (a *goPathMonitorService) lock() {
	a.s = a.call(pathMonitorServiceEffect{Kind: "lock"}).State
	a.locked = true
	if a.q.Guard && !a.s.Identity {
		a.stale = true
	}
}
func (a *goPathMonitorService) unlock() {
	a.call(pathMonitorServiceEffect{Kind: "unlock"})
	a.locked = false
}
func (a *goPathMonitorService) snapshot() {
	a.s = a.call(pathMonitorServiceEffect{Kind: "snapshot"}).State
}
func (a *goPathMonitorService) now() time.Time {
	return a.call(pathMonitorServiceEffect{Kind: "now"}).Now
}
func (a *goPathMonitorService) running() bool {
	return a.call(pathMonitorServiceEffect{Kind: "running"}).Bool
}
func (a *goPathMonitorService) set(n, t string, b bool) {
	a.call(pathMonitorServiceEffect{Kind: "set", Name: n, Text: t, Flag: b})
	switch n {
	case "demanded":
		a.s.Status.Demanded = b
	case "alert":
		a.s.Status.Alert = t
	case "phase":
		a.s.Status.Phase = t
	case "note":
		a.s.Status.Note = t
	case "retry-at":
		a.s.Status.RetryAt = t
	case "retry-mode":
		a.s.Status.RetryMode = t
	}
}
func (a *goPathMonitorService) clearRetry() {
	a.set("retry-at", "", false)
	a.set("retry-mode", "", false)
}
func (a *goPathMonitorService) stop() {
	a.snapshot()
	if a.s.Cancel {
		a.call(pathMonitorServiceEffect{Kind: "close"})
	}
	a.set("demanded", "", false)
	a.set("alert", "", false)
	a.set("phase", "", false)
	a.clearRetry()
}
func (a *goPathMonitorService) start() {
	a.snapshot()
	if a.s.Cancel || !a.s.Enabled {
		return
	}
	a.call(pathMonitorServiceEffect{Kind: "publish"})
	d := time.Duration(a.s.Settings.IntervalSec) * time.Second
	a.call(pathMonitorServiceEffect{Kind: "log", Duration: d})
	a.call(pathMonitorServiceEffect{Kind: "spawn-loop", Duration: d})
}
func (a *goPathMonitorService) round(mode string, sync bool) {
	text := ""
	if sync {
		text = "sync"
	}
	a.call(pathMonitorServiceEffect{Kind: "round", Name: mode, Text: text, Flag: a.q.Guard})
}
func (a *goPathMonitorService) admitted() bool { a.lock(); ok := a.s.Identity; a.unlock(); return ok }
func (a *goPathMonitorService) takeRetry(now time.Time) string {
	a.lock()
	defer a.unlock()
	if !a.s.Identity || a.s.Busy || !a.s.Enabled || a.s.Status.RetryAt == "" {
		return ""
	}
	r := a.call(pathMonitorServiceEffect{Kind: "parse-time", Text: a.s.Status.RetryAt, Time: now})
	if r.Err != nil || r.Bool {
		return ""
	}
	mode := a.s.Status.RetryMode
	a.clearRetry()
	a.set("phase", "scanning", false)
	return mode
}
func (a *goPathMonitorService) demand() []string {
	a.lock()
	if !a.s.Identity {
		a.unlock()
		return nil
	}
	if !a.running() || !a.s.Enabled || a.s.Busy {
		a.unlock()
		return nil
	}
	targets := make([]pathTarget, 0, len(a.s.Settings.Targets))
	for _, t := range a.s.Settings.Targets {
		if t.Enabled && t.URL != a.s.Presets[0].URL && t.URL != a.s.Presets[1].URL {
			targets = append(targets, t)
		}
	}
	a.call(pathMonitorServiceEffect{Kind: "capture"})
	a.unlock()
	if len(targets) == 0 {
		return nil
	}
	r := a.call(pathMonitorServiceEffect{Kind: "traffic"})
	if r.Err != nil {
		return nil
	}
	if a.q.Guard && !a.admitted() {
		return nil
	}
	ds := a.call(pathMonitorServiceEffect{Kind: "demand-state"})
	if !ds.Bool {
		a.stale = true
		return nil
	}
	previous := ds.Samples
	urlFacts := make([]pathMonitorURLFacts, len(targets))
	for i, t := range targets {
		urlFacts[i] = a.call(pathMonitorServiceEffect{Kind: "url", Target: t}).URL
	}
	current := make(map[string]pathConnectionSample, len(r.Connections))
	demanded := map[string]bool{}
	for _, c := range r.Connections {
		if strings.Contains(c.Process, "smartvpn") || c.Host == "" {
			continue
		}
		id := c.ID
		if id == "" {
			id = c.Start + "|" + c.Host + "|" + c.Port
		}
		prior, seen := previous[id]
		current[id] = pathConnectionSample{upload: c.Upload}
		if seen && c.Upload <= prior.upload {
			continue
		}
		for i, t := range targets {
			u := urlFacts[i]
			if !u.Valid || u.Host == "" || !(c.Host == u.Host || strings.HasSuffix(c.Host, "."+u.Host)) {
				continue
			}
			demanded[t.Label] = true
		}
	}
	if !a.call(pathMonitorServiceEffect{Kind: "samples", Samples: current}).Bool {
		a.stale = true
		return nil
	}
	labels := make([]string, 0, len(demanded))
	for label := range demanded {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	return labels
}
func (a *goPathMonitorService) loop() {
	made := []string{}
	defer func() {
		for i := len(made) - 1; i >= 0; i-- {
			a.call(pathMonitorServiceEffect{Kind: "ticker-stop", Name: made[i]})
		}
	}()
	for _, t := range []struct {
		name string
		d    time.Duration
	}{{"baseline", a.q.Interval}, {"demand", time.Second}, {"retry", time.Second}} {
		a.call(pathMonitorServiceEffect{Kind: "ticker-new", Name: t.name, Duration: t.d})
		made = append(made, t.name)
	}
	if !a.admitted() {
		return
	}
	a.round("baseline", false)
	for {
		event := a.call(pathMonitorServiceEffect{Kind: "wait"}).Text
		switch event {
		case "cancel":
			return
		case "baseline":
			if !a.admitted() {
				return
			}
			a.round("baseline", true)
		case "demand":
			labels := a.demand()
			if a.stale {
				return
			}
			for _, label := range labels {
				saved := a.call(pathMonitorServiceEffect{Kind: "throttle-read", Name: label})
				if !saved.Bool {
					return
				}
				if a.call(pathMonitorServiceEffect{Kind: "since", Name: label, Time: saved.Now}).Bool {
					continue
				}
				a.lock()
				if !a.s.Identity {
					a.unlock()
					return
				}
				if !a.s.Busy && a.running() && a.s.Enabled {
					a.call(pathMonitorServiceEffect{Kind: "throttle-write", Name: label, Time: a.now()})
					a.set("demanded", "", true)
					a.set("phase", "scanning", false)
					a.unlock()
					a.round("demand", false)
					break
				}
				a.unlock()
			}
		case "retry":
			mode := a.takeRetry(a.now())
			if a.stale {
				return
			}
			if mode != "" {
				a.round(mode, false)
			}
		}
	}
}
func (a *goPathMonitorService) normalize(input pathCheckSettings) (pathCheckSettings, error) {
	p := a.call(pathMonitorServiceEffect{Kind: "constants"}).State.Presets
	out := pathCheckSettings{Enabled: input.Enabled, IntervalSec: input.IntervalSec}
	if out.IntervalSec == 0 {
		out.IntervalSec = pathCheckDefaultInterval
	}
	if out.IntervalSec < pathCheckMinInterval {
		out.IntervalSec = pathCheckMinInterval
	}
	if out.IntervalSec > pathCheckMaxInterval {
		out.IntervalSec = pathCheckMaxInterval
	}
	seen := map[string]bool{}
	limit := pathCheckMaxTargets - len(p)
	for _, t := range input.Targets {
		f := a.call(pathMonitorServiceEffect{Kind: "url", Name: "normalize", Target: t}).URL
		if f.Raw == "" {
			continue
		}
		if !f.Valid {
			return pathCheckSettings{}, fmt.Errorf(pathMonitorMessage(0), f.Raw)
		}
		if seen[f.Raw] {
			continue
		}
		seen[f.Raw] = true
		if f.Runes > 40 {
			return pathCheckSettings{}, errors.New(pathMonitorMessage(1))
		}
		if len(out.Targets) >= limit {
			return pathCheckSettings{}, fmt.Errorf(pathMonitorMessage(2), pathCheckMaxTargets, len(p))
		}
		builtin := false
		for _, preset := range p {
			if preset.URL == f.Raw {
				builtin = true
				break
			}
		}
		out.Targets = append(out.Targets, pathTarget{Label: f.Label, URL: f.Raw, Enabled: t.Enabled, Builtin: builtin})
	}
	for _, t := range p {
		if !seen[t.URL] {
			out.Targets = append(out.Targets, t)
		}
	}
	return out, nil
}
func runGoPathMonitorService(q pathMonitorServiceRequest, fx pathMonitorServiceEffects) (r pathMonitorServiceResult) {
	defer fx.Release()
	a := &goPathMonitorService{q: q, fx: fx}
	defer func() {
		if a.locked {
			a.unlock()
		}
	}()
	response := func(status int, text, name string, settings pathCheckSettings) {
		a.call(pathMonitorServiceEffect{Kind: "response", Status: status, Text: text, Name: name, Settings: settings})
	}
	switch q.Op {
	case "start":
		a.start()
	case "stop":
		a.stop()
	case "loop":
		a.loop()
	case "demand":
		r.Labels = a.demand()
	case "retry":
		r.Mode = a.takeRetry(q.Now)
	case "normalize":
		r.Settings, r.Err = a.normalize(q.Settings)
	case "update":
		r.Settings, r.Err = a.normalize(q.Settings)
		if r.Err != nil {
			r.Status = 400
			response(400, r.Err.Error(), "", pathCheckSettings{})
			return
		}
		a.lock()
		a.call(pathMonitorServiceEffect{Kind: "settings", Settings: r.Settings})
		if err := a.call(pathMonitorServiceEffect{Kind: "save"}).Err; err != nil {
			r.Err = err
			r.Status = 500
			response(500, "could not save the path check settings", "", pathCheckSettings{})
			return
		}
		a.stop()
		a.start()
		response(0, "", "settings", r.Settings)
	case "snapshot":
		a.lock()
		response(0, "", "snapshot", pathCheckSettings{})
	case "manual":
		a.lock()
		running := a.running()
		a.unlock()
		if !running {
			r.Status = 409
			response(409, pathMonitorMessage(3), "", pathCheckSettings{})
			return
		}
		a.round("manual", false)
		response(0, "", "", pathCheckSettings{})
	case "cancel":
		a.lock()
		a.set("demanded", "", false)
		a.set("alert", "", false)
		a.set("note", pathMonitorMessage(4), false)
		a.clearRetry()
		a.set("phase", "baseline", false)
		a.unlock()
		response(0, "", "", pathCheckSettings{})
	}
	return
}
