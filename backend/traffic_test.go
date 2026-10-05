package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRateFor(t *testing.T) {
	base := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name             string
		previous         trafficSample
		current          trafficSample
		wantUp, wantDown int64
	}{
		{
			name:    "the first reading has nothing to compare against",
			current: trafficSample{at: base, up: 1000, down: 2000},
		},
		{
			name:     "a delta over elapsed time",
			previous: trafficSample{at: base, up: 1000, down: 2000},
			current:  trafficSample{at: base.Add(2 * time.Second), up: 3000, down: 6000},
			wantUp:   1000, wantDown: 2000,
		},
		{
			name:     "a restarted kernel counts from zero again",
			previous: trafficSample{at: base, up: 9_000_000, down: 9_000_000},
			current:  trafficSample{at: base.Add(time.Second), up: 10, down: 20},
		},
		{
			name:     "a clock that did not move cannot produce a rate",
			previous: trafficSample{at: base, up: 10, down: 20},
			current:  trafficSample{at: base, up: 20, down: 40},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			up, down := rateFor(tc.previous, tc.current)
			if up != tc.wantUp || down != tc.wantDown {
				t.Fatalf("got (%d, %d), want (%d, %d)", up, down, tc.wantUp, tc.wantDown)
			}
		})
	}
}

func TestConnectionRowsReadTheKernelView(t *testing.T) {
	start := time.Now().Add(-5 * time.Second)
	connections := []kernelConnection{
		{
			Upload: 100, Download: 200, Start: start.Format(time.RFC3339Nano),
			Chains: []string{"🇯🇵 日本V3 B|中转|x1", "SmartVPN"}, Rule: "DomainSuffix", RulePayload: "googlevideo.com",
			Metadata: kernelMetadata{
				Network: "tcp", Type: "HTTP", Host: "rr1.googlevideo.com",
				DestinationIP: "1.2.3.4", DestinationPort: "443",
				Process: "chrome.exe", ProcessPath: `C:\Program Files\Google\Chrome\chrome.exe`,
			},
		},
		{
			Upload: 4000, Download: 80000, Start: start.Format(time.RFC3339Nano),
			Chains: []string{"DIRECT"}, Rule: "Match",
			Metadata: kernelMetadata{
				Network: "udp", Type: "QUIC", DestinationIP: "9.9.9.9", DestinationPort: "53",
				ProcessPath: `C:\Windows\System32\dns.exe`,
			},
		},
		{
			Upload: 5, Download: 5, Start: "not a timestamp",
			Chains:   []string{},
			Metadata: kernelMetadata{Network: "tcp", Host: "example.com"},
		},
	}
	rows := connectionRows(connections, time.Now())
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(rows))
	}
	// The heaviest connection is first, so the list answers "what is using this".
	if rows[0].Target != "9.9.9.9:53" || rows[0].Process != "dns.exe" {
		t.Fatalf("unexpected first row: %+v", rows[0])
	}
	if rows[0].Chain != "DIRECT" || rows[0].Rule != "Match" {
		t.Fatalf("the outbound and rule must be reported: %+v", rows[0])
	}
	if rows[0].DurationMs < 4000 {
		t.Fatalf("duration = %d, want roughly five seconds", rows[0].DurationMs)
	}

	proxied := rows[1]
	if proxied.Process != "chrome.exe" {
		t.Fatalf("process = %q", proxied.Process)
	}
	if proxied.Target != "rr1.googlevideo.com:443" {
		t.Fatalf("a known host is more useful than its address: %q", proxied.Target)
	}
	// The last link of the chain is the node that carried the connection.
	if proxied.Chain != "🇯🇵 日本V3 B|中转|x1" {
		t.Fatalf("chain = %q", proxied.Chain)
	}
	if proxied.Rule != "DomainSuffix(googlevideo.com)" {
		t.Fatalf("rule = %q", proxied.Rule)
	}
	if proxied.DurationMs < 4000 {
		t.Fatalf("duration = %d", proxied.DurationMs)
	}

	// An unparsable start time costs the duration, not the row.
	if rows[2].DurationMs != 0 || rows[2].Process != "" || rows[2].Chain != "" {
		t.Fatalf("unexpected third row: %+v", rows[2])
	}
	if rows[2].Target != "example.com" {
		t.Fatalf("target = %q, want the host without a port", rows[2].Target)
	}
}

func TestConnectionRowsAreCappedAndKeepTheHeaviest(t *testing.T) {
	connections := make([]kernelConnection, 0, maxConnectionRows*2)
	for i := 0; i < maxConnectionRows*2; i++ {
		connections = append(connections, kernelConnection{
			Upload:   int64(i),
			Metadata: kernelMetadata{Host: fmt.Sprintf("host%d.example.com", i)},
		})
	}
	rows := connectionRows(connections, time.Now())
	if len(rows) != maxConnectionRows {
		t.Fatalf("got %d rows, want the cap of %d", len(rows), maxConnectionRows)
	}
	heaviest := fmt.Sprintf("host%d.example.com", maxConnectionRows*2-1)
	if rows[0].Target != heaviest {
		t.Fatalf("the heaviest connection must survive the cap, got %q", rows[0].Target)
	}
}

