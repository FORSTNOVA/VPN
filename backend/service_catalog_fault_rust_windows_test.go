//go:build windows && amd64 && rustcore

package main

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func catalogFaultApply(t *testing.T, a *serviceAuthority, c serviceCatalogCommand) {
	t.Helper()
	p, e := encodeServiceCatalogCommand(c)
	if e != nil {
		t.Fatal(e)
	}
	if e = a.mutate(serviceStateCommand{Kind: 6, CatalogPayload: p}); e != nil {
		t.Fatal(e)
	}
}
func catalogFaultQuery(t *testing.T, b serviceStateBackend, q serviceCatalogQuery) serviceCatalogReply {
	t.Helper()
	p, e := encodeServiceCatalogQuery(q)
	if e != nil {
		t.Fatal(e)
	}
	out, e := b.CatalogQuery(p)
	if e != nil {
		t.Fatal(e)
	}
	v, e := decodeServiceCatalogReply(out)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func TestServiceStateCatalogPatchDeliveryFault(t *testing.T) {
	for bit := 0; bit < 11; bit++ {
		for _, op := range []byte{3, 6} {
			a, tr, b := newServiceFault(t, 0, false, false)
			catalogFaultApply(t, a, serviceCatalogCommand{Action: catalogAppend, Entry: serviceCatalogEntry{ID: "one"}})
			tr.op = op
			tr.corrupt = true
			tr.fired = false
			before3, before6 := tr.counts[3], tr.counts[6]
			entry := catalogCodecEntry()
			entry.InfoHandle = 0
			catalogFaultApply(t, a, serviceCatalogCommand{Action: catalogPatch, Index: 0, Mask: 1 << bit, Entry: entry})
			v := catalogFaultQuery(t, b, serviceCatalogQuery{Kind: 1, Index: 0})
			expected := serviceCatalogEntry{ID: "one"}
			reflect.ValueOf(&expected).Elem().Field(bit).Set(reflect.ValueOf(entry).Field(bit))
			if !v.Found || v.Entry != expected {
				t.Fatal("patch", bit)
			}
			if tr.counts[3]-before3 != 1 || tr.counts[6]-before6 != 1 || tr.counts[4] != 1 {
				t.Fatal("replay", tr.counts)
			}
			_ = a.Close()
		}
	}
}

type catalogBadExport struct {
	serviceStateBackend
	field int
}

func (b *catalogBadExport) Export() (serviceStateCheckpoint, error) {
	cp, e := b.serviceStateBackend.Export()
	if e != nil {
		return cp, e
	}
	v, e := decodeServiceCatalogState(cp.Snapshot.CatalogPayload)
	if e != nil {
		return cp, e
	}
	f := reflect.ValueOf(&v.Entries[0]).Elem().Field(b.field)
	switch f.Kind() {
	case reflect.String:
		f.SetString(f.String() + "changed")
	case reflect.Bool:
		f.SetBool(!f.Bool())
	case reflect.Int, reflect.Int64:
		f.SetInt(f.Int() + 1)
	case reflect.Uint64:
		f.SetUint(999999)
	}
	cp.Snapshot.CatalogPayload, e = encodeServiceCatalogState(v)
	return cp, e
}
func TestServiceStateCatalogCheckpointEveryEntryField(t *testing.T) {
	for i := 0; i < 11; i++ {
		a, _, b := newServiceFault(t, 0, false, false)
		v := catalogCodecEntry()
		v.InfoHandle = 0
		catalogFaultApply(t, a, serviceCatalogCommand{Action: catalogAppend, Entry: v})
		bad := &catalogBadExport{b, i}
		a.backend = bad
		if e := a.transferToGo(); e == nil {
			t.Fatal("altered CP accepted", i)
		}
		if a.backend != bad {
			t.Fatal("authority changed")
		}
		_ = a.Close()
	}
}
func TestServiceStateCatalogLargeNarrowUpdates(t *testing.T) {
	a, tr, b := newServiceFault(t, 0, false, false)
	entries := make([]serviceCatalogEntry, 10000)
	for i := range entries {
		entries[i] = serviceCatalogEntry{ID: "duplicate", URL: "opaque"}
	}
	for _, entry := range entries {
		catalogFaultApply(t, a, serviceCatalogCommand{Action: catalogAppend, Entry: entry})
	}
	for i := 0; i < 10000; i++ {
		catalogFaultApply(t, a, serviceCatalogCommand{Action: catalogPatch, Index: i, Mask: catalogNodeCount, Entry: serviceCatalogEntry{NodeCount: i}})
		v := catalogFaultQuery(t, b, serviceCatalogQuery{Kind: 1, Index: i})
		if !v.Found || v.Entry.NodeCount != i {
			t.Fatal("narrow item", i)
		}
	}
	if tr.counts[1] != 0 || tr.counts[5] != 0 || len(a.clocks) != 0 {
		t.Fatal("full table read or clock growth", tr.counts)
	}
	m, e := b.Meta()
	if e != nil || m.CatalogPayload != "" {
		t.Fatal("wide meta", e)
	}
}

func TestServiceStateCatalogRollbackReferences(t *testing.T) {
	a, _, b := newServiceFault(t, 0, false, false)
	for _, id := range []string{"a", "a", "b"} {
		catalogFaultApply(t, a, serviceCatalogCommand{Action: catalogAppend, Entry: serviceCatalogEntry{ID: id, URL: "u", InfoHandle: 17}})
	}
	refs := func(want uint64) {
		t.Helper()
		v := catalogFaultQuery(t, b, serviceCatalogQuery{Kind: 5, Handle: 17})
		if v.References != want {
			t.Fatal("references", v.References, want)
		}
	}
	refs(3)
	catalogFaultApply(t, a, serviceCatalogCommand{Action: catalogBeginDelete})
	refs(6)
	catalogFaultApply(t, a, serviceCatalogCommand{Action: catalogRemoveID, ID: "a"})
	refs(4)
	catalogFaultApply(t, a, serviceCatalogCommand{Action: catalogRestoreDelete})
	refs(6)
	catalogFaultApply(t, a, serviceCatalogCommand{Action: catalogFinishDelete})
	refs(3)
	catalogFaultApply(t, a, serviceCatalogCommand{Action: catalogTruncate})
	refs(2)
	catalogFaultApply(t, a, serviceCatalogCommand{Action: catalogReset})
	refs(0)
}

func TestServiceStateCatalogResourceRetirementStress(t *testing.T) {
	a, _, b := newServiceFault(t, 0, false, false)
	app := &app{service: a}
	shared := &subscriptionUserInfo{Upload: 1}
	app.restoreCatalogEntriesLocked([]subscriptionEntry{{ID: "a", UserInfo: shared}, {ID: "b", UserInfo: shared}})
	ea, _ := app.catalogEntryLocked(0)
	eb, _ := app.catalogEntryLocked(1)
	if !catalogQuotaMatches(a, ea.UserInfo, shared, *shared) || !catalogQuotaMatches(a, eb.UserInfo, shared, *shared) {
		t.Fatal("shared identity")
	}
	shared.Upload = 99
	if !catalogQuotaMatches(a, eb.UserInfo, shared, subscriptionUserInfo{Upload: 1}) {
		t.Fatal("write through")
	}
	for i := 0; i < 10000; i++ {
		p := &subscriptionUserInfo{Upload: int64(i)}
		app.patchCatalogEntryLocked(0, catalogInfoHandle, subscriptionEntry{UserInfo: p})
		v, _ := app.catalogEntryLocked(0)
		if !catalogQuotaMatches(a, v.UserInfo, p, *p) {
			t.Fatal("pointer identity", i)
		}
		if len(a.infos) > 2 || len(a.infoReverse) > 2 {
			t.Fatal("resource growth", len(a.infos), len(a.infoReverse))
		}
	}
	before, _ := app.catalogEntryLocked(0)
	if e := a.transferToGo(); e != nil {
		t.Fatal(e)
	}
	after, _ := app.catalogEntryLocked(0)
	if !catalogQuotaMatches(a, after.UserInfo, before.UserInfo, *before.UserInfo) {
		t.Fatal("handoff copied resource")
	}
	_ = b
}
func TestServiceStateCatalogUnknownAndExpiredResources(t *testing.T) {
	for _, expired := range []bool{false, true} {
		a, _, b := newServiceFault(t, 0, false, false)
		app := &app{service: a}
		handle := uint64(999999)
		if expired {
			p := &subscriptionUserInfo{Upload: 1}
			app.restoreCatalogEntriesLocked([]subscriptionEntry{{ID: "a", UserInfo: p}})
			r := catalogFaultQuery(t, b, serviceCatalogQuery{Kind: 1, Index: 0})
			handle = r.Entry.InfoHandle
			app.patchCatalogEntryLocked(0, catalogInfoHandle, subscriptionEntry{})
			if _, ok := a.infos[handle]; ok {
				t.Fatal("resource not retired")
			}
		} else {
			catalogFaultApply(t, a, serviceCatalogCommand{Action: catalogAppend, Entry: serviceCatalogEntry{ID: "a"}})
		}
		catalogFaultApply(t, a, serviceCatalogCommand{Action: catalogPatch, Index: 0, Mask: catalogInfoHandle, Entry: serviceCatalogEntry{InfoHandle: handle}})
		if e := a.transferToGo(); e == nil {
			t.Fatal("unknown resource transferred", expired)
		}
		if a.backend != b {
			t.Fatal("owner changed")
		}
		func() {
			defer func() {
				if recover() == nil {
					t.Fatal("unknown resource resolved")
				}
			}()
			app.catalogEntryLocked(0)
		}()
	}
}

func TestServiceStateCatalogPendingKeepsBothResources(t *testing.T) {
	a, tr, b := newServiceFault(t, 0, false, false)
	app := &app{service: a}
	old := &subscriptionUserInfo{Upload: 1}
	next := &subscriptionUserInfo{Upload: 2}
	app.restoreCatalogEntriesLocked([]subscriptionEntry{{ID: "a", UserInfo: old}})
	oldHandle := catalogFaultQuery(t, b, serviceCatalogQuery{Kind: 1, Index: 0}).Entry.InfoHandle
	before := tr.counts[3]
	tr.op = 3
	tr.before = true
	tr.fired = false
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("unconfirmed patch accepted")
			}
		}()
		app.patchCatalogEntryLocked(0, catalogInfoHandle, subscriptionEntry{UserInfo: next})
	}()
	if a.pending == nil || len(a.infos) != 2 || *a.quotaSnapshot(oldHandle, true) != *old || *a.quotaSnapshot(a.nextInfo, true) != *next {
		t.Fatal("pending resource lost")
	}
	if e := a.transferToGo(); e == nil {
		t.Fatal("pending transferred")
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("pending read accepted")
			}
		}()
		app.catalogEntryLocked(0)
	}()
	if tr.counts[3] != before+1 {
		t.Fatal("commit replay")
	}
	v := catalogFaultQuery(t, b, serviceCatalogQuery{Kind: 1, Index: 0})
	if v.Entry.InfoHandle != oldHandle {
		t.Fatal("before-submit changed live pointer")
	}
}

