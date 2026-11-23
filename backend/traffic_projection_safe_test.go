package main

import (
	"encoding/binary"
	"math"
	"reflect"
	"testing"
	"time"
)

func TestTrafficProjectionServiceSafePureContracts(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("synthetic", 9*3600))
	cs := make([]kernelConnection, 83)
	for i := range cs {
		cs[i] = kernelConnection{Upload: 1, Metadata: kernelMetadata{Process: string(rune('A' + i)), Host: "\u0085::1\u2003", DestinationPort: flexString(" 443 ")}, Chains: []string{"\u2003", " DIRECT ", "ignored"}, Start: now.Add(-1500 * time.Microsecond).Format(time.RFC3339Nano)}
	}
	cs[0].Upload = math.MaxInt64
	cs[0].Download = 1
	q := trafficProjectionRequest{Op: trafficProjectionRows}
	for _, c := range cs {
		q.Rows = append(q.Rows, trafficProjectionConnection(c, now))
	}
	r := runGoTrafficProjection(q)
	if len(r.Rows) != 80 || r.Rows[0].Process != "B" || r.Rows[0].Target != "[::1]:443" || r.Rows[0].Chain != "DIRECT" || r.Rows[0].DurationMs != 1 {
		t.Fatalf("rows contract %#v", r.Rows)
	}
	if z := runGoTrafficProjection(trafficProjectionRequest{Op: trafficProjectionRows}); z.Rows == nil || len(z.Rows) != 0 {
		t.Fatal("nil empty rows")
	}
	f := trafficProjectionMetadata(kernelMetadata{ProcessPath: ` C:\Dir\app.exe `, UID: " -1 "})
	a := runGoTrafficProjection(trafficProjectionRequest{Op: trafficProjectionApp, Rows: []trafficProjectionFacts{f}})
	if a.Text != `path:C:\Dir\app.exe` || a.Other != "app.exe" {
		t.Fatal(a)
	}
	f = trafficProjectionMetadata(kernelMetadata{UID: " -1 "})
	a = runGoTrafficProjection(trafficProjectionRequest{Op: trafficProjectionApp, Rows: []trafficProjectionFacts{f}})
	if a.Text != "uid: -1 " || a.Other != "UID  -1 " {
		t.Fatal(a)
	}
}
func TestTrafficProjectionServiceSafeDatePagination(t *testing.T) {
	now := time.Date(2024, 2, 29, 23, 0, 0, 0, time.FixedZone("synthetic", -7*3600))
	if s, e := historyDate("hour", "", now); e != nil || s != "2024-02-29T23" {
		t.Fatal(s, e)
	}
	for _, v := range []struct{ p, d string }{{" day", ""}, {"month", "2024-2"}, {"day", "2023-02-29"}, {"year", " 2024"}} {
		if _, e := historyDate(v.p, v.d, now); e == nil {
			t.Fatal(v)
		}
	}
	for _, v := range []struct {
		o, l         string
		wantO, wantL int
		bad          bool
	}{{"", "", 0, 100, false}, {"+2", "0200", 2, 200, false}, {" 2", "1", 0, 0, true}, {"0", "0", 0, 0, true}, {"bad", "bad", 0, 0, true}, {"9223372036854775808", "1", 0, 0, true}} {
		o, l, e := projectionPagination(v.o, v.l)
		if (e != nil) != v.bad || (!v.bad && (o != v.wantO || l != v.wantL)) {
			t.Fatalf("%+v got %d %d %v", v, o, l, e)
		}
	}
}
func TestTrafficProjectionServiceSafeWireRejects(t *testing.T) {
	good := []byte{0}
	good = projectionPutString(good, "")
	good = projectionPutString(good, "")
	good = binary.LittleEndian.AppendUint64(good, 0)
	good = binary.LittleEndian.AppendUint64(good, 0)
	good = append(good, 1)
	good = binary.LittleEndian.AppendUint32(good, 0)
	r, err := trafficProjectionDecode(good)
	if err != nil || !reflect.DeepEqual(r.Rows, []connectionRow{}) {
		t.Fatal(r, err)
	}
	for i := 0; i < len(good); i++ {
		if _, err := trafficProjectionDecode(good[:i]); err == nil {
			t.Fatalf("accepted truncated frame %d", i)
		}
	}
	if _, err := trafficProjectionDecode(append(good, 0)); err == nil {
		t.Fatal("accepted trailing bytes")
	}
	q := trafficProjectionRequest{Rows: make([]trafficProjectionFacts, 100001)}
	if _, err := trafficProjectionEncode(q); err == nil {
		t.Fatal("row bound missing")
	}
}
