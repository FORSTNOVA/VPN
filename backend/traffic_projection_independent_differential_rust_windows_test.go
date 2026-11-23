//go:build windows && rustcore

package main

import (
	"fmt"
	"math"
	"reflect"
	"testing"
	"time"
)

func TestTrafficProjectionActualIndependentMatrix(t *testing.T) {
	if !rustTrafficProjectionAvailable() {
		t.Fatal("traffic projection native unavailable")
	}
	for op := byte(0); op <= trafficProjectionPagination; op++ {
		for i := 0; i < 128; i++ {
			t.Run(fmt.Sprintf("%d/%d", op, i), func(t *testing.T) {
				q := trafficProjectionRequest{Op: op, Period: []string{"hour", "day", "month", "year", " DAY "}[i%5], Date: []string{"", "2026-03-08", " invalid ", "\xff"}[i%4], Formatted: "2026-03-08", Parsed: i%2 == 0, Offset: "value", Limit: "value", OffsetValue: int64(i - 2), LimitValue: int64(i * 3), OffsetOK: i%3 != 0, LimitOK: i%5 != 0}
				if i%7 == 0 {
					q.Offset = ""
					q.Limit = ""
				}
				for j := 0; j < i%86; j++ {
					m := kernelMetadata{Process: []string{"", " P ", "İ\xff"}[(i+j)%3], ProcessPath: []string{"", ` C:\raw\exe `, "/"}[j%3], Host: " ::1 ", DestinationPort: " 443 ", UID: flexString([]string{"", "-1", " -1 ", "123"}[j%4])}
					c := kernelConnection{Metadata: m, Upload: int64(j), Download: int64(i - j), Rule: " R ", RulePayload: " x ", Chains: []string{"", " N "}, Start: time.Unix(10, 0).Format(time.RFC3339Nano)}
					if j == 0 {
						c.Upload = math.MaxInt64
						c.Download = 1
					}
					q.Rows = append(q.Rows, trafficProjectionConnection(c, time.Unix(int64(9+i%3), 0)))
				}
				g := runGoTrafficProjection(q)
				before := trafficProjectionRecoveries.Load()
				r, ok := runRustTrafficProjection(q)
				if !ok || trafficProjectionRecoveries.Load() != before || !reflect.DeepEqual(g, r) {
					t.Fatalf("projection mismatch handled%v go=%#v rust=%#v", ok, g, r)
				}
			})
		}
	}
}
func TestTrafficProjectionActualIndependentLegacy(t *testing.T) {
	if !rustTrafficProjectionAvailable() {
		t.Fatal("native unavailable")
	}
	t.Setenv("SMARTVPN_RUST_TRAFFIC_PROJECTION", "1")
	n := trafficProjectionRecoveries.Load()
	TestTrafficProjectionIndependentLegacyIdentity(t)
	TestTrafficProjectionIndependentLegacyRows(t)
	TestTrafficProjectionIndependentLegacyDate(t)
	TestTrafficProjectionIndependentLegacyPagination(t)
	if trafficProjectionRecoveries.Load() != n {
		t.Fatal("legacy native recovered")
	}
}
