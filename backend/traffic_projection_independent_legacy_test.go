package main

import (
	"fmt"
	"math"
	"reflect"
	"testing"
	"time"
)

func TestTrafficProjectionIndependentLegacyIdentity(t *testing.T) {
	cases := []struct {
		m                           kernelMetadata
		process, key, label, target string
	}{
		{kernelMetadata{Process: " P ", ProcessPath: ` C:\dir\exe `, Host: " ::1 ", DestinationPort: " 443 "}, "P", `path:C:\dir\exe`, "P", "[::1]:443"},
		{kernelMetadata{ProcessPath: ` C:\dir\exe `}, "exe", `path:C:\dir\exe`, "exe", ""},
		{kernelMetadata{UID: " -1 "}, "", "uid: -1 ", "UID  -1 ", ""},
		{kernelMetadata{Process: "İ\xff", DestinationIP: " 192.0.2.1 "}, "İ\xff", "process:İ\xff", "İ\xff", "192.0.2.1"},
	}
	for _, c := range cases {
		p := connectionProcess(c.m)
		k, l := trafficApp(c.m)
		target := connectionTarget(c.m)
		if p != c.process || k != c.key || l != c.label || target != c.target {
			t.Fatalf("identity %q %q %q %q", p, k, l, target)
		}
	}
	if connectionRule(" ", "kept") == "kept" || connectionRule(" RULE ", " arg ") != "RULE(arg)" || connectionChain([]string{" ", " X ", "Y"}) != "X" {
		t.Fatal("rule/chain")
	}
}
func TestTrafficProjectionIndependentLegacyRows(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	in := make([]kernelConnection, 83)
	for i := range in {
		in[i] = kernelConnection{Upload: 1, Start: now.Add(-time.Millisecond * 1500).Format(time.RFC3339Nano), Metadata: kernelMetadata{Process: fmt.Sprint(i)}}
	}
	in[0].Upload = math.MaxInt64
	in[0].Download = 1
	rows := connectionRows(in, now)
	if len(rows) != 80 || rows[0].Process != "1" || rows[79].Process != "80" || rows[0].DurationMs != 1500 {
		t.Fatalf("overflow stable cap %#v", rows)
	}
	if connectionRows(nil, now) == nil {
		t.Fatal("nil input becomes nonnil rows")
	}
	in = []kernelConnection{{Start: "bad"}, {Start: now.Add(time.Second).Format(time.RFC3339)}}
	r := connectionRows(in, now)
	if r[0].DurationMs != 0 || r[1].DurationMs != 0 {
		t.Fatal("duration clamps")
	}
}
func TestTrafficProjectionIndependentLegacyDate(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 3, 8, 12, 0, 0, 0, loc)
	for _, c := range []struct {
		p, d, w string
		bad     bool
	}{{"day", "", "2026-03-08", false}, {"hour", "2026-03-08T02", "", true}, {"hour", "2026-11-01T01", "2026-11-01T01", false}, {"day", " 2026-03-08", "", true}, {"month", "2026-3", "", true}, {"DAY", "", "", true}} {
		v, e := historyDate(c.p, c.d, now)
		if (e != nil) != c.bad || v != c.w {
			t.Fatalf("date %v => %q %v", c, v, e)
		}
	}
	// Static offset must keep the caller's local calendar rather than UTC.
	now = time.Date(2026, 1, 1, 0, 10, 0, 0, time.FixedZone("synthetic +08", 8*3600))
	v, e := historyDate("day", "", now)
	if e != nil || !reflect.DeepEqual(v, "2026-01-01") {
		t.Fatal(v, e)
	}
}

func TestTrafficProjectionIndependentLegacyPagination(t *testing.T) {
	for _, c := range []struct {
		o, l         string
		wantO, wantL int
		bad          bool
	}{{"", "", 0, 100, false}, {"+1", "0200", 1, 200, false}, {"-0", "1", 0, 1, false}, {" 1", "1", 0, 0, true}, {"1", "0", 0, 0, true}, {"1", "201", 0, 0, true}, {"999999999999999999999999999999", "1", 0, 0, true}} {
		e := &appTrafficEffects{}
		r := e.Call(trafficEffect{Kind: "pagination", Offset: c.o, Limit: c.l})
		if (r.Err != nil) != c.bad {
			t.Fatalf("pagination %v -> %v", c, r.Err)
		}
		if !c.bad && (r.Offset != c.wantO || r.Limit != c.wantL) {
			t.Fatalf("pagination values %v -> %d/%d", c, r.Offset, r.Limit)
		}
	}
}
