package main

import (
	"errors"
	"fmt"
	"reflect"
	"time"
)

type regionFlowSession interface {
	next() ([]byte, uint64, error)
	advance(uint64, []byte) error
	close()
}
type regionFlowStart func([]byte) (regionFlowSession, error)
type regionFrame struct {
	kind          byte
	a, b          int64
	data, text    string
	indices, pool []int64
	stale         []bool
}

func regionStartInput(q regionServiceRequest) ([]byte, error) {
	op, ok := map[string]byte{"lock": 0, "report": 1}[q.Op]
	if !ok {
		return nil, errors.New("unknown region operation")
	}
	country := q.Country
	b := registryBytes([]byte{op}, country)
	b = registryBool(b, country == "" || looksLikeCountryCode(country))
	return b, nil
}
func decodeRegionFrame(b []byte) (regionFrame, error) {
	r := quotaReader{b: b}
	f := regionFrame{kind: r.byte(), a: r.integer(), b: r.integer(), data: r.str(), text: r.str()}
	ints := func() []int64 {
		n := r.integer()
		if n < 0 || n > 100000 || n > int64((len(b)-r.p)/8) {
			r.bad = true
			return nil
		}
		v := make([]int64, n)
		for i := range v {
			v[i] = r.integer()
		}
		return v
	}
	f.indices = ints()
	n := r.integer()
	if n < 0 || n > 100000 || n > int64(len(b)-r.p) {
		r.bad = true
	} else {
		f.stale = make([]bool, n)
		for i := range f.stale {
			v := r.byte()
			if v > 1 {
				r.bad = true
			}
			f.stale[i] = v == 1
		}
	}
	f.pool = ints()
	if r.bad || r.p != len(b) {
		return f, errors.New("invalid region frame")
	}
	return f, nil
}

type regionArena struct {
	country string
	state   *regionServiceState
	now     time.Time
	names   []string
}

func newRegionArena(q regionServiceRequest) *regionArena { return &regionArena{country: q.Country} }
func (a *regionArena) replyBytes(f regionFrame, r regionReply) ([]byte, error) {
	if r.State != nil {
		a.state = cloneRegionServiceState(r.State)
	}
	if f.kind == 2 || f.kind == 7 || f.kind == 13 {
		a.now = r.Now
	}
	b := registryBool([]byte{f.kind}, r.Err == nil)
	flag := r.Bool
	name := ""
	if f.kind == 8 {
		name, flag = r.Value.(string)
	}
	b = registryBool(b, flag)
	b = registryBytes(b, name)
	msg := ""
	if r.Err != nil {
		msg = r.Err.Error()
	}
	b = registryBytes(b, msg)
	b = registryInt(b, int64(regionTTL))
	aRecords := f.kind == 2 && a.state != nil
	if aRecords {
		a.names = nil
		for name := range a.state.Regions {
			a.names = append(a.names, name)
		}
		b = registryInt(b, int64(len(a.names)))
		for _, name := range a.names {
			rr := a.state.Regions[name]
			h := a.state.Health[name]
			b = registryBytes(b, name)
			b = registryBytes(b, rr.Country)
			b = registryBool(b, rr.Status == regionVerified)
			b = registryInt(b, int64(r.Now.Sub(rr.VerifiedAt)))
			b = registryBool(b, h.cooldownActive(r.Now))
			b = registryInt(b, int64(h.LatencyMs))
		}
	} else {
		b = registryInt(b, 0)
	}
	b = registryBool(b, r.State != nil)
	if s := r.State; s != nil {
		b = registryBool(b, s.Chain)
		b = registryBytes(b, s.LockedRegion)
		b = registryBytes(b, s.LockedNode)
	}
	if len(b) > 8<<20 {
		return nil, errors.New("region reply limit")
	}
	return b, nil
}
func (a *regionArena) port(f regionFrame) (regionEffect, bool) {
	q := regionEffect{}
	switch f.kind {
	case 1:
		q.Kind = "lock"
	case 2, 7, 13:
		q.Kind = "now"
	case 3:
		q.Kind = "set-country"
		q.Country = f.data
	case 4:
		q.Kind = "set-node"
		q.Name = f.data
	case 5:
		q.Kind = "save"
	case 6:
		q.Kind = "running"
	case 8:
		q.Kind = "get"
		q.Path = "/proxies/" + f.data
	case 9:
		q.Kind = "put"
		q.Path = "/proxies/SmartVPN"
		q.Name = f.data
	case 10:
		q.Kind = "block"
		q.Now = a.now
		q.Message = regionBlockMessage(f.data)
	case 11:
		if a.state == nil {
			return q, false
		}
		q.Kind = "log"
		q.Message = fmt.Sprintf("could not pin the group to %s after locking %s: %s", f.data, a.country, f.text)
	case 12:
		q.Kind = "event"
		q.Event = switchEvent{At: a.now, Group: "SmartVPN", ToNode: "-", Trigger: "region_lock", Evidence: regionEvidence(f.data)}
	default:
		return q, false
	}
	return q, true
}
func (a *regionArena) resultMatches(f regionFrame, r regionServiceResult) bool {
	if f.a != int64(r.Status) {
		return false
	}
	if f.b == 0 {
		return len(f.indices) == 1 && r.Message == "" && reflect.DeepEqual(r.Data, map[string]any{"ok": true, "lockedRegion": f.data, "pool": int(f.indices[0])})
	}
	if f.b == 5 {
		if a.state == nil || len(f.indices) != len(a.names) || len(f.stale) != len(f.indices) {
			return false
		}
		names := make([]string, len(f.indices))
		seen := map[int64]bool{}
		for i, j := range f.indices {
			if j < 0 || j >= int64(len(a.names)) || seen[j] {
				return false
			}
			seen[j] = true
			names[i] = a.names[j]
		}
		pool := make([]string, len(f.pool))
		seen = map[int64]bool{}
		for i, j := range f.pool {
			if j < 0 || j >= int64(len(a.names)) || seen[j] {
				return false
			}
			seen[j] = true
			pool[i] = a.names[j]
		}
		return r.Message == "" && reflect.DeepEqual(r.Data, projectRegionReport(a.state, names, f.stale, pool))
	}
	msg := map[int64]string{1: "the region must be a two-letter country code", 2: regionServiceChainError, 3: errNoLockableRegion.Error(), 4: "could not save the locked region"}[f.b]
	return msg != "" && r.Message == msg && r.Data == nil
}
