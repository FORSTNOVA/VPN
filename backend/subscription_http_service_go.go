package main

import (
	"sort"
	"time"
)

type subscriptionHTTPOracle struct {
	fx subscriptionHTTPServiceEffects
}

func (o subscriptionHTTPOracle) call(k string) subscriptionHTTPServiceReply {
	return o.fx.Call(subscriptionHTTPServiceEffect{Kind: k})
}
func subscriptionHTTPCounters(m map[string]int) []subscriptionHTTPValue {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []subscriptionHTTPValue
	for _, k := range keys {
		out = append(out, subscriptionHTTPValue{k, m[k]})
	}
	return out
}
func (o subscriptionHTTPOracle) analyze() subscriptionHTTPAnalysis {
	initial := o.call("payload-start")
	p := subscriptionHTTPAnalysis{Present: initial.Count > 0, Bytes: initial.Count, Selectable: initial.Other}
	if initial.Count == 0 {
		p.Format = "empty"
		return p
	}
	r := o.call("payload-text")
	p.Format = "unknown"
	switch {
	case r.Clash:
		p.Format = "clash-yaml"
	case r.JSON:
		p.Format = "singbox-json"
	case r.URI:
		p.Format = "uri-list"
	default:
		r = o.call("payload-base64")
		if r.Err == nil {
			switch {
			case r.URI:
				p.Format = "base64-uri-list"
			case r.Clash:
				p.Format = "base64-clash-yaml"
			case r.JSON:
				p.Format = "base64-singbox-json"
			}
		}
	}
	uri, nets, tls := map[string]int{}, map[string]int{}, map[string]int{}
	for _, scheme := range []string{"vmess", "vless", "ss", "ssr", "trojan", "hysteria2", "tuic"} {
		uri[scheme] = o.fx.Call(subscriptionHTTPServiceEffect{Kind: "uri-count", Text: scheme}).Count
	}
	if p.Format == "clash-yaml" || p.Format == "base64-clash-yaml" {
		for _, n := range o.call("payload-nodes").Nodes {
			if n.Vmess {
				p.ValidVmess++
			}
			network := n.Network
			if network == "" {
				network = "tcp/default"
			}
			nets[network]++
			if n.TLS {
				tls["tls"]++
			} else {
				tls["none"]++
			}
			if n.WsHost {
				p.WsHostSet++
			}
			if n.WsPath {
				p.WsPathSet++
			}
		}
	}
	count := o.call("vmess-matches").Count
	for i := 0; i < count; i++ {
		r = o.fx.Call(subscriptionHTTPServiceEffect{Kind: "vmess-decode", Count: i})
		if r.Err != nil {
			r = o.fx.Call(subscriptionHTTPServiceEffect{Kind: "vmess-decode", Count: i, Flag: true})
		}
		if r.Err != nil {
			continue
		}
		r = o.call("vmess-json")
		if r.Err != nil {
			continue
		}
		p.ValidVmess++
		if isInformationalNodeGo(r.Text) {
			p.InfoEntries++
		}
		network := r.Network
		if network == "" {
			network = "tcp/default"
		}
		nets[network]++
		t := r.TLS
		if t == "" {
			t = "none"
		}
		tls[t]++
		if network == "ws" {
			if r.Host {
				p.WsHostSet++
			}
			if r.Path {
				p.WsPathSet++
			}
		}
	}
	p.UriCounts = subscriptionHTTPCounters(uri)
	p.Networks = subscriptionHTTPCounters(nets)
	p.TlsModes = subscriptionHTTPCounters(tls)
	return p
}
func runGoSubscriptionHTTPService(q subscriptionHTTPServiceRequest, fx subscriptionHTTPServiceEffects) (out subscriptionHTTPServiceResult) {
	o := subscriptionHTTPOracle{fx}
	defer fx.Release()
	response := func(status int, key string) {
		fx.Call(subscriptionHTTPServiceEffect{Kind: "response", Status: status, Text: key})
	}
	switch q.Op {
	case "analyze":
		out.Analysis = o.analyze()
	case "fetch", "refresh":
		o.call("lock")
		defer o.call("unlock")
		running := o.call("running").Flag
		if q.Op == "fetch" && running {
			response(409, "use-refresh")
			return
		}
		if q.Op == "refresh" && !running {
			response(409, "connect")
			return
		}
		if o.call("fetch-profile").Err != nil {
			response(502, "fetch")
			return
		}
		if q.Op == "fetch" {
			o.call("format-read")
			response(200, "fetch")
			return
		}
		if o.call("provider-put").Err != nil {
			response(502, "provider")
			return
		}
		if fx.Call(subscriptionHTTPServiceEffect{Kind: "wait-nodes", Interval: 15 * time.Second}).Err != nil {
			response(502, "wait")
			return
		}
		if o.call("list-nodes").Err != nil {
			response(502, "list")
			return
		}
		o.call("cache-nodes")
		if o.call("save-nodes").Err != nil {
			response(500, "save")
			return
		}
		response(200, "refresh")
	case "diagnostics":
		o.call("lock")
		s := o.call("source-snapshot")
		o.call("unlock")
		if !s.Flag {
			response(400, "no-url")
			return
		}
		r := o.call("url-parse")
		if r.Err != nil {
			response(400, "url")
			return
		}
		keys := append([]string{}, r.Names...)
		sort.Strings(keys)
		fx.Call(subscriptionHTTPServiceEffect{Kind: "query-keys", Names: keys})
		if o.call("request-new").Err != nil {
			response(400, "url")
			return
		}
		if o.call("request-do").Err != nil {
			fx.Call(subscriptionHTTPServiceEffect{Kind: "fetch-error", Text: "network"})
		} else {
			defer o.call("body-close")
			r = o.call("body-read")
			n := r.Count
			if n > 10*1024*1024 {
				n = 10 * 1024 * 1024
				fx.Call(subscriptionHTTPServiceEffect{Kind: "body-truncate", Count: n})
				fx.Call(subscriptionHTTPServiceEffect{Kind: "fetch-error", Text: "large"})
			} else if r.Err != nil {
				fx.Call(subscriptionHTTPServiceEffect{Kind: "fetch-error", Text: "read"})
			}
			fx.Call(subscriptionHTTPServiceEffect{Kind: "fetch-meta", Count: n})
			p := o.analyze()
			fx.Call(subscriptionHTTPServiceEffect{Kind: "payload-commit", Text: "fetch", Analysis: p})
		}
		if o.call("cache-read").Err == nil {
			p := o.analyze()
			fx.Call(subscriptionHTTPServiceEffect{Kind: "payload-commit", Text: "cache", Analysis: p})
		}
		r = o.call("source-read")
		if r.Err == nil && r.Flag {
			o.call("diagnostic-format")
		}
		response(200, "diagnostics")
	case "start":
		if o.call("cancel-present").Flag {
			return
		}
		o.call("cancel-new")
		o.call("spawn")
	case "stop":
		if o.call("cancel-present").Flag {
			o.call("cancel-close")
		}
	case "loop":
		fx.Call(subscriptionHTTPServiceEffect{Kind: "ticker-new", Interval: 10 * time.Minute})
		defer o.call("ticker-stop")
		for o.call("wait").Flag {
			o.call("tick-child")
		}
	case "tick":
		o.call("lock")
		defer o.call("unlock")
		o.call("auto-tick")
	default:
		panic("invalid subscription HTTP usecase")
	}
	return
}
