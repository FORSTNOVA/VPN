package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type pathFlowSession interface {
	next() ([]byte, uint64, error)
	advance(uint64, []byte) error
	close()
}
type pathFlowStart func([]byte) (pathFlowSession, error)
type pathFrame struct {
	kind       byte
	a, b       int64
	text, name string
	indices    []int64
	strings    []string
}

func pathStartInput(q pathServiceRequest) ([]byte, error) {
	op, ok := map[string]byte{"baseline": 0, "recovery": 1, "round": 2, "switch": 3}[q.Op]
	if !ok {
		return nil, errors.New("invalid path op")
	}
	b := []byte{op}
	for _, s := range []string{q.Mode, q.Target, q.Region, q.Trigger, q.Evidence} {
		b = registryBytes(b, s)
	}
	return b, nil
}
func decodePathFrame(b []byte) (pathFrame, error) {
	r := quotaReader{b: b}
	f := pathFrame{kind: r.byte(), a: r.integer(), b: r.integer(), text: r.str(), name: r.str()}
	n := r.integer()
	if n < 0 || n > 100000 || n > int64((len(b)-r.p)/8) {
		r.bad = true
	} else {
		f.indices = make([]int64, n)
		for i := range f.indices {
			f.indices[i] = r.integer()
		}
	}
	n = r.integer()
	if n < 0 || n > 100000 {
		r.bad = true
	} else {
		f.strings = make([]string, n)
		for i := range f.strings {
			f.strings[i] = r.str()
		}
	}
	if r.bad || r.p != len(b) {
		return f, errors.New("invalid path frame")
	}
	return f, nil
}

type pathArena struct {
	q         pathServiceRequest
	lastNow   time.Time
	next      int64
	targets   map[int64]pathTarget
	targetIDs map[pathTarget]int64
	rows      map[int64]siteCheckResult
	times     map[int64]time.Time
	errs      map[int64]error
}

func newPathArena(q pathServiceRequest) *pathArena {
	return &pathArena{q: q, targets: map[int64]pathTarget{}, targetIDs: map[pathTarget]int64{}, rows: map[int64]siteCheckResult{}, times: map[int64]time.Time{}, errs: map[int64]error{}}
}
func (a *pathArena) id() int64 { a.next++; return a.next }
func (a *pathArena) targetBytes(b []byte, ts []pathTarget) []byte {
	b = registryInt(b, int64(len(ts)))
	for _, t := range ts {
		id, ok := a.targetIDs[t]
		if !ok {
			id = a.id()
			a.targetIDs[t] = id
			a.targets[id] = t
		}
		b = registryInt(b, id)
		b = registryBytes(b, t.Label)
		b = registryBool(b, t.Enabled)
		b = registryBool(b, t.URL == pathCheckPresets[0].URL || t.URL == pathCheckPresets[1].URL)
	}
	return b
}
func (a *pathArena) replyBytes(f pathFrame, r pathServiceReply) ([]byte, error) {
	var token int64
	name := r.Text
	errText := ""
	if r.Err != nil {
		token = a.id()
		a.errs[token] = r.Err
		errText = strconv.FormatInt(token, 10)
	}
	if f.kind == 5 {
		name = r.Record.Country
	}
	if f.kind == 6 {
		a.lastNow = r.Now
		token = a.id()
		a.times[token] = r.Now
		name = r.Now.Add(pathRetryDelay).UTC().Format(time.RFC3339Nano)
	}
	rowToken := int64(0)
	if f.kind == 8 {
		rowToken = a.id()
		a.rows[rowToken] = r.Result
	}
	b := registryBool([]byte{f.kind}, r.Err == nil)
	b = registryBool(b, r.Bool)
	b = registryBool(b, r.Found)
	b = registryBool(b, r.Record.Status == regionVerified)
	b = registryBytes(b, name)
	b = registryBytes(b, errText)
	b = registryInt(b, token)
	b = registryInt(b, int64(regionTTL))
	b = registryInt(b, rowToken)
	b = registryBytes(b, r.Result.Name)
	b = registryBool(b, pathPassed(r.Result))
	b = registryInt(b, int64(len(r.Candidates)))
	for _, c := range r.Candidates {
		b = registryBytes(b, c.Name)
		b = registryBytes(b, c.Country)
		b = registryBool(b, c.Verified)
		b = registryBool(b, c.Cooldown)
		b = registryInt(b, int64(c.Age))
	}
	b = registryBool(b, r.State != nil)
	if s := r.State; s != nil {
		b = registryBool(b, s.Busy)
		b = registryBool(b, s.Enabled)
		b = registryBool(b, s.Chain)
		b = registryBool(b, s.Status.Demanded)
		b = registryBytes(b, s.LockedRegion)
		b = registryBytes(b, s.Status.RetryMode)
		b = registryBytes(b, s.Status.RetryAt)
		b = registryInt(b, int64(s.Status.AlertID))
		b = a.targetBytes(b, s.Targets)
		b = a.targetBytes(b, baselinePathTargets())
	}
	return b, nil
}

