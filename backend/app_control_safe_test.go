package main

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type appControlWriter struct {
	*httptest.ResponseRecorder
	a      *app
	t      *testing.T
	crash  bool
	writes int
}

func (w *appControlWriter) Write(b []byte) (int, error) {
	w.writes++
	if w.a.mu.TryLock() {
		w.a.mu.Unlock()
		w.t.Fatal("response outside actual app mutex")
	}
	if w.crash {
		panic("synthetic writer")
	}
	return w.ResponseRecorder.Write(b)
}
func (w *appControlWriter) WriteHeader(n int) {
	if w.a.mu.TryLock() {
		w.a.mu.Unlock()
		w.t.Fatal("status outside actual app mutex")
	}
	w.ResponseRecorder.WriteHeader(n)
}
func appSafeEffects(t *testing.T, op string) (*app, *appControlEffects) {
	t.Helper()
	a := settingsDocumentTestApp(t, t.TempDir())
	return a, newAppControlEffects(a, appServiceRequest{Op: op})
}
func assertAppMutexReleased(t *testing.T, a *app) {
	t.Helper()
	if !a.mu.TryLock() {
		t.Fatal("app mutex leaked")
	}
	a.mu.Unlock()
}
func TestAppServiceSafeParamsCommitAndRestart(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "savefailure"}[fail], func(t *testing.T) {
			a, e := appSafeEffects(t, "params")
			old := make(chan struct{})
			a.patrolCancel = old
			spawn, path := 0, 0
			e.spawn = func(c <-chan struct{}, d time.Duration) {
				spawn++
				if c != a.patrolCancel || d != 10*time.Second {
					t.Fatal("wrong published channel/interval")
				}
			}
			e.hooks = map[string]func(appServiceEffect) appServiceReply{"store-present": func(appServiceEffect) appServiceReply { return appServiceReply{Flag: true} }, "save-params": func(q appServiceEffect) appServiceReply {
				if a.healthMetaLocked().Params != q.Params {
					t.Fatal("save before authority commit")
				}
				if fail {
					return appServiceReply{Err: errors.New("save")}
				}
				return appServiceReply{}
			}, "path-start": func(appServiceEffect) appServiceReply { path++; return appServiceReply{} }}
			e.w = &appControlWriter{ResponseRecorder: httptest.NewRecorder(), a: a, t: t}
			e.q.Params = healthParams{PatrolIntervalSec: 0}
			r := runAppControlWithEffects(e.q, e)
			if r.Err != nil {
				t.Fatal(r.Err)
			}
			if a.healthMetaLocked().Params.PatrolIntervalSec != 10 {
				t.Fatal("zero did not normalize")
			}
			if fail {
				if spawn != 0 || path != 0 || a.patrolCancel != old {
					t.Fatal("save failure restarted")
				}
				select {
				case <-old:
					t.Fatal("old closed")
				default:
				}
			} else {
				if spawn != 1 || path != 1 || a.patrolCancel == old {
					t.Fatal("restart missing")
				}
				select {
				case <-old:
				default:
					t.Fatal("old not closed")
				}
				close(a.patrolCancel)
			}
			assertAppMutexReleased(t, a)
		})
	}
}
func TestAppServiceSafeStopClearsAfterSwitchFailure(t *testing.T) {
	a, e := appSafeEffects(t, "stop")
	a.setHealthBlockedLocked(true)
	a.setHealthReasonLocked("old")
	c := make(chan struct{})
	a.patrolCancel = c
	calls := 0
	e.hooks = map[string]func(appServiceEffect) appServiceReply{"running": func(appServiceEffect) appServiceReply { return appServiceReply{Flag: true} }, "switch": func(appServiceEffect) appServiceReply { calls++; return appServiceReply{Err: errors.New("synthetic")} }, "log": func(appServiceEffect) appServiceReply { return appServiceReply{} }}
	a.mu.Lock()
	r := runAppControlWithEffects(e.q, e)
	if a.mu.TryLock() {
		a.mu.Unlock()
		t.Fatal("borrowed lock released")
	}
	a.mu.Unlock()
	if r.Err != nil || calls != 1 || a.healthMetaLocked().Blocked || a.healthMetaLocked().BlockReason != "" || a.patrolCancel != nil {
		t.Fatal("stop did not clear")
	}
	select {
	case <-c:
	default:
		t.Fatal("cancel not closed")
	}
}
func TestAppServiceSafeRealStateWriterOwnership(t *testing.T) {
	for _, which := range []string{"ok", "authority", "writer"} {
		t.Run(which, func(t *testing.T) {
			a, e := appSafeEffects(t, "state")
			e.hooks = map[string]func(appServiceEffect) appServiceReply{"running": func(appServiceEffect) appServiceReply { return appServiceReply{} }, "tun-reason": func(appServiceEffect) appServiceReply { return appServiceReply{Text: "synthetic"} }, "resolved": func(appServiceEffect) appServiceReply { e.resolved = "synthetic"; return appServiceReply{} }}
			if which == "authority" {
				e.hooks["state-base"] = func(appServiceEffect) appServiceReply { panic("unavailable") }
			}
			w := &appControlWriter{ResponseRecorder: httptest.NewRecorder(), a: a, t: t, crash: which == "writer"}
			e.w = w
			var p any
			func() { defer func() { p = recover() }(); runAppControlWithEffects(e.q, e) }()
			if (p != nil) != (which == "writer") {
				t.Fatalf("panic %v", p)
			}
			if which == "authority" && w.Code != 500 {
				t.Fatal("fault not 500")
			}
			if which == "writer" && w.writes != 1 {
				t.Fatal("writer repeated")
			}
			assertAppMutexReleased(t, a)
			e.Release()
		})
	}
}
func TestAppServiceSafeLoopTickerAndOldIdentity(t *testing.T) {
	a, e := appSafeEffects(t, "loop")
	old := make(chan struct{})
	a.patrolCancel = make(chan struct{})
	e.cancel = old
	e.q.Interval = 21 * time.Second
	ch := make(chan time.Time, 2)
	ch <- time.Time{}
	ch <- time.Time{}
	calls, stops := 0, 0
	e.newTicker = func(d time.Duration) (<-chan time.Time, func()) {
		if d != 21*time.Second || calls != 0 {
			t.Fatal("invalid ticker or immediate sweep")
		}
		return ch, func() { stops++ }
	}
	e.once = func() {
		calls++
		if calls == 2 {
			close(old)
		}
	}
	if r := runAppControlWithEffects(e.q, e); r.Err != nil {
		t.Fatal(r.Err)
	}
	e.Release()
	if calls != 2 || stops != 1 {
		t.Fatalf("calls=%d stops=%d", calls, stops)
	}
	select {
	case <-a.patrolCancel:
		t.Fatal("old loop closed new cancel")
	default:
	}
	close(a.patrolCancel)
}
func TestAppServiceSafeStartExistingAndCreateFault(t *testing.T) {
	a, e := appSafeEffects(t, "start")
	c := make(chan struct{})
	a.patrolCancel = c
	e.hooks = map[string]func(appServiceEffect) appServiceReply{"meta": func(appServiceEffect) appServiceReply { panic("unavailable") }}
	if r := runAppControlWithEffects(e.q, e); r.Err != nil {
		t.Fatal("existing loop queried authority")
	}
	if a.patrolCancel != c {
		t.Fatal("channel replaced")
	}
	a.patrolCancel = nil
	if r := runAppControlWithEffects(e.q, e); r.Err == nil {
		t.Fatal("authority fault ignored")
	}
	if a.patrolCancel != nil {
		t.Fatal("ghost channel")
	}
	close(c)
}
func TestAppServiceSafeEventsProjection(t *testing.T) {
	for _, limit := range []string{"", " 1", "0", "501", "+2"} {
		t.Run(limit, func(t *testing.T) {
			_, e := appSafeEffects(t, "events")
			e.q.Limit = limit
			w := httptest.NewRecorder()
			e.w = w
			want := 50
			if limit == "+2" {
				want = 2
			}
			at := time.Date(2025, 1, 2, 3, 4, 5, 0, time.FixedZone("test", 9*3600))
			e.hooks = map[string]func(appServiceEffect) appServiceReply{"store-present": func(appServiceEffect) appServiceReply { return appServiceReply{Flag: true} }, "events-read": func(q appServiceEffect) appServiceReply {
				if q.Count != want {
					t.Fatal(q.Count)
				}
				e.events = []switchEvent{{At: at, Group: "g", Evidence: "e"}}
				e.eventPayload = make([]appEventPayload, 0, 1)
				return appServiceReply{Count: 1}
			}}
			if r := runAppControlWithEffects(e.q, e); r.Err != nil {
				t.Fatal(r.Err)
			}
			if !strings.Contains(w.Body.String(), "2025-01-02T03:04:05+09:00") {
				t.Fatal(w.Body.String())
			}
		})
	}
}
func TestAppServiceSafeSnapshotPureCounts(t *testing.T) {
	a, e := appSafeEffects(t, "snapshot")
	a.patchControlLocked(controlRuntimeLockedRegion, serviceControlState{RuntimeLockedRegion: "JP"})
	now := time.Unix(100, 0)
	e.clock = func() time.Time { return now }
	e.haveHealth = true
	e.h = healthDomainProjection{Health: map[string]nodeHealth{"a": {State: "unknown"}, "b": {State: healthHealthy, CooldownUntil: now.Add(time.Second)}}, Regions: map[string]regionRecord{"a": {Status: regionVerified, Country: "JP", VerifiedAt: now.Add(-regionTTL)}, "b": {Status: regionVerified, Country: "JP", VerifiedAt: now}}}
	r := runAppControlWithEffects(e.q, e)
	if r.Err != nil {
		t.Fatal(r.Err)
	}
	v := appHealthProjection(e.h, r.Plan)
	b, _ := json.Marshal(v)
	if r.Plan.PoolSize != 1 || v["counts"].(map[string]int)["unknown"] != 1 || v["counts"].(map[string]int)[healthCooldown] != 1 {
		t.Fatal(string(b))
	}
}
func TestAppServiceSafeStateGraphIdentity(t *testing.T) {
	a, e := appSafeEffects(t, "state")
	hops := []string{"first", "second"}
	graphs := map[string][]string{"profile": hops}
	a.settings.Chain = proxyChain{Hops: hops}
	a.settings.Chains = graphs
	at := time.Date(2026, 1, 2, 3, 4, 5, 6, time.FixedZone("original", 5*3600))
	a.started = at
	e.Call(appServiceEffect{Kind: "project-choice", Text: "chosen"})
	a.settings.Chains = map[string][]string{"other": nil}
	data := e.stateProjection(appServicePlan{ZeroJob: true})
	data["chains"].(map[string][]string)["profile"][0] = "changed"
	if hops[0] != "changed" || data["chain"].(proxyChain).Hops[0] != "changed" {
		t.Fatal("resource graph identity changed")
	}
	if data["startedAt"].(time.Time) != at || data["regionJob"].(*regionJob) == nil {
		t.Fatal("opaque timestamp/job projection changed")
	}
}
