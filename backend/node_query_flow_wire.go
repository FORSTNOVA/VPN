package main

import (
	"errors"
	"fmt"
	"reflect"
	"time"
)

type nodeQueryFlowSession interface {
	next() ([]byte, uint64, error)
	advance(uint64, []byte) error
	close()
}
type nodeQueryFlowStart func([]byte) (nodeQueryFlowSession, error)
type nodeQueryProjection struct {
	node, detail      int64
	network, selected bool
	label             string
}
type nodeQueryFrame struct {
	kind            byte
	a, b            int64
	name            string
	nodes           []nodeQueryProjection
	nilNodes, proxy bool
	error           byte
	token, latency  int64
}

func nodeQueryStartInput(q nodeQueryServiceRequest) ([]byte, error) {
	op, ok := map[string]byte{"nodes": 0, "list": 1, "latency": 2, "node-latency": 3, "wait": 4, "delay": 5}[q.Op]
	if !ok {
		return nil, errors.New("invalid node query op")
	}
	b := []byte{op}
	b = registryBool(b, q.Borrowed)
	b = registryInt(b, int64(q.Timeout))
	return b, nil
}
func decodeNodeQueryFrame(b []byte) (nodeQueryFrame, error) {
	r := quotaReader{b: b}
	f := nodeQueryFrame{kind: r.byte(), a: r.integer(), b: r.integer(), name: r.str()}
	n := r.integer()
	if n < 0 || n > 100000 {
		r.bad = true
	} else {
		for ; n > 0; n-- {
			f.nodes = append(f.nodes, nodeQueryProjection{r.integer(), r.integer(), r.byte() != 0, r.byte() != 0, r.str()})
		}
	}
	f.nilNodes = r.byte() != 0
	f.proxy = r.byte() != 0
	f.error = r.byte()
	f.token = r.integer()
	f.latency = r.integer()
	if r.bad || r.p != len(b) {
		return f, errors.New("invalid node query frame")
	}
	return f, nil
}

type nodeQueryArena struct {
	q                       nodeQueryServiceRequest
	cached, manual, profile []proxyNode
	providers               [2][]nodeQueryProvider
	deadline                time.Time
	lastErr                 error
	delays                  map[string]int
	httpStatus              int
}

func newNodeQueryArena(q nodeQueryServiceRequest) *nodeQueryArena { return &nodeQueryArena{q: q} }
func nodeQueryNodes(b []byte, ns []proxyNode, base int64) []byte {
	b = registryInt(b, int64(len(ns)))
	for i, n := range ns {
		b = registryInt(b, base+int64(i)+1)
		b = registryBytes(b, n.Name)
		b = registryBool(b, n.Network != "")
		b = registryBytes(b, n.SourceLabel)
		b = registryBool(b, n.Manual)
	}
	return b
}
func (a *nodeQueryArena) replyBytes(f nodeQueryFrame, r nodeQueryReply) ([]byte, error) {
	if len(r.Nodes) > 100000 || len(r.Provider) > 100000 {
		return nil, errors.New("node query count limit")
	}
	if m := r.Metadata; m != nil && (len(m.Cached) > 100000 || len(m.Manual) > 100000 || len(m.Sources) > 100000) {
		return nil, errors.New("node query metadata limit")
	}
	token := int64(0)
	if r.Err != nil {
		a.lastErr = r.Err
		token = 1
	}
	if f.kind == 13 {
		a.deadline = r.Now
		token = 1
	}
	if f.kind == 10 {
		a.httpStatus = r.Status
	}
	if f.kind == 11 {
		a.delays = r.Delays
	}
	value := int64(r.Status)
	if f.kind == 17 {
		value = int64(r.Value)
	}
	b := []byte{f.kind}
	b = registryBool(b, r.Err == nil)
	b = registryBool(b, r.Bool)
	b = registryInt(b, value)
	b = registryInt(b, token)
	b = registryBytes(b, r.Text)
	a.profile = func() []proxyNode {
		if f.kind == 8 {
			return r.Nodes
		}
		return a.profile
	}()
	b = nodeQueryNodes(b, r.Nodes, 200000)
	idx := 0
	if f.name == manualProviderName {
		idx = 1
	}
	if f.kind == 9 {
		a.providers[idx] = r.Provider
	}
	b = registryInt(b, int64(len(r.Provider)))
	for i, p := range r.Provider {
		b = registryInt(b, int64(300000+idx*100000+i+1))
		b = registryBytes(b, p.Name)
		b = registryBool(b, p.Valid)
		b = registryBool(b, false)
	}
	b = registryBool(b, r.Metadata != nil)
	if m := r.Metadata; m != nil {
		a.cached = m.Cached
		a.manual = m.Manual
		b = nodeQueryNodes(b, m.Cached, 0)
		b = nodeQueryNodes(b, m.Manual, 100000)
		b = registryInt(b, int64(len(m.Sources)))
		for _, s := range m.Sources {
			b = registryBool(b, s.URL == m.SubscriptionURL)
			b = registryBytes(b, s.Label)
		}
		sub := byte(2)
		if m.SubscriptionURL == "" {
			sub = 0
		} else if m.SubscriptionURL == mergedSubscriptionURL {
			sub = 1
		}
		b = append(b, sub)
		b = registryBytes(b, m.Selected)
		b = registryBool(b, m.ProxyEnabled)
		b = registryInt(b, int64(m.ConnectTimeout))
	}
	if len(b) > 8<<20 || len(r.Nodes) > 100000 || len(r.Provider) > 100000 || len(a.cached) > 100000 || len(a.manual) > 100000 {
		return nil, errors.New("node query reply limit")
	}
	return b, nil
}
func (a *nodeQueryArena) detail(tok int64) (proxyNode, bool) {
	var ns []proxyNode
	switch {
	case tok == 0:
		return proxyNode{}, true
	case tok <= 100000:
		ns = a.cached
	case tok <= 200000:
		ns = a.manual
		tok -= 100000
	case tok <= 300000:
		ns = a.profile
		tok -= 200000
	default:
		return proxyNode{}, false
	}
	if tok < 1 || tok > int64(len(ns)) {
		return proxyNode{}, false
	}
	return ns[tok-1], true
}
func (a *nodeQueryArena) project(f nodeQueryFrame) ([]proxyNode, bool) {
	var out []proxyNode
	if !f.nilNodes {
		out = make([]proxyNode, 0, len(f.nodes))
	}
	for _, p := range f.nodes {
		if p.node <= 300000 {
			n, ok := a.detail(p.node)
			if !ok {
				return nil, false
			}
			n.Selected = p.selected
			out = append(out, n)
			continue
		}
		i := p.node - 300001
		set := i / 100000
		idx := i % 100000
		if set < 0 || set > 1 || idx < 0 || idx >= int64(len(a.providers[set])) {
			return nil, false
		}
		v := a.providers[set][idx]
		d, ok := a.detail(p.detail)
		if !ok {
			return nil, false
		}
		network := v.Network
		if p.network {
			network = d.Network
		}
		out = append(out, proxyNode{Name: v.Name, Type: v.Type, Network: network, TLS: d.TLS, SourceLabel: p.label, WsHost: d.WsHost, WsPath: d.WsPath, Alive: v.Alive, Selected: p.selected, Manual: d.Manual})
	}
	return out, true
}

