package main

import (
	"errors"
	"fmt"
	"strings"
)

type converterOracle struct{ fx converterServiceEffects }

func (o converterOracle) c(k string, t int) converterServiceReply {
	return o.fx.Call(converterServiceEffect{Kind: k, Token: t})
}
func converterFind(fs []converterField, k string) converterField {
	for _, f := range fs {
		if f.Key == k {
			return f
		}
	}
	return converterField{Key: k, Empty: true, TrimEmpty: true}
}
func cr(k string, r int) converterAssignment   { return converterAssignment{Key: k, Ref: r} }
func cs(k, v string) converterAssignment       { return converterAssignment{Key: k, Kind: 1, Text: v} }
func ci(k string, v int64) converterAssignment { return converterAssignment{Key: k, Kind: 2, Value: v} }
func cb(k string) converterAssignment          { return converterAssignment{Key: k, Kind: 3, Value: 1} }
func (o converterOracle) info(s string) bool   { return isInformationalNodeGo(s) }
func (o converterOracle) node(n converterNode) int {
	return o.fx.Call(converterServiceEffect{Kind: "node-new", Node: n}).Token
}
func (o converterOracle) finish(ps, ns []int, nonNil bool) converterServiceResult {
	p := o.fx.Call(converterServiceEffect{Kind: "proxies-build", Tokens: ps, Flag: nonNil}).Token
	n := o.fx.Call(converterServiceEffect{Kind: "nodes-build", Tokens: ns, Flag: nonNil}).Token
	n = o.c("nodes-unique-child", n).Token
	return converterServiceResult{Proxies: p, Nodes: n}
}
func (o converterOracle) singbox(body int) converterServiceResult {
	d := o.c("decode-singbox", body)
	if d.Err != nil || len(d.Tokens) == 0 {
		return converterServiceResult{}
	}
	var ps, ns []int
	for _, t := range d.Tokens {
		b := o.c("decode-base", t)
		if b.Err != nil {
			continue
		}
		bf := func(k string) converterField { return converterFind(b.Fields, k) }
		name := bf("tag").Trim
		if name == "" || o.info(name) {
			continue
		}
		kind := bf("type").Lower
		switch kind {
		case "ss", "shadowsocks", "vmess", "vless", "trojan", "hysteria2", "tuic":
		default:
			continue
		}
		p := o.fx.Call(converterServiceEffect{Kind: "decode-outbound", Token: t, Key: kind})
		if p.Err != nil {
			continue
		}
		f := func(k string) converterField { return converterFind(p.Fields, k) }
		typ := kind
		if kind == "shadowsocks" {
			typ = "ss"
		}
		switch typ {
		case "ss":
			if f("method").Empty || f("password").Empty {
				continue
			}
		case "vmess", "vless":
			if f("uuid").Empty {
				continue
			}
		case "trojan", "hysteria2":
			if f("password").Empty {
				continue
			}
		case "tuic":
			if f("uuid").Empty && f("password").Empty {
				continue
			}
		}
		a := []converterAssignment{cs("name", name), cs("type", typ), cr("server", bf("server").TrimRef), ci("port", bf("server_port").Int)}
		switch typ {
		case "ss":
			a = append(a, cr("cipher", f("method").Ref), cr("password", f("password").Ref), cb("udp"))
		case "vmess":
			a = append(a, cr("uuid", f("uuid").Ref))
			if f("security").TrimEmpty {
				a = append(a, cs("cipher", "auto"))
			} else {
				a = append(a, cr("cipher", f("security").TrimRef))
			}
			a = append(a, ci("alterId", f("alter_id").Int), cb("udp"))
		case "vless":
			a = append(a, cr("uuid", f("uuid").Ref), cb("udp"))
			if !f("flow").Empty {
				a = append(a, cr("flow", f("flow").Ref))
			}
		case "trojan":
			a = append(a, cr("password", f("password").Ref), cb("udp"))
		case "hysteria2":
			a = append(a, cr("password", f("password").Ref))
		case "tuic":
			a = append(a, cr("uuid", f("uuid").Ref), cr("password", f("password").Ref), cb("udp"))
			if !f("congestion_controller").Empty {
				a = append(a, cr("congestion-controller", f("congestion_controller").Ref))
			}
		}
		n := converterNode{Name: name, Type: typ, Network: "tcp"}
		tls := false
		tlsFields := false
		sni := "servername"
		switch typ {
		case "vmess", "vless":
			tls = f("tls").Count > 0 && f("tls.enabled").Bool
			tlsFields = tls
			if tls {
				a = append(a, cb("tls"))
			}
		case "trojan", "hysteria2", "tuic":
			tls = true
			tlsFields = f("tls").Count > 0
			sni = "sni"
		}
		n.TLS = tls
		if tlsFields {
			if !f("tls.server_name").Empty {
				a = append(a, cr(sni, f("tls.server_name").Ref))
			}
			if f("tls.insecure").Bool {
				a = append(a, cb("skip-cert-verify"))
			}
			if f("tls.alpn").Count > 0 {
				a = append(a, cr("alpn", f("tls.alpn").Ref))
			}
			if typ == "vless" && f("tls.reality").Count > 0 && f("tls.reality.enabled").Bool {
				a = append(a, cr("reality-opts.public-key", f("tls.reality.public_key").Ref))
				if !f("tls.reality.short_id").Empty {
					a = append(a, cr("reality-opts.short-id", f("tls.reality.short_id").Ref))
				}
			}
		}
		if typ == "hysteria2" && f("obfs").Count > 0 && !f("obfs.type").Empty {
			a = append(a, cr("obfs", f("obfs.type").Ref))
			if !f("obfs.password").Empty {
				a = append(a, cr("obfs-password", f("obfs.password").Ref))
			}
		}
		pt := o.fx.Call(converterServiceEffect{Kind: "map-new", Fields: a}).Token
		if (typ == "vmess" || typ == "vless" || typ == "trojan") && f("transport").Count > 0 {
			n.Network = f("transport.type").Text
			n.WsHost = !f("transport.host").Empty
			n.WsPath = !f("transport.path").Empty
			o.fx.Call(converterServiceEffect{Kind: "transport-child", Token: pt, Text: n.Network, Tokens: []int{f("transport.path").Ref, f("transport.host").Ref, f("transport.path").Ref}})
		}
		if typ == "hysteria2" || typ == "tuic" {
			n.Network = "udp"
		}
		ps = append(ps, pt)
		ns = append(ns, o.node(n))
	}
	return o.finish(ps, ns, true)
}

