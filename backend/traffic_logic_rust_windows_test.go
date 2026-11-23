//go:build windows && amd64 && rustcore

package main

import (
	"math"
	"math/rand"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestRustTrafficLogicParity(t *testing.T) {
	expected := os.Getenv("SMARTVPN_TEST_EXPECT_TRAFFIC_RUST") == "1"
	if trafficRustAvailable() != expected {
		t.Fatal("traffic DLL availability mismatch")
	}
	r := rand.New(rand.NewSource(20261116))
	now := time.Unix(1700000000, 0)
	values := []int64{math.MinInt64, math.MaxInt64, -1, 0, 1, 10}
	for _, current := range values {
		for _, previous := range values {
			if counterDelta(current, previous) != counterDeltaGo(current, previous) {
				t.Fatal("boundary delta")
			}
		}
	}
	for i := 0; i < 10000; i++ {
		prev := trafficSample{at: now, up: int64(r.Uint64()), down: int64(r.Uint64())}
		cur := trafficSample{at: now.Add(time.Duration(int64(r.Uint64()))), up: int64(r.Uint64()), down: int64(r.Uint64())}
		if i%10 == 0 {
			prev.at = time.Time{}
		}
		if i%11 == 0 {
			cur.at = prev.at.Add(time.Nanosecond)
		}
		up, down := rateFor(prev, cur)
		wu, wd := rateForGo(prev, cur)
		if up != wu || down != wd {
			t.Fatal("rate mismatch", i, up, down, wu, wd)
		}
		if counterDelta(cur.up, prev.up) != counterDeltaGo(cur.up, prev.up) {
			t.Fatal("delta mismatch", i)
		}
		if expected {
			out, ok := rustTrafficValues([7]int64{prev.up, 0, cur.up, 0, 0, 0, 0})
			if !ok || out[0] != counterDeltaGo(cur.up, prev.up) {
				t.Fatal("delta FFI not exercised")
			}
		}
	}
	for i := 0; i < 200; i++ {
		rows := make([]connectionRow, r.Intn(200))
		for j := range rows {
			rows[j] = connectionRow{Process: string(rune(j)), Upload: int64(r.Uint64()), Download: int64(r.Uint64())}
			if j%3 == 0 {
				rows[j].Upload = 1
				rows[j].Download = 1
			}
		}
		want := append([]connectionRow{}, rows...)
		sortTrafficRowsGo(want)
		sortTrafficRows(rows)
		if !reflect.DeepEqual(rows, want) && len(rows) != 0 {
			t.Fatal("ranking mismatch", i)
		}
	}
	// Safe finite positive rate is required to execute Rust, not just fallback.
	if expected {
		out, ok := rustTrafficValues([7]int64{1, 2, 11, 22, 2_000_000_000, 0, 1})
		if !ok || out != [2]int64{5, 10} {
			t.Fatal("rate FFI")
		}
	}
}

func TestRustTrafficLogicBoundaries(t *testing.T) {
	expected := os.Getenv("SMARTVPN_TEST_EXPECT_TRAFFIC_RUST") == "1"
	if trafficRustAvailable() != expected {
		t.Fatal("availability mismatch")
	}
	for _, size := range []int{65536, 65537} {
		rows := make([]connectionRow, size)
		for i := range rows {
			rows[i] = connectionRow{Process: string(rune(i)), Upload: int64(i % 7), Download: int64(i % 11)}
		}
		want := append([]connectionRow{}, rows...)
		sortTrafficRowsGo(want)
		actual := append([]connectionRow{}, rows...)
		used := rustSortTrafficRows(actual)
		if used != (expected && size == 65536) {
			t.Fatalf("actual bridge used=%v for size=%d", used, size)
		}
		if used && !reflect.DeepEqual(actual, want) {
			t.Fatal("bridge upper-bound ranking mismatch")
		}
		if !used && !reflect.DeepEqual(actual, rows) {
			t.Fatal("rejected bridge modified input")
		}
		sortTrafficRows(rows)
		if !reflect.DeepEqual(rows, want) {
			t.Fatal("ranking/fallback mismatch")
		}
	}
	now := time.Unix(1700000000, 0)
	for _, v := range []int64{math.MaxInt64, math.MaxInt64 / 2} {
		input := [7]int64{0, 0, v, v, 1, 0, 1}
		if _, ok := rustTrafficValues(input); ok {
			t.Fatal("out-of-range float conversion must reject")
		}
		prev := trafficSample{at: now}
		cur := trafficSample{at: now.Add(time.Nanosecond), up: v, down: v}
		a, b := rateFor(prev, cur)
		wa, wb := rateForGo(prev, cur)
		if a != wa || b != wb {
			t.Fatal("float overflow fallback mismatch")
		}
	}
}
