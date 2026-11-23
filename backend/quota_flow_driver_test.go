package main

import (
	"errors"
	"runtime"
	"testing"
	"time"
)

func TestQuotaFlowPanicReleasesAndStops(t *testing.T) {
	for _, where := range []string{"proxies", "fetch", "save"} {
		a := quotaTestApp()
		fx := quotaTestEffects(a)
		calls := 0
		fx.fetch = func(string, string) (*subscriptionUserInfo, error) {
			calls++
			if where == "fetch" {
				panic("synthetic")
			}
			return &subscriptionUserInfo{Total: 99}, nil
		}
		if where == "proxies" {
			fx.proxies = func() []string { panic("synthetic") }
		}
		if where == "save" {
			fx.save = func() error { panic("synthetic") }
		}
		fs := &registryFakeSession{frames: [][]byte{{1}}, failStep: 1}
		r, used := runRustQuotaServiceWithStart(quotaServiceRequest{Op: "refresh", ID: "id"}, fx, func([]byte) (quotaFlowSession, error) { return fs, nil })
		if !used || r.Status != 500 || calls > 1 || !fs.closed {
			t.Fatal(where, r, calls)
		}
		quotaAssertUnlocked(t, a)
	}
}
func TestQuotaFlowCancellationAndCapacityBeforeLock(t *testing.T) {
	before := runtime.NumGoroutine()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 1200; i++ {
			a := quotaTestApp()
			fx := quotaTestEffects(a)
			fs := &registryFakeSession{frames: [][]byte{{255}}}
			_, used := runRustQuotaServiceWithStart(quotaServiceRequest{Op: "refresh", ID: "id"}, fx, func([]byte) (quotaFlowSession, error) { return fs, nil })
			if used || fx.owned || !fs.closed {
				panic("unexpected lock")
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("quota mirror cancellation deadlocked")
	}
	runtime.GC()
	if n := runtime.NumGoroutine(); n > before+4 {
		t.Fatal("quota mirror workers leaked", before, n)
	}
	a := quotaTestApp()
	fx := quotaTestEffects(a)
	_, used := runRustQuotaServiceWithStart(quotaServiceRequest{Op: "refresh", ID: "id"}, fx, func([]byte) (quotaFlowSession, error) { return nil, errors.New("capacity") })
	if used || fx.owned {
		t.Fatal("capacity acquired lock")
	}
	quotaAssertUnlocked(t, a)
}

type quotaPanicAfterPort struct {
	*appQuotaEffects
	target string
}

func (f *quotaPanicAfterPort) Call(q quotaEffect) quotaReply {
	r := f.appQuotaEffects.Call(q)
	if q.Kind == f.target {
		panic("synthetic after-port panic")
	}
	return r
}
func TestQuotaFlowOwnershipPanicAfterLockUnlockCommit(t *testing.T) {
	for _, kind := range []string{"lock", "unlock", "commit"} {
		a := quotaTestApp()
		base := quotaTestEffects(a)
		gets, saves := 0, 0
		base.fetch = func(string, string) (*subscriptionUserInfo, error) {
			gets++
			return &subscriptionUserInfo{Total: 99}, nil
		}
		base.save = func() error { saves++; return nil }
		fx := &quotaPanicAfterPort{appQuotaEffects: base, target: kind}
		fs := &registryFakeSession{frames: [][]byte{{1}}, failStep: 1}
		r, used := runRustQuotaServiceWithStart(quotaServiceRequest{Op: "refresh", ID: "id"}, fx, func([]byte) (quotaFlowSession, error) { return fs, nil })
		if !used || r.Status != 500 || saves != 0 || gets > 1 {
			t.Fatal(kind, r, gets, saves)
		}
		if kind == "commit" && testCatalogRead(a)[0].UserInfo.Total != 99 {
			t.Fatal("commit rolled back")
		}
		quotaAssertUnlocked(t, a)
	}
}
