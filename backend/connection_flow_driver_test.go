package main

import (
	"runtime"
	"testing"
	"time"
)

type connectionAfterPanic struct {
	*connectionFakeEffects
	kind string
}

func (f *connectionAfterPanic) Call(q connectionEffect) connectionReply {
	r := f.connectionFakeEffects.Call(q)
	if q.Kind == f.kind {
		panic("synthetic after effect")
	}
	return r
}
func TestConnectionFlowUnknownPanicStopsAndReleases(t *testing.T) {
	for _, kind := range []string{"lock", "start-kernel", "put", "system-proxy"} {
		f := &connectionAfterPanic{connectionFakeEffects: newConnectionFake(false), kind: kind}
		fs := &registryFakeSession{frames: [][]byte{connectionTestFrame(1)}, failStep: 1}
		r, used := runRustConnectionServiceWithStart(connectionServiceRequest{Op: "connect"}, f, func([]byte) (connectionFlowSession, error) { return fs, nil })
		if !used || r.Status != 500 || r.Err == nil || f.Trace[len(f.Trace)-1].Kind != kind || !fs.closed {
			t.Fatal(kind, r, f.Trace)
		}
		if !f.adapter.a.mu.TryLock() {
			t.Fatal("owned lock leaked")
		}
		f.adapter.a.mu.Unlock()
	}
}
func TestConnectionFlowDropPanicReturnsErrorAndRetainsBorrowedLock(t *testing.T) {
	f := &connectionAfterPanic{connectionFakeEffects: newConnectionFake(true), kind: "stop-kernel"}
	f.adapter.a.mu.Lock()
	defer f.adapter.a.mu.Unlock()
	fs := &registryFakeSession{frames: [][]byte{connectionTestFrame(2)}, failStep: 1}
	r, used := runRustConnectionServiceWithStart(connectionServiceRequest{Op: "drop"}, f, func([]byte) (connectionFlowSession, error) { return fs, nil })
	if !used || r.Err == nil || r.Status != 500 {
		t.Fatal(r, used)
	}
	if f.adapter.a.mu.TryLock() {
		f.adapter.a.mu.Unlock()
		t.Fatal("borrowed lock released")
	}
	if f.Trace[len(f.Trace)-1].Kind != "stop-kernel" {
		t.Fatal("continued after uncertain stop", f.Trace)
	}
}
func connectionTestFrame(k byte) []byte {
	b := registryInt([]byte{k}, 0)
	b = registryInt(b, 0)
	b = registryBytes(b, "")
	b = registryBytes(b, "")
	return profileStrings(b, nil)
}
func TestConnectionFlowCancellationReclaimsWorkers(t *testing.T) {
	before := runtime.NumGoroutine()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 1200; i++ {
			f := newConnectionFake(false)
			fs := &registryFakeSession{frames: [][]byte{{255}}}
			_, used := runRustConnectionServiceWithStart(connectionServiceRequest{Op: "connect"}, f, func([]byte) (connectionFlowSession, error) { return fs, nil })
			if used || len(f.Trace) != 0 || !fs.closed {
				panic("unexpected side effect")
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("cancel join deadlock")
	}
	runtime.GC()
	if n := runtime.NumGoroutine(); n > before+4 {
		t.Fatal("mirror leak", before, n)
	}
}
