package main

import (
	"errors"
	"strings"
	"testing"
)

type catalogRefFault struct {
	serviceStateBackend
	fail bool
}

func (b *catalogRefFault) CatalogQuery(p string) (string, error) {
	q, _ := decodeServiceCatalogQuery(p)
	if b.fail && q.Kind == 5 {
		return "", errors.New("refs unavailable")
	}
	return b.serviceStateBackend.CatalogQuery(p)
}
func catalogCatch(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatal("expected failclosed panic")
		}
	}()
	fn()
}
func TestServiceStateSafeCatalogHandleBound(t *testing.T) {
	a := &app{}
	a.restoreCatalogEntriesLocked([]subscriptionEntry{{ID: "one"}})
	for i := 0; i < 10000; i++ {
		a.patchCatalogEntryLocked(0, catalogInfoHandle, subscriptionEntry{UserInfo: &subscriptionUserInfo{Total: int64(i)}})
	}
	b := a.service.backend.(*goServiceStateBackend)
	if len(a.service.infos) > 1 || len(a.service.infoReverse) > 1 || len(b.catalogRefs) > 1 {
		t.Fatalf("unbounded refs %d/%d/%d", len(a.service.infos), len(a.service.infoReverse), len(b.catalogRefs))
	}
	old := a.catalogEntriesLocked()[0].UserInfo
	registered := *old
	old.Upload = 88
	if !catalogQuotaMatches(a.service, a.catalogEntriesLocked()[0].UserInfo, old, registered) {
		t.Fatal("pointer write-through lost")
	}
	if e := a.service.transferToGo(); e != nil {
		t.Fatal(e)
	}
	if !catalogQuotaMatches(a.service, a.catalogEntriesLocked()[0].UserInfo, old, registered) {
		t.Fatal("handoff changed pointer")
	}
}
func TestServiceStateSafeCatalogPureRejectBound(t *testing.T) {
	a := &app{}
	large := strings.Repeat("x", serviceCatalogMaxBytes)
	for i := 0; i < 100; i++ {
		catalogCatch(t, func() {
			a.catalogCommandLocked(serviceCatalogCommand{Action: catalogAppend, Entry: a.serviceStateLocked().packCatalog(subscriptionEntry{ID: large, UserInfo: &subscriptionUserInfo{Total: int64(i)}})})
		})
	}
	if a.service.pending != nil || len(a.service.infos) != 0 || len(a.service.infoReverse) != 0 || len(a.service.pendingInfos) != 0 {
		t.Fatal("pure encode reject leaked registration")
	}
}
func TestServiceStateSafeCatalogRetirementFailure(t *testing.T) {
	a := &app{}
	a.restoreCatalogEntriesLocked([]subscriptionEntry{{ID: "one", UserInfo: &subscriptionUserInfo{Total: 1}}})
	b := &catalogRefFault{serviceStateBackend: a.service.backend, fail: true}
	a.service.backend = b
	catalogCatch(t, func() {
		a.patchCatalogEntryLocked(0, catalogInfoHandle, subscriptionEntry{UserInfo: &subscriptionUserInfo{Total: 2}})
	})
	if a.service.catalogFault == nil || len(a.service.retireInfos) == 0 || len(a.service.infos) != 2 {
		t.Fatal("retirement failure lost protected resources")
	}
	before, _ := b.serviceStateBackend.Meta()
	catalogCatch(t, func() { a.setProfileFromCacheLocked(true) })
	after, _ := b.serviceStateBackend.Meta()
	if before.Revision != after.Revision {
		t.Fatal("mutation after resource fault")
	}
}
func TestServiceStateSafeCatalogQuotaClosed(t *testing.T) {
	a := &app{}
	_ = a.closeServiceStateLocked()
	r := a.runQuotaService(quotaServiceRequest{Op: "refresh", ID: "id"})
	if r.Status != 500 {
		t.Fatalf("%+v", r)
	}
	if !a.mu.TryLock() {
		t.Fatal("quota lock leaked")
	}
	a.mu.Unlock()
}

type catalogAnyQueryFailure struct{ serviceStateBackend }

func (b *catalogAnyQueryFailure) CatalogQuery(string) (string, error) {
	return "", errors.New("transient catalog query")
}
func TestServiceStateSafeCatalogFindFailure(t *testing.T) {
	a := &app{}
	a.restoreCatalogEntriesLocked([]subscriptionEntry{{ID: "id", URL: " exact "}})
	if _, ok := a.findSubscriptionByURL(" exact "); !ok {
		t.Fatal("raw URL semantics changed")
	}
	a.service.backend = &catalogAnyQueryFailure{a.service.backend}
	catalogCatch(t, func() { a.findSubscriptionLocked("id") })
	catalogCatch(t, func() { a.findSubscriptionByURL("url") })
}
func TestServiceStateSafeCatalogCrossDomainFaultGate(t *testing.T) {
	a := &app{}
	s := a.serviceStateLocked()
	s.catalogFault = errors.New("resource unavailable")
	catalogCatch(t, func() { a.controlLocked() })
	catalogCatch(t, func() { a.controlMetaLocked() })
	catalogCatch(t, func() { a.healthMetaLocked() })
	catalogCatch(t, func() { a.pathStateLocked() })
	if _, e := s.read(); e == nil {
		t.Fatal("traffic bypassed fault")
	}
}

