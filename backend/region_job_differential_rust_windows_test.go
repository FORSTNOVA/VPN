//go:build windows && amd64 && rustcore

package main

import (
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"time"
)

type jobDiffAnswer struct {
	IP, Country string
	Err         error
}
type jobDiffSpec struct {
	Chain, Subscription, Running, Store bool
	Job                                 *regionJob
	Regions                             map[string]regionRecord
	Nodes                               []proxyNode
	Delays                              map[string]int
	Fail                                map[string]error
	Answers                             []jobDiffAnswer
	Active, Nested                      any
	Edit                                string
	EditAt                              int
	SpawnEdit                           bool
	Now                                 time.Time
}
type jobDiffEvent struct {
	Effect                   regionJobEffect
	State                    *regionJobServiceState
	Job                      *regionJob
	Regions                  map[string]regionRecord
	Owned, Borrowed, Running bool
	Active                   any
}
type jobDiffFX struct {
	Base       *appRegionJobEffects
	Trace      []jobDiffEvent
	Calls      map[string]int
	Running    bool
	Active     any
	Spec       jobDiffSpec
	SpawnCount int
}

func jobDiffBase() jobDiffSpec {
	now := time.Date(2042, 3, 4, 5, 6, 7, 123456789, time.FixedZone("east", 8*3600))
	return jobDiffSpec{Subscription: true, Running: true, Store: true, Job: &regionJob{Running: true, LastError: "keep-old", StartedAt: "oldstart"}, Regions: map[string]regionRecord{}, Nodes: []proxyNode{{Name: "a"}, {Name: "b"}, {Name: "c"}}, Delays: map[string]int{"a": 20, "b": 30, "c": 40}, Fail: map[string]error{}, Answers: []jobDiffAnswer{{"192.0.2.1", "US", nil}, {"192.0.2.2", "US", nil}, {"192.0.2.3", "JP", nil}}, Active: autoGroup, Nested: "original", Now: now, EditAt: 1}
}
func jobDiffNew(s jobDiffSpec, borrowed bool) *jobDiffFX {
	rs := cloneRegionServiceState(&regionServiceState{Regions: s.Regions})
	a := testHealthApp(&app{}, func(p *healthDomainProjection) { p.Regions = rs.Regions })
	if s.Job != nil {
		v := *s.Job
		testEditHealth(a, func(p *healthDomainProjection) { p.Job = &v })
	}
	a.settings.Chain.Enabled = s.Chain
	if s.Subscription {
		testEditSettings(a, func(s *settings) { s.SubscriptionURL = "https://synthetic.invalid/sub" })
	}
	if s.Store {
		a.store = &store{}
	}
	if borrowed {
		a.mu.Lock()
	}
	f := &jobDiffFX{Base: newAppRegionJobEffects(a, borrowed), Calls: map[string]int{}, Running: s.Running, Active: s.Active, Spec: s}
	f.Base.now = func() time.Time { return s.Now }
	f.Base.running = func() bool { return f.Running }
	f.Base.start = func() error {
		if s.Fail["start"] == nil {
			f.Running = true
		}
		return s.Fail["start"]
	}
	f.Base.stop = func() error { f.Running = false; return s.Fail["stop"] }
	f.Base.list = func() ([]proxyNode, error) { return append([]proxyNode(nil), s.Nodes...), s.Fail["list"] }
	f.Base.delays = func(timeout time.Duration) (map[string]int, error) {
		if timeout != 8*time.Second {
			panic("delay duration changed")
		}
		m := map[string]int{}
		for k, v := range s.Delays {
			m[k] = v
		}
		return m, s.Fail["delays"]
	}
	f.Base.mihomo = func(method, path string, body any) (map[string]any, error) {
		if method == "PUT" {
			name := body.(map[string]string)["name"]
			if err := s.Fail["put:"+name]; err != nil {
				return nil, err
			}
			if err := s.Fail["put"]; err != nil {
				return nil, err
			}
			f.Active = name
			return nil, nil
		}
		if s.Fail["get"] != nil {
			return nil, s.Fail["get"]
		}
		v := f.Active
		if path != "/proxies/SmartVPN" {
			v = s.Nested
		}
		return map[string]any{"now": v}, nil
	}
	f.Base.query = func(src geoSource, timeout time.Duration) (string, string, error) {
		if timeout != 8*time.Second {
			panic("geo duration changed")
		}
		index := -1
		for i, v := range geoSources {
			if v.name == src.name {
				index = i
			}
		}
		if index < 0 || index >= len(s.Answers) {
			return "", "", errors.New("synthetic no answer")
		}
		r := s.Answers[index]
		return r.IP, r.Country, r.Err
	}
	f.Base.save = func(regionRecord) error { return s.Fail["store"] }
	f.Base.logger = func(string) {}
	f.Base.spawn = func(bool) {
		f.SpawnCount++
		if a.mu.TryLock() {
			if s.SpawnEdit && a.healthDomainLocked().Job != nil {
				testEditHealth(a, func(p *healthDomainProjection) { p.Job.Running = false })
				testEditHealth(a, func(p *healthDomainProjection) { p.Job.Done = 99 })
				testEditHealth(a, func(p *healthDomainProjection) { p.Job.LastError = "spawn changed" })
			}
			a.mu.Unlock()
		} else {
			panic("spawn held app lock")
		}
	}
	return f
}
func (f *jobDiffFX) record(e regionJobEffect) {
	e.Record.Sources = append([]string(nil), e.Record.Sources...)
	a := f.Base.a
	var job *regionJob
	if a.healthDomainLocked().Job != nil {
		v := *a.healthDomainLocked().Job
		job = &v
	}
	regions := cloneRegionServiceState(&regionServiceState{Regions: a.healthDomainLocked().Regions}).Regions
	f.Trace = append(f.Trace, jobDiffEvent{e, regionJobStateFromApp(a), job, regions, f.Base.owned, f.Base.borrowed, f.Running, f.Active})
}
func (f *jobDiffFX) Call(e regionJobEffect) regionJobReply {
	f.Calls[e.Kind]++
	r := f.Base.Call(e)
	f.record(e)
	if e.Kind == "unlock" && f.Spec.Edit != "" && f.Calls["unlock"] == f.Spec.EditAt {
		a := f.Base.a
		a.mu.Lock()
		switch f.Spec.Edit {
		case "stop":
			if a.healthDomainLocked().Job != nil {
				testEditHealth(a, func(p *healthDomainProjection) { p.Job.Running = false })
			}
		case "remove":
			testEditHealth(a, func(p *healthDomainProjection) { p.Job = nil })
		case "replace":
			testEditHealth(a, func(p *healthDomainProjection) {
				p.Job = &regionJob{Running: true, Total: 999, Done: 500, LastError: "replacement"}
			})
		case "settings":
			a.settings.Chain.Enabled = true
			testEditSettings(a, func(s *settings) { s.SubscriptionURL = "" })
		}
		a.mu.Unlock()
	}
	return r
}
func (f *jobDiffFX) Release() {
	owned := f.Base.owned
	f.Base.Release()
	if owned {
		f.record(regionJobEffect{Kind: "release"})
	}
}
func jobDiffCompare(t *testing.T, q regionJobServiceRequest, s jobDiffSpec) (*jobDiffFX, regionJobServiceResult) {
	t.Helper()
	if !rustRegionJobFlowAvailable() {
		t.Fatal("actual job DLL required")
	}
	borrowed := q.Op == "finish"
	g, r := jobDiffNew(s, borrowed), jobDiffNew(s, borrowed)
	if borrowed {
		defer g.Base.a.mu.Unlock()
		defer r.Base.a.mu.Unlock()
	}
	gr := runGoRegionJobService(q, g)
	before := regionJobFlowRecoveries.Load()
	rr, used := runRustRegionJobService(q, r)
	if !used {
		t.Fatal("unexpected prestart fallback")
	}
	if regionJobFlowRecoveries.Load() != before {
		t.Fatalf("unexpected recovery req=%+v trace=%+v", q, r.Trace)
	}
	if !reflect.DeepEqual(gr, rr) || !reflect.DeepEqual(g.Trace, r.Trace) || g.Running != r.Running || g.Active != r.Active || g.SpawnCount != r.SpawnCount {
		t.Fatalf("req=%+v\nGo=%+v\nRust=%+v\nGoTrace=%+v\nRustTrace=%+v", q, gr, rr, g.Trace, r.Trace)
	}
	if locked := r.Base.a.mu.TryLock(); locked {
		r.Base.a.mu.Unlock()
		if borrowed {
			t.Fatal("finish unlocked borrowed mutex")
		}
	} else if !borrowed {
		t.Fatal("owned mutex leaked")
	}
	if q.Op == "start" && rr.Status == 200 {
		if rr.Job == nil || !rr.Job.Running || rr.Job.Done != 0 {
			t.Fatal("response was captured after scheduling")
		}
		rr.Job.Total = -1
		if r.Base.a.healthDomainLocked().Job != nil && r.Base.a.healthDomainLocked().Job.Total == -1 {
			t.Fatal("response aliases live job")
		}
	}
	return r, rr
}
func TestMigrationRegionJobActualMatrix(t *testing.T) {
	for _, op := range []string{"start", "run", "finish"} {
		for mask := 0; mask < 64; mask++ {
			t.Run(fmt.Sprintf("%s/%d", op, mask), func(t *testing.T) {
				s := jobDiffBase()
				s.Chain = mask&1 != 0
				s.Subscription = mask&2 != 0
				s.Running = mask&4 != 0
				s.Store = mask&8 != 0
				s.SpawnEdit = true
				if mask&16 != 0 {
					s.Job = nil
				} else {
					s.Job.Running = mask&32 != 0
				}
				jobDiffCompare(t, regionJobServiceRequest{Op: op, Refresh: mask&32 != 0}, s)
			})
		}
	}
}
func TestMigrationRegionJobActualRandom(t *testing.T) {
	for i := 0; i < 1500; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			rng := rand.New(rand.NewSource(int64(180000 + i)))
			s := jobDiffBase()
			ops := []string{"start", "run", "finish"}
			op := ops[rng.Intn(3)]
			s.Chain = rng.Intn(6) == 0
			s.Subscription = rng.Intn(4) != 0
			s.Running = rng.Intn(2) == 0
			s.Store = rng.Intn(2) == 0
			s.SpawnEdit = true
			if rng.Intn(6) == 0 {
				s.Job = nil
			} else {
				s.Job.Running = rng.Intn(5) != 0
			}
			if rng.Intn(2) == 0 {
				faults := []string{"start", "stop", "list", "delays", "get", "put:a", "put:original", "store"}
				s.Fail[faults[rng.Intn(len(faults))]] = errors.New("synthetic failure")
			}
			if rng.Intn(3) == 0 {
				s.Edit = []string{"stop", "remove", "replace", "settings"}[rng.Intn(4)]
				s.EditAt = 1 + rng.Intn(3)
			}
			s.Active = []any{autoGroup, "original", "REJECT", "", nil, 42}[rng.Intn(6)]
			for j := range s.Answers {
				if rng.Intn(3) == 0 {
					s.Answers[j].Err = errors.New("source unavailable")
				}
				s.Answers[j].Country = []string{"US", "JP"}[rng.Intn(2)]
			}
			for _, n := range s.Nodes {
				if rng.Intn(3) == 0 {
					s.Delays[n.Name] = 0
				}
				if rng.Intn(3) == 0 {
					s.Regions[n.Name] = regionRecord{Name: n.Name, Status: regionVerified, Country: "US", VerifiedAt: s.Now.Add(-regionTTL + time.Duration(rng.Intn(3)-1)*time.Nanosecond)}
				}
			}
			var err error
			if rng.Intn(3) == 0 {
				err = errors.New("finish injected")
			}
			jobDiffCompare(t, regionJobServiceRequest{Op: op, Refresh: rng.Intn(2) == 0, Err: err}, s)
		})
	}
}

