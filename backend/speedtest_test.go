package main

import (
	"io"
	"sync/atomic"
	"testing"
	"time"
)

func TestThroughputMbps(t *testing.T) {
	cases := []struct {
		name    string
		bytes   int64
		elapsed time.Duration
		want    float64
	}{
		{"one megabyte in a second", 1_000_000, time.Second, 8},
		{"ten megabytes in ten seconds", 10_000_000, 10 * time.Second, 8},
		{"half a second", 1_000_000, 500 * time.Millisecond, 16},
		{"nothing moved", 0, time.Second, 0},
		{"no time passed", 1_000_000, 0, 0},
		{"negative is not a rate", -1, time.Second, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := throughputMbps(tc.bytes, tc.elapsed); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestZeroReaderCountsWhatItHandsOver(t *testing.T) {
	var started, total atomic.Int64
	source := &zeroReader{remaining: 10, started: &started, total: &total}
	buffer := make([]byte, 4)

	read, err := source.Read(buffer)
	if err != nil || read != 4 {
		t.Fatalf("read %d, err %v", read, err)
	}
	if got := total.Load(); got != 4 {
		t.Fatalf("total = %d, want what it handed over", got)
	}
	// The clock starts with the first byte, not with the request.
	if started.Load() == 0 {
		t.Fatal("the first byte must start the clock")
	}
	for i := range buffer {
		if buffer[i] != 0 {
			t.Fatal("the payload is zeros")
		}
	}

	if _, err := source.Read(buffer); err != nil {
		t.Fatal(err)
	}
	read, err = source.Read(buffer)
	if err != nil || read != 2 {
		t.Fatalf("the final read must be short: %d, %v", read, err)
	}
	if _, err := source.Read(buffer); err != io.EOF {
		t.Fatalf("err = %v, want EOF once the allowance is used", err)
	}
	if got := total.Load(); got != 10 {
		t.Fatalf("total = %d, want the whole allowance", got)
	}
}
