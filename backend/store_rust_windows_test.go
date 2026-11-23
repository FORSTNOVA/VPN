//go:build windows && amd64 && ruststore

package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func requireStoreDLL(t *testing.T) *rustStoreDLL {
	t.Helper()
	d := loadRustStoreDLL(os.Getenv("SMARTVPN_TEST_STORE_DLL"))
	if d == nil {
		t.Fatal("complete test store DLL unavailable")
	}
	return d
}
func mustStore(t *testing.T, e error) {
	t.Helper()
	if e != nil {
		t.Fatal(e)
	}
}
func exerciseStoreBackend(t *testing.T, b storeBackend) []any {
	t.Helper()
	var out []any
	at := time.Unix(123, 0)
	key := "key\x00\xff"
	value := "value\x00\xfe"
	v, f, e := b.getValue("missing")
	mustStore(t, e)
	out = append(out, v, f)
	mustStore(t, b.setValue(key, value))
	v, f, e = b.getValue(key)
	mustStore(t, e)
	out = append(out, v, f)
	mustStore(t, b.setValue("empty", ""))
	v, f, e = b.getValue("empty")
	mustStore(t, e)
	out = append(out, v, f)
	mustStore(t, b.upsertNodesAt([]proxyNode{{Name: "n\xff", Type: "ss", Network: "tcp", TLS: true}}, 321))
	r := regionRecord{Name: "r\xff", ExitIP: "203.0.113.1", Country: "JP", Status: regionVerified, Sources: []string{"a", "b\x00\xfe"}, Detail: "detail\xff", VerifiedAt: at}
	mustStore(t, b.saveRegion(r))
	rs, e := b.loadRegions()
	mustStore(t, e)
	out = append(out, rs)
	mustStore(t, b.clearRegions())
	rs, e = b.loadRegions()
	mustStore(t, e)
	out = append(out, rs)
	h := nodeHealth{Name: "h\xff", State: healthHealthy, ConsecutiveFailures: 2, ConsecutiveSuccesses: 3, LatencyMs: math.MaxInt64 - 2, UpdatedAt: at}
	mustStore(t, b.saveHealth(h))
	hs, e := b.loadHealth()
	mustStore(t, e)
	out = append(out, hs)
	mustStore(t, b.clearHealth())
	hs, e = b.loadHealth()
	mustStore(t, e)
	out = append(out, hs)
	for _, x := range []string{"first", "second"} {
		mustStore(t, b.appendSwitchEvent(switchEvent{At: at, Group: "group\x00", FromNode: x, ToNode: "n\xff", Trigger: "synthetic", Evidence: "e\xff"}))
	}
	es, e := b.recentSwitchEvents(0)
	mustStore(t, e)
	out = append(out, es)
	p := probeRecord{Path: "[\xff]\x00", Fingerprint: "f\xfe", Delay: 1<<54 + 3, Error: "e\x00\xff", Measured: 1<<54 + 5, Details: "not json\xff"}
	mustStore(t, b.saveProbeRecord(p))
	ps, e := b.loadProbeRecords()
	mustStore(t, e)
	out = append(out, ps)
	c := kernelConnection{ID: "id\xff", Start: "start", Upload: 1<<54 + 7, Download: 200, Metadata: kernelMetadata{Process: "app\xff", Host: "example.invalid", Network: "tcp", DestinationPort: "443"}, Chains: []string{"n\xff"}}
	now := time.Date(2026, 12, 31, 23, 59, 59, 0, time.FixedZone("CST", 8*3600))
	snap := kernelTraffic{Connections: []kernelConnection{c, {ID: "", Upload: 999}}}
	mustStore(t, b.recordTraffic("session\xff", snap, now))
	mustStore(t, b.recordTraffic("session\xff", snap, now))
	c.Upload += 100
	c.Download += 40
	mustStore(t, b.recordTraffic("session\xff", kernelTraffic{Connections: []kernelConnection{c}}, now.Add(2*time.Second)))
	mustStore(t, b.recordTraffic("new", kernelTraffic{}, now))
	for _, group := range []string{"app", "connection"} {
		report, e := b.readTrafficHistory(trafficHistoryReport{Period: "year", Date: "2026", Group: group, Timezone: "CST", Limit: 1, Rows: []trafficHistoryRow{{ID: "discard"}}, SamplingError: "test\xff"}, "")
		mustStore(t, e)
		out = append(out, report)
	}
	mustStore(t, b.Close())
	mustStore(t, b.Close())
	_, _, e = b.getValue(key)
	if e == nil {
		t.Fatal("read after close allowed")
	}
	input := trafficHistoryReport{Period: "year", Date: "2026", Rows: []trafficHistoryRow{{ID: "discard"}}, UploadTotal: 42}
	report, e := b.readTrafficHistory(input, "")
	if e == nil {
		t.Fatal("closed traffic read allowed")
	}
	out = append(out, report)
	return out
}
func TestRustStoreBackendParity(t *testing.T) {
	d := requireStoreDLL(t)
	dir := t.TempDir()
	g, e := openGoStoreBackend(filepath.Join(dir, "go.db"))
	mustStore(t, e)
	r, e := openRustStoreBackend(d, filepath.Join(dir, "rust.db"))
	mustStore(t, e)
	ga := exerciseStoreBackend(t, g)
	ra := exerciseStoreBackend(t, r)
	if !reflect.DeepEqual(ga, ra) {
		for i := range ga {
			if !reflect.DeepEqual(ga[i], ra[i]) {
				t.Errorf("result %d mismatch Go=%#v Rust=%#v", i, ga[i], ra[i])
			}
		}
	}
	for _, file := range []string{"go.db", "rust.db"} {
		inspect, e := openGoStoreBackend(filepath.Join(dir, file))
		mustStore(t, e)
		var name, protocol string
		var tls, updated int64
		mustStore(t, inspect.db.QueryRow("SELECT name,protocol,tls,updated_at FROM nodes").Scan(&name, &protocol, &tls, &updated))
		if name != "n\xff" || protocol != "ss" || tls != 1 || updated != 321 {
			t.Fatal("node upsert mismatch")
		}
		mustStore(t, inspect.Close())
	}
}

