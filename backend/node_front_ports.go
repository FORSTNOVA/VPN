package main

import (
	"encoding/json"
	"fmt"
	"gopkg.in/yaml.v3"
	"net/url"
	"strconv"
	"strings"
)

type nodeFrontProfile struct {
	Proxies []subscriptionProxy `yaml:"proxies"`
	Labels  map[string]string   `yaml:"smartvpn-source-labels"`
}
type appNodeFrontEffects struct {
	q                                                           nodeFrontRequest
	a                                                           *app
	text, uri, name, kind, server, network, path, host, service string
	port                                                        int
	proxy                                                       map[string]any
	node                                                        manualNode
	parsed                                                      *url.URL
	decoded                                                     []byte
	link                                                        vmessLink
	useRust                                                     bool
	nodes                                                       []proxyNode
	profile                                                     nodeFrontProfile
	remote                                                      []string
	match                                                       *appPolicyDnsEffects
}

func (f *appNodeFrontEffects) Release() {}
func nodeFrontBool(v bool) string {
	if v {
		return "1"
	}
	return "0"
}
func (f *appNodeFrontEffects) Call(e nodeFrontEffect) nodeFrontReply {
	switch e.Kind {
	case "dispatch-facts":
		f.text = strings.TrimSpace(f.q.Path)
		scheme, _, found := strings.Cut(f.text, "://")
		return nodeFrontReply{Text: strings.ToLower(scheme), Flag: f.text != "", Number: int64(nodeFrontInt(found))}
	case "url-parse":
		u, err := url.Parse(f.text)
		f.parsed = u
		return nodeFrontReply{Err: err}
	case "protocol":
		var n manualNode
		var err error
		switch e.Text {
		case "vmess":
			n, err = parseVmessLink(f.text)
		case "vless":
			n, err = parseVlessLink(f.parsed)
		case "trojan":
			n, err = parseTrojanLink(f.parsed)
		case "hysteria2", "hy2":
			n, err = parseHysteria2Link(f.parsed)
		case "hysteria":
			n, err = parseHysteriaLink(f.parsed)
		case "tuic":
			n, err = parseTuicLink(f.parsed)
		case "ss":
			n, err = parseShadowsocksLink(f.text, f.parsed)
		case "ssr":
			n, err = parseSSRLink(f.text)
		default:
			panic("unsupported protocol effect")
		}
		f.node = n
		return nodeFrontReply{Err: err}
	case "finish-facts":
		return nodeFrontReply{Flag: strings.TrimSpace(f.name) == ""}
	case "name-fallback":
		f.name = fmt.Sprintf("%s:%d", f.server, f.port)
		return nodeFrontReply{}
	case "finish":
		f.proxy["name"] = f.name
		f.node = manualNode{Name: f.name, URI: f.uri, Type: f.kind, Server: f.server, Port: f.port, Proxy: f.proxy}
		return nodeFrontReply{}
	case "transport-facts":
		mask := int64(0)
		if f.path != "" {
			mask |= 1
		}
		if f.host != "" {
			mask |= 2
		}
		if f.service != "" {
			mask |= 4
		}
		return nodeFrontReply{Text: strings.ToLower(strings.TrimSpace(f.network)), Number: mask, Names: []string{fmt.Sprintf(nodeFrontTransportError, f.network)}}
	case "transport-apply":
		f.proxy["network"] = e.Text
		options := map[string]any{}
		switch e.Text {
		case "ws":
			if e.Number&1 != 0 {
				options["path"] = f.path
			}
			if e.Number&2 != 0 {
				options["headers"] = map[string]any{"Host": f.host}
			}
			if len(options) > 0 {
				f.proxy["ws-opts"] = options
			}
		case "h2":
			if e.Number&1 != 0 {
				options["path"] = f.path
			}
			if e.Number&2 != 0 {
				options["host"] = []string{f.host}
			}
			if len(options) > 0 {
				f.proxy["h2-opts"] = options
			}
		case "grpc":
			if e.Number&4 != 0 {
				f.proxy["grpc-opts"] = map[string]any{"grpc-service-name": f.service}
			}
		default:
			panic("invalid transport")
		}
		return nodeFrontReply{}
	case "vmess-input":
		f.text = f.q.Path
		return nodeFrontReply{Flag: strings.HasPrefix(f.text, "vmess://")}
	case "vmess-strip":
		f.text = f.text[len("vmess://"):]
		return nodeFrontReply{}
	case "vmess-cut-facts":
		return nodeFrontReply{Number: int64(strings.IndexAny(f.text, "#?"))}
	case "vmess-cut":
		f.text = f.text[:int(e.Number)]
		return nodeFrontReply{}
	case "vmess-base64":
		b, err := decodeSubscriptionBase64(f.text)
		f.decoded = b
		return nodeFrontReply{Err: err}
	case "vmess-json":
		return nodeFrontReply{Err: json.Unmarshal(f.decoded, &f.link)}
	case "vmess-facts":
		l := f.link
		f.server = strings.TrimSpace(l.Server)
		f.port, _ = portValue(l.Port)
		f.name = strings.TrimSpace(l.Name)
		return nodeFrontReply{Flag: f.useRust, Number: int64(f.port), Names: []string{f.server, f.name, nodeFrontBool(strings.TrimSpace(l.ID) != ""), strings.ToLower(strings.TrimSpace(l.Cipher)), strings.TrimSpace(l.Cipher), strings.ToLower(strings.TrimSpace(l.TLS)), strings.TrimSpace(l.SNI), strings.TrimSpace(l.Host), strings.TrimSpace(l.FP), strconv.Itoa(len(stringList(l.Alpn)))}}
	case "vmess-legacy":
		l := f.link
		fields := map[string]string{"__smartvpn_alter_id": strconv.Itoa(intValue(l.AlterID)), "__smartvpn_cipher": l.Cipher, "__smartvpn_tls": l.TLS, "__smartvpn_sni": l.SNI, "__smartvpn_host": l.Host, "__smartvpn_fp": l.FP, "__smartvpn_network": l.Network, "__smartvpn_path": l.Path}
		alpn := stringList(l.Alpn)
		fields["__smartvpn_alpn_count"] = strconv.Itoa(len(alpn))
		for i, v := range alpn {
			fields[fmt.Sprintf("__smartvpn_alpn_%d", i)] = v
		}
		n, err, ok := rustLegacyNodeFields(f.q.Path, "vmess", f.server, f.port, f.name, l.ID, fields)
		f.node = n
		return nodeFrontReply{Flag: ok, Err: err}
	case "vmess-base":
		f.proxy = map[string]any{"name": f.name, "type": "vmess", "server": f.server, "port": f.port, "uuid": strings.TrimSpace(f.link.ID), "alterId": intValue(f.link.AlterID), "cipher": e.Text, "udp": true}
		return nodeFrontReply{}
	case "vmess-patch":
		switch e.Text {
		case "tls":
			f.proxy["tls"] = true
		case "servername":
			if e.Other == "sni" {
				f.proxy["servername"] = strings.TrimSpace(f.link.SNI)
			} else {
				f.proxy["servername"] = strings.TrimSpace(f.link.Host)
			}
		case "fingerprint":
			f.proxy["client-fingerprint"] = strings.TrimSpace(f.link.FP)
		case "alpn":
			f.proxy["alpn"] = stringList(f.link.Alpn)
		default:
			panic("invalid vmess patch")
		}
		return nodeFrontReply{}
	case "vmess-transport":
		return nodeFrontReply{Err: transportOptions(f.proxy, f.link.Network, f.link.Path, f.link.Host, f.link.Path)}
	case "vmess-finish":
		n, err := finishManualNode(f.q.Path, f.name, "vmess", f.server, f.port, f.proxy)
		f.node = n
		return nodeFrontReply{Err: err}
	case "unique-facts":
		names := make([]string, len(f.nodes))
		for i, n := range f.nodes {
			names[i] = n.Name
		}
		return nodeFrontReply{Names: names}
	case "yaml-facts":
		if err := yaml.Unmarshal(f.q.Body, &f.profile); err != nil {
			return nodeFrontReply{Err: err}
		}
		facts := &policyDnsFacts{}
		for _, p := range f.profile.Proxies {
			kind, value := "", ""
			switch v := p.TLS.(type) {
			case bool:
				kind = "bool"
				value = nodeFrontBool(v)
			case string:
				kind = "string"
				value = v
			}
			facts.put(p.Name, p.Type, nodeFrontBool(isInformationalNode(p.Name)), kind, value, p.WsOpts.Path)
			facts.count(len(p.WsOpts.Headers))
			for k, v := range p.WsOpts.Headers {
				facts.put(nodeFrontBool(strings.EqualFold(k, "host")), nodeFrontBool(strings.TrimSpace(stringValue(v)) != ""))
			}
		}
		return nodeFrontReply{Number: int64(len(f.profile.Proxies)), Names: facts.names}
	case "convert-profile":
		_, nodes, err := parseSubscriptionBodyToProxies(f.q.Body)
		f.nodes = nodes
		return nodeFrontReply{Err: err}
	case "match-read", "match-nodes", "match-source", "subscription", "hash":
		if f.match == nil {
			f.match = &appPolicyDnsEffects{a: f.a}
		}
		r := f.match.Call(policyDnsEffect{Kind: e.Kind})
		return nodeFrontReply{Text: r.Text, Flag: r.Flag, Number: r.Number, Err: r.Err}
	case "validate-facts":
		f.text = strings.TrimSpace(f.q.Path)
		return nodeFrontReply{Flag: f.text != "", Number: int64(nodeFrontInt(f.text == mergedSubscriptionURL))}
	case "inline":
		return nodeFrontReply{Flag: isInlineSubscription(f.text)}
	case "categorize":
		remote, inline := categorizeSubscriptionInput(f.text)
		f.remote = remote
		return nodeFrontReply{Number: int64(len(remote)), Flag: len(inline) > 0}
	case "validate-url":
		u, err := neturlParse(f.remote[int(e.Number)])
		if err != nil {
			return nodeFrontReply{Err: err}
		}
		return nodeFrontReply{Text: u.Scheme, Flag: u.Hostname() != ""}
	}
	panic("unexpected node frontend effect")
}

func nodeFrontInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
