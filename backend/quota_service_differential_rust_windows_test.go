//go:build windows && amd64 && rustcore

package main

import (
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"sync"
	"testing"
)

type quotaDiffAttempt struct {
	Info *subscriptionUserInfo
	Err  error
}
type quotaDiffEvent struct {
	Effect  quotaEffect
	Entries []subscriptionEntry
	Owned   bool
}
type quotaDiffFX struct {
	Base           *appQuotaEffects
	Trace          []quotaDiffEvent
	Problems       []string
	Attempts       []quotaDiffAttempt
	Fetches, Saves int
	SaveFails      bool
	Edit           string
	Edited         bool
	Clock          int64
	Proxies        []string
}

func quotaDiffEntries() []subscriptionEntry {
	return []subscriptionEntry{
		{ID: "target", URL: "https://first.invalid/sub?sub=1\nhttps://second.invalid/sub?sub=1", Label: "old", Prefix: "old-prefix", Enabled: true, UpdatedAt: 9, NodeCount: 7, LastError: "oldfetch", UserInfo: &subscriptionUserInfo{Upload: 1, Download: 2, Total: 3, Expire: 4}, QuotaUpdatedAt: 8, QuotaError: "oldquota"},
		{ID: "target", URL: "https://duplicate.invalid/sub", Label: "duplicate", QuotaError: "untouched"},
		{ID: "other", URL: "https://other.invalid/sub", Label: "other"},
	}
}
func quotaDiffInfoClone(p *subscriptionUserInfo) *subscriptionUserInfo {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
func quotaDiffNew(entries []subscriptionEntry, script quotaDiffFX) *quotaDiffFX {
	f := &quotaDiffFX{Attempts: script.Attempts, SaveFails: script.SaveFails, Edit: script.Edit, Clock: script.Clock, Proxies: script.Proxies}
	a := testControlApp(testControlApp(&app{cachedNodes: []proxyNode{{Name: "untouched-cache"}}}, func(a *app) { testCatalogWrite(a, cloneQuotaEntries(entries)) }), func(a *app) { testSetSettings(a, settings{SelectedNode: "untouched-node"}) })
	f.Base = newAppQuotaEffects(a)
	f.Base.proxies = func() []string { f.mustLocked("proxies"); return append([]string(nil), f.Proxies...) }
	f.Base.now = func() int64 { f.mustLocked("now"); return f.Clock }
	f.Base.save = func() error {
		f.mustLocked("save")
		f.Saves++
		if f.SaveFails {
			return errors.New("synthetic save failure")
		}
		return nil
	}
	f.Base.fetch = func(url, proxy string) (*subscriptionUserInfo, error) {
		if !a.mu.TryLock() {
			f.Problems = append(f.Problems, "GET attempted while application mutex held")
			return nil, errors.New("GET locked")
		}
		if !f.Edited {
			f.editLocked()
			f.Edited = true
		}
		a.mu.Unlock()
		i := f.Fetches
		f.Fetches++
		if i >= len(f.Attempts) {
			return nil, errSubscriptionNetwork
		}
		return quotaDiffInfoClone(f.Attempts[i].Info), f.Attempts[i].Err
	}
	return f
}
func (f *quotaDiffFX) mustLocked(kind string) {
	if f.Base.a.mu.TryLock() {
		f.Base.a.mu.Unlock()
		f.Problems = append(f.Problems, kind+" called without mutex")
	}
}
func (f *quotaDiffFX) editLocked() {
	a := f.Base.a
	switch f.Edit {
	case "delete":
		testCatalogEdit(a, func(entries *[]subscriptionEntry) { (*entries) = (*entries)[1:] })
		testCatalogEdit(a, func(entries *[]subscriptionEntry) { (*entries)[0].ID = "not-target" })
	case "url":
		testCatalogEdit(a, func(entries *[]subscriptionEntry) { (*entries)[0].URL = "https://changed.invalid/sub" })
	case "rename":
		testCatalogEdit(a, func(entries *[]subscriptionEntry) { (*entries)[0].Label = "concurrent-label" })
		testCatalogEdit(a, func(entries *[]subscriptionEntry) { (*entries)[0].Prefix = "new-prefix" })
		testCatalogEdit(a, func(entries *[]subscriptionEntry) { (*entries)[0].Enabled = false })
		testCatalogEdit(a, func(entries *[]subscriptionEntry) { (*entries)[0].NodeCount = 123 })
		testCatalogEdit(a, func(entries *[]subscriptionEntry) { (*entries)[0].LastError = "concurrent-fetch" })
	case "aba":
		v := testCatalogRead(a)[0]
		testCatalogEdit(a, func(entries *[]subscriptionEntry) { (*entries) = (*entries)[1:] })
		v.Label = "replacement-same-id-url"
		testCatalogEdit(a, func(entries *[]subscriptionEntry) { (*entries) = append([]subscriptionEntry{v}, (*entries)...) })
	case "reorder":
		testCatalogEdit(a, func(entries *[]subscriptionEntry) { (*entries)[0], (*entries)[1] = (*entries)[1], (*entries)[0] })
	case "quota-other":
		testCatalogEdit(a, func(entries *[]subscriptionEntry) { (*entries)[2].QuotaError = "concurrent-other" })
	}
}
func (f *quotaDiffFX) snapshot() []subscriptionEntry {
	if f.Base.owned {
		return cloneQuotaEntries(testCatalogRead(f.Base.a))
	}
	f.Base.a.mu.Lock()
	defer f.Base.a.mu.Unlock()
	return cloneQuotaEntries(testCatalogRead(f.Base.a))
}
func (f *quotaDiffFX) Call(q quotaEffect) quotaReply {
	r := f.Base.Call(q)
	q.Info = quotaDiffInfoClone(q.Info)
	f.Trace = append(f.Trace, quotaDiffEvent{q, f.snapshot(), f.Base.owned})
	return r
}
func (f *quotaDiffFX) Release() {
	owned := f.Base.owned
	f.Base.Release()
	if owned {
		f.Trace = append(f.Trace, quotaDiffEvent{Effect: quotaEffect{Kind: "release"}, Entries: f.snapshot()})
	}
}
func quotaDiffCompare(t *testing.T, entries []subscriptionEntry, q quotaServiceRequest, script quotaDiffFX) (quotaServiceResult, *quotaDiffFX) {
	t.Helper()
	if !rustQuotaFlowAvailable() {
		t.Fatal("real quota DLL required")
	}
	g, r := quotaDiffNew(entries, script), quotaDiffNew(entries, script)
	want := runGoQuotaService(q, g)
	g.Release()
	before := quotaFlowRecoveries.Load()
	got, handled := runRustQuotaService(q, r)
	r.Release()
	if !handled {
		t.Fatalf("unexpected quota fallback request=%+v", q)
	}
	if quotaFlowRecoveries.Load() != before {
		t.Fatalf("quota required Go continuation request=%+v", q)
	}
	if len(g.Problems) > 0 || len(r.Problems) > 0 {
		t.Fatalf("mutex invariant Go=%v Rust=%v", g.Problems, r.Problems)
	}
	for _, f := range []*quotaDiffFX{g, r} {
		if !f.Base.a.mu.TryLock() {
			t.Fatal("mutex leaked after request")
		}
		f.Base.a.mu.Unlock()
		if f.Base.a.settingsLocked().SelectedNode != "untouched-node" || f.Base.a.cachedNodes[0].Name != "untouched-cache" {
			t.Fatal("quota changed non-quota application state")
		}
	}
	if !reflect.DeepEqual(want, got) || !reflect.DeepEqual(g.Trace, r.Trace) || !reflect.DeepEqual(testCatalogRead(g.Base.a), testCatalogRead(r.Base.a)) {
		t.Fatalf("quota parity request=%+v edit=%s\nresult=%#v/%#v\ntrace=%#v/%#v\nfinal=%#v/%#v", q, script.Edit, want, got, g.Trace, r.Trace, testCatalogRead(g.Base.a), testCatalogRead(r.Base.a))
	}
	return got, r
}
func TestMigrationQuotaFlowRandomParity(t *testing.T) {
	for i := 0; i < 1800; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			rng := rand.New(rand.NewSource(int64(130000 + i)))
			pick := rng.Intn
			entries := quotaDiffEntries()
			if pick(8) == 0 {
				entries = nil
			}
			q := quotaServiceRequest{Op: "refresh", ID: []string{"target", "missing", ""}[pick(3)]}
			if pick(5) == 0 {
				q.Op = "error"
				q.OriginalURL = quotaDiffEntries()[0].URL
				q.Message = "reported-error"
				q.Status = 502
			}
			f := quotaDiffFX{Clock: []int64{1800000000, 2147483648, 9007199254740993, 9223372036854775806}[pick(4)], SaveFails: pick(3) == 0, Edit: []string{"", "delete", "url", "rename", "aba", "reorder", "quota-other"}[pick(7)], Proxies: []string{"direct", "proxy", ""}}
			if pick(5) == 0 {
				f.Proxies = nil
			}
			for j := 0; j < 12; j++ {
				switch pick(6) {
				case 0:
					f.Attempts = append(f.Attempts, quotaDiffAttempt{Info: &subscriptionUserInfo{Upload: -9223372036854775808, Total: 9223372036854775807}})
				case 1:
					f.Attempts = append(f.Attempts, quotaDiffAttempt{Err: errNoSubscriptionUserInfo})
				case 2:
					f.Attempts = append(f.Attempts, quotaDiffAttempt{Err: fmt.Errorf("wrapped: %w", errNoSubscriptionUserInfo)})
				case 3:
					f.Attempts = append(f.Attempts, quotaDiffAttempt{Err: errors.New(errNoSubscriptionUserInfo.Error())})
				case 4:
					f.Attempts = append(f.Attempts, quotaDiffAttempt{})
				case 5:
					f.Attempts = append(f.Attempts, quotaDiffAttempt{Err: errSubscriptionNetwork})
				}
			}
			quotaDiffCompare(t, entries, q, f)
		})
	}
}
func TestMigrationQuotaFlowInterleavingAndSaveFailure(t *testing.T) {
	for _, edit := range []string{"", "delete", "url", "rename", "aba", "reorder", "quota-other"} {
		for _, success := range []bool{false, true} {
			for _, saveFails := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%v/%v", edit, success, saveFails), func(t *testing.T) {
					f := quotaDiffFX{Clock: 9007199254740993, Edit: edit, SaveFails: saveFails, Proxies: []string{"direct"}}
					if success {
						f.Attempts = []quotaDiffAttempt{{Info: &subscriptionUserInfo{Upload: 77}}}
					}
					result, fx := quotaDiffCompare(t, quotaDiffEntries(), quotaServiceRequest{Op: "refresh", ID: "target"}, f)
					conflict := edit == "delete" || edit == "url" || edit == "reorder"
					if conflict {
						if result.Status != 409 || fx.Saves != 0 {
							t.Fatal("stale/deleted registry committed")
						}
						return
					}
					if saveFails && result.Status != 500 {
						t.Fatal("save failure status not500")
					}
					current := testCatalogRead(fx.Base.a)[0]
					if success {
						if current.UserInfo.Upload != 77 || current.UserInfo.Download != 0 || current.QuotaUpdatedAt != 9007199254740993 || current.QuotaError != "" {
							t.Fatal("successful partial quota replacement lost or rolled back")
						}
					} else {
						if current.UserInfo.Upload != 1 || current.QuotaUpdatedAt != 8 || current.QuotaError == "oldquota" {
							t.Fatal("failure changed quota values or rolled back error")
						}
					}
					if edit == "rename" && (current.Label != "concurrent-label" || current.Prefix != "new-prefix" || current.NodeCount != 123 || current.LastError != "concurrent-fetch" || current.Enabled) {
						t.Fatal("quota overwrote concurrent nonquota edits")
					}
					if testCatalogRead(fx.Base.a)[1].QuotaError != "untouched" {
						t.Fatal("duplicate ID second entry mutated")
					}
				})
			}
		}
	}
}

