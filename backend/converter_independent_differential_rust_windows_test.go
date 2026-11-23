//go:build windows && rustcore

package main

import (
	"fmt"
	"reflect"
	"testing"
)

func converterIndependentActual(t *testing.T, q converterServiceRequest, e converterServiceEffects) converterServiceResult {
	t.Helper()
	if !rustConverterFlowAvailable() {
		t.Fatal("actual converter unavailable")
	}
	before := converterFlowRecoveries.Load()
	childBefore := nodeFrontRecoveries.Load()
	r, ok := runRustConverterService(q, e)
	if !ok || converterFlowRecoveries.Load() != before || nodeFrontRecoveries.Load() != childBefore {
		t.Fatalf("native not complete op=%s handled=%v reason=%v", q.Op, ok, converterFlowRecoveryReason.Load())
	}
	return r
}

func TestConverterActualIndependentCorpus(t *testing.T) {
	for i, b := range converterIndependentCorpus() {
		for _, op := range []string{"parse-body", "singbox", "ensure-yaml"} {
			t.Run(fmt.Sprintf("%d/%s", i, op), func(t *testing.T) {
				gq, ge := converterBodyRequest(op, append([]byte(nil), b...))
				rq, re := converterBodyRequest(op, append([]byte(nil), b...))
				gr := runGoConverterService(gq, ge)
				rr := converterIndependentActual(t, rq, re)
				if fmt.Sprint(gr.Err) != fmt.Sprint(rr.Err) {
					t.Fatalf("error go=%v rust=%v", gr.Err, rr.Err)
				}
				if op == "ensure-yaml" {
					gb, gn, _ := converterByteResult(ge, gr)
					rb, rn, _ := converterByteResult(re, rr)
					if !reflect.DeepEqual(gb, rb) || !reflect.DeepEqual(gn, rn) {
						t.Fatalf("bytes/nodes mismatch go=%q %#v rust=%q %#v", gb, gn, rb, rn)
					}
				} else {
					gp, gn, _ := converterProxyResult(ge, gr)
					rp, rn, _ := converterProxyResult(re, rr)
					if !reflect.DeepEqual(gp, rp) || !reflect.DeepEqual(gn, rn) {
						t.Fatalf("projection mismatch go=%#v %#v rust=%#v %#v", gp, gn, rp, rn)
					}
				}
			})
		}
	}
}

func TestConverterActualIndependentLegacy(t *testing.T) {
	if !rustConverterFlowAvailable() {
		t.Fatal("actual converter unavailable")
	}
	t.Setenv("SMARTVPN_RUST_CONVERTER_FLOW", "1")
	before := converterFlowRecoveries.Load()
	childBefore := nodeFrontRecoveries.Load()
	TestConverterIndependentLegacyProtocolEdges(t)
	TestConverterIndependentLegacyBytes(t)
	TestConverterIndependentLegacyProjectionMismatch(t)
	TestConverterIndependentLegacyOrphanAndIndexMerge(t)
	TestConverterIndependentLegacyMergePolicy(t)
	TestConverterIndependentLegacyVmessDialects(t)
	TestConverterIndependentLegacyFormatPriority(t)
	TestConverterIndependentLegacyCorpusFingerprint(t)
	if converterFlowRecoveries.Load() != before || nodeFrontRecoveries.Load() != childBefore {
		t.Fatalf("golden recovered: %v", converterFlowRecoveryReason.Load())
	}
}

func TestConverterActualIndependentMergeAndNames(t *testing.T) {
	for i := 0; i < 180; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			makeCase := func() (converterServiceRequest, *appConverterEffects) {
				e := newConverterEffects()
				q := converterServiceRequest{Op: "merge-sources"}
				for j := 0; j < 1+i%4; j++ {
					body := []byte(fmt.Sprintf("proxies: [{name: N, type: ss}, {name: N, type: trojan}, {name: N (2), type: ss}]\nproxy-groups: [{name: G, type: select, proxies: [N, DIRECT, REJECT]}]\nrules: ['DOMAIN,example.invalid,G', 'IP-CIDR,192.0.2.0/24,N,no-resolve']\n# %d", j))
					if i%7 == 0 && j == 0 {
						body = []byte("invalid")
					}
					q.Sources = append(q.Sources, converterSource{Body: e.put(body), Label: fmt.Sprintf("L%d", (i+j)%2), Prefix: []string{"", "N", "P "}[i%3]})
				}
				return q, e
			}
			gq, ge := makeCase()
			rq, re := makeCase()
			gr := runGoConverterService(gq, ge)
			rr := converterIndependentActual(t, rq, re)
			gb, gn, gerr := converterByteResult(ge, gr)
			rb, rn, rerr := converterByteResult(re, rr)
			if fmt.Sprint(gerr) != fmt.Sprint(rerr) || !reflect.DeepEqual(gb, rb) || !reflect.DeepEqual(gn, rn) {
				t.Fatalf("merge mismatch %v %v", gerr, rerr)
			}
		})
	}
	for i := 0; i < 40; i++ {
		ge, re := newConverterEffects(), newConverterEffects()
		ge.used = map[string]struct{}{}
		re.used = map[string]struct{}{}
		for j := 0; j < i; j++ {
			n := fmt.Sprintf("N (%d)", j+2)
			ge.used[n] = struct{}{}
			re.used[n] = struct{}{}
		}
		ge.used["N"] = struct{}{}
		re.used["N"] = struct{}{}
		q := converterServiceRequest{Op: "unique-name", Name: "N"}
		gr := runGoConverterService(q, ge)
		rr := converterIndependentActual(t, q, re)
		if gr.Text != rr.Text || !reflect.DeepEqual(ge.used, re.used) {
			t.Fatal("name map mutation mismatch")
		}
	}
	converterSafeOriginalGraph(t, func(q converterServiceRequest, fx converterServiceEffects) converterServiceResult {
		return converterIndependentActual(t, q, fx)
	})
	TestConverterIndependentLegacyDNSFirstWins(t)
}

func TestConverterActualIndependentUnicode(t *testing.T) {
	for i, b := range converterIndependentUnicodeCorpus() {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			gq, ge := converterBodyRequest("parse-body", b)
			rq, re := converterBodyRequest("parse-body", b)
			gr := runGoConverterService(gq, ge)
			rr := converterIndependentActual(t, rq, re)
			gp, gn, ge1 := converterProxyResult(ge, gr)
			rp, rn, re1 := converterProxyResult(re, rr)
			if fmt.Sprint(ge1) != fmt.Sprint(re1) || !reflect.DeepEqual(gp, rp) || !reflect.DeepEqual(gn, rn) {
				t.Fatalf("unicode parity %#v %#v / %#v %#v", gp, gn, rp, rn)
			}
		})
	}
	TestConverterIndependentLegacyUnicode(t)
}
