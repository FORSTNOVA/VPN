package main

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
)

type chinaFlowSession interface {
	next() ([]byte, uint64, error)
	advance(uint64, []byte) error
	close()
}
type chinaFlowStart func([]byte) (chinaFlowSession, error)
type chinaFrame struct {
	kind       byte
	a, b       int64
	flag       bool
	name, text string
	ranges     []string
	status     chinaIPStatus
}

func chinaOp(s string) (byte, bool) {
	v, ok := map[string]byte{"parse": 0, "read": 2, "download": 1, "ensure": 3, "write": 4, "probe": 5, "supported": 6, "start-refresh": 7, "refresh": 8, "status": 9, "options": 10}[s]
	return v, ok
}
func chinaStrings(b []byte, v []string) []byte {
	b = registryInt(b, int64(len(v)))
	for _, s := range v {
		b = registryBytes(b, s)
	}
	return b
}
func chinaStartInput(q chinaServiceRequest) ([]byte, error) {
	op, ok := chinaOp(q.Op)
	if !ok {
		return nil, errors.New("unsupported china op")
	}
	b := registryInt([]byte{op}, int64(q.Mode))
	b = registryInt(b, 1)
	b = chinaStrings(b, q.Ranges)
	if len(b) > 8<<20 {
		return nil, errors.New("china input limit")
	}
	return b, nil
}
func chinaReadStrings(r *quotaReader) []string {
	n := r.integer()
	if n < 0 || n > 100000 {
		r.bad = true
		return nil
	}
	v := make([]string, int(n))
	for i := range v {
		v[i] = r.str()
	}
	return v
}
func decodeChinaFrame(b []byte) (chinaFrame, error) {
	r := quotaReader{b: b}
	f := chinaFrame{kind: r.byte(), a: r.integer(), b: r.integer(), flag: serviceReadBool(&r), name: r.str(), text: r.str(), ranges: chinaReadStrings(&r)}
	f.status = chinaIPStatus{Available: serviceReadBool(&r), Entries: int(r.integer()), UpdatedAt: r.str(), Source: r.str(), Error: r.str(), Refreshing: serviceReadBool(&r), Active: serviceReadBool(&r)}
	return f, serviceWireDone(&r)
}

type chinaArena struct {
	q         chinaServiceRequest
	bodies    map[int64][]byte
	next      int64
	bodyBytes int
	times     map[int64]time.Time
	nextTime  int64
	options   configOptions
}

func newChinaArena(q chinaServiceRequest) *chinaArena {
	return &chinaArena{q: q, bodies: map[int64][]byte{1: q.Body}, next: 1, bodyBytes: len(q.Body), times: map[int64]time.Time{}}
}
func (a *chinaArena) addBody(b []byte, slot int64) (int64, error) {
	if a.bodyBytes-len(a.bodies[slot])+len(b) > 64<<20 {
		return 0, errors.New("china body budget")
	}
	a.bodyBytes += len(b) - len(a.bodies[slot])
	if b != nil {
		b = append([]byte{}, b...)
	}
	a.bodies[slot] = b
	return slot, nil
}
func chinaJoined(v []string) []byte { return []byte(strings.Join(v, "\n") + "\n") }

var chinaPortNames = [...]string{1: "lock", 2: "unlock", 3: "state", 4: "command", 5: "stat", 6: "read", 7: "lines", 8: "cidr", 9: "fetch", 10: "sources", 11: "since", 12: "mkdir", 13: "write-temp", 14: "rename", 15: "now", 16: "free-port", 17: "probe-mkdir", 18: "probe-list-write", 19: "probe-config-write", 20: "probe-check", 21: "probe-remove-list", 22: "probe-remove-config", 23: "spawn", 24: "log", 25: "options-base", 26: "options-import", 27: "options-chain", 28: "options-domains", 29: "options-china", 32: "fetch-override"}

