package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNextHealthStateSuccesses(t *testing.T) {
	params := defaultHealthParams()
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name        string
		current     nodeHealth
		wantState   string
		wantLatency int
		wantSuccess int
	}{
		{
			name:        "a healthy node stays healthy",
			current:     nodeHealth{Name: "n", State: healthHealthy},
			wantState:   healthHealthy,
			wantLatency: 120,
			wantSuccess: 1,
		},
		{
			name:        "a suspect node recovers on the next success",
			current:     nodeHealth{Name: "n", State: healthSuspect, ConsecutiveFailures: 1},
			wantState:   healthHealthy,
			wantLatency: 120,
			wantSuccess: 1,
		},
		{
			name:        "an unhealthy node returns to healthy",
			current:     nodeHealth{Name: "n", State: healthUnhealthy, ConsecutiveFailures: 4},
			wantState:   healthHealthy,
			wantLatency: 120,
			wantSuccess: 1,
		},
		{
			name:        "the first success after a cooldown enters recovery",
			current:     nodeHealth{Name: "n", State: healthCooldown, CooldownUntil: now.Add(-time.Second)},
			wantState:   healthRecovery,
			wantLatency: 120,
			wantSuccess: 1,
		},
		{
			name: "recovery completes once the success streak is long enough",
			current: nodeHealth{
				Name: "n", State: healthRecovery,
				ConsecutiveSuccesses: params.RecoverySuccesses - 1,
				CooldownUntil:        now.Add(-time.Minute),
			},
			wantState:   healthHealthy,
			wantLatency: 120,
			wantSuccess: params.RecoverySuccesses,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nextHealthState(tc.current, true, 120, now, params)
			if got.State != tc.wantState {
				t.Errorf("state = %q, want %q", got.State, tc.wantState)
			}
			if got.LatencyMs != tc.wantLatency {
				t.Errorf("latency = %d, want %d", got.LatencyMs, tc.wantLatency)
			}
			if got.ConsecutiveSuccesses != tc.wantSuccess {
				t.Errorf("successes = %d, want %d", got.ConsecutiveSuccesses, tc.wantSuccess)
			}
			if got.ConsecutiveFailures != 0 {
				t.Errorf("failures = %d, want 0 after a success", got.ConsecutiveFailures)
			}
			if !got.LastSuccessAt.Equal(now) {
				t.Errorf("lastSuccessAt = %v, want %v", got.LastSuccessAt, now)
			}
			if !got.UpdatedAt.Equal(now) {
				t.Errorf("updatedAt = %v, want %v", got.UpdatedAt, now)
			}
		})
	}
}

func TestNextHealthStateFailure(t *testing.T) {
	params := defaultHealthParams()
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name         string
		current      nodeHealth
		wantState    string
		wantFailures int
	}{
		{
			name:         "the first failure only marks the node suspect",
			current:      nodeHealth{Name: "n", State: healthHealthy},
			wantState:    healthSuspect,
			wantFailures: 1,
		},
		{
			name:         "the confirmed failure makes it unhealthy",
			current:      nodeHealth{Name: "n", State: healthSuspect, ConsecutiveFailures: 1},
			wantState:    healthUnhealthy,
			wantFailures: 2,
		},
		{
			name:         "further failures keep it unhealthy",
			current:      nodeHealth{Name: "n", State: healthUnhealthy, ConsecutiveFailures: 5},
			wantState:    healthUnhealthy,
			wantFailures: 6,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nextHealthState(tc.current, false, 0, now, params)
			if got.State != tc.wantState {
				t.Errorf("state = %q, want %q", got.State, tc.wantState)
			}
			if got.ConsecutiveFailures != tc.wantFailures {
				t.Errorf("failures = %d, want %d", got.ConsecutiveFailures, tc.wantFailures)
			}
			if got.ConsecutiveSuccesses != 0 {
				t.Errorf("successes = %d, want 0 after a failure", got.ConsecutiveSuccesses)
			}
			if got.LatencyMs != 0 {
				t.Errorf("latency = %d, want 0 after a failure", got.LatencyMs)
			}
		})
	}
}

func TestNextHealthStateCooldownHolds(t *testing.T) {
	params := defaultHealthParams()
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	cooling := nodeHealth{
		Name: "n", State: healthCooldown,
		CooldownUntil: now.Add(30 * time.Second),
	}

	recovered := nextHealthState(cooling, true, 90, now, params)
	if recovered.State != healthCooldown {
		t.Errorf("a cooling node that answers must stay in cooldown, got %q", recovered.State)
	}
	if recovered.ConsecutiveSuccesses != 1 {
		t.Errorf("successes = %d, want the streak to start counting", recovered.ConsecutiveSuccesses)
	}

	failed := nextHealthState(cooling, false, 0, now, params)
	if failed.State != healthCooldown {
		t.Errorf("a cooling node that fails must stay in cooldown, got %q", failed.State)
	}
}

func TestNextHealthStateHonoursThreshold(t *testing.T) {
	params := defaultHealthParams()
	params.FailureThreshold = 1
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)

	got := nextHealthState(nodeHealth{Name: "n", State: healthHealthy}, false, 0, now, params)
	if got.State != healthUnhealthy {
		t.Fatalf("state = %q, want %q with a threshold of 1", got.State, healthUnhealthy)
	}
}

