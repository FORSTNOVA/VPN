//go:build windows && amd64 && rustcore

package main

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

type policyDnsInjectedSession struct {
	policyDnsSession
	nextAt, advanceAt, nextCalls, advanceCalls int
	closed                                     bool
}

func (s *policyDnsInjectedSession) next() ([]byte, uint64, error) {
	s.nextCalls++
	if s.nextCalls == s.nextAt {
		return nil, 0, errors.New("injected next")
	}
	return s.policyDnsSession.next()
}
func (s *policyDnsInjectedSession) advance(n uint64, b []byte) error {
	s.advanceCalls++
	if s.advanceCalls == s.advanceAt {
		_ = s.policyDnsSession.advance(n, b)
		return errors.New("uncertain advance")
	}
	return s.policyDnsSession.advance(n, b)
}
func (s *policyDnsInjectedSession) close() {
	if s.closed {
		panic("doubleclose")
	}
	s.closed = true
	s.policyDnsSession.close()
}
func TestPolicyDnsFlowFaultAndInvalidReplies(t *testing.T) {
	if !rustPolicyDnsAvailable() {
		t.Fatal("native policy unavailable")
	}
	for _, mode := range []string{"next1", "next2", "advance", "utf8", "budget"} {
		q := policyDnsRequest{Op: "rules"}
		p := sourcePolicy{Rules: []string{"DOMAIN,a.example,DIRECT"}}
		if mode == "utf8" {
			p.Rules = []string{"DOMAIN," + string([]byte{255}) + ",DIRECT"}
		}
		if mode == "budget" {
			p.Rules = make([]string, 3000)
			for i := range p.Rules {
				p.Rules[i] = "DOMAIN," + strings.Repeat("a", 1500) + ",DIRECT"
			}
		}
		want := runGoPolicyDns(q, &appPolicyDnsEffects{q: q, policy: p})
		fx := &policyDnsCountingFX{base: &appPolicyDnsEffects{q: q, policy: p}}
		var session *policyDnsInjectedSession
		n := policyDnsRecoveries.Load()
		got, used := runRustPolicyDnsWithStart(q, fx, func(b []byte) (policyDnsSession, error) {
			v, e := startRustPolicyDns(b)
			if e != nil {
				return nil, e
			}
			session = &policyDnsInjectedSession{policyDnsSession: v}
			switch mode {
			case "next1":
				session.nextAt = 1
			case "next2":
				session.nextAt = 2
			case "advance":
				session.advanceAt = 1
			}
			return session, nil
		})
		if !used || session == nil || !session.closed || fx.calls != 1 || !reflect.DeepEqual(policyDnsResultJSON(got), policyDnsResultJSON(want)) || policyDnsRecoveries.Load() != n+1 {
			t.Fatalf("%s continuation resource/result mismatch: %v", mode, policyDnsRecoveryReason.Load())
		}
	}
}
func TestPolicyDnsFlowOwnedAdapterNoRecovery(t *testing.T) {
	if !rustPolicyDnsAvailable() {
		t.Fatal("native policy unavailable")
	}
	t.Setenv("SMARTVPN_RUST_POLICY_DNS", "1")
	n := policyDnsRecoveries.Load()
	TestPolicyDnsOwnedHTTPResponseAndWriterPanic(t)
	TestPolicyDnsOwnedHTTPReadBranches(t)
	TestPolicyDnsServiceUnicodeCodecFacts(t)
	if policyDnsRecoveries.Load() != n {
		t.Fatal("owned adapter recovered")
	}
}

type policyDnsHTTPFake struct{ trace []policyDnsEffect }

func (f *policyDnsHTTPFake) Release() {}
func (f *policyDnsHTTPFake) Call(e policyDnsEffect) policyDnsReply {
	f.trace = append(f.trace, e)
	switch e.Kind {
	case "lock", "match-read", "read-policy":
		return policyDnsReply{}
	case "subscription", "enabled":
		return policyDnsReply{Flag: true}
	case "match-nodes":
		return policyDnsReply{Number: 1}
	case "match-source", "hash":
		return policyDnsReply{Text: "synthetic-hash"}
	case "running":
		return policyDnsReply{}
	case "source":
		return (&appPolicyDnsEffects{}).source("rules")
	}
	panic("unexpected fake effect")
}
func TestPolicyDnsFlowEveryHTTPBoundary(t *testing.T) {
	if !rustPolicyDnsAvailable() {
		t.Fatal("native policy unavailable")
	}
	q := policyDnsRequest{Op: "http"}
	wantFX := &policyDnsHTTPFake{}
	want := runGoPolicyDns(q, wantFX)
	for _, phase := range []string{"next", "advance"} {
		count := len(wantFX.trace)
		if phase == "next" {
			count++
		}
		for at := 1; at <= count; at++ {
			fx := &policyDnsHTTPFake{}
			var session *policyDnsInjectedSession
			n := policyDnsRecoveries.Load()
			got, used := runRustPolicyDnsWithStart(q, fx, func(b []byte) (policyDnsSession, error) {
				v, e := startRustPolicyDns(b)
				if e != nil {
					return nil, e
				}
				session = &policyDnsInjectedSession{policyDnsSession: v}
				if phase == "next" {
					session.nextAt = at
				} else {
					session.advanceAt = at
				}
				return session, nil
			})
			if !used || session == nil || !session.closed || !reflect.DeepEqual(fx.trace, wantFX.trace) || !reflect.DeepEqual(policyDnsResultJSON(got), policyDnsResultJSON(want)) || policyDnsRecoveries.Load() != n+1 {
				t.Fatalf("HTTP %s/%d replay or result mismatch", phase, at)
			}
		}
	}
}
func TestPolicyDnsFlowLegacyBoundariesWithoutRecovery(t *testing.T) {
	if !rustPolicyDnsAvailable() {
		t.Fatal("native policy unavailable")
	}
	t.Setenv("SMARTVPN_RUST_POLICY_DNS", "1")
	n := policyDnsRecoveries.Load()
	TestSubscriptionRulesCapsAtThreeThousand(t)
	TestProxyServerDomainsIsBoundedAndSorted(t)
	TestPolicyDnsServiceUnicodeCodecFacts(t)
	if policyDnsRecoveries.Load() != n {
		t.Fatalf("normal boundary silently recovered: %v", policyDnsRecoveryReason.Load())
	}
}
