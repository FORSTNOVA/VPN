//go:build windows && amd64 && rustcore

package main

import (
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"
)

func chainDiffCompare(t *testing.T, q chainServiceRequest, s chainDiffSpec) (*chainDiffFX, chainServiceResult) {
	t.Helper()
	if !rustChainFlowAvailable() {
		t.Fatal("actual chain DLL required")
	}
	g, r := chainDiffNew(q, s), chainDiffNew(q, s)
	if q.Op == "build" {
		defer g.Base.a.mu.Unlock()
		defer r.Base.a.mu.Unlock()
	}
	gr := runGoChainService(q, g)
	gr = g.FinalizeChainResult(q, gr)
	before := chainFlowRecoveries.Load()
	rr, used := runRustChainService(q, r)
	rr = r.FinalizeChainResult(q, rr)
	if !used {
		t.Fatal("unexpected fallback")
	}
	if chainFlowRecoveries.Load() != before {
		t.Fatalf("unexpected recovery op=%s", q.Op)
	}
	ge, re := "", ""
	if gr.Err != nil {
		ge = gr.Err.Error()
	}
	if rr.Err != nil {
		re = rr.Err.Error()
	}
	if ge != re {
		t.Fatalf("error mismatch %s/%s", ge, re)
	}
	for _, err := range s.Fail {
		if err != nil && errors.Is(gr.Err, err) != errors.Is(rr.Err, err) {
			t.Fatal("error identity changed")
		}
	}
	gr.Err = nil
	rr.Err = nil
	if !reflect.DeepEqual(gr, rr) || !reflect.DeepEqual(g.Trace, r.Trace) || g.ClockCalls != r.ClockCalls {
		t.Fatalf("req=%+v\nGo=%+v\nRust=%+v\nGoTrace=%+v\nRustTrace=%+v", q, gr, rr, g.Trace, r.Trace)
	}
	if r.Base.a != nil {
		if locked := r.Base.a.mu.TryLock(); locked {
			r.Base.a.mu.Unlock()
			if q.Op == "build" {
				t.Fatal("build released borrowed mutex")
			}
		} else if q.Op != "build" {
			t.Fatal("owned mutex leaked")
		}
	}
	return r, rr
}
func chainDiffRequest(op string, s chainDiffSpec) chainServiceRequest {
	return chainServiceRequest{Op: op, Name: "renamed", Original: "old", Next: proxyChain{Enabled: true, Hops: []string{"a", "b"}}, Hops: []string{"a", "b"}, Profile: []byte("synthetic"), Index: map[string][]map[string]any{"a": {s.Proxies[0]}, "b": {s.Proxies[1]}}}
}
func TestMigrationChainActualMatrix(t *testing.T) {
	for _, op := range []string{"compile", "prefix", "chain", "build", "update", "save-profile"} {
		for mask := 0; mask < 16; mask++ {
			for _, fault := range []string{"", "read", "decode", "yaml", "save"} {
				t.Run(fmt.Sprintf("%s/%d/%s", op, mask, fault), func(t *testing.T) {
					s := chainDiffBase()
					s.Running = mask&1 != 0
					s.Job = mask&2 != 0
					s.State.Subscription = mask&4 != 0
					s.State.Chain.Enabled = mask&8 != 0
					if fault != "" {
						s.Fail[fault] = errors.New("synthetic " + fault)
					}
					chainDiffCompare(t, chainDiffRequest(op, s), s)
				})
			}
		}
	}
}
func TestMigrationChainActualRandom(t *testing.T) {
	for i := 0; i < 1500; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			rng := rand.New(rand.NewSource(int64(200000 + i)))
			s := chainDiffBase()
			ops := []string{"compile", "prefix", "chain", "build", "update", "save-profile"}
			q := chainDiffRequest(ops[rng.Intn(6)], s)
			s.Running = rng.Intn(3) == 0
			s.Job = rng.Intn(3) == 0
			s.State.Subscription = rng.Intn(4) != 0
			s.State.Chain.Enabled = rng.Intn(2) == 0
			if rng.Intn(3) == 0 {
				s.State.LockedRegion = "US"
				rr := s.Regions["b"]
				rr.VerifiedAt = s.Now.Add(-regionTTL + time.Duration(rng.Intn(3)-1)*time.Nanosecond)
				if rng.Intn(3) == 0 {
					rr.Country = "JP"
				}
				s.Regions["b"] = rr
			}
			names := []string{"a", "b", "", "missing", "SmartVPNChain"}
			n := rng.Intn(10)
			q.Hops = make([]string, n)
			for j := range q.Hops {
				q.Hops[j] = names[rng.Intn(len(names))]
			}
			q.Next.Hops = appendPreserveChainSlice(q.Hops)
			q.Next.Enabled = rng.Intn(2) == 0
			q.Next.Name = []string{"", "old", "missing"}[rng.Intn(3)]
			q.Name = []string{"renamed", "old", "nil", "empty", strings.Repeat("界", 80), strings.Repeat("界", 81)}[rng.Intn(6)]
			q.Original = []string{"", "old", "nil", "empty", " old "}[rng.Intn(5)]
			if rng.Intn(2) == 0 {
				k := []string{"read", "decode", "yaml", "save"}[rng.Intn(4)]
				s.Fail[k] = errors.New("synthetic")
				s.DecodeFailAt = 1 + rng.Intn(2)
			}
			if rng.Intn(2) == 0 {
				p := s.Proxies[rng.Intn(2)]
				field := []string{"type", "server", "dialer-proxy"}[rng.Intn(3)]
				p[field] = []any{nil, 123, "", "existing"}[rng.Intn(4)]
			}
			if rng.Intn(5) == 0 {
				q.Index["a"] = append(q.Index["a"], q.Index["a"][0])
			}
			if rng.Intn(5) == 0 {
				s.Proxies = append(s.Proxies, map[string]any{"name": chainExit, "type": "trojan", "server": "collision.invalid"})
				q.Index[chainExit] = []map[string]any{{"name": chainExit}}
			}
			chainDiffCompare(t, q, s)
		})
	}
}
func TestMigrationChainActualPriorities(t *testing.T) {
	for _, stage := range []string{"compile-before-running", "running-before-second-compile", "second-compile-fail", "noncurrent-running-allowed"} {
		t.Run(stage, func(t *testing.T) {
			s := chainDiffBase()
			q := chainDiffRequest("save-profile", s)
			switch stage {
			case "compile-before-running":
				s.Running = true
				s.Fail["decode"] = errors.New("decode first")
			case "running-before-second-compile":
				s.Running = true
				s.Fail["decode"] = errors.New("decode second")
				s.DecodeFailAt = 2
			case "second-compile-fail":
				s.Fail["decode"] = errors.New("decode second")
				s.DecodeFailAt = 2
			case "noncurrent-running-allowed":
				s.Running = true
				q.Original = ""
			}
			f, r := chainDiffCompare(t, q, s)
			switch stage {
			case "compile-before-running":
				if r.Status != 400 || f.Calls["running"] != 0 {
					t.Fatal("compile error must win")
				}
			case "running-before-second-compile":
				if r.Status != 409 || f.Calls["decode"] != 1 {
					t.Fatal("running should reject before second compile")
				}
			case "second-compile-fail":
				if r.Status != 400 || f.Calls["decode"] != 2 {
					t.Fatal("enabled current requires second compile")
				}
			case "noncurrent-running-allowed":
				if r.Status != 200 || f.Calls["running"] != 0 {
					t.Fatal("noncurrent profile must allow running")
				}
			}
		})
	}
	for _, delta := range []time.Duration{-time.Nanosecond, 0, time.Nanosecond} {
		t.Run(fmt.Sprintf("region-ttl/%d", delta), func(t *testing.T) {
			s := chainDiffBase()
			s.State.LockedRegion = "US"
			rec := s.Regions["b"]
			rec.VerifiedAt = s.Now.Add(-regionTTL - delta)
			s.Regions["b"] = rec
			f, r := chainDiffCompare(t, chainDiffRequest("build", s), s)
			if (r.Message != "") != (delta > 0) || f.ClockCalls != 1 {
				t.Fatal("region TTL equality changed")
			}
		})
	}
	for _, country := range []string{"JP", "US"} {
		t.Run("region-clock-shortcircuit/"+country, func(t *testing.T) {
			s := chainDiffBase()
			s.State.LockedRegion = "US"
			rec := s.Regions["b"]
			rec.Country = country
			rec.Status = "unverified"
			s.Regions["b"] = rec
			f, _ := chainDiffCompare(t, chainDiffRequest("build", s), s)
			if f.ClockCalls != 0 {
				t.Fatal("invalid region must not read clock")
			}
		})
	}
	for _, name := range []string{strings.Repeat("界", 80), strings.Repeat("界", 81)} {
		t.Run(fmt.Sprint(len([]rune(name))), func(t *testing.T) {
			s := chainDiffBase()
			q := chainDiffRequest("save-profile", s)
			q.Name = name
			q.Original = ""
			f, r := chainDiffCompare(t, q, s)
			if len([]rune(name)) == 80 && r.Status != 200 {
				t.Fatal("80-rune name rejected")
			}
			if len([]rune(name)) == 81 && (r.Status != 400 || f.Calls["lock"] != 0) {
				t.Fatal("81-rune rejection should precede lock")
			}
		})
	}
}