func (o converterOracle) clash(body int) (converterServiceResult, bool) {
	d := o.c("decode-clash", body)
	if d.Err != nil || d.Count == 0 {
		return converterServiceResult{}, false
	}
	var ps []int
	for _, t := range d.Tokens {
		f := o.c("proxy-facts", t)
		if f.Text != "" && !o.info(f.Text) {
			ps = append(ps, t)
		}
	}
	if len(ps) == 0 {
		return converterServiceResult{}, false
	}
	p := o.fx.Call(converterServiceEffect{Kind: "proxies-build", Tokens: ps, Flag: true}).Token
	n := o.c("yaml-nodes-child", body).Token
	return converterServiceResult{Proxies: p, Nodes: n}, true
}
func (o converterOracle) nonempty(r converterServiceResult) bool {
	return r.Proxies != 0 && len(o.c("proxies-items", r.Proxies).Tokens) > 0
}
func (o converterOracle) parse(body int) converterServiceResult {
	f := o.c("body-facts", body)
	if f.Flag {
		return converterServiceResult{Err: errNoUsableNodes}
	}
	if f.Text == "json" {
		r := o.singbox(body)
		if o.nonempty(r) {
			return r
		}
	}
	if r, ok := o.clash(body); ok {
		return r
	}
	if !converterFind(f.Fields, "uri").Bool {
		d := o.c("base64", body)
		if d.Err == nil {
			body = d.Token
			df := o.c("body-facts", body)
			if df.Text == "json" {
				r := o.singbox(body)
				if o.nonempty(r) {
					return r
				}
			}
			if r, ok := o.clash(body); ok {
				return r
			}
		}
	}
	var ps, ns []int
	for _, line := range o.c("lines", body).Tokens {
		l := o.c("line-facts", line)
		if converterFind(l.Fields, "empty").Bool || converterFind(l.Fields, "comment").Bool || !l.Flag {
			continue
		}
		line = l.Token
		if l.Text == "vmess" {
			d := o.c("vmess-base64", line)
			if d.Err != nil {
				continue
			}
			v := o.c("decode-vmess", d.Token)
			if v.Err != nil {
				continue
			}
			vf := func(k string) converterField { return converterFind(v.Fields, k) }
			name := vf("ps").Trim
			if name == "" {
				name = vf("fallback").Text
			}
			if o.info(name) {
				continue
			}
			tls := vf("tls").Lower
			hasTLS := tls != "" && tls != "none" && tls != "false"
			network := vf("net").Text
			if network == "" {
				network = "tcp"
			}
			ns = append(ns, o.node(converterNode{Name: name, Type: "Vmess", Network: network, TLS: hasTLS, WsHost: !vf("host").Empty, WsPath: !vf("path").Empty}))
			a := []converterAssignment{cs("name", name), cs("type", "vmess"), cr("server", vf("add").TrimRef), ci("port", vf("port-number").Int)}
			if vf("id").TrimEmpty {
				a = append(a, cs("uuid", "00000000-0000-0000-0000-000000000000"))
			} else {
				a = append(a, cr("uuid", vf("id").TrimRef))
			}
			cipher := vf("scy").Trim
			if cipher == "" || vf("scy").Lower == "auto" {
				cipher = "auto"
			}
			a = append(a, ci("alterId", vf("aid-number").Int), cs("cipher", cipher), cb("udp"))
			if hasTLS {
				a = append(a, cb("tls"))
				sni := vf("sni")
				if sni.TrimEmpty {
					sni = vf("host")
				}
				if !sni.TrimEmpty {
					a = append(a, cr("servername", sni.TrimRef))
				}
			}
			p := o.fx.Call(converterServiceEffect{Kind: "map-new", Fields: a}).Token
			o.fx.Call(converterServiceEffect{Kind: "transport-child", Token: p, Text: vf("net").Text, Tokens: []int{vf("path").Ref, vf("host").Ref, vf("path").Ref}})
			ps = append(ps, p)
			continue
		}
		switch l.Text {
		case "vless", "trojan", "ss", "hysteria2", "hy2", "anytls":
			u := o.c("decode-uri", line)
			if u.Err != nil {
				continue
			}
			name := u.Text
			if name == "" && len(u.Names) > 0 {
				name = u.Names[0]
			}
			if name == "" || o.info(name) {
				continue
			}
			uf := func(k string) string { return converterFind(u.Fields, k).Text }
			sec := uf("security")
			tls := tlsEnabled(sec) || uf("tls") == "1"
			if l.Text == "trojan" && sec == "" && uf("tls") == "" {
				tls = true
			}
			if sec == "none" {
				tls = false
			}
			ns = append(ns, o.node(converterNode{Name: name, Type: l.Text, Network: uf("type"), TLS: tls, WsHost: !converterFind(u.Fields, "host").Empty, WsPath: !converterFind(u.Fields, "path").Empty}))
			p := o.c("uri-child", line)
			if p.Err == nil {
				ps = append(ps, p.Token)
			}
			continue
		}
		p := o.c("uri-child", line)
		if p.Err != nil || o.info(p.Node.Name) {
			continue
		}
		ps = append(ps, p.Token)
		facts := o.c("proxy-facts", p.Token)
		n := facts.Node
		n.Name = p.Node.Name
		n.Type = p.Node.Type
		ns = append(ns, o.node(n))
	}
	if len(ps) == 0 {
		return converterServiceResult{Err: errNoUsableNodes}
	}
	return o.finish(ps, ns, false)
}
func (o converterOracle) ensure(body int) converterServiceResult {
	d := o.c("base64", body)
	if d.Err == nil {
		n := o.c("yaml-nodes-child", d.Token)
		if n.Count > 0 {
			return converterServiceResult{Body: d.Token, Nodes: o.c("yaml-nodes-child", d.Token).Token}
		}
	}
	f := o.c("body-facts", body)
	if f.Count > 0 && f.Text != "json" {
		n := o.c("yaml-nodes-child", body)
		if n.Count > 0 {
			return converterServiceResult{Body: body, Nodes: n.Token}
		}
	}
	r := o.parse(body)
	if r.Err != nil {
		return r
	}
	m := o.c("marshal-proxies", r.Proxies)
	if m.Err != nil {
		return converterServiceResult{Err: errors.New("failed to marshal converted proxies to YAML")}
	}
	r.Body = m.Token
	return r
}
func (o converterOracle) unique(name string) string {
	if !o.fx.Call(converterServiceEffect{Kind: "used-name", Text: name}).Flag {
		o.fx.Call(converterServiceEffect{Kind: "use-name", Text: name})
		return name
	}
	for suffix := 2; ; suffix++ {
		candidate := fmt.Sprintf("%s (%d)", name, suffix)
		if o.fx.Call(converterServiceEffect{Kind: "used-name", Text: candidate}).Flag {
			continue
		}
		o.fx.Call(converterServiceEffect{Kind: "use-name", Text: candidate})
		return candidate
	}
}
func converterUniqueLocal(name string, used map[string]bool) string {
	if !used[name] {
		used[name] = true
		return name
	}
	for n := 2; ; n++ {
		s := fmt.Sprintf("%s (%d)", name, n)
		if !used[s] {
			used[s] = true
			return s
		}
	}
}

