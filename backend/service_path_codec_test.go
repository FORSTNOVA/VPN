package main

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestServiceStatePathCodecShapes(t *testing.T) {
	for _, nonnull := range []bool{false, true} {
		v := servicePathState{Config: pathCheckSettings{Enabled: true, IntervalSec: 120}, Status: pathStatus{ActiveNode: "\xff\x00", Note: "note", AlertID: ^uint64(0), RetryAt: "opaque", RetryMode: "retry", Demanded: true, Phase: "waiting"}, AtToken: 9, Generation: 17, NextLease: 7, LeaseID: 0}
		if nonnull {
			v.Config.Targets = []pathTarget{}
			v.Status.Results = []siteCheckResult{}
			v.Status.Failures = []string{}
			v.Status.Switched = []string{}
			v.Previous = []servicePathSample{}
			v.Throttle = []servicePathThrottle{}
		}
		b, e := encodeServicePathState(v)
		if e != nil {
			t.Fatal(e)
		}
		got, e := decodeServicePathState(b)
		if e != nil || !reflect.DeepEqual(v, got) {
			t.Fatal("shape lost", e, v, got)
		}
		v.Config.Targets = []pathTarget{{Label: "中文", URL: "https://user:secret@example.invalid/?key=secret", Builtin: true, Enabled: true}}
		v.Status.Results = []siteCheckResult{{Name: "n", State: "ok", HTTPCode: 429, LatencyMs: 1 << 54, Detail: "\xff"}}
		v.Previous = []servicePathSample{{ID: "x", Upload: -9}}
		v.Throttle = []servicePathThrottle{{Label: "n", TimeToken: 99}}
		b, e = encodeServicePathState(v)
		got, de := decodeServicePathState(b)
		if e != nil || de != nil || !reflect.DeepEqual(v, got) {
			t.Fatal("fields lost", e, de)
		}
		for i := 0; i < len(b); i++ {
			if _, e := decodeServicePathState(b[:i]); i != 0 && e == nil {
				t.Fatal("truncated accepted", i)
			}
		}
		if _, e = decodeServicePathState(b + "x"); e == nil {
			t.Fatal("trailing accepted")
		}
	}
}
func TestServiceStatePathCommandCodec(t *testing.T) {
	for action := uint8(1); action <= 21; action++ {
		v := servicePathCommand{Action: action, Flag: true, Text: "\xff\x00", TimeToken: 1 << 63, Generation: 7, LeaseID: 9, Strings: []string{}, Results: []siteCheckResult{{Name: "a", HTTPCode: 403, LatencyMs: -1}}, Samples: []servicePathSample{{ID: "b", Upload: 1 << 54}}}
		b, e := encodeServicePathCommand(v)
		got, de := decodeServicePathCommand(b)
		if e != nil || de != nil || !reflect.DeepEqual(v, got) {
			t.Fatal(action, e, de)
		}
		if _, e = decodeServicePathCommand(b + "x"); e == nil {
			t.Fatal("trailing accepted")
		}
	}
}
func TestServiceStatePathCodecLimits(t *testing.T) {
	if _, e := encodeServicePathState(servicePathState{Status: pathStatus{At: time.Now()}}); e == nil {
		t.Fatal("raw clock accepted")
	}
	if _, e := encodeServicePathCommand(servicePathCommand{Text: strings.Repeat("x", servicePathMaxBytes)}); e == nil {
		t.Fatal("oversize accepted")
	}
	if _, e := encodeServicePathState(servicePathState{Previous: make([]servicePathSample, servicePathMaxItems+1)}); e == nil {
		t.Fatal("count accepted")
	}
	if _, e := decodeServicePathState(string([]byte{2})); e == nil {
		t.Fatal("bool accepted")
	}
}
func TestServiceStatePathCapabilities(t *testing.T) {
	for _, v := range []struct {
		v, c uint64
		want bool
	}{{1, 3, false}, {2, 1, false}, {2, 2, false}, {2, 3, false}, {2, 7, false}, {3, 3, false}, {3, 7, false}, {3, 15, false}, {4, 7, false}, {4, 15, false}, {4, 31, false}, {5, 15, false}, {5, 31, false}, {5, 63, false}, {6, 31, false}, {6, 63, true}, {6, 127, true}, {7, 63, false}} {
		if serviceStateFullCapabilities(v.v, v.c) != v.want {
			t.Fatal(v)
		}
	}
}
