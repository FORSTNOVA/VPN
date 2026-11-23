//go:build windows && amd64 && rustcore

package main

import (
	"encoding/base64"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestNodeFrontFlowOriginalClosureNoRecovery(t *testing.T) {
	if !rustNodeFrontAvailable() {
		t.Fatal("native node frontend unavailable")
	}
	t.Setenv("SMARTVPN_RUST_NODE_FRONT", "1")
	n := nodeFrontRecoveries.Load()
	TestNodeFrontServiceTransportPreservesOriginalGraph(t)
	TestNodeFrontServiceFinishKeepsProxyAlias(t)
	TestNodeFrontServiceManualVmessDialectAndPrivateWire(t)
	TestNodeFrontServiceYamlProjectionNilAndFirstWins(t)
	TestProfileConversionIndependentLegacyUnique(t)
	TestProfileConversionIndependentLegacyURI(t)
	TestProfileConversionIndependentLegacyURL(t)
	if nodeFrontRecoveries.Load() != n {
		t.Fatalf("normal frontend silently recovered: %v", nodeFrontRecoveryReason.Load())
	}
}

type nodeFrontObservedFX struct {
	base  *appNodeFrontEffects
	trace []nodeFrontEffect
}

func (f *nodeFrontObservedFX) Release() {}
func (f *nodeFrontObservedFX) Call(e nodeFrontEffect) nodeFrontReply {
	f.trace = append(f.trace, e)
	return f.base.Call(e)
}

type nodeFrontInjectedSession struct {
	nodeFrontSession
	nextAt, advanceAt, nextCalls, advanceCalls, closed int
}

func (s *nodeFrontInjectedSession) next() ([]byte, uint64, error) {
	s.nextCalls++
	if s.nextCalls == s.nextAt {
		return nil, 0, errors.New("injected next")
	}
	return s.nodeFrontSession.next()
}
func (s *nodeFrontInjectedSession) advance(n uint64, b []byte) error {
	s.advanceCalls++
	if s.advanceCalls == s.advanceAt {
		_ = s.nodeFrontSession.advance(n, b)
		return errors.New("uncertain acknowledgement")
	}
	return s.nodeFrontSession.advance(n, b)
}
func (s *nodeFrontInjectedSession) close() { s.closed++; s.nodeFrontSession.close() }
func TestNodeFrontFlowEveryBoundaryKeepsGraphAndNoReplay(t *testing.T) {
	if !rustNodeFrontAvailable() {
		t.Fatal("native node frontend unavailable")
	}
	for _, op := range []string{"transport", "finish", "dispatch", "vmess", "unique", "yaml", "parse-profile", "matches", "validate"} {
		t.Run(op, func(t *testing.T) {
			a := settingsDocumentTestApp(t, t.TempDir())
			testEditSettings(a, func(s *settings) { s.SubscriptionURL = "https://synthetic.invalid/profile" })
			body := []byte("proxies: [{name: one, type: ss, server: node.example, port: 443, cipher: aes-128-gcm, password: synthetic}]\n")
			uri := "vmess://" + base64.StdEncoding.EncodeToString([]byte(`{"add":"node.example","id":"synthetic","port":443,"net":"ws","path":"/x","host":"host.example","tls":"tls","fp":"chrome","alpn":"h2"}`)) + "#cut"
			factory := func() *nodeFrontObservedFX {
				q := nodeFrontRequest{Op: op, Body: body, Path: uri}
				if op == "validate" {
					q.Path = "https://synthetic.invalid/x"
				}
				fx := &appNodeFrontEffects{q: q, a: a, proxy: map[string]any{"old": "keep"}, network: "ws", path: "/raw", host: "host", service: "grpc", server: "fallback", port: 443, uri: "synthetic", kind: "ss", nodes: []proxyNode{{Name: "dup", Type: "ss"}, {Name: "dup", Type: "vmess"}, {Name: "tail"}}}
				fx.match = &appPolicyDnsEffects{a: a, readFile: func(path string) ([]byte, error) {
					if strings.HasSuffix(path, ".source") {
						return []byte(subscriptionHash("https://synthetic.invalid/profile")), nil
					}
					return body, nil
				}}
				return &nodeFrontObservedFX{base: fx}
			}
			wantFX := factory()
			want := runGoNodeFront(wantFX.base.q, wantFX)
			for _, phase := range []string{"next", "advance"} {
				count := len(wantFX.trace)
				if phase == "next" {
					count++
				}
				for at := 1; at <= count; at++ {
					fx := factory()
					var s *nodeFrontInjectedSession
					n := nodeFrontRecoveries.Load()
					got, used := runRustNodeFrontWithStart(fx.base.q, fx, func(b []byte) (nodeFrontSession, error) {
						v, e := startRustNodeFront(b)
						if e != nil {
							return nil, e
						}
						s = &nodeFrontInjectedSession{nodeFrontSession: v}
						if phase == "next" {
							s.nextAt = at
						} else {
							s.advanceAt = at
						}
						return s, nil
					})
					if !used || s == nil || s.closed != 1 || !reflect.DeepEqual(fx.trace, wantFX.trace) || !reflect.DeepEqual(nodeFrontResultJSON(got), nodeFrontResultJSON(want)) || !reflect.DeepEqual(fx.base.node, wantFX.base.node) || !reflect.DeepEqual(fx.base.proxy, wantFX.base.proxy) || nodeFrontRecoveries.Load() != n+1 {
						t.Fatalf("%s/%d changed effects/result/graph: %v", phase, at, nodeFrontRecoveryReason.Load())
					}
				}
			}
		})
	}
}
func TestNodeFrontFlowInvalidUTF8AndCapacityContinuation(t *testing.T) {
	if !rustNodeFrontAvailable() {
		t.Fatal("native node frontend unavailable")
	}
	for _, capacity := range []bool{false, true} {
		nodes := []proxyNode{{Name: string([]byte{'x', 255}), Type: "retained"}, {Name: string([]byte{'x', 255}), Type: "discarded"}}
		if capacity {
			nodes = make([]proxyNode, 100001)
			for i := range nodes {
				nodes[i].Name = "same"
			}
		}
		q := nodeFrontRequest{Op: "unique"}
		want := runGoNodeFront(q, &appNodeFrontEffects{q: q, nodes: nodes})
		fx := &nodeFrontObservedFX{base: &appNodeFrontEffects{q: q, nodes: nodes}}
		n := nodeFrontRecoveries.Load()
		got, used := runRustNodeFront(q, fx)
		if !used || len(fx.trace) != 1 || fx.trace[0].Kind != "unique-facts" || !reflect.DeepEqual(nodeFrontResultJSON(got), nodeFrontResultJSON(want)) || nodeFrontRecoveries.Load() != n+1 {
			t.Fatal("source codec failure replayed or changed output")
		}
	}
}