func TestHealthParamsNormalized(t *testing.T) {
	cases := []struct {
		name  string
		input healthParams
		want  healthParams
	}{
		{
			name:  "zero values fall back to the minimums",
			input: healthParams{},
			want: healthParams{
				ConnectTimeoutMs: 1000, PatrolIntervalSec: 10, FailureThreshold: 1,
				CooldownSec: 10, MinDwellSec: 0, RecoverySuccesses: 1,
			},
		},
		{
			name: "absurd values are capped",
			input: healthParams{
				ConnectTimeoutMs: 60000, PatrolIntervalSec: 100000, FailureThreshold: 99,
				CooldownSec: 100000, MinDwellSec: 100000, RecoverySuccesses: 99,
			},
			want: healthParams{
				ConnectTimeoutMs: 15000, PatrolIntervalSec: 600, FailureThreshold: 10,
				CooldownSec: 3600, MinDwellSec: 600, RecoverySuccesses: 10,
			},
		},
		{
			name:  "the documented defaults survive untouched",
			input: defaultHealthParams(),
			want:  defaultHealthParams(),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.input.normalized(); got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestHealthParamsRoundTrip(t *testing.T) {
	original := healthParams{
		ConnectTimeoutMs: 2500, PatrolIntervalSec: 45, FailureThreshold: 3,
		CooldownSec: 90, MinDwellSec: 20, RecoverySuccesses: 4,
	}
	body, err := original.encode()
	if err != nil {
		t.Fatal(err)
	}
	var decoded healthParams
	if err := decoded.decode(body); err != nil {
		t.Fatal(err)
	}
	if decoded != original {
		t.Fatalf("got %+v, want %+v", decoded, original)
	}
}

func TestCandidatesForRegion(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-time.Hour)
	stale := now.Add(-2 * regionTTL)

	regions := map[string]regionRecord{
		"verified-JP":    {Name: "verified-JP", Country: "JP", Status: regionVerified, VerifiedAt: fresh},
		"verified-JP-2":  {Name: "verified-JP-2", Country: "JP", Status: regionVerified, VerifiedAt: fresh},
		"stale-JP":       {Name: "stale-JP", Country: "JP", Status: regionVerified, VerifiedAt: stale},
		"verified-US":    {Name: "verified-US", Country: "US", Status: regionVerified, VerifiedAt: fresh},
		"single-JP":      {Name: "single-JP", Country: "JP", Status: regionSingle, VerifiedAt: fresh},
		"conflict-JP":    {Name: "conflict-JP", Country: "JP", Status: regionConflict, VerifiedAt: fresh},
		"unreachable-JP": {Name: "unreachable-JP", Country: "JP", Status: regionUnreachable, VerifiedAt: fresh},
	}
	health := map[string]nodeHealth{
		"verified-JP":   {Name: "verified-JP", State: healthHealthy},
		"verified-JP-2": {Name: "verified-JP-2", State: healthCooldown, CooldownUntil: now.Add(time.Minute)},
	}

	pool := candidatesForRegion("JP", regions, health, now)
	if len(pool) != 1 || pool[0] != "verified-JP" {
		t.Fatalf("pool = %v, want only verified-JP", pool)
	}

	if got := candidatesForRegion("US", regions, health, now); len(got) != 1 || got[0] != "verified-US" {
		t.Fatalf("US pool = %v, want only verified-US", got)
	}

	if got := candidatesForRegion("", regions, health, now); got != nil {
		t.Fatalf("an empty lock must yield no pool, got %v", got)
	}

	if got := candidatesForRegion("DE", regions, health, now); len(got) != 0 {
		t.Fatalf("an unmeasured region must yield no pool, got %v", got)
	}
}

func TestCandidatesForRegionReadmitsAfterCooldown(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	regions := map[string]regionRecord{
		"n1": {Name: "n1", Country: "JP", Status: regionVerified, VerifiedAt: now},
	}
	health := map[string]nodeHealth{
		"n1": {Name: "n1", State: healthCooldown, CooldownUntil: now.Add(-time.Second)},
	}
	if pool := candidatesForRegion("JP", regions, health, now); len(pool) != 1 {
		t.Fatalf("a node whose cooldown expired must be eligible again, got %v", pool)
	}
}

func TestCandidatesForRegionAreSorted(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	regions := map[string]regionRecord{}
	for _, name := range []string{"c", "a", "b"} {
		regions[name] = regionRecord{Name: name, Country: "JP", Status: regionVerified, VerifiedAt: now}
	}
	pool := candidatesForRegion("JP", regions, map[string]nodeHealth{}, now)
	want := []string{"a", "b", "c"}
	if len(pool) != len(want) {
		t.Fatalf("pool = %v, want %v", pool, want)
	}
	for index := range want {
		if pool[index] != want[index] {
			t.Fatalf("pool = %v, want a stable sorted order %v", pool, want)
		}
	}
}

func TestNodeHealthCooldownActive(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	if (nodeHealth{}).cooldownActive(now) {
		t.Error("a node with no cooldown must not be counted as cooling")
	}
	if !(nodeHealth{CooldownUntil: now.Add(time.Second)}).cooldownActive(now) {
		t.Error("a future cooldown must be active")
	}
	if (nodeHealth{CooldownUntil: now.Add(-time.Second)}).cooldownActive(now) {
		t.Error("an expired cooldown must not be active")
	}
}

func TestDwellElapsed(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	params := defaultHealthParams()

	never := testHealthApp(&app{}, func(p *healthDomainProjection) { p.Params = params })
	if !never.dwellElapsedLocked(now) {
		t.Error("a node that was never switched must be past its dwell time")
	}

	recent := testHealthApp(&app{}, func(p *healthDomainProjection) { p.Params = params; p.LastSwitch = now.Add(-time.Second) })
	if recent.dwellElapsedLocked(now) {
		t.Error("a switch one second ago must still be inside the dwell window")
	}

	old := testHealthApp(&app{}, func(p *healthDomainProjection) { p.Params = params; p.LastSwitch = now.Add(-time.Minute) })
	if !old.dwellElapsedLocked(now) {
		t.Error("a switch a minute ago must be past the dwell window")
	}
}

func TestPickFailoverTargetPrefersMeasuredNodes(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	a := testHealthApp(testControlApp(&app{}, func(a *app) {
		a.patchControlLocked(controlRuntimeLockedRegion, serviceControlState{RuntimeLockedRegion: "JP"})
	}), func(p *healthDomainProjection) {
		p.Params = defaultHealthParams()
		p.Regions = map[string]regionRecord{"fast": {Name: "fast", Country: "JP", Status: regionVerified, VerifiedAt: now}, "slow": {Name: "slow", Country: "JP", Status: regionVerified, VerifiedAt: now}, "dead": {Name: "dead", Country: "JP", Status: regionVerified, VerifiedAt: now}, "elsewhere": {Name: "elsewhere", Country: "US", Status: regionVerified, VerifiedAt: now}}
		p.Health = map[string]nodeHealth{"fast": {Name: "fast", State: healthHealthy, LatencyMs: 80}, "slow": {Name: "slow", State: healthHealthy, LatencyMs: 400}, "dead": {Name: "dead", State: healthUnhealthy, LatencyMs: 90}}
	})
	target, delay := a.pickFailoverTargetLocked(now)
	if target != "fast" || delay != 80 {
		t.Fatalf("got (%q, %d), want the fastest healthy same-region node", target, delay)
	}
}

func TestPickFailoverTargetFailsClosed(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	a := testHealthApp(testControlApp(&app{}, func(a *app) {
		a.patchControlLocked(controlRuntimeLockedRegion, serviceControlState{RuntimeLockedRegion: "JP"})
	}), func(p *healthDomainProjection) {
		p.Params = defaultHealthParams()
		p.Regions = map[string]regionRecord{"only": {Name: "only", Country: "JP", Status: regionVerified, VerifiedAt: now}}
		p.Health = map[string]nodeHealth{"only": {Name: "only", State: healthUnhealthy, LatencyMs: 90}}
	})
	if target, _ := a.pickFailoverTargetLocked(now); target != "" {
		t.Fatalf("an unhealthy same-region pool must yield no target, got %q", target)
	}

	empty := testHealthApp(testControlApp(&app{}, func(a *app) {
		a.patchControlLocked(controlRuntimeLockedRegion, serviceControlState{RuntimeLockedRegion: ""})
	}), func(p *healthDomainProjection) { p.Params = defaultHealthParams() })
	if target, _ := empty.pickFailoverTargetLocked(now); target != "" {
		t.Fatalf("an unlocked region must yield no failover target, got %q", target)
	}
}

func TestHealthSnapshotCounts(t *testing.T) {
	now := time.Now()
	a := testHealthApp(testControlApp(&app{}, func(a *app) {
		a.patchControlLocked(controlRuntimeLockedRegion, serviceControlState{RuntimeLockedRegion: "JP"})
	}), func(p *healthDomainProjection) {
		p.Params = defaultHealthParams()
		p.Regions = map[string]regionRecord{"n1": {Name: "n1", Country: "JP", Status: regionVerified, VerifiedAt: now}}
		p.Health = map[string]nodeHealth{"n1": {Name: "n1", State: healthHealthy}, "n2": {Name: "n2", State: healthUnhealthy}, "n3": {Name: "n3", State: healthCooldown, CooldownUntil: now.Add(time.Minute)}, "n4": {Name: "n4", State: healthHealthy, CooldownUntil: now.Add(time.Minute)}}
	})
	snapshot := a.healthSnapshotLocked()
	counts, ok := snapshot["counts"].(map[string]int)
	if !ok {
		t.Fatalf("counts missing from the snapshot: %+v", snapshot)
	}
	if counts[healthHealthy] != 1 {
		t.Errorf("healthy = %d, want 1 (a cooling node counts as cooling)", counts[healthHealthy])
	}
	if counts[healthUnhealthy] != 1 {
		t.Errorf("unhealthy = %d, want 1", counts[healthUnhealthy])
	}
	if counts[healthCooldown] != 2 {
		t.Errorf("cooldown = %d, want 2", counts[healthCooldown])
	}
	if snapshot["lockedRegion"] != "JP" {
		t.Errorf("lockedRegion = %v, want JP", snapshot["lockedRegion"])
	}
	if snapshot["poolSize"] != 1 {
		t.Errorf("poolSize = %v, want 1", snapshot["poolSize"])
	}
}

// controllerStub stands in for Mihomo's control API so the patrol and failover
// paths can be driven without a running kernel.
type controllerStub struct {
	mu     sync.Mutex
	calls  []string
	server *httptest.Server
}

func newControllerStub(t *testing.T, handler http.HandlerFunc) *controllerStub {
	t.Helper()
	stub := &controllerStub{}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		stub.mu.Lock()
		stub.calls = append(stub.calls, r.Method+" "+r.URL.Path+" "+string(body))
		stub.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer controller-secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if handler != nil {
			handler(w, r)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

func (c *controllerStub) app(t *testing.T) *app {
	t.Helper()
	parsed, err := url.Parse(c.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}
	return testHealthApp(&app{home: t.TempDir(), ctrlPort: port, ctrlSecret: "controller-secret"}, func(p *healthDomainProjection) {
		p.Params = defaultHealthParams()
		p.Health = map[string]nodeHealth{}
		p.Regions = map[string]regionRecord{}
	})
}

func (c *controllerStub) saw(method, path, fragment string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, call := range c.calls {
		if strings.HasPrefix(call, method+" "+path) && strings.Contains(call, fragment) {
			return true
		}
	}
	return false
}

func TestApplyPatrolRecordsTheActiveNode(t *testing.T) {
	stub := newControllerStub(t, nil)
	a := stub.app(t)
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)

	a.applyPatrolLocked("fast", map[string]int{"fast": 210}, now)

	record := a.healthDomainLocked().Health["fast"]
	if record.State != healthHealthy {
		t.Fatalf("state = %q, want %q", record.State, healthHealthy)
	}
	if record.LatencyMs != 210 {
		t.Fatalf("latency = %d, want 210", record.LatencyMs)
	}
	if a.healthDomainLocked().Blocked {
		t.Fatal("a healthy sweep must not block traffic")
	}
}

func TestApplyPatrolKeepsPoolHealthCurrent(t *testing.T) {
	stub := newControllerStub(t, nil)
	a := stub.app(t)
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	a.patchControlLocked(controlRuntimeLockedRegion, serviceControlState{RuntimeLockedRegion: "JP"})
	for _, name := range []string{"active", "standby"} {
		a.writeRegionRecordLocked(name, regionRecord{Name: name, Country: "JP", Status: regionVerified, VerifiedAt: now})
	}

	// One group measurement must update every node the sweep watches.
	a.applyPatrolLocked("active", map[string]int{"active": 180, "standby": 240}, now)

	if a.healthDomainLocked().Health["active"].State != healthHealthy || a.healthDomainLocked().Health["active"].LatencyMs != 180 {
		t.Fatalf("active = %+v, want a healthy record at 180 ms", a.healthDomainLocked().Health["active"])
	}
	if a.healthDomainLocked().Health["standby"].State != healthHealthy || a.healthDomainLocked().Health["standby"].LatencyMs != 240 {
		t.Fatalf("standby = %+v, want a healthy record at 240 ms", a.healthDomainLocked().Health["standby"])
	}
}

func TestApplyPatrolFailsClosedWhenTheRegionIsExhausted(t *testing.T) {
	stub := newControllerStub(t, nil)
	a := stub.app(t)
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	a.patchControlLocked(controlRuntimeLockedRegion, serviceControlState{RuntimeLockedRegion: "JP"})
	a.writeRegionRecordLocked("active", regionRecord{Name: "active", Country: "JP", Status: regionVerified, VerifiedAt: now})

	// The node in use is missing from the measurement: one strike, then a
	// confirmed failure.
	a.applyPatrolLocked("active", map[string]int{}, now)
	if a.healthDomainLocked().Health["active"].State != healthSuspect {
		t.Fatalf("state = %q, want %q after one failed sweep", a.healthDomainLocked().Health["active"].State, healthSuspect)
	}
	if a.healthDomainLocked().Blocked {
		t.Fatal("one failed sweep must not block traffic yet")
	}

	a.applyPatrolLocked("active", map[string]int{}, now.Add(time.Minute))
	if a.healthDomainLocked().Health["active"].State != healthUnhealthy {
		t.Fatalf("state = %q, want %q after two failed sweeps", a.healthDomainLocked().Health["active"].State, healthUnhealthy)
	}
	if !a.healthDomainLocked().Blocked {
		t.Fatal("an exhausted locked region must fail closed")
	}
	if a.healthDomainLocked().BlockReason == "" {
		t.Fatal("blocking must explain itself to the user")
	}
	if !stub.saw(http.MethodPut, "/proxies/SmartVPN", `"name":"REJECT"`) {
		t.Fatalf("the group should have been pointed at REJECT, calls: %v", stub.calls)
	}
}

// tunnelCarriesTraffic stands in for the confirmation probe when the tunnel is
// carrying traffic while the sweep says nothing in the region is alive. The real
// probe asks an HTTPS address through the local port, which a test server cannot
// answer without a certificate for it; the decision it feeds is what is under
// test here, and the probe's own plumbing is covered by the tests below that run
// it for real.
func tunnelCarriesTraffic(t *testing.T, a *app) *int {
	t.Helper()
	asked := 0
	a.confirmProbe = func() bool {
		asked++
		return true
	}
	return &asked
}

// The verdict that ends in blocked traffic comes from one bulk delay
// measurement, and a measurement can fail on its own while the tunnel is fine.
// Dropping the connection there would turn the app's diagnostic trouble into the
// user's outage, so a real request gets the last word.
func TestBlockLockedAsksAnIndependentProbeFirst(t *testing.T) {
	stub := newControllerStub(t, nil)
	a := stub.app(t)
	runningKernel(t, a)
	asked := tunnelCarriesTraffic(t, a)
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)

	a.blockLocked("active", now, "同地区已无可用节点")

	if *asked == 0 {
		t.Fatal("the verdict must be checked against a real request before traffic is dropped")
	}
	if a.healthDomainLocked().Blocked {
		t.Fatal("traffic that still flows must not be dropped")
	}
	if a.healthDomainLocked().SweepNote == "" {
		t.Fatal("the page must be told why the app did not fail over")
	}
	if stub.saw(http.MethodPut, "/proxies/SmartVPN", `"name":"REJECT"`) {
		t.Fatalf("the group must not be pointed at REJECT, calls: %v", stub.calls)
	}
}

func TestBlockLockedProceedsWhenTheTunnelIsDeadToo(t *testing.T) {
	stub := newControllerStub(t, nil)
	a := stub.app(t)
	runningKernel(t, a)
	// Nothing listens here, so the request cannot get through either.
	a.mixedPort = 1

	a.blockLocked("active", time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC), "同地区已无可用节点")

	if !a.healthDomainLocked().Blocked {
		t.Fatal("a region whose probe fails as well must fail closed")
	}
	if !stub.saw(http.MethodPut, "/proxies/SmartVPN", `"name":"REJECT"`) {
		t.Fatalf("the group should have been pointed at REJECT, calls: %v", stub.calls)
	}
	if a.healthDomainLocked().SweepNote != "" {
		t.Fatal("a confirmed block is not an anomaly and must not be reported as one")
	}
	if a.healthDomainLocked().BlockReason == "" {
		t.Fatal("blocking must explain itself to the user")
	}
}

func TestBlockLockedWithoutAKernelDoesNotProbe(t *testing.T) {
	stub := newControllerStub(t, nil)
	a := stub.app(t)
	// Disconnected: there is no tunnel to probe and nothing to leak either.
	a.mixedPort = 1
	a.blockLocked("active", time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC), "同地区已无可用节点")
	if !a.healthDomainLocked().Blocked {
		t.Fatal("a region that cannot be measured at all must still fail closed")
	}
}

