package main

import (
	"errors"
	"unicode/utf8"
)

type chainFlowSession interface {
	next() ([]byte, uint64, error)
	advance(uint64, []byte) error
	close()
}
type chainFlowStart func([]byte) (chainFlowSession, error)
type chainFrame struct {
	kind       byte
	a, b, c    int64
	data, text string
	indices    []int64
}

func chainBytes(b []byte, c proxyChain) []byte {
	b = registryBool(b, c.Enabled)
	b = registryBytes(b, c.Name)
	return profileStrings(b, c.Hops)
}
func chainStartInput(q chainServiceRequest) ([]byte, error) {
	op, ok := map[string]byte{"update": 0, "save-profile": 1, "build": 2, "chain": 3, "prefix": 4, "compile": 5}[q.Op]
	if !ok {
		return nil, errors.New("unknown chain op")
	}
	name := q.Name
	if q.Op == "save-profile" {
		name = chainTrimName(name)
	}
	b := chainBytes([]byte{op}, q.Next)
	b = registryBytes(b, name)
	b = registryBytes(b, q.Original)
	b = registryInt(b, int64(utf8.RuneCountInString(name)))
	b = profileStrings(b, q.Hops)
	return b, nil
}
func decodeChainFrame(b []byte) (chainFrame, error) {
	r := quotaReader{b: b}
	f := chainFrame{kind: r.byte(), a: r.integer(), b: r.integer(), c: r.integer(), data: r.str(), text: r.str()}
	n := r.integer()
	if n < 0 || n > 100000 || n > int64((len(b)-r.p)/8) {
		r.bad = true
	} else {
		f.indices = make([]int64, n)
		for i := range f.indices {
			f.indices[i] = r.integer()
		}
	}
	if r.bad || r.p != len(b) {
		return f, errors.New("invalid chain frame")
	}
	return f, nil
}

type chainArena struct {
	q       chainServiceRequest
	next    int64
	input   []byte
	entries map[int64]int
	proxies map[int64]map[string]any
	bodies  map[int64][]byte
	errs    map[int64]error
}