// Bounds include field whitespace so replacing a target preserves surrounding
// rule syntax, including a trailing no-resolve option.
func mergeRuleTargetGo(rule string) (int, int, bool) {
	end := len(rule)
	comma := strings.LastIndexByte(rule, ',')
	if comma >= 0 && strings.TrimSpace(rule[comma+1:]) == "no-resolve" {
		end = comma
		comma = strings.LastIndexByte(rule[:end], ',')
	}
	if comma < 0 {
		return 0, 0, false
	}
	target := strings.TrimSpace(rule[comma+1 : end])
	if target == "DIRECT" || target == "REJECT" {
		return 0, 0, false
	}
	return comma + 1, end, true
}

func (o converterOracle) merge(sources []converterSource) converterServiceResult {
	m := o.c("merge-new", 0).Token
	usedNames := map[string]bool{}
	usedLabels := map[string]bool{}
	dnsKeys := map[string]bool{}
	proxyCount, groupCount, ruleCount := 0, 0, 0
	for index, src := range sources {
		p := o.parse(src.Body)
		if p.Err != nil || !o.nonempty(p) {
			continue
		}
		d := o.c("dns-child", src.Body)
		for i, k := range d.Names {
			if !dnsKeys[k] {
				dnsKeys[k] = true
				o.fx.Call(converterServiceEffect{Kind: "merge-dns", Token: m, Ref: d.Tokens[i], Text: k})
			}
		}
		label := strings.TrimSpace(src.Label)
		if label == "" {
			label = converterDefaultSourceLabel(index + 1)
		}
		label = converterUniqueLocal(label, usedLabels)
		names := map[string]string{}
		prefix := strings.TrimSpace(src.Prefix)
		ps := o.c("proxies-items", p.Proxies).Tokens
		ns := o.c("nodes-items", p.Nodes).Tokens
		for i, t := range ps {
			f := o.c("proxy-facts", t)
			old := f.Text
			name := old
			if name == "" {
				name = f.Names[0]
			}
			if prefix != "" && !strings.HasPrefix(name, prefix) {
				name = prefix + " " + name
			}
			name = converterUniqueLocal(name, usedNames)
			names[old] = name
			o.fx.Call(converterServiceEffect{Kind: "map-assign", Token: t, Fields: []converterAssignment{cs("name", name)}})
			if i < len(ns) {
				o.fx.Call(converterServiceEffect{Kind: "nodes-rename", Token: p.Nodes, Index: i, Text: name, Key: label})
			}
			o.fx.Call(converterServiceEffect{Kind: "merge-proxy", Token: m, Ref: t, Text: name, Key: label})
			proxyCount++
		}
		pol := o.c("policy-decode", src.Body)
		if pol.Err == nil {
			o.fx.Call(converterServiceEffect{Kind: "section-append", Token: m, Ref: pol.Token, Text: label})
			gs := o.c("policy-clone-groups", pol.Token).Tokens
			if len(sources) > 1 {
				for _, g := range gs {
					f := o.c("group-facts", g)
					alias := label + " / " + f.Text
					names[f.Text] = alias
					o.fx.Call(converterServiceEffect{Kind: "group-name", Token: g, Text: alias})
				}
			}
			for _, g := range gs {
				f := o.c("group-facts", g)
				for j, name := range f.Names {
					if alias, ok := names[name]; ok && name != "DIRECT" && name != "REJECT" {
						o.fx.Call(converterServiceEffect{Kind: "group-member", Token: g, Index: j, Text: alias})
					}
				}
				o.fx.Call(converterServiceEffect{Kind: "merge-group", Token: m, Ref: g})
				groupCount++
			}
			for _, rule := range o.c("policy-rules", pol.Token).Names {
				if start, end, ok := mergeRuleTarget(rule); ok {
					if alias, exists := names[strings.TrimSpace(rule[start:end])]; exists {
						rule = rule[:start] + alias + rule[end:]
					}
				}
				o.fx.Call(converterServiceEffect{Kind: "merge-rule", Token: m, Text: rule})
				ruleCount++
			}
		}
		o.fx.Call(converterServiceEffect{Kind: "merge-nodes", Token: m, Ref: p.Nodes})
	}
	if proxyCount == 0 {
		return converterServiceResult{Err: errNoUsableNodes}
	}
	b := o.fx.Call(converterServiceEffect{Kind: "merge-marshal", Token: m, Flag: len(dnsKeys) > 0, Index: groupCount, Value: int64(ruleCount)})
	if b.Err != nil {
		return converterServiceResult{Err: errors.New("could not marshal merged proxies")}
	}
	ns := o.c("merge-node-list", m).Token
	ns = o.c("nodes-unique-child", ns).Token
	return converterServiceResult{Body: b.Token, Nodes: ns}
}
func runGoConverterService(q converterServiceRequest, fx converterServiceEffects) converterServiceResult {
	defer fx.Release()
	o := converterOracle{fx}
	switch q.Op {
	case "singbox":
		return o.singbox(q.Body)
	case "parse-body":
		return o.parse(q.Body)
	case "ensure-yaml":
		return o.ensure(q.Body)
	case "merge-sources":
		return o.merge(q.Sources)
	case "unique-name":
		return converterServiceResult{Text: o.unique(q.Name)}
	default:
		return converterServiceResult{Err: errors.New("invalid converter operation")}
	}
}