// The shape of the field failure: every node of the locked region fails the
// delay test while the tunnel is carrying traffic perfectly well.
func TestApplyPatrolKeepsTrafficFlowingWhenTheMeasurementIsWrong(t *testing.T) {
	stub := newControllerStub(t, nil)
	a := stub.app(t)
	runningKernel(t, a)
	tunnelCarriesTraffic(t, a)
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	a.patchControlLocked(controlRuntimeLockedRegion, serviceControlState{RuntimeLockedRegion: "JP"})
	a.writeRegionRecordLocked("active", regionRecord{Name: "active", Country: "JP", Status: regionVerified, VerifiedAt: now})

	a.applyPatrolLocked("active", map[string]int{}, now)
	a.applyPatrolLocked("active", map[string]int{}, now.Add(time.Minute))

	if a.healthDomainLocked().Health["active"].State != healthUnhealthy {
		t.Fatalf("state = %q, want the sweep's own verdict recorded", a.healthDomainLocked().Health["active"].State)
	}
	if a.healthDomainLocked().Blocked {
		t.Fatal("a request that still gets through must keep the connection alive")
	}
	if a.healthDomainLocked().SweepNote == "" {
		t.Fatal("the contradiction must be visible on the connection page")
	}
	if stub.saw(http.MethodPut, "/proxies/SmartVPN", `"name":"REJECT"`) {
		t.Fatalf("the group must not have been pointed at REJECT, calls: %v", stub.calls)
	}

	// A sweep that measured something means the machinery works again, so the
	// note stops describing the present.
	a.applyPatrolLocked("active", map[string]int{"active": 120}, now.Add(2*time.Minute))
	if a.healthDomainLocked().SweepNote != "" {
		t.Fatal("a sweep that reached a node must clear the note")
	}
}

