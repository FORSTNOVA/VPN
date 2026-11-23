//go:build windows && amd64 && rustcore

package main

import (
	"bufio"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRustCoreBridgeParity(t *testing.T) {
	expected := os.Getenv("SMARTVPN_TEST_EXPECT_RUST") == "1"
	if rustCoreAvailable() != expected {
		t.Fatalf("Rust availability = %v, expected %v", rustCoreAvailable(), expected)
	}
	fixture := os.Getenv("SMARTVPN_TEST_RUST_FIXTURES")
	if fixture == "" {
		t.Skip("set fixture path for full bridge parity test")
	}
	f, err := os.Open(fixture)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	count := 0
	for s.Scan() {
		hexdata, _, ok := strings.Cut(s.Text(), "\t")
		if !ok {
			t.Fatal("invalid fixture")
		}
		input, err := hex.DecodeString(hexdata)
		if err != nil {
			t.Fatal(err)
		}
		actual, ae := parseSTUNAddress(input)
		want, we := parseSTUNAddressGo(input)
		if actual != want || (ae == nil) != (we == nil) {
			t.Fatalf("fixture %d differs", count)
		}
		if ae != nil && ae.Error() != we.Error() {
			t.Fatalf("fixture %d error differs", count)
		}
		count++
	}
	if err := s.Err(); err != nil {
		t.Fatal(err)
	}
	if count < 197 {
		t.Fatalf("incomplete fixture set: %d", count)
	}
	for _, pair := range [][2]int64{{1_000_000, 500_000_000}, {-1, 1}, {1, -1}, {0, 0}, {9223372036854775807, 1}, {1_000_000, 1_026_847_926_404_610_462}, {9223372036854775807, 9223372036854775807}} {
		if got, want := throughputMbps(pair[0], time.Duration(pair[1])), throughputMbpsGo(pair[0], time.Duration(pair[1])); got != want {
			t.Fatalf("throughput %v != %v", got, want)
		}
	}
}
