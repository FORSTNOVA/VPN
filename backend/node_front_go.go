package main

import "errors"

func nodeFrontFailure(n int) nodeFrontResult {
	if n == 4 {
		return nodeFrontResult{Err: errLinkUnsupported}
	}
	if n == 5 {
		return nodeFrontResult{Err: errLinkFields}
	}
	return nodeFrontResult{Err: errors.New(nodeFrontTexts[n])}
}
func runGoNodeFront(q nodeFrontRequest, fx nodeFrontEffects) nodeFrontResult {
	a := nodeFrontOracle{fx}
	call := func(k string) nodeFrontReply { return a.call(k, "", "", 0) }
	done := func(r nodeFrontReply) nodeFrontResult { return nodeFrontResult{Err: r.Err} }
	switch q.Op {
	case "dispatch":
		r := call("dispatch-facts")
		if !r.Flag {
			return nodeFrontFailure(0)
		}
		if r.Number == 0 {
			return nodeFrontFailure(4)
		}
		if r.Text == "vmess" {
			return done(a.call("protocol", "vmess", "", 0))
		}
		scheme := r.Text
		if call("url-parse").Err != nil {
			return nodeFrontFailure(1)
		}
		switch scheme {
		case "vless", "trojan", "hysteria2", "hy2", "hysteria", "tuic", "ss", "ssr":
			return done(a.call("protocol", scheme, "", 0))
		default:
			return nodeFrontFailure(4)
		}
	case "finish":
		if call("finish-facts").Flag {
			call("name-fallback")
		}
		return done(call("finish"))
	case "transport":
		r := call("transport-facts")
		kind := r.Text
		switch kind {
		case "", "tcp":
			return nodeFrontResult{}
		case "ws", "websocket":
			kind = "ws"
		case "grpc":
		case "h2", "http":
			kind = "h2"
		default:
			return nodeFrontResult{Err: errors.New(r.Names[0])}
		}
		return done(a.call("transport-apply", kind, "", r.Number))
	case "vmess":
		if call("vmess-input").Flag {
			call("vmess-strip")
		}
		if cut := call("vmess-cut-facts").Number; cut >= 0 {
			a.call("vmess-cut", "", "", cut)
		}
		if call("vmess-base64").Err != nil {
			return nodeFrontFailure(2)
		}
		if call("vmess-json").Err != nil {
			return nodeFrontFailure(3)
		}
		r := call("vmess-facts")
		v := r.Names
		if v[0] == "" || r.Number == 0 || v[2] == "0" {
			return nodeFrontFailure(5)
		}
		if v[1] == "" {
			call("name-fallback")
		}
		if r.Flag {
			legacy := call("vmess-legacy")
			if legacy.Flag {
				return done(legacy)
			}
		}
		cipher := v[4]
		if v[3] == "" || v[3] == "auto" {
			cipher = "auto"
		}
		a.call("vmess-base", cipher, "", 0)
		if v[5] != "" && v[5] != "none" && v[5] != "false" {
			a.call("vmess-patch", "tls", "", 0)
			if v[6] != "" {
				a.call("vmess-patch", "servername", "sni", 0)
			} else if v[7] != "" {
				a.call("vmess-patch", "servername", "host", 0)
			}
		}
		if v[8] != "" {
			a.call("vmess-patch", "fingerprint", "", 0)
		}
		if v[9] != "0" {
			a.call("vmess-patch", "alpn", "", 0)
		}
		if r := call("vmess-transport"); r.Err != nil {
			return done(r)
		}
		return done(call("vmess-finish"))
	case "unique":
		r := call("unique-facts")
		indices := make([]int, 0, len(r.Names))
		seen := map[string]bool{}
		for i, name := range r.Names {
			if !seen[name] {
				seen[name] = true
				indices = append(indices, i)
			}
		}
		return nodeFrontResult{Data: map[string]any{"indices": indices}}
	case "yaml":
		r := call("yaml-facts")
		var indices, bits []int
		if r.Err == nil && r.Number > 0 {
			indices = make([]int, 0, int(r.Number))
			bits = make([]int, 0, int(r.Number))
			s := &policyDnsFacts{names: r.Names}
			seen := map[string]bool{}
			for i := 0; i < int(r.Number); i++ {
				name, kind, info, tlsKind, tlsValue, path := s.str(), s.str(), s.str(), s.str(), s.str(), s.str()
				host := false
				for n := s.num(); n > 0; n-- {
					fold, nonempty := s.str(), s.str()
					if fold == "1" && nonempty == "1" {
						host = true
					}
				}
				if name == "" || kind == "" || info == "1" || seen[name] {
					continue
				}
				seen[name] = true
				bit := 0
				if tlsKind == "bool" && tlsValue == "1" || tlsKind == "string" && tlsValue != "" && tlsValue != "none" && tlsValue != "false" && tlsValue != "0" {
					bit |= 1
				}
				if host {
					bit |= 2
				}
				if path != "" {
					bit |= 4
				}
				indices = append(indices, i)
				bits = append(bits, bit)
			}
		}
		return nodeFrontResult{Data: map[string]any{"indices": indices, "bits": bits}}
	case "parse-profile":
		if call("convert-profile").Err != nil {
			return nodeFrontResult{}
		}
		return nodeFrontResult{Status: 1}
	case "matches":
		if call("match-read").Err != nil {
			return nodeFrontResult{}
		}
		if call("match-nodes").Number == 0 {
			return nodeFrontResult{}
		}
		r := call("match-source")
		if r.Err != nil {
			return nodeFrontResult{}
		}
		actual := r.Text
		ok := false
		if call("subscription").Number == 1 {
			ok = actual == mergedSubscriptionURL
		} else {
			call("subscription")
			ok = actual == call("hash").Text
		}
		if ok {
			return nodeFrontResult{Status: 1}
		}
		return nodeFrontResult{}
	case "validate":
		r := call("validate-facts")
		if !r.Flag {
			return nodeFrontFailure(6)
		}
		if r.Number == 1 {
			return nodeFrontResult{}
		}
		if call("inline").Flag {
			return nodeFrontResult{}
		}
		r = call("categorize")
		if r.Number == 0 && !r.Flag {
			return nodeFrontFailure(6)
		}
		for i := int64(0); i < r.Number; i++ {
			v := a.call("validate-url", "", "", i)
			if v.Err != nil || (v.Text != "https" && v.Text != "http") || !v.Flag {
				return nodeFrontFailure(6)
			}
		}
		return nodeFrontResult{}
	}
	panic("unknown node frontend operation")
}
