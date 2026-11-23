package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

type converterSafeEffects struct {
	inner    *appConverterEffects
	before   func(converterServiceEffect) (converterServiceReply, bool)
	after    func(converterServiceEffect, converterServiceReply)
	calls    map[string]int
	releases int
}

func (e *converterSafeEffects) Call(q converterServiceEffect) converterServiceReply {
	e.calls[q.Kind]++
	if e.before != nil {
		if r, ok := e.before(q); ok {
			if e.after != nil {
				e.after(q, r)
			}
			return r
		}
	}
	r := e.inner.Call(q)
	if e.after != nil {
		e.after(q, r)
	}
	return r
}
func (e *converterSafeEffects) Release() { e.releases++; e.inner.Release() }
func newConverterSafeEffects(e *appConverterEffects) *converterSafeEffects {
	return &converterSafeEffects{inner: e, calls: map[string]int{}}
}

func TestConverterServiceSafeOriginalBytes(t *testing.T) {
	body := []byte("# preserved\nproxies:\n  - {name: N, type: ss, server: example.invalid, port: 1}\n")
	q, e := converterBodyRequest("ensure-yaml", body)
	r := runGoConverterService(q, e)
	b, _, err := converterByteResult(e, r)
	if err != nil || len(b) != len(body) || &b[0] != &body[0] {
		t.Fatalf("original body resource lost: %v", err)
	}
	b[0] = '!'
	if body[0] != '!' {
		t.Fatal("byte resource copied")
	}
}

func TestConverterServiceSafeOriginalGraph(t *testing.T) {
	converterSafeOriginalGraph(t, runGoConverterService)
}
func converterSafeOriginalGraph(t *testing.T, run func(converterServiceRequest, converterServiceEffects) converterServiceResult) {
	nested := map[string]any{"x": "original"}
	proxy := map[string]any{"name": "Node", "type": "ss", "server": "example.invalid", "port": 1, "extension": nested}
	originalGroups := []sourceGroup{{Name: "Group", Members: []string{"Node", "DIRECT"}}}
	policy := &sourcePolicy{Groups: originalGroups, Rules: []string{"DOMAIN,example.invalid,Group"}}
	e := newConverterEffects()
	body := e.put([]byte("proxies:\n - {name: Node, type: ss}\n"))
	q := converterServiceRequest{Op: "merge-sources", Sources: []converterSource{{Body: body, Label: "L", Prefix: "P"}, {Body: e.put([]byte("invalid"))}}}
	fx := newConverterSafeEffects(e)
	var merge *converterMergeResources
	fx.before = func(x converterServiceEffect) (converterServiceReply, bool) {
		if x.Kind == "decode-clash" && x.Token == body {
			return converterServiceReply{Count: 1, Tokens: []int{e.put(proxy)}}, true
		}
		if x.Kind == "policy-decode" && x.Token == body {
			return converterServiceReply{Token: e.put(policy)}, true
		}
		return converterServiceReply{}, false
	}
	fx.after = func(x converterServiceEffect, r converterServiceReply) {
		if x.Kind == "merge-new" {
			merge = e.get(r.Token).(*converterMergeResources)
		}
	}
	r := run(q, fx)
	if r.Err != nil {
		t.Fatal(r.Err)
	}
	if proxy["name"] != "P Node" || len(merge.Proxies) != 1 || reflect.ValueOf(merge.Proxies[0]).Pointer() != reflect.ValueOf(proxy).Pointer() {
		t.Fatal("proxy identity or name mutation changed")
	}
	nested["x"] = "visible"
	if merge.Proxies[0]["extension"].(map[string]any)["x"] != "visible" {
		t.Fatal("nested alias cloned")
	}
	if originalGroups[0].Name != "Group" || originalGroups[0].Members[0] != "Node" || merge.Sections[0].Groups[0].Name != "Group" {
		t.Fatal("section originals overwritten")
	}
	if merge.Groups[0].Name != "L / Group" || merge.Groups[0].Members[0] != "P Node" {
		t.Fatal("working group rewrite missing")
	}
	originalGroups[0].Members[0] = "external"
	if merge.Sections[0].Groups[0].Members[0] != "external" || merge.Groups[0].Members[0] != "P Node" {
		t.Fatal("wrong group/member copy boundary")
	}
	if fx.releases != 1 {
		t.Fatalf("release %d", fx.releases)
	}
}

func TestConverterServiceSafeMutationFailure(t *testing.T) {
	e := newConverterEffects()
	body := e.put([]byte("proxies:\n - {name: N, type: ss}\n"))
	q := converterServiceRequest{Op: "merge-sources", Sources: []converterSource{{Body: body, Prefix: "P"}}}
	fx := newConverterSafeEffects(e)
	var original map[string]any
	fx.after = func(x converterServiceEffect, r converterServiceReply) {
		if x.Kind == "decode-clash" && len(r.Tokens) > 0 {
			original = e.get(r.Tokens[0]).(map[string]any)
		}
	}
	fx.before = func(x converterServiceEffect) (converterServiceReply, bool) {
		if x.Kind == "merge-marshal" {
			return converterServiceReply{Err: errors.New("fake codec failure")}, true
		}
		return converterServiceReply{}, false
	}
	r := runGoConverterService(q, fx)
	if r.Err == nil || r.Err.Error() != "could not marshal merged proxies" {
		t.Fatalf("error %v", r.Err)
	}
	if original["name"] != "P N" || fx.calls["map-assign"] != 1 || fx.calls["merge-marshal"] != 1 || fx.releases != 1 {
		t.Fatal("mutation rollback/replay or release mismatch")
	}
}