func TestApplyPatrolSwitchesToASameRegionStandby(t *testing.T) {
	stub := newControllerStub(t, nil)
	a := stub.app(t)
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	a.patchControlLocked(controlRuntimeLockedRegion, serviceControlState{RuntimeLockedRegion: "JP"})
	testEditHealth(a, func(p *healthDomainProjection) { p.LastSwitch = now.Add(-time.Hour) })
	testEditHealth(a, func(p *healthDomainProjection) { p.Params.FailureThreshold = 1 })
	for _, name := range []string{"active", "standby"} {
		a.writeRegionRecordLocked(name, regionRecord{Name: name, Country: "JP", Status: regionVerified, VerifiedAt: now})
	}

	a.applyPatrolLocked("active", map[string]int{"standby": 150}, now)

	// The failed node is parked in cooldown once traffic has moved off it.
	if a.healthDomainLocked().Health["active"].State != healthCooldown {
		t.Fatalf("state = %q, want %q for the node that was switched away from",
			a.healthDomainLocked().Health["active"].State, healthCooldown)
	}
	if a.healthDomainLocked().Blocked {
		t.Fatal("a usable standby means traffic must keep flowing")
	}
	if !stub.saw(http.MethodPut, "/proxies/SmartVPN", `"name":"standby"`) {
		t.Fatalf("the group should have moved to the standby, calls: %v", stub.calls)
	}
	if !a.healthDomainLocked().Health["active"].cooldownActive(now) {
		t.Fatal("the failed node must be sent to cooldown")
	}
}

