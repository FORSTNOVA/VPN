package main

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

type pathMonitorTimer struct {
	ch   <-chan time.Time
	stop func()
}
type appPathMonitorEffects struct {
	responded   bool
	a           *app
	q           pathMonitorServiceRequest
	w           http.ResponseWriter
	token       <-chan struct{}
	locked      bool
	port        int
	secret      string
	timers      map[string]pathMonitorTimer
	hooks       map[string]func(pathMonitorServiceEffect) pathMonitorServiceReply
	now         func() time.Time
	fetch       func(int, string) (kernelTraffic, error)
	launch      func(func())
	roundRunner func(string, bool, <-chan struct{})
	loopRunner  func(<-chan struct{}, time.Duration)
	makeTicker  func(string, time.Duration, func(<-chan time.Time, func()))
}

func newAppPathMonitorEffects(a *app, q pathMonitorServiceRequest, w http.ResponseWriter, token <-chan struct{}) *appPathMonitorEffects {
	e := &appPathMonitorEffects{a: a, q: q, w: w, token: token, timers: map[string]pathMonitorTimer{}, now: time.Now, fetch: kernelTrafficAt, launch: func(f func()) { go f() }}
	e.makeTicker = func(_ string, d time.Duration, register func(<-chan time.Time, func())) {
		t := time.NewTicker(d)
		register(t.C, t.Stop)
	}
	e.roundRunner = func(mode string, guard bool, token <-chan struct{}) {
		op := "recovery"
		if mode == "baseline" {
			op = "baseline"
		}
		r := pathServiceRequest{Op: op, Mode: mode}
		if guard {
			result, _ := runGuardedPathMonitorRound(a, token, r, func(fx *appPathEffects) pathServiceResult { return runSelectedPathService(r, fx) })
			if result.Err != nil {
				log.Print("path worker operation interrupted")
			}
		} else {
			runAppPathService(a, checkPathTargets, r)
		}
	}
	e.loopRunner = func(token <-chan struct{}, d time.Duration) {
		runAppPathMonitorService(a, pathMonitorServiceRequest{Op: "loop", Guard: true, Interval: d}, nil, token)
	}
	return e
}
func (e *appPathMonitorEffects) snapshot() *pathMonitorServiceState {
	s := &pathMonitorServiceState{Identity: !e.q.Guard, Presets: append([]pathTarget(nil), pathCheckPresets...)}
	if e.a != nil {
		s.Identity = !e.q.Guard || (e.token != nil && e.token == e.a.pathCancel)
		s.Cancel = e.a.pathCancel != nil
		if !s.Identity {
			return s
		}
		p := e.a.pathStateLocked()
		s.Settings = p.Config
		s.Enabled = p.Config.Enabled
		s.Busy = p.Busy
		s.Status = e.a.pathStatusLocked()
	}
	return clonePathMonitorState(s)
}
func (e *appPathMonitorEffects) stopTimer(n string) {
	if t, ok := e.timers[n]; ok {
		delete(e.timers, n)
		if t.stop != nil {
			t.stop()
		}
	}
}
func (e *appPathMonitorEffects) Release() {
	defer func() {
		if e.locked {
			e.locked = false
			e.a.mu.Unlock()
		}
	}()
	defer e.stopTimer("baseline")
	defer e.stopTimer("demand")
	e.stopTimer("retry")
}
func (e *appPathMonitorEffects) Call(q pathMonitorServiceEffect) (r pathMonitorServiceReply) {
	if h := e.hooks[q.Kind]; h != nil {
		if q.Kind == "response" {
			e.responded = true
		}
		return h(q)
	}
	a := e.a
	switch q.Kind {
	case "lock":
		a.mu.Lock()
		e.locked = true
		r.State = e.snapshot()
	case "unlock":
		e.locked = false
		a.mu.Unlock()
	case "snapshot":
		r.State = e.snapshot()
	case "constants":
		r.State = &pathMonitorServiceState{Presets: append([]pathTarget(nil), pathCheckPresets...)}
	case "running":
		r.Bool = a.kernelRunningLocked()
	case "now":
		r.Now = e.now()
	case "since":
		r.Bool = e.now().Sub(q.Time) < pathRetryDelay
	case "parse-time":
		r.Now, r.Err = time.Parse(time.RFC3339Nano, q.Text)
		r.Bool = q.Time.Before(r.Now)
	case "publish":
		c := make(chan struct{})
		a.applyPathCommandLocked(servicePathCommand{Action: pathStateOpenMonitor})
		a.pathCancel = c
		e.token = c
	case "close":
		if a.pathCancel != nil {
			c := a.pathCancel
			close(c)
			a.pathCancel = nil
			a.applyPathCommandLocked(servicePathCommand{Action: pathStateRevokeMonitor})
		}
	case "ticker-new":
		e.makeTicker(q.Name, q.Duration, func(ch <-chan time.Time, stop func()) { e.timers[q.Name] = pathMonitorTimer{ch, stop} })
	case "ticker-stop":
		e.stopTimer(q.Name)
	case "wait":
		select {
		case <-e.token:
			r.Text = "cancel"
		case <-e.timers["baseline"].ch:
			r.Text = "baseline"
		case <-e.timers["demand"].ch:
			r.Text = "demand"
		case <-e.timers["retry"].ch:
			r.Text = "retry"
		}
	case "spawn-loop":
		token := e.token
		d := q.Duration
		e.launch(func() { e.loopRunner(token, d) })
	case "round":
		token := e.token
		mode := q.Name
		guard := q.Flag
		if q.Text == "sync" {
			e.roundRunner(mode, guard, token)
		} else {
			e.launch(func() { e.roundRunner(mode, guard, token) })
		}
	case "capture":
		e.port = a.ctrlPort
		e.secret = a.ctrlSecret
	case "traffic":
		var v kernelTraffic
		v, r.Err = e.fetch(e.port, e.secret)
		if r.Err == nil {
			r.Connections = make([]pathMonitorConnection, 0, len(v.Connections))
			for _, c := range v.Connections {
				r.Connections = append(r.Connections, pathMonitorConnection{ID: c.ID, Start: c.Start, Host: normalizePathHost(c.Metadata.Host), Port: string(c.Metadata.DestinationPort), Process: strings.ToLower(connectionProcess(c.Metadata)), Upload: c.Upload})
			}
		}
	case "url":
		raw := strings.TrimSpace(q.Target.URL)
		var u *url.URL
		var err error
		if q.Name == "normalize" {
			u, err = url.ParseRequestURI(raw)
		} else {
			u, err = url.Parse(q.Target.URL)
		}
		r.URL.Raw = raw
		if err == nil {
			r.URL.Host = normalizePathHost(u.Hostname())
			r.URL.Valid = q.Name != "normalize" || (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
			label := strings.TrimSpace(q.Target.Label)
			if label == "" {
				label = u.Hostname()
			}
			r.URL.Label = label
			r.URL.Runes = len([]rune(label))
		}
	case "demand-state":
		if e.q.Op == "demand" {
			r.Bool = true
			r.Samples = map[string]pathConnectionSample{}
			for k, v := range e.q.Previous {
				r.Samples[k] = v
			}
			return
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if e.q.Guard && (e.token == nil || e.token != a.pathCancel) {
			return
		}
		p := a.pathStateLocked()
		if !p.MonitorActive {
			return
		}
		r.Bool = true
		r.Samples = map[string]pathConnectionSample{}
		for _, v := range p.Previous {
			r.Samples[v.ID] = pathConnectionSample{upload: v.Upload}
		}
	case "samples":
		if e.q.Op == "demand" {
			clear(e.q.Previous)
			for k, v := range q.Samples {
				e.q.Previous[k] = v
			}
			r.Bool = true
			return
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if e.q.Guard && (e.token == nil || e.token != a.pathCancel) {
			return
		}
		p := a.pathStateLocked()
		if !p.MonitorActive {
			return
		}
		samples := make([]servicePathSample, 0, len(q.Samples))
		for k, v := range q.Samples {
			samples = append(samples, servicePathSample{k, v.upload})
		}
		sort.Slice(samples, func(i, j int) bool { return samples[i].ID < samples[j].ID })
		a.applyPathCommandLocked(servicePathCommand{Action: pathStateSamples, Generation: p.Generation, Samples: samples})
		r.Bool = true
	case "throttle-read":
		a.mu.Lock()
		defer a.mu.Unlock()
		if e.q.Guard && (e.token == nil || e.token != a.pathCancel) {
			return
		}
		p := a.pathStateLocked()
		if !p.MonitorActive {
			return
		}
		r.Bool = true
		for _, v := range p.Throttle {
			if v.Label == q.Name {
				var ok bool
				r.Now, ok = a.service.clocks[v.TimeToken]
				if !ok {
					panic("missing demand clock")
				}
				break
			}
		}
	case "throttle-write":
		p := a.pathStateLocked()
		a.pathClockCommandLocked(pathStateThrottle, q.Time, q.Name, p.Generation)

	case "settings":
		a.replacePathConfigLocked(q.Settings)
	case "save":
		r.Err = a.saveSettings()
	case "set":
		a.applyPathCommandLocked(servicePathCommand{Action: servicePathFieldAction(q.Name), Flag: q.Flag, Text: q.Text})
	case "log":
		log.Printf("path monitor: checking Google and YouTube every %s; target access starts recovery", q.Duration)
	case "response":
		if e.w != nil {
			if q.Status != 0 {
				e.responded = true
				http.Error(e.w, q.Text, q.Status)
			} else {
				switch q.Name {
				case "snapshot":
					p := a.pathStateLocked()
					s := a.pathStatusLocked()
					s.Running = p.Busy
					body := map[string]any{"settings": a.pathConfigLocked(), "status": s, "limits": map[string]any{"minIntervalSec": pathCheckMinInterval, "maxIntervalSec": pathCheckMaxInterval, "maxTargets": pathCheckMaxTargets}}
					e.responded = true
					writeJSON(e.w, body)
				case "settings":
					e.responded = true
					writeJSON(e.w, map[string]any{"ok": true, "settings": q.Settings})
				default:
					e.responded = true
					writeJSON(e.w, map[string]any{"ok": true})
				}
			}
		}
	default:
		panic(fmt.Sprintf("unknown path monitor effect %q", q.Kind))
	}
	return
}
func runAppPathMonitorService(a *app, q pathMonitorServiceRequest, w http.ResponseWriter, token <-chan struct{}) (r pathMonitorServiceResult) {
	e := newAppPathMonitorEffects(a, q, w, token)
	defer func() {
		if recover() != nil {
			r = pathMonitorServiceResult{Err: errors.New("path monitor operation interrupted"), Status: 500}
		}
		if r.Err != nil && w != nil && !e.responded {
			e.responded = true
			http.Error(w, "path monitor operation interrupted", 500)
		}
	}()
	defer e.Release()
	return runSelectedPathMonitorService(q, e)
}

// Admission and the Phase24 first lock are atomic. Once admitted, the round is
// deliberately not cancelled by later stop/restart or cancel-recovery calls.
func withPathMonitorRound(a *app, token <-chan struct{}, q pathServiceRequest, run func(*appPathEffects)) bool {
	a.mu.Lock()
	if token != a.pathCancel || a.pathCancel == nil {
		a.mu.Unlock()
		return false
	}
	e := newAppPathEffects(a, checkPathTargets, q)
	e.locked = true
	e.initialLock = true
	defer e.Release()
	run(e)
	return true
}

// Recover outside admission and its deferred Release: default planners and
// cleanup can both fail. The worker never repeats an executed effect.
func runGuardedPathMonitorRound(a *app, token <-chan struct{}, q pathServiceRequest, run func(*appPathEffects) pathServiceResult) (r pathServiceResult, admitted bool) {
	defer func() {
		if recover() != nil {
			r = pathServiceResult{Err: errors.New("path worker operation interrupted")}
		}
	}()
	admitted = withPathMonitorRound(a, token, q, func(fx *appPathEffects) { r = run(fx) })
	return
}
