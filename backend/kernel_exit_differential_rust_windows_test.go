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

type exitDiffSpec struct {
	Identity                                            int
	Relay, Patrol, Proxy, History, Path, Blocked, China bool
	ProxyError                                          bool
}
type exitDiffEvent struct {
	Effect                                                                      exitEffect
	Matches, Relay, Patrol, Proxy, Kernel, History, Path, Blocked, China, Owned bool
	Reason                                                                      string
	Config                                                                      bool
	Drops, RelayCloses, HistoryStops                                            int
}
type exitDiffFX struct {
	Base                             *appExitEffects
	Trace                            []exitDiffEvent
	Config                           bool
	Drops, RelayCloses, HistoryStops int
	Spec                             exitDiffSpec
	BeforeLock                       chan struct{}
}

func exitDiffNew(s exitDiffSpec) *exitDiffFX {
	a := testHealthApp(testChinaApp(&app{proxyOn: s.Proxy}, s.China), func(p *healthDomainProjection) { p.Blocked = s.Blocked; p.BlockReason = "oldreason" })
	if s.Relay {
		a.dnsRelay = &adaptiveDNS{}
	}
	if s.Patrol {
		a.patrolCancel = make(chan struct{})
	}
	if s.History {
		a.trafficHistoryCancel = make(chan struct{})
	}
	if s.Path {
		a.pathCancel = make(chan struct{})
	}
	var which kernelRunner
	if s.Identity != 0 {
		which = &migrationKernelStub{}
		a.kernel = which
		if s.Identity == 2 {
			a.kernel = &migrationKernelStub{Starts: 1}
		}
	}
	f := &exitDiffFX{Base: newAppExitEffects(a, which), Config: true, Spec: s}
	f.Base.closeRelay = func(r *adaptiveDNS) {
		if r == nil {
			panic("nil relay close")
		}
		f.RelayCloses++
	}
	f.Base.stopHistory = func() { f.HistoryStops++; a.trafficHistoryCancel = nil }
	f.Base.removeConfig = func() { f.Config = false }
	f.Base.proxyOff = func() error {
		if s.ProxyError {
			return errors.New("synthetic proxy failure")
		}
		return nil
	}
	f.Base.dropTunnel = func() { f.Drops++ }
	f.Base.logMessage = func(string) {}
	return f
}
func (f *exitDiffFX) snapshot(e exitEffect) exitDiffEvent {
	a := f.Base.a
	return exitDiffEvent{Effect: e, Matches: a.kernel == f.Base.which, Relay: a.dnsRelay != nil, Patrol: a.patrolCancel != nil, Proxy: a.proxyOn, Kernel: a.kernel != nil, History: a.trafficHistoryCancel != nil, Path: a.pathCancel != nil, Blocked: a.healthDomainLocked().Blocked, China: a.chinaStateLocked().Active, Owned: f.Base.owned, Reason: a.healthDomainLocked().BlockReason, Config: f.Config, Drops: f.Drops, RelayCloses: f.RelayCloses, HistoryStops: f.HistoryStops}
}
func (f *exitDiffFX) Call(e exitEffect) exitReply {
	if e.Kind == "lock" && f.BeforeLock != nil {
		close(f.BeforeLock)
	}
	r := f.Base.Call(e)
	f.Trace = append(f.Trace, f.snapshot(e))
	return r
}
func (f *exitDiffFX) Release() {
	owned := f.Base.owned
	f.Base.Release()
	if owned {
		f.Trace = append(f.Trace, f.snapshot(exitEffect{Kind: "release"}))
	}
}
func exitDiffCompare(t *testing.T, s exitDiffSpec, repeats int) {
	t.Helper()
	if !rustExitFlowAvailable() {
		t.Fatal("actual exit DLL required")
	}
	g, r := exitDiffNew(s), exitDiffNew(s)
	for i := 0; i < repeats; i++ {
		gr := runGoExitService(exitServiceRequest{}, g)
		before := exitFlowRecoveries.Load()
		rr, handled := runRustExitService(exitServiceRequest{}, r)
		if !handled || exitFlowRecoveries.Load() != before {
			t.Fatalf("unexpected fallback/recovery spec=%+v", s)
		}
		if !reflect.DeepEqual(gr, rr) || !reflect.DeepEqual(g.Trace, r.Trace) || g.snapshot(exitEffect{}) != r.snapshot(exitEffect{}) {
			t.Fatalf("spec=%+v repeat=%d\nGo=%+v\nRust=%+v", s, i, g.Trace, r.Trace)
		}
		if !r.Base.a.mu.TryLock() {
			t.Fatal("callback leaked mutex")
		}
		r.Base.a.mu.Unlock()
	}
	if s.Identity == 2 {
		if r.Drops != 0 || r.RelayCloses != 0 || r.HistoryStops != 0 || !r.Config {
			t.Fatal("stale identity performed cleanup")
		}
	} else {
		want := 1
		if s.Identity == 0 {
			want = repeats
		}
		if r.Drops != want || r.Config || r.Base.a.proxyOn != (s.Proxy && s.ProxyError) || r.Base.a.healthDomainLocked().Blocked {
			t.Fatal("matched callback cleanup wrong")
		}
		if s.Relay && r.RelayCloses != 1 {
			t.Fatal("relay closed more than once")
		}
	}
	if (r.Base.a.pathCancel != nil) != (s.Identity == 2 && s.Path) {
		t.Fatal("incorrect path monitor ownership after exit")
	}
}
func TestMigrationExitActualMatrix(t *testing.T) {
	for id := 0; id < 3; id++ {
		for mask := 0; mask < 128; mask++ {
			t.Run(fmt.Sprintf("%d/%d", id, mask), func(t *testing.T) {
				s := exitDiffSpec{Identity: id, Relay: mask&1 != 0, Patrol: mask&2 != 0, Proxy: mask&4 != 0, History: mask&8 != 0, Path: mask&16 != 0, ProxyError: mask&32 != 0, Blocked: true, China: mask&64 != 0}
				exitDiffCompare(t, s, 2)
			})
		}
	}
}
func TestMigrationExitActualRandom(t *testing.T) {
	for i := 0; i < 1000; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			rng := rand.New(rand.NewSource(int64(160000 + i)))
			s := exitDiffSpec{Identity: rng.Intn(3), Relay: rng.Intn(2) == 0, Patrol: rng.Intn(2) == 0, Proxy: rng.Intn(2) == 0, History: rng.Intn(2) == 0, Path: rng.Intn(2) == 0, ProxyError: rng.Intn(2) == 0, Blocked: rng.Intn(2) == 0, China: rng.Intn(2) == 0}
			exitDiffCompare(t, s, 1)
		})
	}
}

