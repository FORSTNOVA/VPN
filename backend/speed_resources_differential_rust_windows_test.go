//go:build windows && rustcore

package main

import (
	"errors"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestSpeedFlowIndependentNestedNativeResources(t *testing.T) {
	if !rustSpeedFlowAvailable() {
		t.Fatal("native unavailable")
	}
	for _, up := range []bool{false, true} {
		name := "download"
		workers := int64(4)
		if up {
			name = "upload"
			workers = 2
		}
		t.Run(name, func(t *testing.T) {
			var arrived, closed, calls, streams, zeros atomic.Int64
			gate := make(chan struct{})
			before := speedFlowRecoveries.Load()
			rt, cancels := speedTestRuntime(t, func(r *http.Request) (*http.Response, error) {
				if arrived.Add(1) == workers {
					close(gate)
				}
				select {
				case <-gate:
				case <-time.After(5 * time.Second):
					return nil, errors.New("fake concurrent barrier timeout")
				}
				if up {
					if r.ContentLength != 5 {
						t.Errorf("chunk=%d", r.ContentLength)
					}
					b, e := io.ReadAll(r.Body)
					if e != nil || len(b) != 5 {
						t.Errorf("body %d %v", len(b), e)
					}
					return nil, errors.New("after upload bytes")
				}
				return &http.Response{StatusCode: 503, Header: make(http.Header), Body: &speedFakeBody{closed: &closed, read: func([]byte) (int, error) { return 3, io.EOF }}}, nil
			})
			rt.run = func(q speedServiceRequest, fx speedServiceEffects) speedServiceResult {
				calls.Add(1)
				if q.Op == "down-stream" || q.Op == "up-stream" {
					streams.Add(1)
				}
				if q.Op == "zero-read" {
					zeros.Add(1)
				}
				r, ok := runRustSpeedService(q, fx)
				if !ok {
					panic("nested native refused before start")
				}
				return r
			}
			allowance := int64(1)
			want := int64(12)
			if up {
				allowance = 11
				want = 10
			}
			q := speedServiceRequest{Op: name, Allowance: allowance, Budget: speedTestSeconds}
			e := newSpeedEffects(nil, q, nil, rt)
			r, ok := runRustSpeedService(q, e)
			if !ok || r.Err != nil || r.Bytes != want || r.Elapsed != time.Second || arrived.Load() != workers || streams.Load() != workers || cancels.Load() != 1 || speedFlowRecoveries.Load() != before {
				t.Fatalf("nested actual result %+v ok=%v workers=%d streams=%d calls=%d zeros=%d cancel=%d recoveries=%d->%d", r, ok, arrived.Load(), streams.Load(), calls.Load(), zeros.Load(), cancels.Load(), before, speedFlowRecoveries.Load())
			}
			t.Logf("actual native parent=1 child_calls=%d streams=%d zero_reads=%d recovery_delta=%d", calls.Load(), streams.Load(), zeros.Load(), speedFlowRecoveries.Load()-before)
			if up && zeros.Load() < 2 {
				t.Fatal("upload zero readers did not use native")
			}
			if !up && closed.Load() != 4 {
				t.Fatal("download bodies not closed once")
			}
			e.Release()
			if cancels.Load() != 1 {
				t.Fatal("cancel replay")
			}
		})
	}
}
