//go:build windows && rustcore

package main

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
)

type profileIndependentFX struct {
	base    *appNodeFrontEffects
	trace   []nodeFrontEffect
	replies map[string]nodeFrontReply
}

func (f *profileIndependentFX) Release() {}
func (f *profileIndependentFX) Call(e nodeFrontEffect) nodeFrontReply {
	f.trace = append(f.trace, e)
	if f.replies != nil {
		r, ok := f.replies[e.Kind]
		if !ok {
			panic("unexpected profile match effect " + e.Kind)
		}
		return r
	}
	return f.base.Call(e)
}
func profileIndependentActual(t *testing.T, q nodeFrontRequest, f nodeFrontEffects) nodeFrontResult {
	t.Helper()
	if !rustNodeFrontAvailable() {
		t.Fatal("node native unavailable")
	}
	before := nodeFrontRecoveries.Load()
	r, ok := runRustNodeFront(q, f)
	if !ok || nodeFrontRecoveries.Load() != before {
		t.Fatalf("node incomplete %s %v %v", q.Op, ok, nodeFrontRecoveryReason.Load())
	}
	return r
}
func TestProfileConversionActualIndependentMatrix(t *testing.T) {
	qs := []nodeFrontRequest{}
	for _, b := range converterIndependentCorpus() {
		for _, op := range []string{"yaml", "parse-profile"} {
			qs = append(qs, nodeFrontRequest{Op: op, Body: b})
		}
	}
	for _, s := range []string{"", "bad", "ftp://example.invalid", "https://example.invalid", "https://", mergedSubscriptionURL, "trojan://synthetic@example.invalid#N", "anytls://synthetic@example.invalid#N", "vless://id@example.invalid:443?security=none#N", "VMESS://bad"} {
		for _, op := range []string{"dispatch", "vmess", "validate"} {
			qs = append(qs, nodeFrontRequest{Op: op, Path: s})
		}
	}
	for i, q := range qs {
		t.Run(fmt.Sprint(i)+q.Op, func(t *testing.T) {
			g := &profileIndependentFX{base: &appNodeFrontEffects{q: q}}
			r := &profileIndependentFX{base: &appNodeFrontEffects{q: q}}
			gr := runGoNodeFront(q, g)
			rr := profileIndependentActual(t, q, r)
			if fmt.Sprint(gr.Err) != fmt.Sprint(rr.Err) || gr.Status != rr.Status || !reflect.DeepEqual(gr.Data, rr.Data) || !reflect.DeepEqual(g.trace, r.trace) || !reflect.DeepEqual(g.base.node, r.base.node) || !reflect.DeepEqual(g.base.nodes, r.base.nodes) {
				t.Fatalf("node parity go=%#v rust=%#v traces %v / %v", gr, rr, g.trace, r.trace)
			}
		})
	}
}
func TestProfileConversionActualIndependentMatch(t *testing.T) {
	for i := 0; i < 8; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			replies := map[string]nodeFrontReply{"match-read": {}, "match-nodes": {Number: 1}, "match-source": {Text: "hash"}, "subscription": {}, "hash": {Text: "hash"}}
			want := 1
			wantKinds := []string{"match-read", "match-nodes", "match-source", "subscription", "subscription", "hash"}
			switch i {
			case 0:
				replies["match-read"] = nodeFrontReply{Err: errors.New("synthetic read")}
				want = 0
				wantKinds = wantKinds[:1]
			case 1:
				replies["match-nodes"] = nodeFrontReply{}
				want = 0
				wantKinds = wantKinds[:2]
			case 2:
				replies["match-source"] = nodeFrontReply{Err: errors.New("synthetic source")}
				want = 0
				wantKinds = wantKinds[:3]
			case 3:
				replies["match-source"] = nodeFrontReply{Text: "other"}
				want = 0
			case 4, 5:
				replies["subscription"] = nodeFrontReply{Number: 1}
				wantKinds = wantKinds[:4]
				if i == 4 {
					replies["match-source"] = nodeFrontReply{Text: mergedSubscriptionURL}
				} else {
					want = 0
				}
			case 6:
				replies["match-source"] = nodeFrontReply{}
				want = 0
			}
			f := &profileIndependentFX{replies: replies}
			r := profileIndependentActual(t, nodeFrontRequest{Op: "matches"}, f)
			kinds := []string{}
			for _, e := range f.trace {
				kinds = append(kinds, e.Kind)
			}
			if r.Err != nil || r.Status != want || !reflect.DeepEqual(kinds, wantKinds) {
				t.Fatalf("match contract %#v %v", r, kinds)
			}
		})
	}
}