func nodeQueryMessage(code int64) string {
	switch code {
	case 0:
		return ""
	case 1:
		return "could not read Mihomo nodes"
	case 2:
		return "connect before measuring latency"
	case 3:
		return "could not measure the current node"
	case 4:
		return "could not read the current node"
	case 5:
		return "current node could not reach YouTube; select another node"
	case 6:
		return "connect before measuring node latency"
	case 7:
		return "could not measure the nodes"
	}
	return "invalid node query result"
}
func (a *nodeQueryArena) result(f nodeQueryFrame) (nodeQueryServiceResult, bool) {
	r := nodeQueryServiceResult{Status: int(f.a), Message: nodeQueryMessage(f.b), Selected: f.name, Latency: int(f.latency), ProxyEnabled: f.proxy}
	switch f.error {
	case 0:
	case 1:
		if f.token != 1 || a.lastErr == nil {
			return r, false
		}
		r.Err = a.lastErr
	case 2:
		r.Err = fmt.Errorf("Mihomo delay API returned HTTP %d", f.token)
	case 3:
		r.Err = errors.New("the kernel reported no usable nodes")
	default:
		return r, false
	}
	var ok bool
	r.Nodes, ok = a.project(f)
	if !ok {
		return r, false
	}
	if a.q.Op == "delay" || a.q.Op == "latency" || a.q.Op == "node-latency" {
		r.Delays = a.delays
		if f.error != 0 {
			r.Delays = nil
		}
	}
	return r, true
}
func (a *nodeQueryArena) port(f nodeQueryFrame) (nodeQueryEffect, bool) {
	e := nodeQueryEffect{}
	switch f.kind {
	case 1:
		e.Kind = "lock"
	case 2:
		e.Kind = "unlock"
	case 3:
		e.Kind = "running"
	case 4:
		e.Kind = "endpoint"
	case 5:
		e.Kind = "metadata"
		e.Name = f.name
	case 6:
		e.Kind = "group"
		e.Name = f.name
	case 7:
		e.Kind = "read-profile"
	case 8:
		e.Kind = "parse-profile"
	case 9:
		e.Kind = "provider"
		e.Name = f.name
	case 10:
		e.Kind = "delay-request"
		e.Duration = time.Duration(f.a)
		if f.b == 0 {
			e.Text = a.q.Target
		} else if f.b == 1 {
			e.Text = patrolTargetPrimary
		} else {
			return e, false
		}
	case 11:
		e.Kind = "delay-decode"
	case 12:
		e.Kind = "body-close"
	case 13:
		e.Kind = "deadline"
		e.Duration = time.Duration(f.a)
	case 14:
		e.Kind = "before"
		if f.a != 1 {
			return e, false
		}
		e.Time = a.deadline
	case 15:
		e.Kind = "sleep"
		e.Duration = time.Duration(f.a)
	case 16:
		e.Kind = "response"
		e.Name = a.q.Op
		var ok bool
		e.Result, ok = a.result(f)
		if !ok {
			return e, false
		}
	case 17:
		e.Kind = "delay-value"
		e.Name = f.name
		e.Result.Delays = a.delays
	default:
		return e, false
	}
	return e, true
}
func (a *nodeQueryArena) resultMatches(f nodeQueryFrame, want nodeQueryServiceResult) bool {
	r, ok := a.result(f)
	if !ok {
		return false
	}
	if (r.Err == nil) != (want.Err == nil) {
		return false
	}
	if r.Err != nil {
		if f.error == 1 {
			if !errors.Is(want.Err, r.Err) {
				return false
			}
		} else if r.Err.Error() != want.Err.Error() {
			return false
		}
	}
	r.Err = nil
	want.Err = nil
	return reflect.DeepEqual(r, want)
}
