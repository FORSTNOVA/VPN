package main

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"
)

func TestStoreProbeBoundaryRoundTrip(t *testing.T) {
	file := filepath.Join(t.TempDir(), "probe.db")
	s, err := openStore(file)
	if err != nil {
		t.Fatal(err)
	}
	empty, err := s.loadProbeRecords()
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatal("empty result", empty, err)
	}
	first := probeRecord{Path: `["synthetic'a","节点"]`, Fingerprint: "fake; DROP TABLE probe_results;", Delay: -1, Error: "synthetic\x00error", Measured: 100, Details: `{"warmDelay":5}`}
	second := probeRecord{Path: "not JSON", Fingerprint: "other", Delay: 20, Error: "", Measured: 200, Details: "malformed JSON kept verbatim"}
	for _, record := range []probeRecord{first, second} {
		if err = s.saveProbeRecord(record); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.loadProbeRecords()
	if err != nil || !reflect.DeepEqual(got, []probeRecord{second, first}) {
		t.Fatalf("roundtrip/order: %+v %v", got, err)
	}
	first.Delay = 30
	first.Error = ""
	first.Measured = 300
	first.Details = ""
	if err = s.saveProbeRecord(first); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = openStore(file)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err = s.loadProbeRecords()
	if err != nil || !reflect.DeepEqual(got, []probeRecord{first, second}) {
		t.Fatalf("upsert/reopen: %+v %v", got, err)
	}
}
func TestStoreProbeBoundaryClosedStore(t *testing.T) {
	s := newTestStore(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.saveProbeRecord(probeRecord{}); err == nil {
		t.Fatal("write should fail")
	}
	if _, err := s.loadProbeRecords(); err == nil {
		t.Fatal("read should fail")
	}
}
func TestStoreProbeBoundaryListingWithoutProbe(t *testing.T) {
	s := newTestStore(t)
	a := app{store: s, manualNodes: []manualNode{{Proxy: map[string]any{"type": "http", "name": "synthetic", "server": "synthetic.invalid", "port": 8080}}}}
	index, err := a.currentProxyIndexLocked()
	if err != nil {
		t.Fatal(err)
	}
	proxies, err := compilePrefix([]string{"synthetic"}, index)
	if err != nil {
		t.Fatal(err)
	}
	record := probeRecord{Path: `["synthetic"]`, Fingerprint: probeFingerprint(proxies), Delay: 12, Error: "synthetic-failure", Measured: 123}
	if err = s.saveProbeRecord(record); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	a.listProbeResults(w, nil)
	var body struct{ Results []probeResult }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body.Results) != 1 {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	if got := body.Results[0]; got.Delay != 12 || got.Target != probeTarget || got.Error != "synthetic-failure" {
		t.Fatal("legacy record defaults", got)
	}
	a.manualNodes[0].Proxy["port"] = 8081
	w = httptest.NewRecorder()
	a.listProbeResults(w, nil)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body.Results) != 0 {
		t.Fatal("fingerprint mismatch retained")
	}
	record.Details = "not JSON"
	if err = s.saveProbeRecord(record); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	a.listProbeResults(w, nil)
	if w.Code != 500 {
		t.Fatal("malformed details must remain an error")
	}
	record.Details = ""
	record.Path = "not JSON"
	if err = s.saveProbeRecord(record); err != nil {
		t.Fatal(err)
	}
	// Remove the deliberately bad details on the original path before checking path rejection.
	if err = s.saveProbeRecord(probeRecord{Path: `["synthetic"]`, Fingerprint: "mismatch"}); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	a.listProbeResults(w, nil)
	if w.Code != 200 {
		t.Fatal("bad path should be skipped")
	}
}
