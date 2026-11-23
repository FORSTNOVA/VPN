package main

import (
	"errors"
	"fmt"
	"slices"
	"unicode/utf8"
)

func runGoChainService(q chainServiceRequest, fx chainServiceEffects) chainServiceResult {
	defer fx.Release()
	if q.Op == "build" && !q.Next.Enabled {
		return chainServiceResult{}
	}
	call := fx.Call
	fail := func(status int, err error) chainServiceResult {
		return chainServiceResult{Status: status, Message: err.Error(), Err: err}
	}
	// Match the HTTP early checks without reading app state.
	if q.Op == "update" && len(q.Next.Hops) > 8 {
		return fail(400, errors.New(chainHTTPMessage0()))
	}
	if q.Op == "save-profile" {
		q.Name = chainTrimName(q.Name)
		if q.Name == "" || utf8.RuneCountInString(q.Name) > 80 {
			return fail(400, errors.New(chainHTTPMessage5()))
		}
	}
	kind := "snapshot"
	if q.Op == "update" || q.Op == "save-profile" {
		kind = "lock"
	}
	s := call(chainEffect{Kind: kind}).State
	compile := func(hops []string, entries []chainProxyEntry) ([]map[string]any, error) {
		if len(hops) < 1 || len(hops) > 8 {
			return nil, errors.New(chainCompileMessage2())
		}
		byName := make(map[string][]int)
		for i, e := range entries {
			byName[e.Key] = append(byName[e.Key], i)
		}
		seen := make(map[string]bool)
		var out []map[string]any
		previous := ""
		for i, name := range hops {
			if seen[name] {
				return nil, errors.New(chainCompileMessage3(i + 1))
			}
			seen[name] = true
			indices := byName[name]
			if name == "" || len(indices) != 1 {
				return nil, errors.New(chainCompileMessage4(i + 1))
			}
			index := indices[0]
			p := entries[index].Proxy
			if stringValue(p["dialer-proxy"]) != "" {
				return nil, errors.New(chainCompileMessage5(i + 1))
			}
			if stringValue(p["server"]) == "" || stringValue(p["type"]) == "" {
				return nil, errors.New(chainCompileMessage6(i + 1))
			}
			alias := fmt.Sprintf("SmartVPNChainHop%d", i+1)
			if i == len(hops)-1 {
				alias = chainExit
			}
			if len(byName[alias]) != 0 {
				return nil, errors.New(chainCompileMessage7(alias))
			}
			r := call(chainEffect{Kind: "clone", Index: index, Name: alias, Bool: i > 0, Previous: previous})
			out = append(out, r.Proxy)
			previous = alias
		}
		return out, nil
	}
	fromProfile := func(hops []string, profile []byte, full bool) ([]map[string]any, error) {
		if full && (len(hops) < 2 || len(hops) > 8) {
			return nil, errors.New(chainCompileMessage0())
		}
		r := call(chainEffect{Kind: "decode", Body: profile})
		if r.Err != nil {
			return nil, errors.New(chainCompileMessage1())
		}
		return compile(hops, r.Entries)
	}
	build := func(next proxyChain, profile []byte) ([]byte, error) {
		if !next.Enabled {
			return nil, nil
		}
		if !s.Subscription {
			profile = nil
		}
		ps, err := fromProfile(next.Hops, profile, true)
		if err != nil {
			return nil, err
		}
		if s.LockedRegion != "" {
			name := next.Hops[len(next.Hops)-1]
			r := call(chainEffect{Kind: "region", Name: name})
			if r.Record.Status != regionVerified || r.Record.Country != s.LockedRegion {
				return nil, errors.New(chainCompileMessage8())
			}
			r = call(chainEffect{Kind: "age", Name: name})
			if r.Age > r.TTL {
				return nil, errors.New(chainCompileMessage8())
			}
		}
		r := call(chainEffect{Kind: "yaml", Proxies: ps})
		return r.Body, r.Err
	}
	switch q.Op {
	case "compile":
		if len(q.Hops) < 1 || len(q.Hops) > 8 {
			return fail(0, errors.New(chainCompileMessage2()))
		}
		r := call(chainEffect{Kind: "index"})
		ps, err := compile(q.Hops, r.Entries)
		if err != nil {
			return fail(0, err)
		}
		return chainServiceResult{Proxies: ps}
	case "prefix", "chain":
		ps, err := fromProfile(q.Hops, q.Profile, q.Op == "chain")
		if err != nil {
			return fail(0, err)
		}
		return chainServiceResult{Proxies: ps}
	case "build":
		b, err := build(q.Next, q.Profile)
		if err != nil {
			return fail(0, err)
		}
		return chainServiceResult{Body: b}
	case "update":
		if call(chainEffect{Kind: "running"}).Bool || call(chainEffect{Kind: "job"}).Bool {
			return fail(409, errors.New(chainHTTPMessage1()))
		}
		if q.Next.Enabled {
			if q.Next.Name != "" && !slices.Equal(q.Next.Hops, s.Chains[q.Next.Name]) {
				return fail(409, errors.New(chainHTTPMessage2()))
			}
			profile := call(chainEffect{Kind: "read"}).Body
			if _, err := build(q.Next, profile); err != nil {
				return fail(400, err)
			}
		}
		call(chainEffect{Kind: "commit-chain", Next: q.Next})
		if call(chainEffect{Kind: "save"}).Err != nil {
			call(chainEffect{Kind: "restore-chain"})
			return fail(500, errors.New(chainHTTPMessage3()))
		}
		return chainServiceResult{Status: 200, Next: q.Next}
	case "save-profile":
		if q.Original != "" && s.Chains[q.Original] == nil {
			return fail(409, errors.New(chainHTTPMessage6()))
		}
		if q.Name != q.Original && s.Chains[q.Name] != nil {
			return fail(409, errors.New(chainHTTPMessage7()))
		}
		profile := call(chainEffect{Kind: "read"}).Body
		if !s.Subscription {
			profile = nil
		}
		if _, err := fromProfile(q.Hops, profile, true); err != nil {
			return fail(400, err)
		}
		if s.Chain.Enabled && q.Original != "" && s.Chain.Name == q.Original {
			if call(chainEffect{Kind: "running"}).Bool || call(chainEffect{Kind: "job"}).Bool {
				return fail(409, errors.New(chainHTTPMessage8()))
			}
			next := proxyChain{Name: q.Name, Enabled: true, Hops: q.Hops}
			if _, err := build(next, profile); err != nil {
				return fail(400, err)
			}
			call(chainEffect{Kind: "commit-chain", Next: next})
		}
		call(chainEffect{Kind: "commit-profiles", Name: q.Name, Previous: q.Original})
		if call(chainEffect{Kind: "save"}).Err != nil {
			call(chainEffect{Kind: "restore-profiles"})
			call(chainEffect{Kind: "restore-chain"})
			return fail(500, errors.New(chainHTTPMessage9()))
		}
		return chainServiceResult{Status: 200}
	default:
		return fail(500, errors.New("unknown chain operation"))
	}
}