func newChainArena(q chainServiceRequest) *chainArena {
	if q.Op == "save-profile" {
		q.Name = chainTrimName(q.Name)
	}
	return &chainArena{q: q, input: q.Profile, entries: map[int64]int{}, proxies: map[int64]map[string]any{}, bodies: map[int64][]byte{}, errs: map[int64]error{}}
}
func (a *chainArena) id() int64 { a.next++; return a.next }
func (a *chainArena) replyBytes(f chainFrame, r chainReply) ([]byte, error) {
	token := int64(0)
	msg := ""
	if r.Err != nil {
		token = a.id()
		a.errs[token] = r.Err
		msg = "opaque chain error"
	}
	if f.kind == 5 {
		a.input = r.Body
	}
	if f.kind == 8 {
		token = a.id()
		a.proxies[token] = chainCloneProxy(r.Proxy)
	}
	if f.kind == 11 && r.Err == nil {
		token = a.id()
		if r.Body != nil {
			a.bodies[token] = append([]byte{}, r.Body...)
		} else {
			a.bodies[token] = nil
		}
	}
	flag := r.Bool
	name := ""
	if f.kind == 9 {
		flag = r.Record.Status == regionVerified
		name = r.Record.Country
	}
	b := registryBool([]byte{f.kind}, r.Err == nil)
	b = registryBool(b, flag)
	b = registryInt(b, int64(r.Age))
	b = registryInt(b, int64(r.TTL))
	b = registryInt(b, token)
	b = registryBytes(b, name)
	b = registryBytes(b, msg)
	groups := map[string][]int{}
	keys := []string{}
	for i, e := range r.Entries {
		if _, ok := groups[e.Key]; !ok {
			keys = append(keys, e.Key)
		}
		groups[e.Key] = append(groups[e.Key], i)
	}
	b = registryInt(b, int64(len(keys)))
	for _, key := range keys {
		b = registryBytes(b, key)
		indices := groups[key]
		b = registryInt(b, int64(len(indices)))
		for _, i := range indices {
			t := a.id()
			a.entries[t] = i
			p := r.Entries[i].Proxy
			b = registryInt(b, t)
			b = registryBool(b, stringValue(p["dialer-proxy"]) != "")
			b = registryBool(b, stringValue(p["server"]) != "")
			b = registryBool(b, stringValue(p["type"]) != "")
		}
	}
	b = registryBool(b, r.State != nil)
	if s := r.State; s != nil {
		b = registryBool(b, s.Subscription)
		b = registryBytes(b, s.LockedRegion)
		b = chainBytes(b, s.Chain)
		b = registryInt(b, int64(len(s.Chains)))
		for name, hops := range s.Chains {
			b = registryBytes(b, name)
			b = profileStrings(b, hops)
			b = registryBool(b, hops == nil)
		}
	}
	if len(b) > 8<<20 {
		return nil, errors.New("chain reply limit")
	}
	return b, nil
}
func (a *chainArena) selectProxies(ids []int64) ([]map[string]any, bool) {
	var ps []map[string]any
	for _, id := range ids {
		p, ok := a.proxies[id]
		if !ok {
			return nil, false
		}
		ps = append(ps, p)
	}
	return ps, true
}
func (a *chainArena) port(f chainFrame) (chainEffect, bool) {
	q := chainEffect{}
	switch f.kind {
	case 1:
		q.Kind = "lock"
	case 2:
		q.Kind = "snapshot"
	case 3:
		q.Kind = "running"
	case 4:
		q.Kind = "job"
	case 5:
		q.Kind = "read"
	case 6:
		q.Kind = "decode"
		q.Body = a.input
		if f.a != 0 {
			q.Body = nil
		}
	case 7:
		q.Kind = "index"
	case 8:
		var ok bool
		q.Index, ok = a.entries[f.a]
		if !ok {
			return q, false
		}
		q.Kind = "clone"
		q.Name = f.data
		q.Bool = f.b != 0
		q.Previous = f.text
	case 9:
		q.Kind = "region"
		q.Name = f.data
	case 10:
		q.Kind = "age"
		q.Name = f.data
	case 11:
		q.Kind = "yaml"
		var ok bool
		q.Proxies, ok = a.selectProxies(f.indices)
		if !ok {
			return q, false
		}
	case 12:
		q.Kind = "commit-chain"
		if f.a == 0 {
			q.Next = a.q.Next
		} else if f.a == 1 {
			q.Next = proxyChain{Name: a.q.Name, Enabled: true, Hops: a.q.Hops}
		} else {
			return q, false
		}
	case 13:
		q.Kind = "commit-profiles"
		q.Name = f.data
		q.Previous = f.text
	case 14:
		q.Kind = "save"
	case 15:
		if f.a == 0 {
			q.Kind = "restore-chain"
		} else if f.a == 1 {
			q.Kind = "restore-profiles"
		} else {
			return q, false
		}
	default:
		return q, false
	}
	return q, true
}
func (a *chainArena) resultMatches(f chainFrame, r chainServiceResult) bool {
	if int64(r.Status) != f.a {
		return false
	}
	switch f.b {
	case 0:
		return r.Err == nil && r.Message == "" && manualOpaqueEqual(r.Next, a.q.Next)
	case 1:
		return r.Err == nil && r.Message == ""
	case 2:
		ps, ok := a.selectProxies(f.indices)
		return ok && r.Err == nil && r.Message == "" && manualOpaqueEqual(ps, r.Proxies)
	case 3:
		if len(f.indices) != 1 {
			return false
		}
		b, ok := a.bodies[f.indices[0]]
		return ok && r.Err == nil && r.Message == "" && manualOpaqueEqual(b, r.Body)
	case 4:
		return r.Err == nil && r.Message == "" && r.Body == nil
	case 40:
		e := a.errs[f.c]
		return e != nil && r.Err != nil && errors.Is(r.Err, e) && f.text == "opaque chain error" && r.Message == r.Err.Error()
	}
	msg := map[int64]string{10: chainHTTPMessage0(), 11: chainHTTPMessage5(), 12: chainHTTPMessage1(), 13: chainHTTPMessage2(), 14: chainHTTPMessage6(), 15: chainHTTPMessage7(), 16: chainHTTPMessage8(), 20: chainCompileMessage0(), 21: chainCompileMessage2(), 22: chainCompileMessage3(int(f.c)), 23: chainCompileMessage4(int(f.c)), 24: chainCompileMessage5(int(f.c)), 25: chainCompileMessage6(int(f.c)), 26: chainCompileMessage7(f.data), 27: chainCompileMessage8(), 30: chainHTTPMessage3(), 31: chainHTTPMessage9(), 28: chainCompileMessage1()}[f.b]
	return msg != "" && r.Err != nil && r.Err.Error() == msg && r.Message == msg
}
