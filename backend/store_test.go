package main

import (
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *store {
	t.Helper()
	opened, err := openStore(filepath.Join(t.TempDir(), "smartvpn.db"))
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = opened.Close() })
	return opened
}

func TestStoreRegionRoundTrip(t *testing.T) {
	s := newTestStore(t)
	verifiedAt := time.Now().Truncate(time.Second)
	record := regionRecord{
		Name: "香港 01", ExitIP: "1.2.3.4", Country: "JP", Status: regionVerified,
		Sources: []string{"geojs.io", "ipwho.is"}, Detail: "两个数据源一致",
		VerifiedAt: verifiedAt,
	}
	if err := s.saveRegion(record); err != nil {
		t.Fatalf("saveRegion: %v", err)
	}
	loaded, err := s.loadRegions()
	if err != nil {
		t.Fatalf("loadRegions: %v", err)
	}
	got, ok := loaded["香港 01"]
	if !ok {
		t.Fatalf("the record did not survive the round trip: %+v", loaded)
	}
	if got.ExitIP != "1.2.3.4" || got.Country != "JP" || got.Status != regionVerified {
		t.Fatalf("unexpected record: %+v", got)
	}
	if len(got.Sources) != 2 || got.Sources[0] != "geojs.io" || got.Sources[1] != "ipwho.is" {
		t.Fatalf("sources = %v, want both service names", got.Sources)
	}
	if !got.VerifiedAt.Equal(verifiedAt) {
		t.Fatalf("verifiedAt = %v, want %v", got.VerifiedAt, verifiedAt)
	}

	// Re-saving a node must update it in place rather than duplicate it.
	record.Country = "US"
	record.Status = regionConflict
	record.Sources = nil
	if err := s.saveRegion(record); err != nil {
		t.Fatal(err)
	}
	reloaded, err := s.loadRegions()
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded) != 1 {
		t.Fatalf("got %d records, want 1", len(reloaded))
	}
	if reloaded["香港 01"].Country != "US" || reloaded["香港 01"].Status != regionConflict {
		t.Fatalf("the second save did not win: %+v", reloaded["香港 01"])
	}
	if reloaded["香港 01"].Sources != nil {
		t.Fatalf("cleared sources should stay empty, got %v", reloaded["香港 01"].Sources)
	}
}

func TestStoreHealthRoundTrip(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().Truncate(time.Second)
	record := nodeHealth{
		Name: "日本 02", State: healthCooldown, ConsecutiveFailures: 3,
		ConsecutiveSuccesses: 0, LatencyMs: 142,
		LastSuccessAt: now.Add(-2 * time.Minute), CooldownUntil: now.Add(time.Minute),
		UpdatedAt: now,
	}
	if err := s.saveHealth(record); err != nil {
		t.Fatalf("saveHealth: %v", err)
	}
	loaded, err := s.loadHealth()
	if err != nil {
		t.Fatalf("loadHealth: %v", err)
	}
	got, ok := loaded["日本 02"]
	if !ok {
		t.Fatalf("the health record did not survive: %+v", loaded)
	}
	if got.State != healthCooldown || got.ConsecutiveFailures != 3 || got.LatencyMs != 142 {
		t.Fatalf("unexpected record: %+v", got)
	}
	if !got.CooldownUntil.Equal(record.CooldownUntil) {
		t.Fatalf("cooldownUntil = %v, want %v", got.CooldownUntil, record.CooldownUntil)
	}
	if !got.LastSuccessAt.Equal(record.LastSuccessAt) {
		t.Fatalf("lastSuccessAt = %v, want %v", got.LastSuccessAt, record.LastSuccessAt)
	}

	if err := s.clearHealth(); err != nil {
		t.Fatal(err)
	}
	emptied, err := s.loadHealth()
	if err != nil {
		t.Fatal(err)
	}
	if len(emptied) != 0 {
		t.Fatalf("got %d records after clearing, want 0", len(emptied))
	}
}

func TestStoreHealthHandlesUnsetTimes(t *testing.T) {
	s := newTestStore(t)
	if err := s.saveHealth(nodeHealth{Name: "fresh", State: healthHealthy}); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.loadHealth()
	if err != nil {
		t.Fatal(err)
	}
	got := loaded["fresh"]
	if !got.LastSuccessAt.IsZero() || !got.CooldownUntil.IsZero() {
		t.Fatalf("unset timestamps must stay zero, got %+v", got)
	}
	if got.cooldownActive(time.Now()) {
		t.Fatal("a record with no cooldown must not read as cooling")
	}
}