func TestMigrationChainActualLegacyIdentity(t *testing.T) {
	if !rustChainFlowAvailable() {
		t.Fatal("actual DLL required")
	}
	chainLegacyIdentityContracts(t, func(q chainServiceRequest, fx chainServiceEffects) chainServiceResult {
		before := chainFlowRecoveries.Load()
		r, used := runRustChainService(q, fx)
		if !used || chainFlowRecoveries.Load() != before {
			t.Fatal("identity contract fallback/recovery")
		}
		return r
	})
}

func TestMigrationChainActualHopAndProfileGoldens(t *testing.T) {
	for _, scenario := range []string{"duplicate", "missing", "ambiguous", "dialer-before-fields", "fields-before-alias", "alias-collision", "numeric-dialer-ignored", "map-key-not-proxy-name"} {
		t.Run(scenario, func(t *testing.T) {
			s := chainDiffBase()
			q := chainDiffRequest("compile", s)
			want := ""
			switch scenario {
			case "duplicate":
				q.Hops = []string{"a", "a"}
				want = chainCompileMessage3(2)
			case "missing":
				q.Hops = []string{"a", "missing"}
				want = chainCompileMessage4(2)
			case "ambiguous":
				q.Index["a"] = append(q.Index["a"], q.Index["a"][0])
				want = chainCompileMessage4(1)
			case "dialer-before-fields":
				q.Index["a"][0]["dialer-proxy"] = "external"
				delete(q.Index["a"][0], "server")
				want = chainCompileMessage5(1)
			case "fields-before-alias":
				delete(q.Index["a"][0], "server")
				q.Index["SmartVPNChainHop1"] = []map[string]any{{}}
				want = chainCompileMessage6(1)
			case "alias-collision":
				q.Index[chainExit] = []map[string]any{{}}
				want = chainCompileMessage7(chainExit)
			case "numeric-dialer-ignored":
				q.Index["a"][0]["dialer-proxy"] = 123
			case "map-key-not-proxy-name":
				q.Index["a"][0]["name"] = "different"
			}
			_, r := chainDiffCompare(t, q, s)
			if r.Message != want {
				t.Fatalf("priority mismatch got=%s want=%s", r.Message, want)
			}
		})
	}
	for _, scenario := range []string{"nil-map", "nil-original", "empty-original", "nil-target", "empty-target", "untrimmed-original"} {
		t.Run(scenario, func(t *testing.T) {
			s := chainDiffBase()
			q := chainDiffRequest("save-profile", s)
			q.Original = ""
			want := 200
			switch scenario {
			case "nil-map":
				s.State.Chains = nil
			case "nil-original":
				q.Original = "nil"
				want = 409
			case "empty-original":
				q.Original = "empty"
			case "nil-target":
				q.Name = "nil"
			case "empty-target":
				q.Name = "empty"
				want = 409
			case "untrimmed-original":
				q.Original = " old "
				want = 409
			}
			f, r := chainDiffCompare(t, q, s)
			if r.Status != want {
				t.Fatal("nil/empty profile existence changed", r.Status, want)
			}
			if want == 409 && f.Calls["read"] != 0 {
				t.Fatal("profile conflict must precede read/compile")
			}
		})
	}
}
