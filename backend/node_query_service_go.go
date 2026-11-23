package main

import (
	"errors"
	"fmt"
	"time"
)

type goNodeQueryService struct {
	q            nodeQueryServiceRequest
	fx           nodeQueryServiceEffects
	locked, owns bool
}

func (a *goNodeQueryService) call(q nodeQueryEffect) nodeQueryReply { return a.fx.Call(q) }
func (a *goNodeQueryService) lock() {
	a.call(nodeQueryEffect{Kind: "lock"})
	a.locked = true
	a.owns = true
}
func (a *goNodeQueryService) unlock() {
	a.call(nodeQueryEffect{Kind: "unlock"})
	a.locked = false
	a.owns = false
}
func (a *goNodeQueryService) capture(kind, name string) nodeQueryReply {
	own := !a.locked
	if own {
		a.lock()
	}
	r := a.call(nodeQueryEffect{Kind: kind, Name: name})
	if own {
		a.unlock()
	}
	return r
}
func (a *goNodeQueryService) endpoint() { a.capture("endpoint", "") }
func (a *goNodeQueryService) list() ([]proxyNode, error) {
	a.endpoint()
	g := a.call(nodeQueryEffect{Kind: "group", Name: "SmartVPN"})
	if g.Err != nil {
		return nil, g.Err
	}
	m := a.capture("metadata", "list").Metadata
	details := map[string]proxyNode{}
	if a.call(nodeQueryEffect{Kind: "read-profile"}).Err == nil {
		parsed := a.call(nodeQueryEffect{Kind: "parse-profile"}).Nodes
		label := ""
		if m.SubscriptionURL != "" && m.SubscriptionURL != mergedSubscriptionURL {
			label = nodeQueryDefaultLabel()
			for _, s := range m.Sources {
				if s.URL == m.SubscriptionURL && s.Label != "" {
					label = s.Label
					break
				}
			}
		}
		for _, n := range parsed {
			if !n.Manual && n.SourceLabel == "" && label != "" {
				n.SourceLabel = label
			}
			details[n.Name] = n
		}
	}
	for _, n := range m.Manual {
		details[n.Name] = n
	}
	out := make([]proxyNode, 0, 64)
	seen := map[string]bool{}
	for _, provider := range []string{"SmartVPNSubscription", manualProviderName} {
		r := a.call(nodeQueryEffect{Kind: "provider", Name: provider})
		if r.Err != nil {
			continue
		}
		for _, p := range r.Provider {
			if !p.Valid || p.Name == "" || seen[p.Name] || isInformationalNodeGo(p.Name) {
				continue
			}
			seen[p.Name] = true
			extra := details[p.Name]
			network := extra.Network
			if network == "" {
				network = p.Network
			}
			out = append(out, proxyNode{Name: p.Name, Type: p.Type, Network: network, TLS: extra.TLS, SourceLabel: extra.SourceLabel, WsHost: extra.WsHost, WsPath: extra.WsPath, Alive: p.Alive, Selected: p.Name == g.Text, Manual: extra.Manual})
		}
	}
	return out, nil
}
func (a *goNodeQueryService) delay(target string, timeout time.Duration) (map[string]int, error) {
	a.endpoint()
	r := a.call(nodeQueryEffect{Kind: "delay-request", Text: target, Duration: timeout})
	if r.Err != nil {
		return nil, r.Err
	}
	defer a.call(nodeQueryEffect{Kind: "body-close"})
	if r.Status != 200 {
		return nil, fmt.Errorf("Mihomo delay API returned HTTP %d", r.Status)
	}
	r = a.call(nodeQueryEffect{Kind: "delay-decode"})
	if r.Err != nil {
		return nil, r.Err
	}
	return r.Delays, nil
}
func (a *goNodeQueryService) active() string {
	a.endpoint()
	r := a.call(nodeQueryEffect{Kind: "group", Name: "SmartVPN"})
	if r.Err != nil {
		return ""
	}
	n := r.Text
	if n == autoGroup || n == fallbackGroup {
		r = a.call(nodeQueryEffect{Kind: "group", Name: n})
		if r.Err == nil {
			n = r.Text
		}
	}
	return n
}
func runGoNodeQueryService(q nodeQueryServiceRequest, fx nodeQueryServiceEffects) (r nodeQueryServiceResult) {
	defer fx.Release()
	a := &goNodeQueryService{q: q, fx: fx, locked: q.Borrowed}
	defer func() {
		if a.owns {
			a.unlock()
		}
	}()
	response := func() { a.call(nodeQueryEffect{Kind: "response", Name: q.Op, Result: r}) }
	fail := func(code int, msg string) { r.Status = code; r.Message = msg; response() }
	switch q.Op {
	case "list":
		r.Nodes, r.Err = a.list()
	case "delay":
		r.Delays, r.Err = a.delay(q.Target, q.Timeout)
	case "wait":
		deadline := a.call(nodeQueryEffect{Kind: "deadline", Duration: q.Timeout}).Now
		for a.call(nodeQueryEffect{Kind: "before", Time: deadline}).Bool {
			nodes, err := a.list()
			if err == nil && len(nodes) > 0 {
				return
			}
			a.call(nodeQueryEffect{Kind: "sleep", Duration: 50 * time.Millisecond})
		}
		r.Err = errors.New("the kernel reported no usable nodes")
	case "nodes":
		a.lock()
		running := a.call(nodeQueryEffect{Kind: "running"}).Bool
		m := a.call(nodeQueryEffect{Kind: "metadata", Name: "nodes"}).Metadata
		a.unlock()
		if running {
			r.Nodes, r.Err = a.list()
			if r.Err != nil {
				fail(502, "could not read Mihomo nodes")
				return
			}
			r.ProxyEnabled = m.ProxyEnabled
			response()
			return
		}
		r.Nodes = append([]proxyNode(nil), m.Cached...)
		present := map[string]bool{}
		for i := range r.Nodes {
			r.Nodes[i].Selected = r.Nodes[i].Name == m.Selected
			present[r.Nodes[i].Name] = true
		}
		for _, n := range m.Manual {
			if present[n.Name] {
				continue
			}
			n.Selected = n.Name == m.Selected
			r.Nodes = append(r.Nodes, n)
		}
		response()
	case "latency":
		a.lock()
		running := a.call(nodeQueryEffect{Kind: "running"}).Bool
		a.unlock()
		if !running {
			fail(409, "connect before measuring latency")
			return
		}
		r.Delays, r.Err = a.delay(patrolTargetPrimary, 8*time.Second)
		if r.Err != nil {
			fail(502, "could not measure the current node")
			return
		}
		r.Selected = a.active()
		if r.Selected == "" {
			fail(502, "could not read the current node")
			return
		}
		r.Latency = a.call(nodeQueryEffect{Kind: "delay-value", Name: r.Selected, Result: nodeQueryServiceResult{Delays: r.Delays}}).Value
		if r.Latency <= 0 {
			fail(502, "current node could not reach YouTube; select another node")
			return
		}
		response()
	case "node-latency":
		a.lock()
		if !a.call(nodeQueryEffect{Kind: "running"}).Bool {
			fail(409, "connect before measuring node latency")
			return
		}
		m := a.call(nodeQueryEffect{Kind: "metadata", Name: "timeout"}).Metadata
		r.Delays, r.Err = a.delay(patrolTargetPrimary, m.ConnectTimeout)
		if r.Err != nil {
			fail(502, "could not measure the nodes")
			return
		}
		response()
	}
	return
}