func TestConverterServiceSafeSecretsRemainResources(t *testing.T) {
	q, e := converterBodyRequest("singbox", []byte(`{"outbounds":[{"type":"vless","tag":"Node","server":"example.invalid","server_port":443,"uuid":"synthetic-secret","tls":{"enabled":true,"reality":{"enabled":true,"public_key":"synthetic-public","short_id":"synthetic-id"}},"transport":{"type":"ws","path":"/synthetic-path","headers":{"Host":"synthetic-host"}}}]}`))
	fx := newConverterSafeEffects(e)
	fx.after = func(x converterServiceEffect, r converterServiceReply) {
		for _, f := range r.Fields {
			for _, s := range []string{f.Text, f.Trim} {
				if strings.Contains(s, "synthetic-") {
					t.Fatalf("resource exposed in metadata key %s", f.Key)
				}
			}
		}
	}
	r := runGoConverterService(q, fx)
	ps, _, err := converterProxyResult(e, r)
	if err != nil || len(ps) != 1 {
		t.Fatalf("result %v", err)
	}
	if ps[0]["uuid"] != "synthetic-secret" || ps[0]["reality-opts"].(map[string]any)["public-key"] != "synthetic-public" {
		t.Fatal("opaque field assignment lost")
	}
}

func TestConverterServiceSafeUniqueMap(t *testing.T) {
	used := map[string]struct{}{"N": {}, "N (2)": {}}
	e := newConverterEffects()
	e.used = used
	r := runGoConverterService(converterServiceRequest{Op: "unique-name", Name: "N"}, e)
	if r.Text != "N (3)" {
		t.Fatal(r)
	}
	if _, ok := used[r.Text]; !ok {
		t.Fatal("original used-name map not updated")
	}
	e = newConverterEffects()
	r = runConverterService(converterServiceRequest{Op: "unique-name", Name: "N"}, e)
	if r.Err == nil {
		t.Fatal("nil original map must not silently become a new map")
	}
}

func TestConverterServiceSafeUnicodeLower(t *testing.T) {
	converterSafeUnicodeLower(t, runGoConverterService)
}
func converterSafeUnicodeLower(t *testing.T, run func(converterServiceRequest, converterServiceEffects) converterServiceResult) {
	for _, tc := range []struct{ kind, want string }{{"hysterİa2", "hysteria2"}, {"tuİc", "tuic"}, {"\u0085 TUİC \u2003", "tuic"}} {
		body := []byte(fmt.Sprintf(`{"outbounds":[{"type":%q,"tag":"Node","server":"example.invalid","server_port":443,"uuid":"synthetic","password":"synthetic"}]}`, tc.kind))
		q, e := converterBodyRequest("singbox", body)
		r := run(q, e)
		ps, ns, err := converterProxyResult(e, r)
		if err != nil || len(ps) != 1 || len(ns) != 1 || ps[0]["type"] != tc.want || ns[0].Type != tc.want {
			t.Fatalf("Go simple lowercase contract %q: proxies=%v nodes=%v err=%v", tc.kind, ps, ns, err)
		}
	}
	for _, text := range []string{"İ", " I\u0307 ", " AUTO ", "FaLsE", "\u0085HYSTERİA2\u2003"} {
		e := newConverterEffects()
		fs := e.fields(struct {
			Type    string `json:"type"`
			TLS     string `json:"tls"`
			Cipher  string `json:"scy"`
			Network string `json:"net"`
		}{text, text, text, text})
		for _, key := range []string{"type", "tls", "scy", "net"} {
			if got := converterFind(fs, key).Lower; got != strings.ToLower(strings.TrimSpace(text)) {
				t.Fatalf("lower fact %s %q: %q", key, text, got)
			}
		}
	}
	body := []byte("vmess://" + base64.StdEncoding.EncodeToString([]byte(`{"ps":"N","add":"example.invalid","port":443,"id":"synthetic","tls":" FaLsE ","scy":" AuTo ","net":" GRPC ","path":"svc"}`)))
	q, e := converterBodyRequest("parse-body", body)
	ps, ns, err := converterProxyResult(e, run(q, e))
	if err != nil || len(ps) != 1 || len(ns) != 1 || ns[0].TLS || ps[0]["cipher"] != "auto" || ps[0]["network"] != "grpc" {
		t.Fatalf("nested normalization ps=%v ns=%v err=%v", ps, ns, err)
	}
}