// A sweep that could not be measured at all is still evidence about the node in
// use: staying silent here is what leaves a user on a dead node while the UI
// reports it healthy.
func TestFailedSweepMarksTheNodeInUse(t *testing.T) {
	stub := newControllerStub(t, nil)
	a := stub.app(t)
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	a.patchControlLocked(controlRuntimeLockedRegion, serviceControlState{RuntimeLockedRegion: "JP"})
	testEditHealth(a, func(p *healthDomainProjection) { p.LastSwitch = now.Add(-time.Hour) })
	testEditHealth(a, func(p *healthDomainProjection) { p.Params.FailureThreshold = 1 })
	for _, name := range []string{"active", "standby"} {
		a.writeRegionRecordLocked(name, regionRecord{Name: name, Country: "JP", Status: regionVerified, VerifiedAt: now})
	}

	a.applyFailedSweepLocked("active", now)

	if a.healthDomainLocked().Health["active"].State != healthUnhealthy {
		t.Fatalf("the node in use must count the failure, got %q", a.healthDomainLocked().Health["active"].State)
	}
	if _, recorded := a.healthDomainLocked().Health["standby"]; recorded {
		t.Fatal("a candidate that was not measured must keep its earlier health")
	}
	// Nothing was learned about any candidate, so there is no evidence that
	// another node would work: protected traffic fails closed instead.
	if !a.healthDomainLocked().Blocked {
		t.Fatalf("an unmeasured region must fail closed, calls: %v", stub.calls)
	}
	if !stub.saw(http.MethodPut, "/proxies/SmartVPN", "REJECT") {
		t.Fatalf("the group should have been pointed at REJECT, calls: %v", stub.calls)
	}
}

