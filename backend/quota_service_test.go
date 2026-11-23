package main

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func quotaTestApp() *app {
	return testControlApp(&app{}, func(a *app) {
		testCatalogWrite(a, []subscriptionEntry{{ID: "id", URL: "https://quota.invalid/sub", Label: "original", UserInfo: &subscriptionUserInfo{Total: 5}, QuotaUpdatedAt: 10, QuotaError: "old"}})
	})
}
func quotaTestEffects(a *app) *appQuotaEffects {
	e := newAppQuotaEffects(a)
	e.proxies = func() []string {
		if a.mu.TryLock() {
			a.mu.Unlock()
			panic("proxies without lock")
		}
		return []string{"direct"}
	}
	e.now = func() int64 { return 1 << 40 }
	e.save = func() error {
		if a.mu.TryLock() {
			a.mu.Unlock()
			panic("save without lock")
		}
		return nil
	}
	e.fetch = func(string, string) (*subscriptionUserInfo, error) {
		if !a.mu.TryLock() {
			panic("fetch with lock")
		}
		a.mu.Unlock()
		return &subscriptionUserInfo{Total: 99}, nil
	}
	return e
}
func quotaAssertUnlocked(t *testing.T, a *app) {
	t.Helper()
	if !a.mu.TryLock() {
		t.Fatal("leaked quota lock")
	}
	a.mu.Unlock()
}
func TestQuotaServiceReacquiresLatestAndOwnsResponse(t *testing.T) {
	a := quotaTestApp()
	e := quotaTestEffects(a)
	info := &subscriptionUserInfo{Total: 99}
	e.fetch = func(string, string) (*subscriptionUserInfo, error) {
		a.mu.Lock()
		testCatalogEdit(a, func(entries *[]subscriptionEntry) { (*entries)[0].Label = "concurrent rename" })
		testCatalogEdit(a, func(entries *[]subscriptionEntry) { (*entries) = append((*entries), subscriptionEntry{ID: "other"}) })
		a.mu.Unlock()
		return info, nil
	}
	r := runGoQuotaService(quotaServiceRequest{Op: "refresh", ID: "id"}, e)
	if r.Status != 200 || r.Entry.Label != "concurrent rename" || len(testCatalogRead(a)) != 2 || testCatalogRead(a)[0].QuotaUpdatedAt != 1<<40 {
		t.Fatal(r, testCatalogRead(a))
	}
	info.Total = 7
	testCatalogEdit(a, func(entries *[]subscriptionEntry) { (*entries)[0].UserInfo.Total = 8 })
	if r.Entry.UserInfo.Total != 99 {
		t.Fatal("response aliases live or fetch memory")
	}
	quotaAssertUnlocked(t, a)
}
func TestQuotaServiceFirstIDAndABA(t *testing.T) {
	for _, mode := range []string{"changed", "deleted", "aba"} {
		t.Run(mode, func(t *testing.T) {
			a := quotaTestApp()
			e := quotaTestEffects(a)
			saves := 0
			e.save = func() error { saves++; return nil }
			e.fetch = func(string, string) (*subscriptionUserInfo, error) {
				a.mu.Lock()
				original := testCatalogRead(a)[0]
				switch mode {
				case "changed":
					testCatalogEdit(a, func(entries *[]subscriptionEntry) { (*entries)[0].URL = "https://changed.invalid" })
					testCatalogEdit(a, func(entries *[]subscriptionEntry) { (*entries) = append((*entries), original) })
				case "deleted":
					testCatalogEdit(a, func(entries *[]subscriptionEntry) { (*entries) = nil })
				case "aba":
					testCatalogEdit(a, func(entries *[]subscriptionEntry) {
						(*entries) = []subscriptionEntry{{ID: original.ID, URL: original.URL, Label: "reborn"}}
					})
				}
				a.mu.Unlock()
				return &subscriptionUserInfo{Total: 99}, nil
			}
			r := runGoQuotaService(quotaServiceRequest{Op: "refresh", ID: "id"}, e)
			if mode == "aba" {
				if r.Status != 200 || r.Entry.Label != "reborn" || saves != 1 {
					t.Fatal(r, saves)
				}
			} else if r.Status != 409 || saves != 0 {
				t.Fatal(r, saves)
			}
			quotaAssertUnlocked(t, a)
		})
	}
}
func TestQuotaServiceSaveFailureRetainsFields(t *testing.T) {
	for _, success := range []bool{false, true} {
		a := quotaTestApp()
		e := quotaTestEffects(a)
		e.save = func() error { return errors.New("save failed") }
		if !success {
			e.fetch = func(string, string) (*subscriptionUserInfo, error) { return nil, errNoSubscriptionUserInfo }
		}
		r := runGoQuotaService(quotaServiceRequest{Op: "refresh", ID: "id"}, e)
		if r.Status != 500 {
			t.Fatal(r)
		}
		if success {
			if testCatalogRead(a)[0].UserInfo.Total != 99 || testCatalogRead(a)[0].QuotaError != "" {
				t.Fatal(testCatalogRead(a))
			}
		} else if testCatalogRead(a)[0].UserInfo.Total != 5 || testCatalogRead(a)[0].QuotaUpdatedAt != 10 || testCatalogRead(a)[0].QuotaError != quotaServiceMessages["no-info"] {
			t.Fatal(testCatalogRead(a))
		}
		quotaAssertUnlocked(t, a)
	}
}
func TestQuotaServicePanicReleasesLock(t *testing.T) {
	for _, where := range []string{"proxies", "fetch", "save"} {
		t.Run(where, func(t *testing.T) {
			a := quotaTestApp()
			e := quotaTestEffects(a)
			switch where {
			case "proxies":
				e.proxies = func() []string { panic("injected") }
			case "fetch":
				e.fetch = func(string, string) (*subscriptionUserInfo, error) { panic("injected") }
			case "save":
				e.save = func() error { panic("injected") }
			}
			panicked := false
			func() {
				defer func() { panicked = recover() != nil }()
				runGoQuotaService(quotaServiceRequest{Op: "refresh", ID: "id"}, e)
			}()
			if !panicked {
				t.Fatal("expected panic")
			}
			quotaAssertUnlocked(t, a)
		})
	}
}
func TestQuotaServiceConcurrentCompletionOrder(t *testing.T) {
	a := quotaTestApp()
	e1, e2 := quotaTestEffects(a), quotaTestEffects(a)
	entered := make(chan struct{}, 2)
	release1, release2 := make(chan struct{}), make(chan struct{})
	done1, done2 := make(chan quotaServiceResult, 1), make(chan quotaServiceResult, 1)
	e1.fetch = func(string, string) (*subscriptionUserInfo, error) {
		entered <- struct{}{}
		<-release1
		return &subscriptionUserInfo{Total: 1}, nil
	}
	e2.fetch = func(string, string) (*subscriptionUserInfo, error) {
		entered <- struct{}{}
		<-release2
		return &subscriptionUserInfo{Total: 2}, nil
	}
	var once sync.Once
	defer once.Do(func() {
		select {
		case <-release1:
		default:
			close(release1)
		}
		select {
		case <-release2:
		default:
			close(release2)
		}
	})
	go func() { done1 <- runGoQuotaService(quotaServiceRequest{Op: "refresh", ID: "id"}, e1) }()
	go func() { done2 <- runGoQuotaService(quotaServiceRequest{Op: "refresh", ID: "id"}, e2) }()
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("requests serialized by lock")
		}
	}
	close(release2)
	r2 := <-done2
	close(release1)
	r1 := <-done1
	if r1.Status != 200 || r2.Status != 200 || testCatalogRead(a)[0].UserInfo.Total != 1 || r2.Entry.UserInfo.Total != 2 {
		t.Fatal(r1, r2, testCatalogRead(a))
	}
	quotaAssertUnlocked(t, a)
}
func TestQuotaServiceErrorEntryPoint(t *testing.T) {
	a := quotaTestApp()
	e := quotaTestEffects(a)
	e.fetch = func(string, string) (*subscriptionUserInfo, error) { panic("unexpected network") }
	r := runGoQuotaService(quotaServiceRequest{Op: "error", ID: "id", OriginalURL: testCatalogRead(a)[0].URL, Message: "failed synthetic", Status: 502}, e)
	if r.Status != 502 || testCatalogRead(a)[0].QuotaError != "failed synthetic" || testCatalogRead(a)[0].QuotaUpdatedAt != 10 {
		t.Fatal(r, testCatalogRead(a))
	}
	quotaAssertUnlocked(t, a)
}
