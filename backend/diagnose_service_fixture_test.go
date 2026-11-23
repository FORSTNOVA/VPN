package main

import (
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sync"
	"testing"
	"time"
)

type diagnosticDiffSpec struct {
	Running, Chain, StartFail, StopFail, ListFail, PutFail, GetFail, ResolverFail, CountryFail, BuildFail bool
	Choice, Active, IPv4, Trace4, IPv6, Resolver, Country, Mapped                                         string
	DetailSuccess, STUNSuccess                                                                            int
	Body                                                                                                  []byte
	Details                                                                                               egressFacts
}
type diagnosticDiffFrame struct {
	Effect           diagnoseEffect
	Owned, Temporary bool
}
type diagnosticDiffFX struct {
	a        *app
	e        *appDiagnoseEffects
	trace    []diagnosticDiffFrame
	hooks    []string
	mu       sync.Mutex
	sites    []int
	response diagnoseServiceResult
}

func diagnosticDiffBase() diagnosticDiffSpec {
	return diagnosticDiffSpec{Choice: "node", Active: "node", IPv4: "192.0.2.1", IPv6: "2001:db8::1", Trace4: "192.0.2.2", Resolver: "198.51.100.1", Country: "US", Mapped: "192.0.2.1:1234", Details: egressFacts{IPv4: "203.0.113.9", IPv6: "2001:db8::9", CountryCode: "us", Country: "USA", ISP: "synthetic", ASN: "AS1"}, Body: []byte(`{"security":{"hosting":true,"vpn":true}}`)}
}
func diagnosticDiffNew(q diagnoseServiceRequest, s diagnosticDiffSpec) *diagnosticDiffFX {
	a := &app{}
	testEditSettings(a, func(s *settings) { s.SelectionMode = "manual" })
	testEditSettings(a, func(v *settings) { v.SelectedNode = s.Choice })
	a.settings.Chain.Enabled = s.Chain
	f := &diagnosticDiffFX{a: a}
	var target *app
	if q.Op == "diagnose" || q.Op == "ensure" {
		target = a
	}
	e := newAppDiagnoseEffects(target, q, nil)
	f.e = e
	hook := func(k string) { f.hooks = append(f.hooks, k) }
	e.running = func() bool { hook("running"); return s.Running }
	e.start = func() error {
		hook("start")
		if s.StartFail {
			return errors.New("synthetic start")
		}
		return nil
	}
	e.stop = func() error {
		hook("stop")
		if s.StopFail {
			return errors.New("synthetic stop")
		}
		return nil
	}
	e.list = func() ([]proxyNode, error) {
		hook("list")
		if s.ListFail {
			return nil, errors.New("synthetic list")
		}
		return []proxyNode{{Name: s.Choice}}, nil
	}
	e.mihomo = func(method, path string, v any) (map[string]any, error) {
		hook(method + ":" + path)
		if method == "PUT" && s.PutFail {
			return nil, errors.New("synthetic PUT")
		}
		if method == "GET" && s.GetFail {
			return nil, errors.New("synthetic GET")
		}
		return map[string]any{"now": s.Active}, nil
	}
	e.exit = func(v6 bool) exitIPResult {
		if v6 {
			return exitIPResult{Address: s.IPv6}
		}
		return exitIPResult{Address: s.IPv4}
	}
	e.trace = func() exitIPResult { return exitIPResult{Address: s.Trace4} }
	e.detail = func(i int) ([]byte, egressFacts, error) {
		if i != s.DetailSuccess {
			return nil, egressFacts{}, fmt.Errorf("detail-%d", i)
		}
		return s.Body, s.Details, nil
	}
	e.resolver = func() (dnsFacts, error) {
		if s.ResolverFail {
			return dnsFacts{}, errors.New("synthetic resolver")
		}
		return dnsFacts{Resolver: s.Resolver, Geo: "synthetic geo"}, nil
	}
	e.country = func(ip string) (string, error) {
		if s.CountryFail {
			return "", errors.New("synthetic country")
		}
		return s.Country, nil
	}
	e.relay = func() udpRelayResult { return udpRelayResult{} }
	e.buildSTUN = func() ([]byte, error) {
		if s.BuildFail {
			return nil, errors.New("synthetic build")
		}
		return []byte{1, 2, 3}, nil
	}
	e.exchange = func(i int, b []byte) (string, int64, error) {
		if i != s.STUNSuccess {
			return "", 0, fmt.Errorf("stun-%d", i)
		}
		return s.Mapped, 123, nil
	}
	e.site = func(i int) siteCheckResult {
		f.mu.Lock()
		f.sites = append(f.sites, i)
		f.mu.Unlock()
		return siteCheckResult{Name: checkTargets[i].name, State: "ok", HTTPCode: 200, LatencyMs: int64(i + 1), Detail: "synthetic"}
	}
	e.respond = func(status int, msg string, r diagnoseReport) {
		if a.mu.TryLock() {
			a.mu.Unlock()
			panic("response without app lock")
		}
		hook("response")
		f.response = diagnoseServiceResult{Status: status, Message: msg, Report: r}
	}
	return f
}
func (f *diagnosticDiffFX) Call(e diagnoseEffect) diagnoseReply {
	r := f.e.Call(e)
	f.trace = append(f.trace, diagnosticDiffFrame{e, f.e.owned, f.e.temporary})
	return r
}
func (f *diagnosticDiffFX) Release() { f.e.Release() }
func diagnosticDiffRun(t *testing.T, q diagnoseServiceRequest, s diagnosticDiffSpec, run func(diagnoseServiceRequest, diagnoseServiceEffects) diagnoseServiceResult) (diagnoseServiceResult, *diagnosticDiffFX) {
	t.Helper()
	f := diagnosticDiffNew(q, s)
	if f.e.borrowed {
		f.a.mu.Lock()
	}
	r := run(q, f)
	f.Release()
	if f.e.borrowed {
		if f.a.mu.TryLock() {
			f.a.mu.Unlock()
			t.Fatal("borrowed lock released")
		}
		f.a.mu.Unlock()
	}
	if !f.a.mu.TryLock() {
		t.Fatal("mutex leaked")
	}
	f.a.mu.Unlock()
	return r, f
}
func diagnosticDiffCompare(t *testing.T, q diagnoseServiceRequest, s diagnosticDiffSpec, run func(diagnoseServiceRequest, diagnoseServiceEffects) diagnoseServiceResult) {
	t.Helper()
	a, x := diagnosticDiffRun(t, q, s, runGoDiagnoseService)
	b, y := diagnosticDiffRun(t, q, s, run)
	if fmt.Sprint(a.Err) != fmt.Sprint(b.Err) {
		t.Fatalf("errors %v / %v", a.Err, b.Err)
	}
	a.Err = nil
	b.Err = nil
	if !reflect.DeepEqual(a, b) || !reflect.DeepEqual(x.trace, y.trace) || !reflect.DeepEqual(x.hooks, y.hooks) || !reflect.DeepEqual(x.response, y.response) {
		t.Fatalf("op=%s\nGo=%#v\nRust=%#v\nGoTrace=%#v\nRustTrace=%#v\nGoHooks=%v RustHooks=%v", q.Op, a, b, x.trace, y.trace, x.hooks, y.hooks)
	}
}
func TestMigrationDiagnosticSafeSelectedLifecycle(t *testing.T) {
	for _, op := range []string{"diagnose", "ensure", "details", "dns", "stun"} {
		diagnosticDiffCompare(t, diagnoseServiceRequest{Op: op, ProxyAddress: "synthetic"}, diagnosticDiffBase(), runSelectedDiagnoseService)
	}
}
func diagnosticKinds(f *diagnosticDiffFX) []string {
	var a []string
	for _, v := range f.trace {
		a = append(a, v.Effect.Kind)
	}
	return a
}
func diagnosticCount(f *diagnosticDiffFX, k string) int {
	n := 0
	for _, v := range f.trace {
		if v.Effect.Kind == k {
			n++
		}
	}
	return n
}
func diagnosticLegacyGoldens(t *testing.T, run func(diagnoseServiceRequest, diagnoseServiceEffects) diagnoseServiceResult) {
	for mode := 0; mode < 8; mode++ {
		s := diagnosticDiffBase()
		switch mode {
		case 1:
			s.Running = true
		case 2:
			s.StartFail = true
		case 3:
			s.Chain = true
			s.PutFail = true
		case 4:
			s.Active = ""
		case 5:
			s.IPv4 = ""
			s.Trace4 = ""
		case 6:
			s.IPv4 = ""
		case 7:
			s.StopFail = true
		}
		r, f := diagnosticDiffRun(t, diagnoseServiceRequest{Op: "diagnose"}, s, run)
		if diagnosticCount(f, "response") != 1 {
			t.Fatal("response not exactly once")
		}
		calls := diagnosticKinds(f)
		if calls[len(calls)-1] != "unlock" {
			t.Fatal("final unlock missing")
		}
		stop := diagnosticCount(f, "stop")
		if mode == 1 || mode == 2 {
			if stop != 0 {
				t.Fatal("already running/startfailed stopped")
			}
		} else if stop != 1 {
			t.Fatalf("mode %d stops=%d", mode, stop)
		}
		if mode == 1 && diagnosticCount(f, "put") != 0 {
			t.Fatal("running repinned")
		}
		if mode == 2 || mode == 3 {
			if r.Status != 502 {
				t.Fatal("start/pin priority")
			}
		}
		if mode == 4 && r.Status != 409 {
			t.Fatal("empty selection not rejected")
		}
		if mode == 5 {
			for _, k := range []string{"ipv6", "detail", "resolver", "relay", "stun-build", "sites-start", "sites-join"} {
				if diagnosticCount(f, k) != 0 {
					t.Fatal("early skip called", k)
				}
			}
			for _, site := range r.Report.Sites {
				if site.State != "skipped" {
					t.Fatal("site not skipped")
				}
			}
		}
		if mode == 6 && r.Report.Egress.IPv4 != s.Trace4 {
			t.Fatal("trace fallback missing")
		}
		if mode == 0 || mode == 6 || mode == 7 {
			if calls[len(calls)-3] != "response" || calls[len(calls)-2] != "stop" {
				t.Fatal("response/stop/unlock order")
			}
			if r.Report.Egress.IPv6 != s.IPv6 || !r.Report.DNS.MatchesExit || r.Report.Quality.DNSLeak {
				t.Fatal("address authority/country verdict")
			}
		}
	}
	for _, success := range []int{0, 1, -1} {
		s := diagnosticDiffBase()
		s.DetailSuccess = success
		r, f := diagnosticDiffRun(t, diagnoseServiceRequest{Op: "details"}, s, run)
		want := success + 1
		if success < 0 {
			want = len(egressSources)
			if fmt.Sprint(r.Err) != fmt.Sprintf("detail-%d", want-1) {
				t.Fatal("last detail error lost")
			}
		}
		if diagnosticCount(f, "detail") != want {
			t.Fatal("detail fallback ordering")
		}
	}
	for _, resolver := range []string{"198.51.100.1", " bad ", ""} {
		s := diagnosticDiffBase()
		s.Resolver = resolver
		_, f := diagnosticDiffRun(t, diagnoseServiceRequest{Op: "dns"}, s, run)
		want := 0
		if resolver == "198.51.100.1" {
			want = 1
		}
		if diagnosticCount(f, "country") != want {
			t.Fatal("invalid resolver queried country")
		}
	}
	for success := -1; success < len(stunTargets); success++ {
		s := diagnosticDiffBase()
		s.STUNSuccess = success
		r, f := diagnosticDiffRun(t, diagnoseServiceRequest{Op: "stun"}, s, run)
		want := success + 1
		if success < 0 {
			want = len(stunTargets)
			if fmt.Sprint(r.Err) != fmt.Sprintf("stun-%d", want-1) {
				t.Fatal("last STUN error lost")
			}
		}
		if diagnosticCount(f, "stun-exchange") != want {
			t.Fatal("STUN target ordering")
		}
	}
}
func TestMigrationDiagnosticSafeLegacyGoldens(t *testing.T) {
	diagnosticLegacyGoldens(t, runSelectedDiagnoseService)
}
func diagnosticConcurrentGolden(t *testing.T, run func(diagnoseServiceRequest, diagnoseServiceEffects) diagnoseServiceResult) {
	t.Helper()
	q := diagnoseServiceRequest{Op: "diagnose"}
	f := diagnosticDiffNew(q, diagnosticDiffBase())
	n := len(checkTargets)
	arrived := make(chan int, n)
	completed := make(chan int, n)
	gates := make([]chan struct{}, n)
	for i := range gates {
		gates[i] = make(chan struct{})
	}
	f.e.site = func(i int) siteCheckResult {
		arrived <- i
		<-gates[i]
		completed <- i
		return siteCheckResult{Name: checkTargets[i].name, State: "ok", HTTPCode: 200, LatencyMs: int64(i)}
	}
	done := make(chan diagnoseServiceResult, 1)
	go func() { done <- run(q, f) }()
	for i := 0; i < n; i++ {
		select {
		case <-arrived:
		case <-time.After(5 * time.Second):
			for _, g := range gates {
				select {
				case <-g:
				default:
					close(g)
				}
			}
			t.Fatal("sites did not overlap concurrently")
		}
	}
	for i := n - 1; i >= 0; i-- {
		close(gates[i])
		select {
		case actual := <-completed:
			if actual != i {
				t.Fatal("completion order")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("site completion watchdog")
		}
	}
	select {
	case r := <-done:
		for i, v := range r.Report.Sites {
			if v.Name != checkTargets[i].name || v.LatencyMs != int64(i) {
				t.Fatal("reverse completion corrupted report order")
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("join watchdog")
	}
	if !f.a.mu.TryLock() {
		t.Fatal("mutex leaked")
	}
	f.a.mu.Unlock()
}
func TestMigrationDiagnosticSafeConcurrency(t *testing.T) {
	diagnosticConcurrentGolden(t, runSelectedDiagnoseService)
}

type diagnosticErrorWriter struct {
	header http.Header
	writes int
}

func (w *diagnosticErrorWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}
func (w *diagnosticErrorWriter) WriteHeader(int) {}
func (w *diagnosticErrorWriter) Write([]byte) (int, error) {
	w.writes++
	return 0, errors.New("synthetic response write")
}
func TestMigrationDiagnosticSafeResponseWriteError(t *testing.T) {
	q := diagnoseServiceRequest{Op: "diagnose"}
	f := diagnosticDiffNew(q, diagnosticDiffBase())
	w := &diagnosticErrorWriter{}
	f.e.respond = func(status int, message string, r diagnoseReport) {
		if f.a.mu.TryLock() {
			f.a.mu.Unlock()
			t.Fatal("response unlocked")
		}
		writeJSON(w, r)
	}
	r := runSelectedDiagnoseService(q, f)
	if r.Status != 200 || w.writes != 1 || diagnosticCount(f, "stop") != 1 {
		t.Fatal("write failure changed cleanup/result")
	}
	if !f.a.mu.TryLock() {
		t.Fatal("write failure lock leak")
	}
	f.a.mu.Unlock()
}
func (f *diagnosticDiffFX) FinalizeDiagnoseResult(q diagnoseServiceRequest, r diagnoseServiceResult) diagnoseServiceResult {
	return f.e.FinalizeDiagnoseResult(q, r)
}
func TestMigrationDiagnosticSafeEnsureOwnership(t *testing.T) {
	r, f := diagnosticDiffRun(t, diagnoseServiceRequest{Op: "ensure"}, diagnosticDiffBase(), runSelectedDiagnoseService)
	if !r.Temporary || r.Err != nil {
		t.Fatal("ensure failed")
	}
	for _, h := range f.hooks {
		if h == "stop" {
			t.Fatal("successful ensure stopped instead of transferring ownership")
		}
	}
}