// Only the existing store callback is simulated; no DB/secret/files are used.
type catalogSaveObserver struct {
	a     *app
	calls int
	t     *testing.T
}

func (f *catalogSaveObserver) Call(kind, arg string, s *subscriptionServiceState) (r subscriptionEffectReply) {
	f.a.flushCatalogElementIntentsLocked(s)
	switch kind {
	case "seal-secret":
		r.Text = arg
	case "write-registry":
		f.calls++
		v, ok := f.a.findSubscriptionLocked("id")
		if !ok || v.Label != "renamed" {
			f.t.Fatal("save saw stale catalog")
		}
		r.Err = errors.New("save denied")
	}
	return
}
func TestServiceStateSafeCatalogSaveObservation(t *testing.T) {
	testCatalogSaveObservation(t, func() *app { return &app{} }, runGoSubscriptionService)
}
func testCatalogSaveObservation(t *testing.T, newApp func() *app, run func(*subscriptionServiceState, subscriptionServiceRequest, subscriptionServiceEffects) subscriptionServiceResult) {
	a := newApp()
	a.restoreCatalogEntriesLocked([]subscriptionEntry{{ID: "id", URL: "synthetic", Label: "old"}})
	a.store = &store{}
	s := settingsStateFromApp(a)
	f := &catalogSaveObserver{a: a, t: t}
	r := run(&s, subscriptionServiceRequest{Op: "rename", ID: "id", Label: "renamed", LabelRunes: 7}, f)
	if r.Status != 500 || f.calls != 1 {
		t.Fatalf("%+v %d", r, f.calls)
	}
	v, _ := a.findSubscriptionLocked("id")
	if v.Label != "renamed" {
		t.Fatal("save failure rolled back rename")
	}
}

func TestServiceStateSafeCatalogHeaderVisibility(t *testing.T) {
	testCatalogHeaderVisibility(t, func() *app { return &app{} })
}
func testCatalogHeaderVisibility(t *testing.T, newApp func() *app) {
	for _, action := range []uint8{catalogAppend, catalogRemoveID} {
		t.Run(map[uint8]string{catalogAppend: "append", catalogRemoveID: "delete"}[action], func(t *testing.T) {
			a := newApp()
			a.restoreCatalogEntriesLocked([]subscriptionEntry{{ID: "id", Label: "old"}})
			s := settingsStateFromApp(a)
			markCatalogIntent(&s, catalogIntent{Action: action, ID: "id", Entry: subscriptionEntry{ID: "new"}})
			// The ordinary write-registry boundary flushes elements, not a new slice header.
			a.flushCatalogElementIntentsLocked(&s)
			got := a.catalogEntriesLocked()
			if len(got) != 1 || got[0].ID != "id" || s.CatalogIntents.Applied != 0 {
				t.Fatal("header published before save")
			}
			a.applyCatalogIntentsLocked(&s)
			got = a.catalogEntriesLocked()
			if action == catalogAppend && (len(got) != 2 || got[1].ID != "new") {
				t.Fatal("append not committed")
			}
			if action == catalogRemoveID && (got == nil || len(got) != 0) {
				t.Fatal("delete shape changed")
			}
		})
	}
}
func TestServiceStateSafeCatalogMixedIntentPrefix(t *testing.T) {
	testCatalogMixedIntentPrefix(t, func() *app { return &app{} })
}
func testCatalogMixedIntentPrefix(t *testing.T, newApp func() *app) {
	a := newApp()
	a.restoreCatalogEntriesLocked([]subscriptionEntry{{ID: "id", Label: "old"}})
	s := settingsStateFromApp(a)
	markCatalogIntent(&s, catalogIntent{Action: catalogPatch, Index: 0, Mask: catalogLabel, Entry: subscriptionEntry{Label: "first"}})
	markCatalogAppend(&s, subscriptionEntry{ID: "new"})
	markCatalogIntent(&s, catalogIntent{Action: catalogPatch, Index: 0, Mask: catalogLabel, Entry: subscriptionEntry{Label: "last"}})
	a.flushCatalogElementIntentsLocked(&s)
	got := a.catalogEntriesLocked()
	if len(got) != 1 || got[0].Label != "first" || s.CatalogIntents.Applied != 1 {
		t.Fatal("flush crossed structural boundary")
	}
	a.applyCatalogIntentsLocked(&s)
	got = a.catalogEntriesLocked()
	if len(got) != 2 || got[0].Label != "last" {
		t.Fatal("final intent sequence lost")
	}
}
func TestServiceStateSafeCatalogCheckpointGuards(t *testing.T) {
	st := serviceCatalogState{Entries: []serviceCatalogEntry{{ID: "actual", URL: "u"}}}
	if validateCatalogLast(st, serviceCatalogCommand{Action: catalogQuotaGuarded, Index: 0, ID: "missing", URL: "u"}) == nil {
		t.Fatal("missing first ID accepted")
	}
	if validateCatalogLast(serviceCatalogState{}, serviceCatalogCommand{Action: catalogRemoveID}) == nil {
		t.Fatal("nil remove result accepted")
	}
	if err := validateCatalogLast(serviceCatalogState{HasBackup: true, Backup: []serviceCatalogEntry{}}, serviceCatalogCommand{Action: catalogRestoreDelete}); err != nil {
		t.Fatal(err)
	}
}