type rustOpenCleanupSpy struct {
	storeBackend
	closes int
	err    error
}

func (s *rustOpenCleanupSpy) Close() error { s.closes++; return s.err }
func TestRustStoreOpenCleanupFailure(t *testing.T) {
	freeErr := errors.New("free failure")
	closeErr := errors.New("close failure")
	for _, ce := range []error{nil, closeErr} {
		b := &rustOpenCleanupSpy{err: ce}
		owner, e := finishRustStoreOpen(b, func() error { return freeErr })
		if owner != nil || e == nil || b.closes != 1 || !errors.Is(e, freeErr) {
			t.Fatal("claim owner lost or close not attempted")
		}
		var retained *rustStoreOpenCleanupError
		if !errors.As(e, &retained) || retained.owner.(*rustOpenCleanupGroup).backend != b {
			t.Fatal("cleanup error must retain owner")
		}
		if ce != nil && !errors.Is(e, ce) {
			t.Fatal("close error lost")
		}
	}
}
func TestRustStoreSelectionAndFailure(t *testing.T) {
	d := requireStoreDLL(t)
	dir := t.TempDir()
	for _, dll := range []string{"", filepath.Join(dir, "missing.dll"), "relative.dll"} {
		t.Setenv("SMARTVPN_RUST_STORE_DLL", dll)
		s, e := openStore(filepath.Join(dir, "fallback.db"))
		mustStore(t, e)
		if _, ok := s.backend.(*goStoreBackend); !ok {
			t.Fatal("preopen fallback missing")
		}
		mustStore(t, s.Close())
	}
	t.Setenv("SMARTVPN_RUST_STORE_DLL", os.Getenv("SMARTVPN_TEST_STORE_DLL"))
	s, e := openStore(filepath.Join(dir, "selected.db"))
	mustStore(t, e)
	if _, ok := s.backend.(*rustStoreBackend); !ok {
		t.Fatal("Rust not selected")
	}
	mustStore(t, s.Close())
	bad := filepath.Join(dir, "bad.db")
	mustStore(t, os.WriteFile(bad, []byte("not a database"), 0600))
	if s, e := openRustStoreBackend(d, bad); e == nil || s != nil {
		t.Fatal("corrupt open succeeded")
	}
	bytes, e := os.ReadFile(bad)
	mustStore(t, e)
	if string(bytes) != "not a database" {
		t.Fatal("corrupt file replaced")
	}
}

