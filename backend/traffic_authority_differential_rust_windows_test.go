//go:build windows && rustcore

package main

import (
	"math/rand"
	"testing"
	"time"
)

func authorityRustFactory() (serviceStateBackend, error) { return newRustServiceStateBackend() }
func authorityRequireRust(t *testing.T) {
	t.Helper()
	if !rustServiceStateAvailable() {
		t.Fatal("real service state DLL unavailable")
	}
}
func authorityRustTraffic(t *testing.T) authorityTrafficRun {
	authorityRequireRust(t)
	if !rustTrafficFlowAvailable() {
		t.Fatal("traffic flow DLL unavailable")
	}
	return func(q trafficServiceRequest, f trafficServiceEffects) trafficServiceResult {
		n := trafficFlowRecoveries.Load()
		r, used := runRustTrafficService(q, f)
		if !used || trafficFlowRecoveries.Load() != n {
			t.Fatalf("traffic used=%v recovery=%d reason=%v", used, trafficFlowRecoveries.Load()-n, trafficFlowRecoveryReason.Load())
		}
		return r
	}
}
func TestServiceStateActualContracts(t *testing.T) {
	authorityRequireRust(t)
	authorityRateGoldens(t, authorityRustFactory)
	authoritySharedGolden(t, authorityRustFactory)
	authorityCommandGolden(t, authorityRustFactory)
	authorityFaultGoldens(t, authorityRustFactory)
	authorityAckGolden(t, authorityRustFactory)
}
func TestServiceStateActualTrafficLifecycle(t *testing.T) {
	authorityTrafficGolden(t, authorityRustFactory, authorityRustTraffic(t))
	authorityTrafficCompare(t, authorityRustFactory, authorityRustTraffic(t))
}
func TestServiceStateActualRandomSharedAuthority(t *testing.T) {
	authorityRequireRust(t)
	g := authorityNew(t, authorityGoFactory)
	r := authorityNew(t, authorityRustFactory)
	clock := time.Now()
	random := rand.New(rand.NewSource(280000))
	for i := 0; i < 2000; i++ {
		var ge, re error
		clock = clock.Add(time.Duration(random.Intn(3000)-1000) * time.Millisecond)
		sample := trafficSample{at: clock, up: int64(random.Intn(100000)), down: int64(random.Intn(100000))}
		switch random.Intn(4) {
		case 0:
			ge = g.baseline(sample)
			re = r.baseline(sample)
		case 1:
			msg := []string{"", "fake history error", "第二次错误\x00"}[random.Intn(3)]
			ge = g.samplingError(msg)
			re = r.samplingError(msg)
		case 2:
			before := authoritySnapshot(t, r)
			gu, gd, e := g.rate(sample)
			ru, rd, f := r.rate(sample)
			ge = e
			re = f
			if gu != ru || gd != rd {
				t.Fatalf("rate %d %d/%d vs %d/%d", i, gu, gd, ru, rd)
			}
			if after := authoritySnapshot(t, r); after != before {
				t.Fatal("rate mutated")
			}
		case 3:
			ge = g.baseline(trafficSample{})
			re = r.baseline(trafficSample{})
		}
		if (ge == nil) != (re == nil) {
			t.Fatalf("step %d error %v/%v", i, ge, re)
		}
		gs, rs := authoritySnapshot(t, g), authoritySnapshot(t, r)
		gs.Epoch = 0
		rs.Epoch = 0
		if gs != rs {
			t.Fatalf("step %d state %+v/%+v", i, gs, rs)
		}
		if _, ok := r.backend.(*rustServiceStateBackend); !ok {
			t.Fatal("normal request changed owner to Go")
		}
		if len(r.clocks) > 1 {
			t.Fatal("unbounded clock ledger")
		}
	}
}
func TestServiceStateActualClosedRoutes(t *testing.T) {
	authorityClosedGolden(t, authorityRustFactory, authorityRustTraffic(t))
}
func TestServiceStateActualOverflowCompatibility(t *testing.T) {
	authorityRequireRust(t)
	s := authorityNew(t, authorityRustFactory)
	base := time.Now()
	if err := s.baseline(trafficSample{at: base}); err != nil {
		t.Fatal(err)
	}
	_, _, supported, err := s.backend.Rate(1, 20, 30)
	if err != nil || !supported {
		t.Fatal("ordinary rate not native")
	}
	_, _, supported, err = s.backend.Rate(1e-9, 1<<63-1, 1<<63-1)
	if err != nil || supported {
		t.Fatal("overflow must explicitly defer platform conversion")
	}
	sample := trafficSample{at: base.Add(time.Nanosecond), up: 1<<63 - 1, down: 1<<63 - 1}
	u, d, err := s.rate(sample)
	wu, wd := authorityLegacyRate(trafficSample{at: base}, sample)
	if err != nil || u != wu || d != wd {
		t.Fatal("platform overflow compatibility")
	}
	if _, ok := s.backend.(*rustServiceStateBackend); !ok {
		t.Fatal("overflow changed state owner")
	}
}