var _ = fmt.Sprintf
var _ = strings.Join

func (a *pathArena) message(code int, text string, args []string) (string, bool) {
	if code < 0 || code > 19 {
		return "", false
	}
	switch code {
	case 6:
		if len(args) < 1 {
			return "", false
		}
		return fmt.Sprintf(pathServiceMessage(6), args[0], strings.Join(args[1:], pathServiceMessage(7))), true
	case 8:
		id, e := strconv.ParseInt(text, 10, 64)
		if e != nil || a.errs[id] == nil {
			return "", false
		}
		return pathServiceMessage(8) + a.errs[id].Error(), true
	case 9:
		return fmt.Sprintf(pathServiceMessage(9), strings.Join(args, pathServiceMessage(7))), true
	case 16:
		if len(args) != 1 {
			return "", false
		}
		return fmt.Sprintf(pathServiceMessage(16), args[0]), true
	default:
		return pathServiceMessage(code), true
	}
}
func (a *pathArena) port(f pathFrame) (pathServiceEffect, bool) {
	e := pathServiceEffect{}
	switch f.kind {
	case 1:
		e.Kind = "lock"
	case 2:
		e.Kind = "unlock"
	case 3:
		e.Kind = "running"
	case 4:
		e.Kind = "get"
		e.Name = f.name
	case 5:
		e.Kind = "region"
		e.Name = f.name
	case 6:
		e.Kind = "now"
	case 7:
		e.Kind = "candidates"
	case 8:
		e.Kind = "check"
		var ok bool
		e.Target, ok = a.targets[f.a]
		if !ok {
			return e, false
		}
	case 9:
		e.Kind = "set"
		e.Name = f.name
		e.Text = f.text
		e.Flag = f.a != 0
		if f.b > 0 {
			var ok bool
			e.Text, ok = a.message(int(f.b)-1, f.text, f.strings)
			if !ok {
				return e, false
			}
		} else if f.b == -1 {
			e.Text = a.lastNow.Add(pathRetryDelay).UTC().Format(time.RFC3339Nano)
		}
		switch f.name {
		case "at", "last-switch":
			e.Time = a.lastNow
		case "results":
			e.Results = make([]siteCheckResult, len(f.indices))
			for i, id := range f.indices {
				r, ok := a.rows[id]
				if !ok {
					return e, false
				}
				e.Results[i] = r
			}
		case "failures":
			e.Strings = append([]string{}, f.strings...)
		case "switched":
			if len(f.strings) > 0 {
				e.Strings = append([]string(nil), f.strings...)
			}
		}
	case 10:
		e.Kind = "save"
	case 11:
		if len(f.strings) < 3 {
			return e, false
		}
		e.Kind = "event"
		ev := ""
		if f.b == 0 {
			if len(f.strings) != 4 {
				return e, false
			}
			ev = f.strings[3]
		} else {
			var ok bool
			ev, ok = a.message(int(f.b)-1, f.text, f.strings[3:])
			if !ok {
				return e, false
			}
		}
		e.Event = switchEvent{Group: "SmartVPN", FromNode: f.strings[0], ToNode: f.strings[1], Trigger: f.strings[2], Evidence: ev}
	case 12:
		e.Kind = "put"
		e.Name = f.name
	case 13:
		e.Kind = "age"
		e.Name = f.name
	case 14:
		e.Kind = "proxy"
	default:
		return e, false
	}
	return e, true
}
func (a *pathArena) resultMatches(f pathFrame, r pathServiceResult) bool {
	var err error
	switch f.a {
	case 0:
	case 1:
		err = a.errs[f.b]
		if err == nil {
			return false
		}
	case 2:
		err = errors.New(pathServiceMessage(18))
	case 3:
		err = errors.New(pathServiceMessage(19))
	default:
		return false
	}
	if (err == nil) != (r.Err == nil) {
		return false
	}
	if err == nil {
		return true
	}
	if f.a == 1 {
		return errors.Is(r.Err, err)
	}
	return r.Err.Error() == err.Error()
}