func TestRustStoreProfileCopyAndRollback(t *testing.T) {
	dir := t.TempDir()
	original := filepath.Join(dir, "original.db")
	legacy, err := openGoStoreBackend(original)
	mustStore(t, err)
	mustStore(t, legacy.setValue(subscriptionsKey, `[{"label":"旧订阅","enabled":true}]`))
	mustStore(t, legacy.setValue(manualNodesKey, `[{"name":"手动","uri":"ss://fixture"}]`))
	mustStore(t, legacy.upsertNodesAt([]proxyNode{{Name: "旧节点", Type: "ss"}}, 19))
	mustStore(t, legacy.appendSwitchEvent(switchEvent{At: time.Unix(17, 0), ToNode: "旧节点"}))
	mustStore(t, legacy.Close())
	image, err := os.ReadFile(original)
	mustStore(t, err)
	path := filepath.Join(dir, "工作副本.db")
	mustStore(t, os.WriteFile(path, image, 0600))
	t.Setenv("SMARTVPN_RUST_STORE_DLL", os.Getenv("SMARTVPN_TEST_STORE_DLL"))
	t.Setenv("SMARTVPN_STORE_BACKEND", "")
	active, err := openStore(path)
	mustStore(t, err)
	if _, ok := active.backend.(*rustStoreBackend); !ok {
		t.Fatal("upgrade did not select Rust")
	}
	t.Cleanup(func() { _ = active.Close() })
	for key, want := range map[string]string{subscriptionsKey: `[{"label":"旧订阅","enabled":true}]`, manualNodesKey: `[{"name":"手动","uri":"ss://fixture"}]`} {
		got, found, err := active.getValue(key)
		if err != nil || !found || got != want {
			t.Fatalf("lost %s on upgrade: %v", key, err)
		}
	}
	events, err := active.recentSwitchEvents(2)
	if err != nil || len(events) != 1 || events[0].ToNode != "旧节点" {
		t.Fatal("lost old switch history", err)
	}
	mustStore(t, active.appendSwitchEvent(switchEvent{At: time.Unix(23, 0), ToNode: "新节点"}))
	mustStore(t, active.setValue(subscriptionsKey, "updated"))
	mustStore(t, active.Close())
	t.Setenv("SMARTVPN_STORE_BACKEND", "go")
	rolled, err := openStore(path)
	mustStore(t, err)
	defer rolled.Close()
	g, ok := rolled.backend.(*goStoreBackend)
	if !ok {
		t.Fatal("explicit rollback did not select Go")
	}
	v, found, err := rolled.getValue(subscriptionsKey)
	if err != nil || !found || v != "updated" {
		t.Fatal("lost Rust catalog write", err)
	}
	events, err = rolled.recentSwitchEvents(2)
	if err != nil || len(events) != 2 || events[0].ToNode != "新节点" || events[1].ToNode != "旧节点" {
		t.Fatal("history changed on rollback", err)
	}
	var stamp int64
	mustStore(t, g.db.QueryRow("SELECT updated_at FROM nodes WHERE name='旧节点'").Scan(&stamp))
	if stamp != 19 {
		t.Fatal("node changed on rollback")
	}
}

func TestRustStoreSiblingSelection(t *testing.T) {
	dll := os.Getenv("SMARTVPN_TEST_STORE_DLL")
	if filepath.Base(dll) != "smartvpn_store.dll" {
		t.Fatal("expected the staged store DLL")
	}
	t.Setenv("SMARTVPN_RUST_STORE_DLL", "")
	mustStore(t, os.Unsetenv("SMARTVPN_RUST_STORE_DLL"))
	t.Setenv("SMARTVPN_RUST_CORE_LIB", filepath.Join(filepath.Dir(dll), "smartvpn_core.dll"))
	t.Setenv("SMARTVPN_STORE_BACKEND", "")
	s, err := openStore(filepath.Join(t.TempDir(), "sibling.db"))
	mustStore(t, err)
	defer s.Close()
	if _, ok := s.backend.(*rustStoreBackend); !ok {
		t.Fatal("configured library directory did not select Rust")
	}
}
func TestRustStoreWireRejectsMalformed(t *testing.T) {
	for _, b := range [][]byte{{}, {255}, {1, 0, 0}} {
		r := storeReader{b: b}
		_ = r.s()
		if r.done() == nil {
			t.Fatal("malformed bytes accepted")
		}
	}
	w := storeWriter{}
	w.count(100001)
	if w.err == nil {
		t.Fatal("unbounded list")
	}
}