func TestMigrationRegionJobActualProbeGolden(t *testing.T) {
	for mask := 0; mask < 8; mask++ {
		for _, conflict := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/%t", mask, conflict), func(t *testing.T) {
				s := jobDiffBase()
				s.Nodes = s.Nodes[:1]
				s.Active = "original"
				s.Running = false
				for i := range s.Answers {
					if mask&(1<<i) != 0 {
						s.Answers[i].Err = errors.New("source fail")
					}
					if !conflict {
						s.Answers[i].Country = "US"
					} else {
						s.Answers[i].Country = []string{"US", "JP", "US"}[i]
					}
				}
				f, _ := jobDiffCompare(t, regionJobServiceRequest{Op: "run", Refresh: true}, s)
				record := f.Base.a.healthDomainLocked().Regions["a"]
				success := []int{}
				queries := 0
				for i := 0; i < 3; i++ {
					queries++
					if mask&(1<<i) == 0 {
						success = append(success, i)
						if len(success) == 2 {
							break
						}
					}
				}
				status := regionUnreachable
				if len(success) == 1 {
					status = regionSingle
				}
				if len(success) == 2 {
					status = regionVerified
					if s.Answers[success[0]].Country != s.Answers[success[1]].Country {
						status = regionConflict
					}
				}
				if record.Status != status || f.Calls["query"] != queries {
					t.Fatalf("probe combination status=%s queries=%d want=%s/%d", record.Status, f.Calls["query"], status, queries)
				}
				if len(success) > 0 {
					if record.ExitIP != s.Answers[success[0]].IP || record.Country != s.Answers[success[0]].Country {
						t.Fatal("first successful answer not preserved")
					}
					for j, i := range success {
						if record.Sources[j] != geoSources[i].name {
							t.Fatal("source ordering changed")
						}
					}
				}
				if f.Calls["start"] != 1 || f.Calls["stop"] != 1 || f.Base.a.healthDomainLocked().Job.Done != 1 || f.Base.a.healthDomainLocked().Job.LastError != "keep-old" {
					t.Fatal("temporary/finish counters changed")
				}
				if status == regionVerified && f.Base.a.healthDomainLocked().Job.Verified != 1 {
					t.Fatal("verified count")
				}
				if status != regionVerified && f.Base.a.healthDomainLocked().Job.Failed != 1 {
					t.Fatal("failed count")
				}
			})
		}
	}
}
func TestMigrationRegionJobActualLifecycleGolden(t *testing.T) {
	t.Run("restore-only-final", func(t *testing.T) {
		s := jobDiffBase()
		s.Active = "original"
		f, _ := jobDiffCompare(t, regionJobServiceRequest{Op: "run", Refresh: true}, s)
		var puts []string
		for _, e := range f.Trace {
			if e.Effect.Kind == "put" {
				puts = append(puts, e.Effect.Name)
			}
		}
		if !reflect.DeepEqual(puts, []string{"a", "b", "c", "original"}) {
			t.Fatal("restore not final-only", puts)
		}
		if f.Calls["running"] != 1 || f.Calls["start"] != 0 || f.Calls["stop"] != 0 {
			t.Fatal("initial temporary decision changed")
		}
	})
	for _, edit := range []string{"stop", "remove", "replace", "settings"} {
		t.Run("interleave/"+edit, func(t *testing.T) {
			s := jobDiffBase()
			s.Running = false
			s.Edit = edit
			s.EditAt = 1
			f, _ := jobDiffCompare(t, regionJobServiceRequest{Op: "run", Refresh: true}, s)
			if f.Calls["start"] != 1 || f.Calls["stop"] != 1 {
				t.Fatal("temporary flag recomputed")
			}
			switch edit {
			case "stop", "remove":
				if f.Calls["query"] != 0 {
					t.Fatal("stopped job still probed")
				}
			case "replace":
				if f.Base.a.healthDomainLocked().Job.Done != 503 || f.Base.a.healthDomainLocked().Job.Total != 999 || f.Base.a.healthDomainLocked().Job.LastError != "replacement" {
					t.Fatal("unexpected generation guard or stale job overwrite")
				}
			case "settings":
				if f.Base.a.healthDomainLocked().Job.Done != 3 {
					t.Fatal("later settings edit incorrectly aborted job")
				}
			}
		})
	}
	for _, delta := range []time.Duration{-time.Nanosecond, 0, time.Nanosecond} {
		t.Run(fmt.Sprintf("fresh/%d", delta), func(t *testing.T) {
			s := jobDiffBase()
			s.Nodes = s.Nodes[:1]
			s.Regions["a"] = regionRecord{Name: "a", Status: regionVerified, VerifiedAt: s.Now.Add(-regionTTL - delta)}
			f, _ := jobDiffCompare(t, regionJobServiceRequest{Op: "run"}, s)
			want := 0
			if delta < 0 {
				want = 1
			}
			if f.Base.a.healthDomainLocked().Job.Skipped != want {
				t.Fatal("fresh TTL boundary changed")
			}
		})
	}
	for _, fault := range []string{"start", "list", "delays", "put:a", "store", "put:original", "stop"} {
		t.Run("failure/"+fault, func(t *testing.T) {
			s := jobDiffBase()
			s.Running = false
			s.Active = "original"
			s.Fail[fault] = errors.New("primary " + fault)
			f, _ := jobDiffCompare(t, regionJobServiceRequest{Op: "run", Refresh: true}, s)
			if fault == "start" && f.Calls["stop"] != 0 {
				t.Fatal("start failure incorrectly stops")
			}
			if fault == "store" && len(f.Base.a.healthDomainLocked().Regions) != 3 {
				t.Fatal("save error rolled back memory")
			}
			if (fault == "delays" || fault == "store" || fault == "stop") && f.Base.a.healthDomainLocked().Job.LastError != "keep-old" {
				t.Fatal("ignored error unexpectedly replaced job error")
			}
		})
	}
}