type catalogRefsFail struct {
	serviceStateTransport
	fired bool
}

func (t *catalogRefsFail) call(op byte, input []byte) ([]byte, error) {
	if op == 11 && len(input) > 0 && input[0] == 5 && !t.fired {
		t.fired = true
		return nil, errors.New("ref query interrupted")
	}
	return t.serviceStateTransport.call(op, input)
}
func TestServiceStateCatalogResourceFailureBoundaries(t *testing.T) {
	t.Run("encode", func(t *testing.T) {
		a, tr, _ := newServiceFault(t, 0, false, false)
		app := &app{service: a}
		old := &subscriptionUserInfo{Upload: 1}
		app.restoreCatalogEntriesLocked([]subscriptionEntry{{ID: "a", UserInfo: old}})
		before := tr.counts[3]
		func() {
			defer func() {
				if recover() == nil {
					t.Fatal("oversize accepted")
				}
			}()
			app.patchCatalogEntryLocked(0, catalogInfoHandle, subscriptionEntry{UserInfo: &subscriptionUserInfo{Upload: 2}, URL: strings.Repeat("x", serviceCatalogMaxBytes)})
		}()
		if tr.counts[3] != before || len(a.infos) != 1 || len(a.pendingInfos) != 0 || a.pending != nil {
			t.Fatal("unsubmitted resources leaked")
		}
		v, _ := app.catalogEntryLocked(0)
		if !catalogQuotaMatches(a, v.UserInfo, old, *old) {
			t.Fatal("old lost")
		}
	})
	t.Run("retirement", func(t *testing.T) {
		a, tr, b := newServiceFault(t, 0, false, false)
		app := &app{service: a}
		old := &subscriptionUserInfo{Upload: 1}
		next := &subscriptionUserInfo{Upload: 2}
		app.restoreCatalogEntriesLocked([]subscriptionEntry{{ID: "a", UserInfo: old}})
		b.transport = &catalogRefsFail{serviceStateTransport: b.transport}
		before := tr.counts[3]
		func() {
			defer func() {
				if recover() == nil {
					t.Fatal("unknown refs accepted")
				}
			}()
			app.patchCatalogEntryLocked(0, catalogInfoHandle, subscriptionEntry{UserInfo: next})
		}()
		if a.catalogFault == nil || len(a.infos) != 2 || len(a.retireInfos) == 0 || a.pending != nil {
			t.Fatal("retirement uncertainty lost resources")
		}
		for i := 0; i < 2; i++ {
			func() {
				defer func() {
					if recover() == nil {
						t.Fatal("failed authority reused")
					}
				}()
				app.setProfileFromCacheLocked(true)
			}()
		}
		if tr.counts[3] != before+1 {
			t.Fatal("replayed after cleanup failure")
		}
	})
}

