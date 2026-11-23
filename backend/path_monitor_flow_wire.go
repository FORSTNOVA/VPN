package main

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"time"
)

type pathMonitorFlowSession interface {
	next() ([]byte, uint64, error)
	advance(uint64, []byte) error
	close()
}
type pathMonitorFlowStart func([]byte) (pathMonitorFlowSession, error)
type pathMonitorWireTarget struct {
	url              int64
	label            string
	enabled, builtin bool
}
type pathMonitorFrame struct {
	kind       byte
	a, b       int64
	name, text string
	indices    []int64
	strings    []string
	settings   *pathMonitorWireSettings
	samples    map[string]pathConnectionSample
}
type pathMonitorWireSettings struct {
	enabled  bool
	interval int64
	targets  []pathMonitorWireTarget
}
type pathMonitorArena struct {
	timeSlots map[string]int64
	errs      map[int64]error
	q         pathMonitorServiceRequest
	next      int64
	targets   map[int64]pathTarget
	targetIDs map[pathTarget]int64
	urls      map[int64]string
	urlIDs    map[string]int64
	times     map[int64]time.Time
	presets   int
	urlFacts  map[int64]pathMonitorURLFacts
}

func newPathMonitorArena(q pathMonitorServiceRequest) *pathMonitorArena {
	a := &pathMonitorArena{q: q, timeSlots: map[string]int64{}, errs: map[int64]error{}, targets: map[int64]pathTarget{}, targetIDs: map[pathTarget]int64{}, urls: map[int64]string{}, urlIDs: map[string]int64{}, times: map[int64]time.Time{}, urlFacts: map[int64]pathMonitorURLFacts{}}
	for _, t := range q.Settings.Targets {
		a.target(t)
	}
	return a
}
func (a *pathMonitorArena) id() int64 { a.next++; return a.next }
func (a *pathMonitorArena) target(t pathTarget) int64 {
	if n, ok := a.targetIDs[t]; ok {
		return n
	}
	n := a.id()
	a.targetIDs[t] = n
	a.targets[n] = t
	return n
}
func (a *pathMonitorArena) url(s string) int64 {
	if n, ok := a.urlIDs[s]; ok {
		return n
	}
	n := a.id()
	a.urlIDs[s] = n
	a.urls[n] = s
	return n
}
func (a *pathMonitorArena) refs(b []byte, ts []pathTarget) []byte {
	b = registryInt(b, int64(len(ts)))
	for _, t := range ts {
		b = registryInt(b, a.target(t))
		b = registryBytes(b, t.Label)
		b = registryBool(b, t.Enabled)
		b = registryBool(b, t.URL == pathCheckPresets[0].URL || t.URL == pathCheckPresets[1].URL)
	}
	return b
}
func pathMonitorStartInput(q pathMonitorServiceRequest) ([]byte, error) {
	op, ok := map[string]byte{"start": 0, "stop": 1, "loop": 2, "demand": 3, "retry": 4, "normalize": 5, "update": 6, "snapshot": 7, "manual": 8, "cancel": 9}[q.Op]
	if !ok {
		return nil, errors.New("invalid monitor op")
	}
	a := newPathMonitorArena(q)
	b := []byte{op}
	b = registryBool(b, q.Guard)
	b = registryBool(b, q.Settings.Enabled)
	interval := int64(q.Settings.IntervalSec)
	if op == 2 {
		interval = int64(q.Interval)
	}
	b = registryInt(b, interval)
	b = a.refs(b, q.Settings.Targets)
	keys := make([]string, 0, len(q.Previous))
	for k := range q.Previous {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	b = registryInt(b, int64(len(keys)))
	for _, k := range keys {
		b = registryBytes(b, k)
		b = registryInt(b, q.Previous[k].upload)
	}
	return b, nil
}
func decodePathMonitorFrame(b []byte) (pathMonitorFrame, error) {
	r := quotaReader{b: b}
	f := pathMonitorFrame{kind: r.byte(), a: r.integer(), b: r.integer(), name: r.str(), text: r.str()}
	count := func() int {
		n := r.integer()
		if n < 0 || n > 100000 {
			r.bad = true
			return 0
		}
		return int(n)
	}
	for n := count(); n > 0; n-- {
		f.indices = append(f.indices, r.integer())
	}
	for n := count(); n > 0; n-- {
		f.strings = append(f.strings, r.str())
	}
	if r.byte() != 0 {
		s := &pathMonitorWireSettings{enabled: r.byte() != 0, interval: r.integer()}
		for n := count(); n > 0; n-- {
			s.targets = append(s.targets, pathMonitorWireTarget{r.integer(), r.str(), r.byte() != 0, r.byte() != 0})
		}
		f.settings = s
	}
	f.samples = make(map[string]pathConnectionSample)
	for n := count(); n > 0; n-- {
		k := r.str()
		f.samples[k] = pathConnectionSample{upload: r.integer()}
	}
	if r.bad || r.p != len(b) {
		return f, errors.New("invalid monitor frame")
	}
	return f, nil
}
func (a *pathMonitorArena) replyBytes(f pathMonitorFrame, r pathMonitorServiceReply) ([]byte, error) {
	b := []byte{f.kind}
	b = registryBool(b, r.Err == nil)
	b = registryBool(b, r.Bool)
	b = registryInt(b, 0)
	token := int64(0)
	if r.Err != nil && f.kind == 20 {
		token = -1
		a.errs[token] = r.Err
	}
	if f.kind == 6 || (f.kind == 26 && !r.Now.IsZero()) {
		key := "retry"
		if f.kind == 26 {
			key = "history:" + f.name
		}
		if f.name == "demand" {
			key = "demand:" + f.text
		}
		var ok bool
		token, ok = a.timeSlots[key]
		if !ok {
			token = a.id()
			a.timeSlots[key] = token
		}
		a.times[token] = r.Now
	}
	b = registryInt(b, token)
	b = registryBytes(b, r.Text)
	b = registryBytes(b, "")
	u := r.URL
	if f.kind == 17 {
		a.urlFacts[f.a] = u
	}
	b = registryInt(b, a.url(u.Raw))
	b = registryBool(b, u.Raw != "")
	b = registryBytes(b, u.Label)
	b = registryBytes(b, u.Host)
	b = registryBool(b, u.Valid)
	b = registryInt(b, int64(u.Runes))
	t := a.targets[f.a]
	b = registryBool(b, t.Enabled)
	b = registryInt(b, int64(len(r.Connections)))
	for _, c := range r.Connections {
		for _, s := range []string{c.ID, c.Start, c.Host, c.Port, c.Process} {
			b = registryBytes(b, s)
		}
		b = registryInt(b, c.Upload)
	}
	b = registryBool(b, r.State != nil)
	if s := r.State; s != nil {
		for _, v := range []bool{s.Identity, s.Cancel, s.Enabled, s.Busy} {
			b = registryBool(b, v)
		}
		b = registryInt(b, int64(s.Settings.IntervalSec))
		b = registryBytes(b, s.Status.RetryAt)
		b = registryBytes(b, s.Status.RetryMode)
		b = a.refs(b, s.Settings.Targets)
		a.presets = len(s.Presets)
		b = registryInt(b, int64(len(s.Presets)))
		for _, t := range s.Presets {
			b = registryInt(b, a.url(t.URL))
			b = registryBytes(b, t.Label)
			b = registryBool(b, t.Enabled)
			b = registryBool(b, t.Builtin)
		}
	}
	keys := make([]string, 0, len(r.Samples))
	for k := range r.Samples {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	b = registryInt(b, int64(len(keys)))
	for _, k := range keys {
		b = registryBytes(b, k)
		b = registryInt(b, r.Samples[k].upload)
	}

	if len(b) > 8<<20 || len(a.times) > 100000 || len(a.targets) > 100000 || len(a.urls) > 100000 || len(a.errs) > 100000 {
		return nil, errors.New("monitor reply limit")
	}
	return b, nil
}
func (a *pathMonitorArena) settings(s *pathMonitorWireSettings) (pathCheckSettings, bool) {
	if s == nil {
		return pathCheckSettings{}, true
	}
	v := pathCheckSettings{Enabled: s.enabled, IntervalSec: int(s.interval)}
	for _, t := range s.targets {
		u, ok := a.urls[t.url]
		if !ok {
			return v, false
		}
		v.Targets = append(v.Targets, pathTarget{URL: u, Label: t.label, Enabled: t.enabled, Builtin: t.builtin})
	}
	return v, true
}
func (a *pathMonitorArena) message(f pathMonitorFrame) string {
	switch f.b {
	case 1:
		var token int64
		if len(f.indices) > 1 {
			token = f.indices[1]
		}
		return fmt.Sprintf(pathMonitorMessage(0), a.urlFacts[token].Raw)
	case 2:
		return pathMonitorMessage(1)
	case 3:
		return fmt.Sprintf(pathMonitorMessage(2), pathCheckMaxTargets, a.presets)
	case 4:
		return "could not save the path check settings"
	case 5:
		return pathMonitorMessage(3)
	}
	return ""
}
func (a *pathMonitorArena) port(f pathMonitorFrame) (pathMonitorServiceEffect, bool) {
	e := pathMonitorServiceEffect{}
	switch f.kind {
	case 1:
		e.Kind = "lock"
	case 2:
		e.Kind = "unlock"
	case 3:
		e.Kind = "snapshot"
	case 4:
		e.Kind = "constants"
	case 5:
		e.Kind = "running"
	case 6:
		if f.name != "retry" && f.name != "demand" {
			return e, false
		}
		e.Kind = "now"
	case 7:
		e.Kind = "parse-time"
		e.Text = f.text
		e.Time = a.q.Now
		if f.a != 0 {
			var ok bool
			e.Time, ok = a.times[f.a]
			if !ok {
				return e, false
			}
		}
	case 8:
		e.Kind = "publish"
	case 9:
		e.Kind = "close"
	case 10:
		e.Kind = "ticker-new"
		e.Name = f.name
		e.Duration = time.Duration(f.a)
	case 11:
		e.Kind = "ticker-stop"
		e.Name = f.name
	case 12:
		e.Kind = "wait"
	case 13:
		e.Kind = "spawn-loop"
		e.Duration = time.Duration(f.a)
	case 14:
		e.Kind = "round"
		e.Name = f.name
		e.Text = f.text
		e.Flag = f.a != 0
	case 15:
		e.Kind = "capture"
	case 16:
		e.Kind = "traffic"
	case 17:
		e.Kind = "url"
		e.Name = f.name
		var ok bool
		e.Target, ok = a.targets[f.a]
		if !ok {
			return e, false
		}
	case 18:
		e.Kind = "samples"
		e.Samples = f.samples
	case 19:
		e.Kind = "settings"
		var ok bool
		e.Settings, ok = a.settings(f.settings)
		if !ok {
			return e, false
		}
	case 20:
		e.Kind = "save"
	case 21:
		e.Kind = "set"
		e.Name = f.name
		e.Text = f.text
		e.Flag = f.a != 0
		if f.b == 5 {
			e.Text = pathMonitorMessage(4)
		}
	case 22:
		e.Kind = "log"
		e.Duration = time.Duration(f.a)
	case 23:
		e.Kind = "response"
		e.Name = f.name
		e.Status = int(f.a)
		e.Text = a.message(f)
		var ok bool
		if f.name == "settings" {
			e.Settings, ok = a.settings(f.settings)
		} else {
			ok = true
		}
		if !ok {
			return e, false
		}
	case 24:
		e.Kind = "since"
		e.Name = f.name
		if f.a != 0 {
			var ok bool
			e.Time, ok = a.times[f.a]
			if !ok {
				return e, false
			}
		}
	case 25:
		e.Kind = "demand-state"
	case 26:
		e.Kind = "throttle-read"
		e.Name = f.name
	case 27:
		e.Kind = "throttle-write"
		e.Name = f.name
		var ok bool
		e.Time, ok = a.times[f.a]
		if !ok {
			return e, false
		}

	default:
		return e, false
	}
	return e, true
}
func (a *pathMonitorArena) resultMatches(f pathMonitorFrame, r pathMonitorServiceResult) bool {
	if int(f.a) != r.Status || f.text != r.Mode {
		return false
	}
	if f.b == 4 {
		if len(f.indices) < 2 || r.Err == nil || !errors.Is(r.Err, a.errs[f.indices[1]]) {
			return false
		}
	} else if f.b == 5 {
		if r.Err != nil {
			return false
		}
	} else {
		if (r.Err != nil) != (f.b != 0) {
			return false
		}
		if r.Err != nil && r.Err.Error() != a.message(f) {
			return false
		}
	}
	var labels []string
	if len(f.indices) > 0 && f.indices[0] != 0 {
		labels = append([]string{}, f.strings...)
	}
	if !reflect.DeepEqual(labels, r.Labels) {
		return false
	}
	s, ok := a.settings(f.settings)
	return ok && reflect.DeepEqual(s, r.Settings)
}
