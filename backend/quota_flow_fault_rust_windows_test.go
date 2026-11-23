//go:build windows && amd64 && rustcore

package main

import (
	"errors"
	"reflect"
	"testing"
)

type quotaInjectedSession struct {
	quotaFlowSession
	kind, target                 byte
	nextFailure, pending, failed bool
}

func (s *quotaInjectedSession) next() ([]byte, uint64, error) {
	if s.pending {
		s.pending = false
		s.failed = true
		return nil, 0, errors.New("synthetic next failure")
	}
	b, n, e := s.quotaFlowSession.next()
	if e == nil {
		f, err := decodeQuotaFrame(b)
		if err == nil {
			s.kind = f.kind
		}
	}
	return b, n, e
}
func (s *quotaInjectedSession) advance(n uint64, b []byte) error {
	if !s.failed && s.kind == s.target {
		if s.nextFailure {
			s.pending = true
		} else {
			s.failed = true
			return errors.New("synthetic advance failure")
		}
	}
	return s.quotaFlowSession.advance(n, b)
}
func TestQuotaFlowActualFaultAtEveryEffectPreservesFreshState(t *testing.T) {
	if !rustQuotaFlowAvailable() {
		t.Fatal("actual quota DLL required")
	}
	for target := byte(1); target <= 10; target++ {
		for _, next := range []bool{false, true} {
			a, b := quotaTestApp(), quotaTestApp()
			x, y := quotaTestEffects(a), quotaTestEffects(b)
			xget, yget, xsave, ysave := 0, 0, 0, 0
			setup := func(app *app, fx *appQuotaEffects, get, save *int) {
				fx.fetch = func(string, string) (*subscriptionUserInfo, error) {
					*get++
					app.mu.Lock()
					testCatalogEdit(app, func(entries *[]subscriptionEntry) { (*entries)[0].Label = "concurrent edit" })
					testCatalogEdit(app, func(entries *[]subscriptionEntry) {
						(*entries) = append((*entries), subscriptionEntry{ID: "other", URL: "unchanged"})
					})
					app.mu.Unlock()
					return &subscriptionUserInfo{Total: -1, Expire: 1 << 40}, nil
				}
				fx.save = func() error { *save++; return nil }
			}
			setup(a, x, &xget, &xsave)
			setup(b, y, &yget, &ysave)
			q := quotaServiceRequest{Op: "refresh", ID: "id"}
			want := runGoQuotaService(q, x)
			var injected *quotaInjectedSession
			got, used := runRustQuotaServiceWithStart(q, y, func(input []byte) (quotaFlowSession, error) {
				session, err := startRustQuotaFlow(input)
				if err != nil {
					return nil, err
				}
				injected = &quotaInjectedSession{quotaFlowSession: session, target: target, nextFailure: next}
				return injected, nil
			})
			if !used || injected == nil || !injected.failed || !reflect.DeepEqual(got, want) || !reflect.DeepEqual(testCatalogRead(a), testCatalogRead(b)) || xget != 1 || yget != 1 || xsave != 1 || ysave != 1 {
				t.Fatal(target, next, got, want, xget, yget, xsave, ysave)
			}
			quotaAssertUnlocked(t, a)
			quotaAssertUnlocked(t, b)
		}
	}
}
func TestQuotaFlowActualCapacityFallbackAndReuse(t *testing.T) {
	if !rustQuotaFlowAvailable() {
		t.Fatal("actual quota DLL required")
	}
	q := quotaServiceRequest{Op: "refresh", ID: "id"}
	input, err := quotaStartInput(q)
	if err != nil {
		t.Fatal(err)
	}
	var held []quotaFlowSession
	defer func() {
		for _, s := range held {
			s.close()
		}
	}()
	for i := 0; i < 64; i++ {
		s, err := startRustQuotaFlow(input)
		if err != nil {
			t.Fatal(i, err)
		}
		held = append(held, s)
	}
	if s, err := startRustQuotaFlow(input); err == nil {
		s.close()
		t.Fatal("capacity unbounded")
	}
	t.Setenv("SMARTVPN_RUST_QUOTA_FLOW", "1")
	a := quotaTestApp()
	r := runSelectedQuotaService(q, quotaTestEffects(a))
	if r.Status != 200 {
		t.Fatal(r)
	}
	quotaAssertUnlocked(t, a)
	for _, s := range held {
		s.close()
	}
	held = nil
	a = quotaTestApp()
	r, used := runRustQuotaService(q, quotaTestEffects(a))
	if !used || r.Status != 200 {
		t.Fatal(r, used)
	}
	quotaAssertUnlocked(t, a)
}

func TestQuotaFlowActualErrorCommitAndSaveFailureRemainApplied(t *testing.T) {
	if !rustQuotaFlowAvailable() {
		t.Fatal("actual quota DLL required")
	}
	for _, mode := range []string{"refresh", "error"} {
		for _, target := range []byte{9, 10} {
			for _, next := range []bool{false, true} {
				a, b := quotaTestApp(), quotaTestApp()
				x, y := quotaTestEffects(a), quotaTestEffects(b)
				xgets, ygets, xsaves, ysaves := 0, 0, 0, 0
				setup := func(fx *appQuotaEffects, gets, saves *int) {
					fx.fetch = func(string, string) (*subscriptionUserInfo, error) { *gets++; return nil, errNoSubscriptionUserInfo }
					fx.save = func() error { *saves++; return errors.New("synthetic save failure") }
				}
				setup(x, &xgets, &xsaves)
				setup(y, &ygets, &ysaves)
				q := quotaServiceRequest{Op: mode, ID: "id", OriginalURL: testCatalogRead(a)[0].URL, Message: "synthetic warning", Status: 502}
				want := runGoQuotaService(q, x)
				var injected *quotaInjectedSession
				got, used := runRustQuotaServiceWithStart(q, y, func(input []byte) (quotaFlowSession, error) {
					session, err := startRustQuotaFlow(input)
					if err != nil {
						return nil, err
					}
					injected = &quotaInjectedSession{quotaFlowSession: session, target: target, nextFailure: next}
					return injected, nil
				})
				if !used || !injected.failed || !reflect.DeepEqual(got, want) || !reflect.DeepEqual(testCatalogRead(a), testCatalogRead(b)) || xgets != ygets || xsaves != 1 || ysaves != 1 || got.Status != 500 || testCatalogRead(b)[0].UserInfo.Total != 5 || testCatalogRead(b)[0].QuotaUpdatedAt != 10 {
					t.Fatal(mode, target, next, got, want, xgets, ygets, xsaves, ysaves)
				}
				quotaAssertUnlocked(t, b)
			}
		}
	}
}