func TestRustStoreSQLFailureAndLegacyBytes(t *testing.T) {
	d := requireStoreDLL(t)
	dir := t.TempDir()
	var got []trafficHistoryReport
	for _, rust := range []bool{false, true} {
		path := filepath.Join(dir, fmt.Sprintf("fixture-%v.db", rust))
		setup, e := openGoStoreBackend(path)
		mustStore(t, e)
		_, e = setup.db.Exec("INSERT INTO settings_kv VALUES('blob',x'80ff00'); INSERT INTO settings_kv VALUES('text',CAST(x'81fe00' AS TEXT)); CREATE TRIGGER fail_event BEFORE INSERT ON switch_events BEGIN SELECT RAISE(ABORT,'synthetic failure'); END; INSERT INTO traffic_hours VALUES('2026','a','app','name','target','tcp','chain',9223372036854775807,0,1,1),('2026','b','app','name','target','tcp','chain',1,0,1,1)")
		mustStore(t, e)
		mustStore(t, setup.Close())
		var b storeBackend
		if rust {
			b, e = openRustStoreBackend(d, path)
		} else {
			b, e = openGoStoreBackend(path)
		}
		mustStore(t, e)
		for key, want := range map[string]string{"blob": "\x80\xff\x00", "text": "\x81\xfe\x00"} {
			v, found, e := b.getValue(key)
			mustStore(t, e)
			if !found || v != want {
				t.Fatalf("legacy %s lost bytes", key)
			}
		}
		e = b.appendSwitchEvent(switchEvent{Trigger: "must fail"})
		if e == nil {
			t.Fatal("trigger did not fail")
		}
		if rust {
			var re *rustStoreError
			if !errors.As(e, &re) {
				t.Fatal("Rust error lost category or backend changed")
			}
		}
		report, e := b.readTrafficHistory(trafficHistoryReport{Period: "year", Date: "2026", Group: "app", UploadTotal: 77, DownloadTotal: 88, Rows: []trafficHistoryRow{{ID: "discard"}}, Limit: 100}, "")
		if e == nil {
			t.Fatal("sum overflow should fail")
		}
		got = append(got, report)
		mustStore(t, b.Close())
		inspect, e := openGoStoreBackend(path)
		mustStore(t, e)
		var n int
		mustStore(t, inspect.db.QueryRow("SELECT count(*) FROM switch_events").Scan(&n))
		if n != 0 {
			t.Fatal("failed event was retried/committed")
		}
		mustStore(t, inspect.Close())
	}
	if !reflect.DeepEqual(got[0], got[1]) {
		t.Fatalf("partial report error mismatch: %#v / %#v", got[0], got[1])
	}
}
func TestRustStoreCachedResultExecutesOnce(t *testing.T) {
	d := requireStoreDLL(t)
	owner, e := openRustStoreBackend(d, filepath.Join(t.TempDir(), "once.db"))
	mustStore(t, e)
	b := owner.(*rustStoreBackend)
	defer b.Close()
	var w storeWriter
	w.event(switchEvent{At: time.Unix(123, 0), Trigger: "once"})
	var id uint64
	status, _, _ := syscall.SyscallN(d.call, uintptr(b.handle), 10, uintptr(unsafe.Pointer(&w.b[0])), uintptr(len(w.b)), uintptr(unsafe.Pointer(&id)))
	runtime.KeepAlive(w.b)
	mustStore(t, storeTransport(status))
	defer d.freeResult(id)
	for i := 0; i < 5; i++ {
		r, e := d.result(id)
		mustStore(t, e)
		mustStore(t, r.err())
		if len(r.payload) != 0 {
			t.Fatal("write payload")
		}
	}
	events, e := b.recentSwitchEvents(100)
	mustStore(t, e)
	if len(events) != 1 {
		t.Fatalf("SQL executed %d times", len(events))
	}
}
func TestRustStoreInputLimitsAndConcurrentClose(t *testing.T) {
	d := requireStoreDLL(t)
	path := filepath.Join(t.TempDir(), "limits.db")
	owner, e := openRustStoreBackend(d, path)
	mustStore(t, e)
	b := owner.(*rustStoreBackend)
	if e = b.upsertNodesAt(make([]proxyNode, 100001), 1); e == nil {
		t.Fatal("oversized node batch accepted")
	}
	if e = b.setValue("too-big", strings.Repeat("x", storeInputLimit)); e == nil {
		t.Fatal("oversized value accepted")
	}
	v, f, e := b.getValue("too-big")
	mustStore(t, e)
	if f || v != "" {
		t.Fatal("oversized write partially applied")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				_ = b.setValue(fmt.Sprintf("%d/%d", i, j), "synthetic")
			}
		}(i)
	}
	mustStore(t, b.Close())
	wg.Wait()
	mustStore(t, b.Close())
	if _, _, e = b.getValue("after"); e == nil {
		t.Fatal("closed store accessed")
	}
	inspect, e := openGoStoreBackend(path)
	mustStore(t, e)
	defer inspect.Close()
	var n int
	mustStore(t, inspect.db.QueryRow("SELECT count(*) FROM nodes").Scan(&n))
	if n != 0 {
		t.Fatal("oversized batch partially applied")
	}
}
func TestRustStoreClosedOwnerErrors(t *testing.T) {
	d := requireStoreDLL(t)
	dir := t.TempDir()
	for _, rust := range []bool{false, true} {
		var b storeBackend
		var e error
		path := filepath.Join(dir, fmt.Sprintf("closed-%v.db", rust))
		if rust {
			b, e = openRustStoreBackend(d, path)
		} else {
			b, e = openGoStoreBackend(path)
		}
		mustStore(t, e)
		mustStore(t, b.Close())
		for _, call := range []func() error{func() error { return b.upsertNodesAt(nil, 0) }, func() error { return b.saveRegion(regionRecord{}) }, b.clearRegions, func() error { return b.saveHealth(nodeHealth{}) }, b.clearHealth, func() error { return b.appendSwitchEvent(switchEvent{}) }, func() error { return b.setValue("k", "v") }, func() error { return b.saveProbeRecord(probeRecord{}) }, func() error { return b.recordTraffic("s", kernelTraffic{}, time.Time{}) }} {
			if call() == nil {
				t.Fatal("closed write succeeded")
			}
		}
		regions, e := b.loadRegions()
		if e == nil || regions != nil {
			t.Fatal("closed regions return contract")
		}
		health, e := b.loadHealth()
		if e == nil || health != nil {
			t.Fatal("closed health return contract")
		}
		events, e := b.recentSwitchEvents(1)
		if e == nil || events != nil {
			t.Fatal("closed events return contract")
		}
		probes, e := b.loadProbeRecords()
		if e == nil || probes != nil {
			t.Fatal("closed probes return contract")
		}
		v, f, e := b.getValue("k")
		if e == nil || f || v != "" {
			t.Fatal("closed value contract")
		}
	}
}