// The failure of the sweep itself must not discard what earlier sweeps learned:
// a known-good candidate is still the right place to move traffic to.
func TestFailedSweepKeepsAKnownStandby(t *testing.T) {
	stub := newControllerStub(t, nil)
	a := stub.app(t)
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	a.patchControlLocked(controlRuntimeLockedRegion, serviceControlState{RuntimeLockedRegion: "JP"})
	testEditHealth(a, func(p *healthDomainProjection) { p.LastSwitch = now.Add(-time.Hour) })
	testEditHealth(a, func(p *healthDomainProjection) { p.Params.FailureThreshold = 1 })
	for _, name := range []string{"active", "standby"} {
		a.writeRegionRecordLocked(name, regionRecord{Name: name, Country: "JP", Status: regionVerified, VerifiedAt: now})
	}
	a.writeHealthRecordLocked("standby", nodeHealth{Name: "standby", State: healthHealthy, UpdatedAt: now, LastSuccessAt: now, LatencyMs: 150})

	a.applyFailedSweepLocked("active", now)

	if a.healthDomainLocked().Blocked {
		t.Fatal("a known-good standby means traffic must keep flowing")
	}
	if !stub.saw(http.MethodPut, "/proxies/SmartVPN", `"name":"standby"`) {
		t.Fatalf("traffic should have moved to the standby, calls: %v", stub.calls)
	}
}

func TestFailedSweepWithoutALockedRegionOnlyRecords(t *testing.T) {
	stub := newControllerStub(t, nil)
	a := stub.app(t)
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)

	a.applyFailedSweepLocked("active", now)

	if a.healthDomainLocked().Health["active"].State != healthSuspect {
		t.Fatalf("state = %q, want the first failure recorded as a suspicion", a.healthDomainLocked().Health["active"].State)
	}
	if len(stub.calls) != 0 {
		t.Fatalf("without a locked region nothing may be switched: %v", stub.calls)
	}
}

func TestSelectedChoicePrefersTheLockedNode(t *testing.T) {
	locked := testControlApp(&app{}, func(a *app) {
		testSetSettings(a, settings{SelectionMode: "auto", SelectedNode: "other"})
		a.patchControlLocked(controlRuntimeLockedRegion, serviceControlState{RuntimeLockedRegion: "JP"})
		a.patchControlLocked(controlRuntimeLockedNode, serviceControlState{RuntimeLockedNode: "jp-a"})
	})
	if got := locked.selectedChoice(); got != "jp-a" {
		t.Fatalf("got %q, want the pinned node so automatic groups cannot leave the region", got)
	}

	// A pinned node without a locked region must not override the user's mode.
	stale := testControlApp(&app{}, func(a *app) {
		testSetSettings(a, settings{SelectionMode: "auto"})
		a.patchControlLocked(controlRuntimeLockedNode, serviceControlState{RuntimeLockedNode: "jp-a"})
	})
	if got := stale.selectedChoice(); got != autoGroup {
		t.Fatalf("got %q, want %q", got, autoGroup)
	}
}

