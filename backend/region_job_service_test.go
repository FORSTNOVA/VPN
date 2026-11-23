package main

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func safeRegionJobFX(a *app) *appRegionJobEffects {
	e := newAppRegionJobEffects(a, false)
	e.now = func() time.Time { return time.Unix(100000, 0) }
	e.running = func() bool { return true }
	e.start = func() error { return nil }
	e.stop = func() error { return nil }
	e.list = func() ([]proxyNode, error) { return nil, nil }
	e.delays = func(time.Duration) (map[string]int, error) { return nil, nil }
	e.mihomo = func(string, string, any) (map[string]any, error) { return nil, nil }
	e.query = func(geoSource, time.Duration) (string, string, error) { return "1.1.1.1", "US", nil }
	e.save = func(regionRecord) error { return nil }
	e.spawn = func(bool) {}
	e.logger = func(string) {}
	return e
}
func TestRegionJobServiceStartSnapshot(t *testing.T) {
	a := &app{}
	testEditSettings(a, func(s *settings) { s.SubscriptionURL = "synthetic" })
	fx := safeRegionJobFX(a)
	fx.spawn = func(bool) {
		a.mu.Lock()
		defer a.mu.Unlock()
		testEditHealth(a, func(p *healthDomainProjection) { p.Job.Done = 99 })
		testEditHealth(a, func(p *healthDomainProjection) { p.Job.Running = false })
	}
	r := runGoRegionJobService(regionJobServiceRequest{Op: "start"}, fx)
	if r.Status != 200 || r.Job.Done != 0 || !r.Job.Running || a.healthDomainLocked().Job.Done != 99 {
		t.Fatalf("snapshot %#v live %#v", r, a.healthDomainLocked().Job)
	}
	if !a.mu.TryLock() {
		t.Fatal("lock leaked")
	}
	a.mu.Unlock()
}
func TestRegionJobServiceRunOrder(t *testing.T) {
	a := testHealthApp(&app{}, func(p *healthDomainProjection) {
		p.Regions = map[string]regionRecord{}
		p.Job = &regionJob{Running: true}
	})
	fx := safeRegionJobFX(a)
	var puts []string
	fx.list = func() ([]proxyNode, error) { return []proxyNode{{Name: "a"}, {Name: "b"}}, nil }
	fx.delays = func(d time.Duration) (map[string]int, error) {
		if d != 8*time.Second {
			t.Fatal(d)
		}
		return map[string]int{"a": 1, "b": 2}, errors.New("ignored")
	}
	fx.mihomo = func(method, path string, body any) (map[string]any, error) {
		if method == "GET" {
			return map[string]any{"now": "original"}, nil
		}
		puts = append(puts, body.(map[string]string)["name"])
		return nil, nil
	}
	queries := 0
	fx.query = func(geoSource, time.Duration) (string, string, error) {
		queries++
		if a.mu.TryLock() {
			a.mu.Unlock()
			t.Fatal("query unlocked")
		}
		return "1.1.1.1", "US", nil
	}
	runGoRegionJobService(regionJobServiceRequest{Op: "run"}, fx)
	if !reflect.DeepEqual(puts, []string{"a", "b", "original"}) || queries != 4 || a.healthDomainLocked().Job.Verified != 2 || a.healthDomainLocked().Job.Running {
		t.Fatalf("%v %d %#v", puts, queries, a.healthDomainLocked().Job)
	}
}
func TestRegionJobServiceFailuresAndFinish(t *testing.T) {
	for _, stage := range []string{"start", "list"} {
		t.Run(stage, func(t *testing.T) {
			a := testHealthApp(&app{}, func(p *healthDomainProjection) { p.Job = &regionJob{Running: true} })
			fx := safeRegionJobFX(a)
			fx.running = func() bool { return false }
			stops := 0
			fx.stop = func() error { stops++; return errors.New("ignored stop") }
			boom := errors.New(stage)
			if stage == "start" {
				fx.start = func() error { return boom }
			} else {
				fx.list = func() ([]proxyNode, error) { return nil, boom }
			}
			runGoRegionJobService(regionJobServiceRequest{Op: "run"}, fx)
			want := 0
			if stage == "list" {
				want = 1
			}
			if stops != want || a.healthDomainLocked().Job.LastError != stage || a.healthDomainLocked().Job.Running {
				t.Fatalf("%d %#v", stops, a.healthDomainLocked().Job)
			}
		})
	}
	a := testHealthApp(&app{}, func(p *healthDomainProjection) { p.Job = &regionJob{Running: true, LastError: "preserve"} })
	a.mu.Lock()
	fx := safeRegionJobFX(a)
	fx.borrowed = true
	runGoRegionJobService(regionJobServiceRequest{Op: "finish"}, fx)
	if a.mu.TryLock() {
		a.mu.Unlock()
		t.Fatal("borrowed mutex released")
	}
	a.mu.Unlock()
	if a.healthDomainLocked().Job.LastError != "preserve" {
		t.Fatal(a.healthDomainLocked().Job)
	}
}
func TestRegionJobServiceTTLAndPanic(t *testing.T) {
	at := time.Unix(100000, 0)
	a := testHealthApp(&app{}, func(p *healthDomainProjection) {
		p.Regions = map[string]regionRecord{"fresh": {Status: regionVerified, VerifiedAt: at.Add(-regionTTL + time.Nanosecond)}, "edge": {Status: regionVerified, VerifiedAt: at.Add(-regionTTL)}}
		p.Job = &regionJob{Running: true}
	})
	fx := safeRegionJobFX(a)
	fx.list = func() ([]proxyNode, error) { return []proxyNode{{Name: "fresh"}, {Name: "edge"}}, nil }
	runGoRegionJobService(regionJobServiceRequest{Op: "run"}, fx)
	if a.healthDomainLocked().Job.Skipped != 1 || a.healthDomainLocked().Job.Failed != 1 {
		t.Fatal(a.healthDomainLocked().Job)
	}
	fx = safeRegionJobFX(a)
	fx.running = func() bool { panic("synthetic") }
	testEditHealth(a, func(p *healthDomainProjection) { p.Job.Running = true })
	func() {
		defer func() {
			if r := recover(); r == nil || !strings.Contains(r.(string), "synthetic") {
				t.Fatal(r)
			}
		}()
		runGoRegionJobService(regionJobServiceRequest{Op: "run"}, fx)
	}()
	if !a.mu.TryLock() {
		t.Fatal("panic lock leak")
	}
	a.mu.Unlock()
}
func TestRegionJobServiceZeroSources(t *testing.T) {
	a := testHealthApp(&app{store: &store{}}, func(p *healthDomainProjection) {
		p.Regions = map[string]regionRecord{}
		p.Job = &regionJob{Running: true}
	})
	fx := safeRegionJobFX(a)
	fx.list = func() ([]proxyNode, error) { return []proxyNode{{Name: "synthetic"}}, nil }
	fx.delays = func(time.Duration) (map[string]int, error) { return map[string]int{"synthetic": 1}, nil }
	fx.query = func(geoSource, time.Duration) (string, string, error) { return "", "", errors.New("fake fail") }
	saved := false
	fx.save = func(r regionRecord) error {
		saved = true
		if r.Sources == nil || a.healthDomainLocked().Regions[r.Name].Sources == nil {
			t.Fatal("empty sources became nil")
		}
		return errors.New("ignored persistence")
	}
	runGoRegionJobService(regionJobServiceRequest{Op: "run"}, fx)
	if !saved || a.healthDomainLocked().Job.Failed != 1 {
		t.Fatal(a.healthDomainLocked().Job)
	}
}