func TestRustStorePendingCleanupFailure(t *testing.T) {
	primary := errors.New("decode failed")
	attempts := 0
	failed := true
	e := cleanupRustPendingResult(primary, func() error {
		attempts++
		if failed {
			return errors.New("pending close failed")
		}
		return nil
	})
	var retained *rustStoreOpenCleanupError
	if !errors.Is(e, primary) || !errors.As(e, &retained) || attempts != 1 {
		t.Fatal("pending owner/error lost")
	}
	if _, ok := retained.owner.(*rustPendingResultCleanup); !ok {
		t.Fatal("pending owner exposes incorrect lifecycle")
	}
	failed = false
	mustStore(t, retained.Close())
	mustStore(t, retained.Close())
	if attempts != 2 {
		t.Fatal("pending cleanup replayed after successful free")
	}
}

func TestRustStoreEnvelopeValidation(t *testing.T) {
	good := make([]byte, 20)
	binary.LittleEndian.PutUint32(good[8:], 1)
	if _, e := decodeRustStoreReply(good); e != nil {
		t.Fatal(e)
	}
	for _, mutate := range []func([]byte){func(b []byte) { b[0] = 5 }, func(b []byte) { b[8] = 3 }, func(b []byte) { b[12] = 1 }, func(b []byte) { b[8] = 0 }, func(b []byte) { b[4] = 1 }, func(b []byte) { b[0] = 2 }} {
		b := append([]byte{}, good...)
		mutate(b)
		if _, e := decodeRustStoreReply(b); e == nil {
			t.Fatal("malformed envelope accepted")
		}
	}
	if _, e := decodeRustStoreReply(good[:19]); e == nil {
		t.Fatal("short envelope")
	}
}