func TestBestLockedCandidateStaysInsideTheRegion(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	a := testHealthApp(testControlApp(&app{}, func(a *app) {
		a.patchControlLocked(controlRuntimeLockedRegion, serviceControlState{RuntimeLockedRegion: "JP"})
		a.patchControlLocked(controlRuntimeLockedNode, serviceControlState{RuntimeLockedNode: "jp-b"})
	}), func(p *healthDomainProjection) {
		p.Params = defaultHealthParams()
		p.Health = map[string]nodeHealth{}
		p.Regions = map[string]regionRecord{"jp-a": {Name: "jp-a", Country: "JP", Status: regionVerified, VerifiedAt: now}, "jp-b": {Name: "jp-b", Country: "JP", Status: regionVerified, VerifiedAt: now}, "us-a": {Name: "us-a", Country: "US", Status: regionVerified, VerifiedAt: now}}
	})

	// The pinned node wins while it is still reachable, even when it is slower.
	if target, delay := a.bestLockedCandidateLocked(map[string]int{"jp-a": 100, "jp-b": 300, "us-a": 50}, now); target != "jp-b" || delay != 300 {
		t.Fatalf("got (%q, %d), want the pinned node jp-b", target, delay)
	}
	// Only when the pinned node is gone does the fastest same-region node win,
	// and the out-of-region node is never eligible.
	if target, delay := a.bestLockedCandidateLocked(map[string]int{"jp-a": 100, "us-a": 50}, now); target != "jp-a" || delay != 100 {
		t.Fatalf("got (%q, %d), want jp-a", target, delay)
	}
	// Nothing usable inside the region yields an empty result so the caller can
	// fail closed instead of crossing regions.
	if target, _ := a.bestLockedCandidateLocked(map[string]int{"us-a": 50}, now); target != "" {
		t.Fatalf("got %q, want an empty result", target)
	}
}

func TestApplyPatrolKeepsThePinInsideTheRegion(t *testing.T) {
	stub := newControllerStub(t, nil)
	a := stub.app(t)
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	a.patchControlLocked(controlRuntimeLockedRegion, serviceControlState{RuntimeLockedRegion: "JP"})
	a.patchControlLocked(controlRuntimeLockedNode, serviceControlState{RuntimeLockedNode: "active"})
	testEditHealth(a, func(p *healthDomainProjection) { p.LastSwitch = now.Add(-time.Hour) })
	testEditHealth(a, func(p *healthDomainProjection) { p.Params.FailureThreshold = 1 })
	for _, name := range []string{"active", "standby"} {
		a.writeRegionRecordLocked(name, regionRecord{Name: name, Country: "JP", Status: regionVerified, VerifiedAt: now})
	}
	a.writeRegionRecordLocked("other", regionRecord{Name: "other", Country: "US", Status: regionVerified, VerifiedAt: now})

	a.applyPatrolLocked("active", map[string]int{"standby": 200, "other": 90}, now)

	if a.controlLocked().RuntimeLockedNode != "standby" || a.settingsLocked().LockedNode != "standby" {
		t.Fatalf("the pin should follow the failover, got %q / %q", a.controlLocked().RuntimeLockedNode, a.settingsLocked().LockedNode)
	}
	if got := a.selectedChoice(); got != "standby" {
		t.Fatalf("selectedChoice = %q, want the standby", got)
	}
}

func TestApplyPatrolRespectsTheMinDwellTime(t *testing.T) {
	stub := newControllerStub(t, nil)
	a := stub.app(t)
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	a.patchControlLocked(controlRuntimeLockedRegion, serviceControlState{RuntimeLockedRegion: "JP"})
	testEditHealth(a, func(p *healthDomainProjection) { p.LastSwitch = now.Add(-time.Second) })
	testEditHealth(a, func(p *healthDomainProjection) { p.Params.FailureThreshold = 1 })
	for _, name := range []string{"active", "standby"} {
		a.writeRegionRecordLocked(name, regionRecord{Name: name, Country: "JP", Status: regionVerified, VerifiedAt: now})
	}

	a.applyPatrolLocked("active", map[string]int{"standby": 150}, now)

	if a.healthDomainLocked().Health["active"].State != healthUnhealthy {
		t.Fatalf("state = %q, want a recorded failure", a.healthDomainLocked().Health["active"].State)
	}
	if stub.saw(http.MethodPut, "/proxies/SmartVPN", `"name":"standby"`) {
		t.Fatal("a switch inside the dwell window must wait")
	}
	if a.healthDomainLocked().Blocked {
		t.Fatal("the dwell window must not block traffic either")
	}
}

func TestApplyPatrolNeverCrossesRegions(t *testing.T) {
	stub := newControllerStub(t, nil)
	a := stub.app(t)
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	a.patchControlLocked(controlRuntimeLockedRegion, serviceControlState{RuntimeLockedRegion: "JP"})
	testEditHealth(a, func(p *healthDomainProjection) { p.LastSwitch = now.Add(-time.Hour) })
	testEditHealth(a, func(p *healthDomainProjection) { p.Params.FailureThreshold = 1 })
	a.writeRegionRecordLocked("active", regionRecord{Name: "active", Country: "JP", Status: regionVerified, VerifiedAt: now})
	a.writeRegionRecordLocked("other", regionRecord{Name: "other", Country: "US", Status: regionVerified, VerifiedAt: now})

	// A fast node in another region must never be chosen.
	a.applyPatrolLocked("active", map[string]int{"other": 120}, now)

	if stub.saw(http.MethodPut, "/proxies/SmartVPN", `"name":"other"`) {
		t.Fatalf("traffic must not cross regions, calls: %v", stub.calls)
	}
	if !a.healthDomainLocked().Blocked {
		t.Fatal("with no same-region node left the sweep must fail closed")
	}
}

