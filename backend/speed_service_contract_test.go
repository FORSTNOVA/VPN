package main

import (
	"io"
	"reflect"
	"testing"
	"time"
)

func speedIndependentKinds(f *speedIndependentFX) []string {
	v := make([]string, len(f.trace))
	for i, e := range f.trace {
		v[i] = e.Kind
	}
	return v
}
func speedIndependentGolden(t *testing.T, run speedIndependentRun) {
	t.Run("response-before-cleanup-first-error-upload-still-runs", func(t *testing.T) {
		f := &speedIndependentFX{s: speedIndependentSpec{Temporary: true, Node: " n ", Down: 0, Up: 600, Started: 1, Elapsed: 2 * time.Second}}
		r := run(t, speedServiceRequest{Op: "handler", MegaBytes: 31}, f)
		if r.Status != 200 || r.Node != " n " || r.Speed.DownBytes != 0 || r.Speed.UpBytes != 600 || r.Speed.UpMbps != .0024 || r.Speed.Error == "" {
			t.Fatalf("result %+v", r)
		}
		want := []string{"lock", "ensure", "active", "endpoint", "measure-begin", "batch-start", "batch-join", "totals", "measure-end", "measure-begin", "batch-start", "batch-join", "totals", "total-load", "elapsed", "measure-end", "rate", "response", "stop", "unlock"}
		if !reflect.DeepEqual(speedIndependentKinds(f), want) {
			t.Fatalf("sequence %v", speedIndependentKinds(f))
		}
		b := []speedServiceEffect{}
		for _, e := range f.trace {
			if e.Kind == "batch-start" {
				b = append(b, e)
			}
		}
		if len(b) != 2 || b[0].Count != 4 || b[0].Bytes != 31<<20 || b[0].Upload || b[1].Count != 2 || b[1].Bytes != 5<<20 || !b[1].Upload {
			t.Fatalf("shared download vs floored upload budget %+v", b)
		}
	})
	t.Run("EOF-with-bytes-before-close", func(t *testing.T) {
		f := &speedIndependentFX{s: speedIndependentSpec{Reads: []speedServiceReply{{Bytes: 65536, Err: io.EOF}}}}
		run(t, speedServiceRequest{Op: "down-stream", Allowance: 1}, f)
		if f.total != 65536 || !reflect.DeepEqual(speedIndependentKinds(f), []string{"request-new", "request-do", "body-read", "download-count", "body-close"}) {
			t.Fatalf("lost overshoot or EOF count: %+v", f)
		}
	})
	t.Run("zero-empty-start-count", func(t *testing.T) {
		f := &speedIndependentFX{}
		r := run(t, speedServiceRequest{Op: "zero-read", Remaining: 3, Length: 0}, f)
		if r.EOF || r.Bytes != 0 || !reflect.DeepEqual(speedIndependentKinds(f), []string{"zero-fill", "zero-remaining", "zero-count"}) {
			t.Fatalf("empty read %+v trace=%v", r, speedIndependentKinds(f))
		}
	})
	t.Run("empty-before-length", func(t *testing.T) {
		f := &speedIndependentFX{}
		r := run(t, speedServiceRequest{Op: "zero-read", Remaining: 0, Length: 4}, f)
		if !r.EOF || len(f.trace) != 0 {
			t.Fatalf("exhausted read %+v trace=%v", r, f.trace)
		}
	})
	t.Run("ensure-error-no-stop", func(t *testing.T) {
		f := &speedIndependentFX{s: speedIndependentSpec{Fail: "ensure", Temporary: true}}
		r := run(t, speedServiceRequest{Op: "handler"}, f)
		if r.Status != 502 || !reflect.DeepEqual(speedIndependentKinds(f), []string{"lock", "ensure", "response", "unlock"}) {
			t.Fatalf("bad ensure cleanup %+v %v", r, speedIndependentKinds(f))
		}
	})
	t.Run("selection-refusal-temporary-stop", func(t *testing.T) {
		f := &speedIndependentFX{s: speedIndependentSpec{Temporary: true, Node: "rEjEcT"}}
		r := run(t, speedServiceRequest{Op: "handler"}, f)
		if r.Status != 409 || !reflect.DeepEqual(speedIndependentKinds(f), []string{"lock", "ensure", "active", "response", "stop", "unlock"}) {
			t.Fatalf("bad rejection %+v %v", r, speedIndependentKinds(f))
		}
	})
}
func TestSpeedServiceIndependentSelectedGolden(t *testing.T) {
	speedIndependentGolden(t, func(_ *testing.T, q speedServiceRequest, fx speedServiceEffects) speedServiceResult {
		return runSelectedSpeedService(q, fx)
	})
}