func TestMigrationRegionJobActualStartWorkerComposition(t *testing.T) {
	if !rustRegionJobFlowAvailable() {
		t.Fatal("actual DLL required")
	}
	for _, scenario := range []string{"existing-kernel", "temporary", "list-failure"} {
		t.Run(scenario, func(t *testing.T) {
			s := jobDiffBase()
			s.Job = nil
			s.Running = scenario == "existing-kernel"
			if scenario == "list-failure" {
				s.Fail["list"] = errors.New("synthetic list")
			}
			makeFX := func(rust bool) *jobDiffFX {
				f := jobDiffNew(s, false)
				f.Base.spawn = func(refresh bool) {
					f.SpawnCount++
					if !f.Base.a.mu.TryLock() {
						t.Fatal("worker scheduled under caller lock")
					}
					f.Base.a.mu.Unlock()
					if rust {
						_, handled := runRustRegionJobService(regionJobServiceRequest{Op: "run", Refresh: refresh}, f)
						if !handled {
							t.Fatal("nested worker fell back")
						}
					} else {
						runGoRegionJobService(regionJobServiceRequest{Op: "run", Refresh: refresh}, f)
					}
				}
				return f
			}
			g, r := makeFX(false), makeFX(true)
			gr := runGoRegionJobService(regionJobServiceRequest{Op: "start", Refresh: true}, g)
			before := regionJobFlowRecoveries.Load()
			rr, used := runRustRegionJobService(regionJobServiceRequest{Op: "start", Refresh: true}, r)
			if !used || regionJobFlowRecoveries.Load() != before {
				t.Fatal("nested start/worker recovery")
			}
			if !reflect.DeepEqual(gr, rr) || !reflect.DeepEqual(g.Trace, r.Trace) {
				t.Fatalf("composition mismatch Go=%+v Rust=%+v", gr, rr)
			}
			if rr.Job == nil || !rr.Job.Running || rr.Job.Done != 0 || r.Base.a.healthDomainLocked().Job.Running || r.SpawnCount != 1 {
				t.Fatal("start response aliases completed immediate worker")
			}
			if scenario != "list-failure" && r.Base.a.healthDomainLocked().Job.Done != 3 {
				t.Fatal("worker did not complete")
			}
		})
	}
}