func TestApplyPatrolDoesNotFailOverWithoutALockedRegion(t *testing.T) {
	stub := newControllerStub(t, nil)
	a := stub.app(t)
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	testEditHealth(a, func(p *healthDomainProjection) { p.Params.FailureThreshold = 1 })
	a.writeRegionRecordLocked("active", regionRecord{Name: "active", Country: "JP", Status: regionVerified, VerifiedAt: now})

	// Without a locked region the health is still recorded, but selection keeps
	// belonging to Mihomo's own automatic group.
	a.applyPatrolLocked("active", map[string]int{}, now)

	if a.healthDomainLocked().Health["active"].State != healthUnhealthy {
		t.Fatalf("state = %q, want the failure recorded", a.healthDomainLocked().Health["active"].State)
	}
	if a.healthDomainLocked().Blocked || stub.saw(http.MethodPut, "/proxies/SmartVPN", "REJECT") {
		t.Fatalf("an unlocked region must not block or switch, calls: %v", stub.calls)
	}
}

func TestNodeLatencyRequiresTheKernel(t *testing.T) {
	a := testHealthApp(&app{home: t.TempDir()}, func(p *healthDomainProjection) {
		p.Health = map[string]nodeHealth{}
		p.Regions = map[string]regionRecord{}
	})
	recorder := httptest.NewRecorder()
	a.nodeLatency(recorder, httptest.NewRequest(http.MethodGet, "/api/nodes/latency", nil))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("code = %d, want 409 while the kernel is down", recorder.Code)
	}
}

func TestNodeLatencyReturnsTheGroupMeasurement(t *testing.T) {
	stub := newControllerStub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/group/SmartVPN/delay" {
			t.Errorf("path = %q, want the group delay route", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"node-a":120,"node-b":340}`))
	})
	a := stub.app(t)
	runningKernel(t, a)

	recorder := httptest.NewRecorder()
	a.nodeLatency(recorder, httptest.NewRequest(http.MethodGet, "/api/nodes/latency", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Latency map[string]int `json:"latency"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Latency["node-a"] != 120 || payload.Latency["node-b"] != 340 {
		t.Fatalf("latency = %v, want both nodes", payload.Latency)
	}
}

func TestNodeLatencyReportsAMeasurementFailure(t *testing.T) {
	stub := newControllerStub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusGatewayTimeout)
	})
	a := stub.app(t)
	runningKernel(t, a)

	recorder := httptest.NewRecorder()
	a.nodeLatency(recorder, httptest.NewRequest(http.MethodGet, "/api/nodes/latency", nil))
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("code = %d, want 502 when the kernel refuses to measure", recorder.Code)
	}
}

func TestSelectingANodeWhileDisconnectedMovesTheLock(t *testing.T) {
	now := time.Now()
	a := testHealthApp(&app{home: t.TempDir()}, func(p *healthDomainProjection) {
		p.Health = map[string]nodeHealth{}
		p.Regions = map[string]regionRecord{}
	})
	a.cachedNodes = []proxyNode{{Name: "jp-a"}}
	a.writeRegionRecordLocked("jp-a", regionRecord{Name: "jp-a", Country: "JP", Status: regionVerified, VerifiedAt: now})

	recorder := httptest.NewRecorder()
	a.selectNode(recorder, httptest.NewRequest(http.MethodPut, "/api/nodes/select",
		strings.NewReader(`{"name":"jp-a"}`)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	if a.controlLocked().RuntimeLockedRegion != "JP" {
		t.Fatalf("lockedRegion = %q, want JP: the lock follows the chosen node", a.controlLocked().RuntimeLockedRegion)
	}
	if a.settingsLocked().LockedRegion != "JP" || a.settingsLocked().LockedNode != "jp-a" {
		t.Fatalf("the lock must be persisted: %+v", a.settingsLocked())
	}
	if a.settingsLocked().SelectedNode != "jp-a" || a.settingsLocked().SelectionMode != "manual" {
		t.Fatalf("the pending selection must be saved: %+v", a.settingsLocked())
	}
}

func TestSelectingAnUnknownNodeWhileDisconnectedIsRefused(t *testing.T) {
	a := testHealthApp(&app{home: t.TempDir()}, func(p *healthDomainProjection) {
		p.Health = map[string]nodeHealth{}
		p.Regions = map[string]regionRecord{}
	})
	a.cachedNodes = []proxyNode{{Name: "jp-a"}}

	recorder := httptest.NewRecorder()
	a.selectNode(recorder, httptest.NewRequest(http.MethodPut, "/api/nodes/select",
		strings.NewReader(`{"name":"not-in-the-list"}`)))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("code = %d, want 409 for a node that was never fetched", recorder.Code)
	}
}