func TestMigrationQuotaFlowAttemptOrderAndErrorPriority(t *testing.T) {
	for _, mode := range []string{"network", "absent", "wrapped-absent", "same-text", "nil-success", "late-success"} {
		t.Run(mode, func(t *testing.T) {
			f := quotaDiffFX{Clock: 2147483648, Proxies: []string{"direct", "proxy"}}
			for i := 0; i < 8; i++ {
				a := quotaDiffAttempt{Err: errSubscriptionNetwork}
				if i == 0 {
					switch mode {
					case "absent":
						a.Err = errNoSubscriptionUserInfo
					case "wrapped-absent":
						a.Err = fmt.Errorf("wrapped: %w", errNoSubscriptionUserInfo)
					case "same-text":
						a.Err = errors.New(errNoSubscriptionUserInfo.Error())
					case "nil-success":
						a.Err = nil
					}
				}
				if mode == "late-success" && i == 6 {
					a = quotaDiffAttempt{Info: &subscriptionUserInfo{Download: 9}}
				}
				f.Attempts = append(f.Attempts, a)
			}
			result, fx := quotaDiffCompare(t, quotaDiffEntries(), quotaServiceRequest{Op: "refresh", ID: "target"}, f)
			var attempts []quotaEffect
			for _, e := range fx.Trace {
				if e.Effect.Kind == "fetch" {
					attempts = append(attempts, e.Effect)
				}
			}
			urls := []string{"https://first.invalid/sub?sub=1", "https://second.invalid/sub?sub=1"}
			var expected []quotaEffect
			for _, url := range urls {
				alt, _ := clashFormatURL(url)
				for _, proxy := range f.Proxies {
					for _, candidate := range []string{url, alt} {
						if mode == "nil-success" && len(expected) == 1 && candidate == alt && proxy == "direct" && url == urls[0] {
							continue
						}
						expected = append(expected, quotaEffect{Kind: "fetch", URL: candidate, Proxy: proxy})
					}
				}
			}
			if mode == "late-success" {
				expected = expected[:7]
			}
			if !reflect.DeepEqual(attempts, expected) {
				t.Fatalf("remote->proxy->candidate order wrong: got=%+v want=%+v", attempts, expected)
			}
			if mode == "late-success" {
				if result.Status != 200 {
					t.Fatal("late success not used")
				}
				return
			}
			want := quotaServiceMessages["network"]
			if mode == "absent" || mode == "wrapped-absent" {
				want = quotaServiceMessages["no-info"]
			}
			if result.Status != 502 || result.Message != want {
				t.Fatalf("error priority lost: %+v", result)
			}
		})
	}
}