func (a *chinaArena) port(f chinaFrame) (chinaServiceEffect, bool) {
	if int(f.kind) >= len(chinaPortNames) || chinaPortNames[f.kind] == "" {
		return chinaServiceEffect{}, false
	}
	k := chinaPortNames[f.kind]
	e := chinaServiceEffect{Kind: k}
	switch f.kind {
	case 4:
		e.Action = uint8(f.a)
		e.Status = f.status
		e.Flag = f.flag
	case 7:
		var found bool
		e.Body, found = a.bodies[f.a]
		if !found {
			return e, false
		}
	case 8, 9:
		e.Name = f.name
	case 11:
		var found bool
		e.Time, found = a.times[f.a]
		if !found {
			return e, false
		}
	case 13:
		e.Body = chinaJoined(f.ranges)
	case 23:
		e.Index = int(f.a)
	case 24:
		e.Name = f.name
		if f.name == "probe" {
			e.Text = f.text
		} else {
			e.Status = f.status
		}
	case 29:
		e.Flag = f.flag
	}
	return e, true
}
func chinaStatusBytes(b []byte, s chinaIPStatus) []byte {
	b = registryBool(b, s.Available)
	b = registryInt(b, int64(s.Entries))
	b = registryBytes(b, s.UpdatedAt)
	b = registryBytes(b, s.Source)
	return registryBytes(b, s.Error)
}
func (a *chinaArena) replyBytes(f chinaFrame, r chinaServiceReply) ([]byte, error) {
	var token, value int64
	var e error
	switch f.kind {
	case 6, 9, 32:
		token, e = a.addBody(r.Body, map[byte]int64{6: 2, 9: 3, 32: 4}[f.kind])
		if e != nil {
			return nil, e
		}
	case 5:
		a.nextTime++
		value = a.nextTime
		a.times[value] = r.Now
		r.Text = r.Now.Format(time.RFC3339)
	case 15:
		r.Text = r.Now.Format(time.RFC3339)
	case 25, 26, 27, 28:
		a.options = r.Options
	case 29:
		a.options.ChinaDirect = f.flag
	}
	b := registryBool([]byte{f.kind}, r.Err == nil)
	b = registryBool(b, r.Flag)
	b = registryInt(b, value)
	b = registryInt(b, token)
	b = registryBytes(b, r.Text)
	errText := ""
	if r.Err != nil {
		errText = r.Err.Error()
	}
	b = registryBytes(b, errText)
	b = chinaStrings(b, r.Names)
	b = chinaStrings(b, r.Ranges)
	b = chinaStatusBytes(b, r.State.Status)
	for _, v := range []bool{r.State.Fetching, r.State.Probed, r.State.RuleOK, r.State.Active, r.Options.TUN, r.Options.DomesticWeb, r.ImportPolicy} {
		b = registryBool(b, v)
	}
	if len(b) > 8<<20 {
		return nil, errors.New("china reply limit")
	}
	return b, nil
}
func (a *chinaArena) internal(f chinaFrame) ([]byte, error) {
	r := chinaServiceReply{}
	if f.kind == 33 {
		n, e := a.addBody(chinaJoined(f.ranges), 5)
		if e != nil {
			return nil, e
		}
		b, e := a.replyBytes(f, r)
		if e != nil {
			return nil, e
		} /* token follows kind,ok,flag,value */
		copy(b[11:19], registryInt(nil, n))
		return b, nil
	}
	switch f.a {
	case 1:
		r.Text = fmt.Sprintf("not an address range: %q", f.name)
	case 2:
		r.Text = fmt.Sprintf("only %d address ranges, expected at least %d", f.b, chinaIPMinRanges)
	case 3:
		r.Text = fmt.Sprintf("%s: %s", f.name, f.text)
	case 4:
		r.Text = "no source for the address list answered"
	case 5:
		r.Text = chinaRefreshError(errors.New(f.text))
	case 6:
		r.Text = chinaWriteError(errors.New(f.text))
	case 7:
		r.Text = chinaDomesticError().Error()
	default:
		return nil, errors.New("china formatter code")
	}
	return a.replyBytes(f, r)
}
func (a *chinaArena) resultMatches(f chinaFrame, r chinaServiceResult) bool {
	if (f.b != 0) != (r.Err != nil) {
		return false
	}
	if r.Err != nil && r.Err.Error() != f.text {
		return false
	}
	switch a.q.Op {
	case "parse":
		if r.Err == nil {
			return reflect.DeepEqual(r.Ranges, f.ranges)
		}
	case "download":
		if r.Err == nil {
			return reflect.DeepEqual(r.Body, chinaJoined(f.ranges)) && r.Source == f.name
		}
	case "read":
		if r.Err != nil {
			return true
		}
		return r.Ranges == nil && len(f.ranges) == 0 && reflect.DeepEqual(r.Now, a.times[f.a]) && r.Status == f.status
	case "ensure", "refresh", "status":
		return r.Status == f.status
	case "probe", "supported", "start-refresh":
		return r.Flag == f.flag
	case "options":
		return reflect.DeepEqual(r.Options, a.options)
	}
	return true
}
