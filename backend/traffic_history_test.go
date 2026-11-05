package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func historyReport(t *testing.T, s *store, period, date, group, appKey string, limit, offset int) trafficHistoryReport {
	t.Helper()
	r, err := s.readTrafficHistory(trafficHistoryReport{Period: period, Date: date, Group: group, Limit: limit, Offset: offset}, appKey)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestTrafficHistoryPersistsDeltasAcrossCalendarBoundaries(t *testing.T) {
	file := filepath.Join(t.TempDir(), "traffic.db")
	s, err := openStore(file)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 12, 31, 23, 59, 59, 0, time.FixedZone("CST", 8*3600))
	c := kernelConnection{ID: "one", Start: "2026-12-31T23:00:00+08:00", Upload: 100, Download: 200,
		Metadata: kernelMetadata{Process: "browser.exe", ProcessPath: "C:/app/browser.exe", Host: "example.com", DestinationPort: "443"}}
	record := func(at time.Time) {
		t.Helper()
		if err := s.recordTraffic("session1", kernelTraffic{Connections: []kernelConnection{c}}, at); err != nil {
			t.Fatal(err)
		}
	}
	record(now)
	record(now) // Identical snapshots must never double count.
	c.Upload = 150
	c.Download = 260
	record(now.Add(2 * time.Second))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = openStore(file)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	record(now.Add(3 * time.Second)) // A restarted collector reuses the durable checkpoint.
	for _, pair := range [][2]string{{"hour", "2026-12-31T23"}, {"day", "2026-12-31"}, {"month", "2026-12"}, {"year", "2026"}} {
		r := historyReport(t, s, pair[0], pair[1], "app", "", 100, 0)
		if r.UploadTotal != 100 || r.DownloadTotal != 200 || r.ConnectionCount != 1 {
			t.Fatalf("%s: %+v", pair, r)
		}
	}
	r := historyReport(t, s, "year", "2027", "connection", "path:C:/app/browser.exe", 100, 0)
	if r.UploadTotal != 50 || r.DownloadTotal != 60 || len(r.Rows) != 1 || r.Rows[0].Target != "example.com:443" {
		t.Fatalf("next year: %+v", r)
	}
	// Restarted kernel, reused connection ID, and a reset in the same connection.
	c.Upload = 10
	c.Download = 20
	if err := s.recordTraffic("session2", kernelTraffic{Connections: []kernelConnection{c}}, now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	c.Upload = 3
	c.Download = 4
	if err := s.recordTraffic("session2", kernelTraffic{Connections: []kernelConnection{c}}, now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	r = historyReport(t, s, "year", "2027", "app", "", 100, 0)
	if r.UploadTotal != 63 || r.DownloadTotal != 84 || r.ConnectionCount != 2 {
		t.Fatalf("reset: %+v", r)
	}
}

func TestTrafficHistoryAtomicRetryAndFullPagination(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	connections := make([]kernelConnection, 135)
	for i := range connections {
		connections[i] = kernelConnection{ID: fmt.Sprint(i), Upload: 10, Download: 20, Metadata: kernelMetadata{Process: "test.exe"}}
	}
	// Failure after the hourly increment must also roll that increment back.
	_, err := s.db.Exec(`CREATE TRIGGER fail_checkpoint BEFORE INSERT ON traffic_checkpoints BEGIN SELECT RAISE(ABORT,'test failure'); END;`)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.recordTraffic("session", kernelTraffic{Connections: connections}, now); err == nil {
		t.Fatal("expected transaction failure")
	}
	if r := historyReport(t, s, "day", "2026-11-05", "app", "", 100, 0); r.UploadTotal != 0 {
		t.Fatalf("partial write: %+v", r)
	}
	if _, err = s.db.Exec("DROP TRIGGER fail_checkpoint"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := s.recordTraffic("session", kernelTraffic{Connections: connections}, now); err != nil {
			t.Fatal(err)
		}
	}
	r := historyReport(t, s, "hour", "2026-11-05T10", "connection", "", 100, 0)
	if r.RowCount != 135 || r.UploadTotal != 1350 || r.DownloadTotal != 2700 || len(r.Rows) != 100 || !r.HasMore {
		t.Fatalf("first page: %+v", r)
	}
	next := historyReport(t, s, "day", "2026-11-05", "connection", "", 100, 100)
	if len(next.Rows) != 35 || next.HasMore || next.UploadTotal != 1350 {
		t.Fatalf("second page: %+v", next)
	}
	seen := map[string]bool{}
	for _, row := range r.Rows {
		seen[row.ID] = true
	}
	for _, row := range next.Rows {
		if seen[row.ID] {
			t.Fatal("pagination duplicated a connection")
		}
	}
	r = historyReport(t, s, "month", "2026-11", "app", "", 100, 0)
	if len(r.Rows) != 1 || r.Rows[0].Connections != 135 {
		t.Fatalf("app aggregation: %+v", r)
	}
}

func TestTrafficHistoryAPIValidationAndOfflineRead(t *testing.T) {
	a := &app{store: newTestStore(t)}
	routes := a.routes("test-token")
	for _, query := range []string{"period=week", "period=hour&date=2026-11-05", "period=day&date=2026-02-30", "group=bad", "limit=0", "offset=-1", "limit=201", "date=2026%25"} {
		r := httptest.NewRequest("GET", "/api/traffic/history?"+query, nil)
		r.Header.Set("Authorization", "Bearer test-token")
		w := httptest.NewRecorder()
		routes.ServeHTTP(w, r)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d", query, w.Code)
		}
	}
	w := httptest.NewRecorder()
	routes.ServeHTTP(w, httptest.NewRequest("GET", "/api/traffic/history", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatal("history must require authentication")
	}
	r := httptest.NewRequest("GET", "/api/traffic/history?period=year&date=2026", nil)
	r.Header.Set("Authorization", "Bearer test-token")
	w = httptest.NewRecorder()
	routes.ServeHTTP(w, r)
	var report trafficHistoryReport
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &report) != nil || report.Rows == nil {
		t.Fatalf("offline history: %s", w.Body.String())
	}
}

func TestTrafficHistoryBackgroundCollection(t *testing.T) {
	stub := newControllerStub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != connectionsPath {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"connections":[{"id":"background","upload":42,"download":84,"metadata":{"uid":10001}}]}`)
	})
	a := stub.app(t)
	runningKernel(t, a)
	a.store = newTestStore(t)
	a.started = time.Now()
	a.mu.Lock()
	a.startTrafficHistoryLocked()
	a.mu.Unlock()
	defer func() { a.mu.Lock(); a.stopTrafficHistoryLocked(); a.mu.Unlock() }()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		a.mu.Lock()
		r := historyReport(t, a.store, "day", time.Now().Format("2006-01-02"), "app", "", 100, 0)
		a.mu.Unlock()
		if r.UploadTotal == 42 && len(r.Rows) == 1 && r.Rows[0].App == "UID 10001" {
			return
		}
	}
	t.Fatal("collector did not record traffic without a UI request")
}