func TestConnectionProcessTrimsAPath(t *testing.T) {
	cases := map[string]string{
		`C:\Program Files\App\app.exe`: "app.exe",
		`/usr/bin/curl`:                "curl",
		`app.exe`:                      "app.exe",
		``:                             "",
	}
	for value, want := range cases {
		got := connectionProcess(kernelMetadata{ProcessPath: value})
		if got != want {
			t.Errorf("connectionProcess(%q) = %q, want %q", value, got, want)
		}
	}
	// A named process wins over the path.
	if got := connectionProcess(kernelMetadata{Process: "curl.exe", ProcessPath: `C:\x\y.exe`}); got != "curl.exe" {
		t.Errorf("got %q, want the reported process name", got)
	}
}

func TestFlexStringReadsBothPortForms(t *testing.T) {
	for _, body := range []string{`{"destinationPort":"443"}`, `{"destinationPort":443}`} {
		var metadata kernelMetadata
		if err := json.Unmarshal([]byte(body), &metadata); err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		if got := string(metadata.DestinationPort); got != "443" {
			t.Errorf("%s gave port %q", body, got)
		}
	}
	var metadata kernelMetadata
	if err := json.Unmarshal([]byte(`{"destinationPort":null}`), &metadata); err != nil {
		t.Fatalf("null port: %v", err)
	}
	if got := string(metadata.DestinationPort); got != "" {
		t.Errorf("a null port must read as empty, got %q", got)
	}
}

func TestTrafficWithoutAKernel(t *testing.T) {
	a := &app{}
	recorder := httptest.NewRecorder()
	a.traffic(recorder, httptest.NewRequest(http.MethodGet, "/api/traffic", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("code = %d", recorder.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["available"] != false || body["reason"] == "" {
		t.Fatalf("an idle service must report why monitoring is unavailable: %v", body)
	}
}

func TestTrafficReportsTotalsRatesAndRows(t *testing.T) {
	call := 0
	stub := newControllerStub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != connectionsPath {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		call++
		up := int64(1000 + call*20000)
		down := int64(5000 + call*40000)
		_, _ = fmt.Fprintf(w, `{"uploadTotal":%d,"downloadTotal":%d,"connections":[
			{"id":"a","upload":10,"download":20,"start":"%s","chains":["node-a","SmartVPN"],
			 "rule":"Match","metadata":{"network":"tcp","type":"HTTP","host":"example.com","destinationPort":"443","process":"curl.exe"}}]}`,
			up, down, time.Now().Format(time.RFC3339Nano))
	})
	a := stub.app(t)
	runningKernel(t, a)

	first := trafficBody(t, a)
	if first["available"] != true {
		t.Fatalf("a running kernel must be monitored: %v", first)
	}
	if first["connectionCount"] != float64(1) || first["shownCount"] != float64(1) {
		t.Fatalf("unexpected counts: %v", first)
	}
	// The first reading establishes the baseline, so it has no rate yet.
	if first["uploadRate"] != float64(0) || first["downloadRate"] != float64(0) {
		t.Fatalf("the first reading must not invent a rate: %v", first)
	}
	rows, ok := first["connections"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("unexpected rows: %v", first["connections"])
	}
	row := rows[0].(map[string]any)
	if row["process"] != "curl.exe" || row["chain"] != "node-a" || row["target"] != "example.com:443" {
		t.Fatalf("unexpected row: %v", row)
	}

	time.Sleep(30 * time.Millisecond)
	second := trafficBody(t, a)
	if second["uploadRate"].(float64) <= 0 || second["downloadRate"].(float64) <= 0 {
		t.Fatalf("the second reading must report a rate: %v", second)
	}
	if second["uploadTotal"] != float64(1000+2*20000) {
		t.Fatalf("uploadTotal = %v", second["uploadTotal"])
	}
}

func TestTrafficReportsAnUnreadableKernel(t *testing.T) {
	stub := newControllerStub(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	a := stub.app(t)
	runningKernel(t, a)

	body := trafficBody(t, a)
	if body["available"] != false || body["reason"] == "" {
		t.Fatalf("an unreadable table must be reported: %v", body)
	}
	// The baseline is dropped, so a later reading cannot be mistaken for a burst.
	if !a.lastTraffic.at.IsZero() {
		t.Fatalf("the baseline should have been cleared: %+v", a.lastTraffic)
	}
}

func trafficBody(t *testing.T, a *app) map[string]any {
	t.Helper()
	recorder := httptest.NewRecorder()
	a.traffic(recorder, httptest.NewRequest(http.MethodGet, "/api/traffic", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("code = %d: %s", recorder.Code, recorder.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body
}