func TestMigrationExitActualLatestIdentityAtLock(t *testing.T) {
	if !rustExitFlowAvailable() {
		t.Fatal("actual DLL required")
	}
	for _, becomesMatch := range []bool{false, true} {
		t.Run(fmt.Sprint(becomesMatch), func(t *testing.T) {
			run := func(rust bool) *exitDiffFX {
				s := exitDiffSpec{Identity: 1, Relay: true, Patrol: true, Proxy: true, History: true, Path: true, Blocked: true, China: true}
				if becomesMatch {
					s.Identity = 2
				}
				f := exitDiffNew(s)
				f.BeforeLock = make(chan struct{})
				a := f.Base.a
				a.mu.Lock()
				done := make(chan bool, 1)
				before := exitFlowRecoveries.Load()
				go func() {
					if rust {
						r, used := runRustExitService(exitServiceRequest{}, f)
						done <- used && r.Err == nil
					} else {
						r := runGoExitService(exitServiceRequest{}, f)
						done <- r.Err == nil
					}
				}()
				select {
				case <-f.BeforeLock:
				case <-time.After(5 * time.Second):
					a.mu.Unlock()
					t.Fatal("callback did not attempt lock")
				}
				if becomesMatch {
					a.kernel = f.Base.which
				} else {
					a.kernel = &migrationKernelStub{Starts: 99}
				}
				a.mu.Unlock()
				select {
				case ok := <-done:
					if !ok {
						t.Fatal("callback fallback/error")
					}
				case <-time.After(5 * time.Second):
					t.Fatal("callback lock leaked")
				}
				if rust && exitFlowRecoveries.Load() != before {
					t.Fatal("unexpected recovery")
				}
				if (f.Drops == 1) != becomesMatch {
					t.Fatal("callback used prelock stale identity")
				}
				return f
			}
			g, r := run(false), run(true)
			if !reflect.DeepEqual(g.Trace, r.Trace) {
				t.Fatalf("latest identity trace mismatch Go=%+v Rust=%+v", g.Trace, r.Trace)
			}
		})
	}
}
