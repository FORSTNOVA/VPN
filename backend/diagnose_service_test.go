package main

import (
	"errors"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

func safeDiagnoseFX(a *app, q diagnoseServiceRequest) *appDiagnoseEffects {
	e := newAppDiagnoseEffects(a, q, nil)
	e.running = func() bool { return true }
	e.start = func() error { return nil }
	e.stop = func() error { return nil }
	e.list = func() ([]proxyNode, error) { return []proxyNode{{Name: "fixture"}}, nil }
	e.mihomo = func(string, string, any) (map[string]any, error) { return map[string]any{"now": "fixture"}, nil }
	e.exit = func(v6 bool) exitIPResult {
		if v6 {
			return exitIPResult{}
		}
		return exitIPResult{Address: "192.0.2.1"}
	}
	e.trace = func() exitIPResult { return exitIPResult{} }
	e.detail = func(int) ([]byte, egressFacts, error) {
		return []byte(`{"security":{}}`), egressFacts{CountryCode: "US"}, nil
	}
	e.resolver = func() (dnsFacts, error) { return dnsFacts{Resolver: "192.0.2.2"}, nil }
	e.country = func(string) (string, error) { return "US", nil }
	e.relay = func() udpRelayResult { return udpRelayResult{Reachable: true} }
	e.buildSTUN = func() ([]byte, error) { return []byte("synthetic"), nil }
	e.exchange = func(int, []byte) (string, int64, error) { return "192.0.2.1:1234", 7, nil }
	e.site = func(i int) siteCheckResult { return siteCheckResult{Name: checkTargets[i].name, State: "ok"} }
	e.respond = func(int, string, diagnoseReport) {}
	return e
}
func TestDiagnoseServiceResponseCleanupOrder(t *testing.T) {
	a := &app{}
	q := diagnoseServiceRequest{Op: "diagnose"}
	e := safeDiagnoseFX(a, q)
	var trace []string
	e.running = func() bool { return false }
	e.start = func() error { trace = append(trace, "start"); return nil }
	e.stop = func() error {
		trace = append(trace, "stop")
		if a.mu.TryLock() {
			a.mu.Unlock()
			t.Fatal("stop unlocked")
		}
		return errors.New("ignored")
	}
	e.respond = func(status int, _ string, r diagnoseReport) {
		trace = append(trace, "response")
		if status != 200 || !r.Temporary || !r.DNS.MatchesExit || !r.UDP.MatchesExit || r.Quality.DNSLeak || r.Quality.WebRTCLeak {
			t.Fatal("report contract")
		}
		if a.mu.TryLock() {
			a.mu.Unlock()
			t.Fatal("response unlocked")
		}
	}
	r := runGoDiagnoseService(q, e)
	e.Release()
	if r.Err != nil || !reflect.DeepEqual(trace, []string{"start", "response", "stop"}) {
		t.Fatal(trace)
	}
	if !a.mu.TryLock() {
		t.Fatal("mutex leak")
	}
	a.mu.Unlock()
}
func TestDiagnoseServiceIPv4EarlyExit(t *testing.T) {
	a := &app{}
	q := diagnoseServiceRequest{Op: "diagnose"}
	e := safeDiagnoseFX(a, q)
	exits := 0
	e.exit = func(v6 bool) exitIPResult {
		exits++
		if v6 {
			t.Fatal("IPv6 after unavailable IPv4")
		}
		return exitIPResult{}
	}
	e.detail = func(int) ([]byte, egressFacts, error) {
		t.Fatal("details after unavailable IPv4")
		return nil, egressFacts{}, nil
	}
	r := runGoDiagnoseService(q, e)
	if r.Status != 200 || exits != 1 || r.Report.Egress.Error != diagnoseExitFailureMessage() {
		t.Fatal("early result")
	}
	for _, s := range r.Report.Sites {
		if s.State != "skipped" {
			t.Fatal("sites not skipped")
		}
	}
}
func TestDiagnoseServiceEnsureOwnership(t *testing.T) {
	for _, crash := range []bool{false, true} {
		a := &app{}
		testEditSettings(a, func(s *settings) { s.SelectionMode = "manual" })
		testEditSettings(a, func(s *settings) { s.SelectedNode = "fixture" })
		q := diagnoseServiceRequest{Op: "ensure"}
		e := safeDiagnoseFX(a, q)
		e.running = func() bool { return false }
		stops := 0
		e.stop = func() error { stops++; return nil }
		if crash {
			e.list = func() ([]proxyNode, error) { panic("synthetic after start") }
		}
		a.mu.Lock()
		func() {
			defer func() {
				v := recover()
				if crash != (v != nil) {
					t.Fatal("panic mismatch")
				}
			}()
			r := runGoDiagnoseService(q, e)
			if !r.Temporary || r.Err != nil {
				t.Fatal("ensure result")
			}
		}()
		e.Release()
		if a.mu.TryLock() {
			a.mu.Unlock()
			t.Fatal("borrowed mutex released")
		}
		a.mu.Unlock()
		want := 0
		if crash {
			want = 1
		}
		if stops != want {
			t.Fatalf("cleanup %d want %d", stops, want)
		}
	}
}
func TestDiagnoseServiceFallbacks(t *testing.T) {
	a := &app{}
	q := diagnoseServiceRequest{Op: "diagnose"}
	e := safeDiagnoseFX(a, q)
	details, stuns := 0, 0
	e.detail = func(i int) ([]byte, egressFacts, error) {
		details++
		if i == 0 {
			return nil, egressFacts{}, errors.New("first")
		}
		return []byte(`{"connection":{"isp":"cloudflare"}}`), egressFacts{CountryCode: "US", IPv4: "not-authority", IPv6: "2001:db8::1"}, nil
	}
	e.exchange = func(i int, _ []byte) (string, int64, error) {
		stuns++
		if i == 0 {
			return "", 0, errors.New("first")
		}
		return "192.0.2.1:9", 8, nil
	}
	r := runGoDiagnoseService(q, e)
	if details != 2 || stuns != 2 || r.Report.Egress.IPv4 != "192.0.2.1" || r.Report.Egress.IPv6 != "2001:db8::1" || !r.Report.Quality.IsHosting {
		t.Fatal("fallback contract")
	}
}
func TestDiagnoseServiceQualityParity(t *testing.T) {
	for mask := 0; mask < 32; mask++ {
		for _, isp := range []string{"ordinary ISP", "GOOGLE Cloud", "hostİng", ""} {
			b := []byte(fmt.Sprintf(`{"connection":{"isp":%q},"security":{"hosting":%t,"proxy":%t,"vpn":%t,"tor":%t,"anonymous":%t}}`, isp, mask&1 != 0, mask&2 != 0, mask&4 != 0, mask&8 != 0, mask&16 != 0))
			if want, got := parseIpQuality(b), diagnoseQualityFromFacts(diagnoseQualityFacts(b)); !reflect.DeepEqual(want, got) {
				t.Fatal("classification differs")
			}
		}
	}
}
func TestDiagnoseServiceWorkerPanicJoinsBeforeStop(t *testing.T) {
	a := &app{}
	q := diagnoseServiceRequest{Op: "diagnose"}
	e := safeDiagnoseFX(a, q)
	e.running = func() bool { return false }
	var finished atomic.Int32
	stops := 0
	e.site = func(i int) siteCheckResult {
		defer finished.Add(1)
		if i == 0 {
			panic("synthetic worker")
		}
		return siteCheckResult{}
	}
	e.stop = func() error {
		stops++
		if int(finished.Load()) != len(checkTargets) {
			t.Fatal("stopped before workers joined")
		}
		return nil
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected worker panic")
			}
		}()
		runGoDiagnoseService(q, e)
	}()
	e.Release()
	if stops != 1 || e.sitesDone != nil {
		t.Fatal("resource replay or leak")
	}
	if !a.mu.TryLock() {
		t.Fatal("mutex leaked")
	}
	a.mu.Unlock()
}
func TestDiagnoseServiceExplicitConcurrentBatch(t *testing.T) {
	a := &app{}
	q := diagnoseServiceRequest{Op: "diagnose"}
	e := safeDiagnoseFX(a, q)
	n := len(checkTargets)
	entered := make(chan int, n)
	gates := make([]chan struct{}, n)
	for i := range gates {
		gates[i] = make(chan struct{})
	}
	e.site = func(i int) siteCheckResult { entered <- i; <-gates[i]; return siteCheckResult{Name: fmt.Sprint(i)} }
	a.mu.Lock()
	e.owned = true
	indices := make([]int, n)
	for i := range indices {
		indices[i] = i
	}
	e.Call(diagnoseEffect{Kind: "sites-start", Indices: indices})
	for i := 0; i < n; i++ {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("batch serialized")
		}
	}
	for i := n - 1; i >= 0; i-- {
		close(gates[i])
	}
	r := e.Call(diagnoseEffect{Kind: "sites-join"})
	for i, s := range r.Sites {
		if s.Name != fmt.Sprint(i) {
			t.Fatal("completion order changed")
		}
	}
	e.Release()
}