func TestStoreSwitchEventsOrderAndLimit(t *testing.T) {
	s := newTestStore(t)
	base := time.Now().Truncate(time.Second)
	for i := 0; i < 5; i++ {
		event := switchEvent{
			At: base.Add(time.Duration(i) * time.Second), Group: "SmartVPN",
			FromNode: "香港 01", ToNode: "日本 02", Trigger: "unhealthy",
			Evidence: "probe failed",
		}
		if err := s.appendSwitchEvent(event); err != nil {
			t.Fatalf("appendSwitchEvent: %v", err)
		}
	}
	events, err := s.recentSwitchEvents(2)
	if err != nil {
		t.Fatalf("recentSwitchEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
	if !events[0].At.After(events[1].At) {
		t.Fatalf("events must come back newest first: %v", events)
	}
	if events[0].FromNode != "香港 01" || events[0].Trigger != "unhealthy" {
		t.Fatalf("unexpected event: %+v", events[0])
	}

	all, err := s.recentSwitchEvents(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 5 {
		t.Fatalf("a zero limit should use the default, got %d events", len(all))
	}
}

func TestStoreHealthParams(t *testing.T) {
	s := newTestStore(t)
	params, err := s.loadHealthParams()
	if err != nil {
		t.Fatalf("loadHealthParams: %v", err)
	}
	if params != defaultHealthParams() {
		t.Fatalf("a fresh database must yield the documented defaults, got %+v", params)
	}

	custom := healthParams{
		ConnectTimeoutMs: 2500, PatrolIntervalSec: 45, FailureThreshold: 3,
		CooldownSec: 120, MinDwellSec: 15, RecoverySuccesses: 2,
	}
	if err := s.saveHealthParams(custom); err != nil {
		t.Fatalf("saveHealthParams: %v", err)
	}
	loaded, err := s.loadHealthParams()
	if err != nil {
		t.Fatal(err)
	}
	if loaded != custom {
		t.Fatalf("got %+v, want %+v", loaded, custom)
	}

	// Out-of-range values are clamped on the way out.
	if err := s.setValue("health_params", `{"patrolIntervalSec":1,"failureThreshold":500}`); err != nil {
		t.Fatal(err)
	}
	clamped, err := s.loadHealthParams()
	if err != nil {
		t.Fatal(err)
	}
	if clamped.PatrolIntervalSec != 10 || clamped.FailureThreshold != 10 {
		t.Fatalf("clamping did not apply: %+v", clamped)
	}
}

func TestStoreHealthParamsRecoversFromGarbage(t *testing.T) {
	s := newTestStore(t)
	if err := s.setValue("health_params", "not json"); err != nil {
		t.Fatal(err)
	}
	params, err := s.loadHealthParams()
	if err == nil {
		t.Fatal("unreadable stored parameters must be reported")
	}
	if params != defaultHealthParams() {
		t.Fatalf("the defaults must be used as a fallback, got %+v", params)
	}
}

func TestStoreUpsertNodes(t *testing.T) {
	s := newTestStore(t)
	nodes := []proxyNode{
		{Name: "香港 01", Type: "vmess", Network: "ws", TLS: true},
		{Name: "日本 02", Type: "trojan"},
	}
	if err := s.upsertNodes(nodes); err != nil {
		t.Fatalf("upsertNodes: %v", err)
	}
	// Re-running the same subscription must not fail or duplicate.
	nodes[1].Network = "grpc"
	if err := s.upsertNodes(nodes); err != nil {
		t.Fatalf("second upsertNodes: %v", err)
	}

	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM nodes`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("got %d node rows, want 2", count)
	}
	var network string
	if err := s.db.QueryRow(`SELECT network FROM nodes WHERE name = ?`, "日本 02").Scan(&network); err != nil {
		t.Fatal(err)
	}
	if network != "grpc" {
		t.Fatalf("network = %q, want the updated value", network)
	}
}

func TestStoreKeyValue(t *testing.T) {
	s := newTestStore(t)
	if _, found, err := s.getValue("missing"); err != nil || found {
		t.Fatalf("a missing key must report not-found, got found=%v err=%v", found, err)
	}
	if err := s.setValue("k", "first"); err != nil {
		t.Fatal(err)
	}
	if err := s.setValue("k", "second"); err != nil {
		t.Fatal(err)
	}
	value, found, err := s.getValue("k")
	if err != nil || !found {
		t.Fatalf("getValue: found=%v err=%v", found, err)
	}
	if value != "second" {
		t.Fatalf("value = %q, want the latest write", value)
	}
}

func TestStoreReopenKeepsData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "smartvpn.db")
	first, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	record := regionRecord{Name: "n", Country: "JP", Status: regionVerified, VerifiedAt: time.Now().Truncate(time.Second)}
	if err := first.saveRegion(record); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := openStore(path)
	if err != nil {
		t.Fatalf("reopening the database failed: %v", err)
	}
	defer func() { _ = second.Close() }()
	loaded, err := second.loadRegions()
	if err != nil {
		t.Fatal(err)
	}
	if loaded["n"].Country != "JP" {
		t.Fatalf("data did not survive a reopen: %+v", loaded)
	}
}

func TestStoreCloseIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	var nilStore *store
	if err := nilStore.Close(); err != nil {
		t.Fatalf("closing a nil store must be safe, got %v", err)
	}
}
