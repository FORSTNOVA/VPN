package main

import (
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

// All cases reject before scheduling any worker or invoking kernel/network ports.
func TestMigrationRegionJobSafeHTTPRejections(t *testing.T) {
	for _, tc := range []struct {
		body                string
		chain, url, running bool
		status              int
	}{
		{`{`, false, false, false, 400}, {`{"refresh":3}`, false, false, false, 400}, {`{"refresh":true,"unknown":1}`, false, false, false, 400}, {``, false, false, false, 400}, {`null`, false, false, false, 400}, {`{}`, true, false, false, 409}, {`{"refresh":true}`, false, true, true, 409},
	} {
		a := &app{}
		a.settings.Chain.Enabled = tc.chain
		if tc.url {
			testEditSettings(a, func(s *settings) { s.SubscriptionURL = "https://synthetic.invalid/sub" })
		}
		if tc.running {
			testEditHealth(a, func(p *healthDomainProjection) { p.Job = &regionJob{Running: true} })
		}
		w := httptest.NewRecorder()
		a.startRegionVerification(w, httptest.NewRequest("POST", "/api/regions/check", strings.NewReader(tc.body)))
		if w.Code != tc.status {
			t.Fatalf("body=%s status=%d", tc.body, w.Code)
		}
		if !a.mu.TryLock() {
			t.Fatal("handler leaked mutex")
		}
		a.mu.Unlock()
	}
}

type selectedRegionJobEvent struct {
	Effect          regionJobEffect
	Job             *regionJob
	Regions         map[string]regionRecord
	Owned, Borrowed bool
}
type selectedRegionJobFX struct {
	base  *appRegionJobEffects
	trace []selectedRegionJobEvent
}

func (f *selectedRegionJobFX) Call(e regionJobEffect) regionJobReply {
	r := f.base.Call(e)
	f.capture(e)
	return r
}
func (f *selectedRegionJobFX) capture(e regionJobEffect) {
	var job *regionJob
	if f.base.a.healthDomainLocked().Job != nil {
		v := *f.base.a.healthDomainLocked().Job
		job = &v
	}
	e.Record.Sources = append([]string(nil), e.Record.Sources...)
	regions := cloneRegionServiceState(&regionServiceState{Regions: f.base.a.healthDomainLocked().Regions}).Regions
	f.trace = append(f.trace, selectedRegionJobEvent{e, job, regions, f.base.owned, f.base.borrowed})
}
func (f *selectedRegionJobFX) Release() {
	owned := f.base.owned
	f.base.Release()
	if owned {
		f.capture(regionJobEffect{Kind: "release"})
	}
}

// This selector test intentionally does not require Rust availability: it must
// exercise successful Go fallback in default, disabled and missing-DLL builds.
func TestMigrationRegionJobSafeSelectedLifecycle(t *testing.T) {
	type outcome struct {
		Results                               []regionJobServiceResult
		Trace                                 []selectedRegionJobEvent
		Job                                   regionJob
		Regions                               map[string]regionRecord
		Puts                                  []string
		Queries, Saves, Starts, Stops, Spawns int
	}
	execute := func(selected bool) outcome {
		a := testHealthApp(&app{store: &store{}}, func(p *healthDomainProjection) { p.Regions = map[string]regionRecord{} })
		testEditSettings(a, func(s *settings) { s.SubscriptionURL = "synthetic" })
		f := &selectedRegionJobFX{base: safeRegionJobFX(a)}
		o := outcome{}
		f.base.running = func() bool { return false }
		f.base.start = func() error { o.Starts++; return nil }
		f.base.stop = func() error { o.Stops++; return nil }
		f.base.list = func() ([]proxyNode, error) { return []proxyNode{{Name: "a"}, {Name: "b"}}, nil }
		f.base.delays = func(time.Duration) (map[string]int, error) { return map[string]int{"a": 1, "b": 2}, nil }
		f.base.mihomo = func(method, path string, body any) (map[string]any, error) {
			if method == "GET" {
				return map[string]any{"now": "original"}, nil
			}
			o.Puts = append(o.Puts, body.(map[string]string)["name"])
			return nil, nil
		}
		f.base.query = func(geoSource, time.Duration) (string, string, error) { o.Queries++; return "192.0.2.1", "US", nil }
		f.base.save = func(regionRecord) error { o.Saves++; return nil }
		f.base.spawn = func(bool) {
			o.Spawns++
			if !a.mu.TryLock() {
				t.Fatal("selected spawn remained locked")
			}
			testEditHealth(a, func(p *healthDomainProjection) { p.Job.LastError = "keep" })
			a.mu.Unlock()
		}
		run := func(q regionJobServiceRequest) regionJobServiceResult {
			if selected {
				return runSelectedRegionJobService(q, f)
			}
			return runGoRegionJobService(q, f)
		}
		o.Results = append(o.Results, run(regionJobServiceRequest{Op: "start", Refresh: true}))
		if o.Results[0].Status != 200 || o.Results[0].Job == nil || o.Results[0].Job.LastError != "" {
			t.Fatal("start response not detached")
		}
		o.Results = append(o.Results, run(regionJobServiceRequest{Op: "run", Refresh: true}))
		a.mu.Lock()
		f.base.borrowed = true
		o.Results = append(o.Results, run(regionJobServiceRequest{Op: "finish"}))
		if a.mu.TryLock() {
			a.mu.Unlock()
			t.Fatal("selected finish released caller mutex")
		}
		a.mu.Unlock()
		f.base.borrowed = false
		if !a.mu.TryLock() {
			t.Fatal("selector lifecycle leaked lock")
		}
		a.mu.Unlock()
		o.Trace = f.trace
		o.Job = *a.healthDomainLocked().Job
		o.Regions = cloneRegionServiceState(&regionServiceState{Regions: a.healthDomainLocked().Regions}).Regions
		if o.Job.Running || o.Job.Done != 2 || o.Job.Verified != 2 || o.Job.LastError != "keep" || o.Queries != 4 || o.Saves != 2 || o.Starts != 1 || o.Stops != 1 || o.Spawns != 1 || !reflect.DeepEqual(o.Puts, []string{"a", "b", "original"}) {
			t.Fatalf("successful selector lifecycle incomplete: %+v", o)
		}
		return o
	}
	want, got := execute(false), execute(true)
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("selector differs from Go oracle\nGo=%+v\nSelected=%+v", want, got)
	}
}