func TestMigrationQuotaFlowReverseCompletionLastWins(t *testing.T) {
	type outcome struct {
		results  [2]quotaServiceResult
		entries  []subscriptionEntry
		trace    [2][]quotaDiffEvent
		problems []string
	}
	scenario := func(useRust bool) outcome {
		a := testControlApp(&app{}, func(a *app) { testCatalogWrite(a, quotaDiffEntries()) })
		entered := make(chan int, 2)
		gates := [2]chan struct{}{make(chan struct{}), make(chan struct{})}
		finished := [2]chan quotaServiceResult{make(chan quotaServiceResult, 1), make(chan quotaServiceResult, 1)}
		var effects [2]*quotaDiffFX
		var problems []string
		var pmu sync.Mutex
		problem := func(v string) { pmu.Lock(); problems = append(problems, v); pmu.Unlock() }
		before := quotaFlowRecoveries.Load()
		for i := 0; i < 2; i++ {
			i := i
			base := newAppQuotaEffects(a)
			fx := &quotaDiffFX{Base: base}
			effects[i] = fx
			base.proxies = func() []string {
				if a.mu.TryLock() {
					a.mu.Unlock()
					problem("proxies unlocked")
				}
				return []string{"direct"}
			}
			base.now = func() int64 { return 9007199254740993 + int64(i) }
			base.save = func() error {
				if a.mu.TryLock() {
					a.mu.Unlock()
					problem("save unlocked")
				}
				return nil
			}
			base.fetch = func(string, string) (*subscriptionUserInfo, error) {
				if !a.mu.TryLock() {
					problem("GET locked")
				} else {
					a.mu.Unlock()
				}
				entered <- i
				<-gates[i]
				return &subscriptionUserInfo{Upload: int64(10 + i)}, nil
			}
			go func() {
				defer fx.Release()
				q := quotaServiceRequest{Op: "refresh", ID: "target"}
				var result quotaServiceResult
				if useRust {
					var handled bool
					result, handled = runRustQuotaService(q, fx)
					if !handled {
						problem("actual Rust request fell back")
					}
				} else {
					result = runGoQuotaService(q, fx)
				}
				finished[i] <- result
			}()
			if seen := <-entered; seen != i {
				problem("wrong request entry order")
			}
		}
		close(gates[1])
		second := <-finished[1]
		a.mu.Lock()
		testCatalogEdit(a, func(entries *[]subscriptionEntry) { (*entries)[0].Label = "edited-between-completions" })
		a.mu.Unlock()
		close(gates[0])
		first := <-finished[0]
		if useRust && quotaFlowRecoveries.Load() != before {
			problem("actual Rust continuation occurred")
		}
		if !a.mu.TryLock() {
			problem("mutex leaked after completion")
		} else {
			a.mu.Unlock()
		}
		return outcome{results: [2]quotaServiceResult{first, second}, entries: cloneQuotaEntries(testCatalogRead(a)), trace: [2][]quotaDiffEvent{effects[0].Trace, effects[1].Trace}, problems: problems}
	}
	if !rustQuotaFlowAvailable() {
		t.Fatal("real quota DLL required")
	}
	g, r := scenario(false), scenario(true)
	if len(g.problems) > 0 || len(r.problems) > 0 || !reflect.DeepEqual(g, r) {
		t.Fatalf("reverse completion mismatch Go=%#v Rust=%#v", g, r)
	}
	if r.entries[0].UserInfo.Upload != 10 || r.entries[0].QuotaUpdatedAt != 9007199254740993 || r.entries[0].Label != "edited-between-completions" {
		t.Fatal("last completion or concurrent label was lost")
	}
	if r.results[1].Entry.UserInfo.Upload != 11 || r.results[1].Entry.Label == "edited-between-completions" {
		t.Fatal("returned prior result borrowed mutable registry")
	}
}