func TestServiceStateCatalogActualSaveAndHeaderTiming(t *testing.T) {
	if !rustServiceStateAvailable() || !rustSubscriptionFlowAvailable() {
		t.Fatal("required native owner/planner unavailable")
	}
	makeApp := func() *app {
		b, e := newRustServiceStateBackend()
		if e != nil {
			t.Fatal(e)
		}
		a := &app{service: newServiceAuthority(b)}
		t.Cleanup(func() { _ = a.service.Close() })
		return a
	}
	run := func(s *subscriptionServiceState, q subscriptionServiceRequest, fx subscriptionServiceEffects) subscriptionServiceResult {
		before := subscriptionFlowRecoveries.Load()
		r, handled := runRustSubscriptionService(s, q, fx)
		if !handled || subscriptionFlowRecoveries.Load() != before {
			t.Fatal("native planner fallback")
		}
		return r
	}
	t.Run("save-observation", func(t *testing.T) { testCatalogSaveObservation(t, makeApp, run) })
	t.Run("header", func(t *testing.T) { testCatalogHeaderVisibility(t, makeApp) })
	t.Run("mixed-prefix", func(t *testing.T) { testCatalogMixedIntentPrefix(t, makeApp) })
}
func TestServiceStateCatalogActualGuardAndEmptyRestore(t *testing.T) {
	a, _, b := newServiceFault(t, 0, false, false)
	catalogFaultApply(t, a, serviceCatalogCommand{Action: catalogAppend, Entry: serviceCatalogEntry{ID: "actual", URL: "u"}})
	before, e := b.Export()
	if e != nil {
		t.Fatal(e)
	}
	bad, _ := encodeServiceCatalogCommand(serviceCatalogCommand{Action: catalogQuotaGuarded, Index: 0, ID: "missing", URL: "u"})
	c := serviceStateCommand{Epoch: before.Snapshot.Epoch, ID: before.LastCommand.ID + 1, ExpectedRevision: before.Snapshot.Revision, Kind: 6, CatalogPayload: bad}
	if _, e = b.Apply(c); e == nil {
		t.Fatal("missing ID accepted")
	}
	after, e := b.Export()
	if e != nil || after != before {
		t.Fatal("failed guard mutated", e)
	}
	catalogFaultApply(t, a, serviceCatalogCommand{Action: catalogTruncate})
	catalogFaultApply(t, a, serviceCatalogCommand{Action: catalogBeginDelete})
	catalogFaultApply(t, a, serviceCatalogCommand{Action: catalogRestoreDelete})
	v := catalogFaultQuery(t, b, serviceCatalogQuery{Kind: 0})
	if !v.NilEntries || !v.HasBackup {
		t.Fatal("empty backup restore must nil")
	}
	catalogFaultApply(t, a, serviceCatalogCommand{Action: catalogFinishDelete})
	catalogFaultApply(t, a, serviceCatalogCommand{Action: catalogRemoveID, ID: "missing"})
	v = catalogFaultQuery(t, b, serviceCatalogQuery{Kind: 0})
	if v.NilEntries || v.Count != 0 {
		t.Fatal("remove must nonnil empty")
	}
}