func TestMigrationQuotaFlowPanicReleasesMutex(t *testing.T) {
	for _, port := range []string{"proxies", "fetch", "save"} {
		t.Run(port, func(t *testing.T) {
			run := func(useRust bool) []subscriptionEntry {
				f := quotaDiffNew(quotaDiffEntries(), quotaDiffFX{Clock: 2147483648, Proxies: []string{"direct"}, Attempts: []quotaDiffAttempt{{Info: &subscriptionUserInfo{Upload: 99}}}})
				calls := 0
				fail := func() { calls++; panic("synthetic " + port + " panic") }
				switch port {
				case "proxies":
					f.Base.proxies = func() []string { fail(); return nil }
				case "fetch":
					f.Base.fetch = func(string, string) (*subscriptionUserInfo, error) { fail(); return nil, nil }
				case "save":
					f.Base.save = func() error { fail(); return nil }
				}
				func() {
					defer func() { _ = recover() }()
					q := quotaServiceRequest{Op: "refresh", ID: "target"}
					if useRust {
						_, handled := runRustQuotaService(q, f)
						if !handled {
							t.Error("panic after effects must not claim prestart fallback")
						}
					} else {
						runGoQuotaService(q, f)
					}
				}()
				if calls != 1 {
					t.Fatalf("panicking external port repeated %d times", calls)
				}
				if f.Base.owned || !f.Base.a.mu.TryLock() {
					t.Fatal("panic leaked application mutex")
				}
				f.Base.a.mu.Unlock()
				return cloneQuotaEntries(testCatalogRead(f.Base.a))
			}
			if !reflect.DeepEqual(run(false), run(true)) {
				t.Fatal("panic changed Go-vs-Rust partial state")
			}
		})
	}
}

func TestMigrationQuotaFlowNoRemoteSkipsHTTP(t *testing.T) {
	for _, url := range []string{"", "ss://synthetic-only", "unrecognized"} {
		for _, saveFails := range []bool{false, true} {
			t.Run(fmt.Sprintf("%q/%v", url, saveFails), func(t *testing.T) {
				entries := quotaDiffEntries()
				entries[0].URL = url
				result, fx := quotaDiffCompare(t, entries, quotaServiceRequest{Op: "refresh", ID: "target"}, quotaDiffFX{SaveFails: saveFails, Proxies: []string{"direct"}})
				if fx.Fetches != 0 || fx.Saves != 1 {
					t.Fatal("no remote URL should commit one error and make no HTTP attempt")
				}
				want := 400
				if saveFails {
					want = 500
				}
				if result.Status != want || testCatalogRead(fx.Base.a)[0].QuotaError != quotaServiceMessages["no-remote"] {
					t.Fatal("no remote error precedence or memory persistence changed")
				}
			})
		}
	}
}
