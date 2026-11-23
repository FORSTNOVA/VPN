package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type pathAuthorityReadFault struct {
	serviceStateBackend
	reads, failAt, applies int
}

func (b *pathAuthorityReadFault) Read() (serviceStateSnapshot, error) {
	b.reads++
	if b.failAt == b.reads {
		return serviceStateSnapshot{}, errors.New("injected read failure")
	}
	return b.serviceStateBackend.Read()
}
func (b *pathAuthorityReadFault) Apply(c serviceStateCommand) (serviceStateAck, error) {
	b.applies++
	return b.serviceStateBackend.Apply(c)
}
func TestServiceStatePathResponseReadFailure(t *testing.T) {
	for _, n := range []int{3, 4, 5} {
		b := &pathAuthorityReadFault{serviceStateBackend: newGoServiceStateBackend(), failAt: n}
		a := &app{service: newServiceAuthority(b)}
		w := httptest.NewRecorder()
		a.pathChecks(w, httptest.NewRequest("GET", "/api/path-checks", nil))
		if w.Code != 500 {
			t.Fatalf("read%d code%d reads%d", n, w.Code, b.reads)
		}
		if !a.mu.TryLock() {
			t.Fatal("lock leaked")
		}
		a.mu.Unlock()
		_ = a.service.Close()
	}
}

type pathPanicWriter struct {
	header http.Header
	writes int
}

func (w *pathPanicWriter) Header() http.Header       { return w.header }
func (w *pathPanicWriter) WriteHeader(int)           { w.writes++; panic("writer interrupted") }
func (w *pathPanicWriter) Write([]byte) (int, error) { w.writes++; panic("writer interrupted") }
func TestServiceStatePathResponseWriterFailure(t *testing.T) {
	a := &app{}
	w := &pathPanicWriter{header: http.Header{}}
	a.pathChecks(w, httptest.NewRequest("GET", "/api/path-checks", nil))
	if w.writes != 1 {
		t.Fatalf("response replay %d", w.writes)
	}
	if !a.mu.TryLock() {
		t.Fatal("lock leaked")
	}
	a.mu.Unlock()
	_ = a.service.Close()
}
func TestServiceStatePathGuardedWorkerFailure(t *testing.T) {
	for _, cleanupFailure := range []bool{false, true} {
		b := &pathAuthorityReadFault{serviceStateBackend: newGoServiceStateBackend()}
		a := &app{service: newServiceAuthority(b), pathCancel: make(chan struct{})}
		var fx *appPathEffects
		done := make(chan pathServiceResult, 1)
		go func() {
			r, _ := runGuardedPathMonitorRound(a, a.pathCancel, pathServiceRequest{Op: "recovery"}, func(e *appPathEffects) pathServiceResult {
				fx = e
				e.Call(pathServiceEffect{Kind: "lock"})
				e.Call(pathServiceEffect{Kind: "set", Name: "busy", Flag: true})
				if cleanupFailure {
					b.failAt = b.reads + 1
					return pathServiceResult{}
				}
				panic("worker failed after admission")
			})
			done <- r
		}()
		r := <-done
		if r.Err == nil {
			t.Fatal("worker failure hidden")
		}
		if !a.mu.TryLock() {
			t.Fatal("worker leaked lock")
		}
		a.mu.Unlock()
		count := b.applies
		fx.Release()
		if b.applies != count {
			t.Fatal("cleanup replayed")
		}
		_ = a.service.Close()
	}
}
func (b *pathAuthorityReadFault) Meta() (serviceStateSnapshot, error) {
	b.reads++
	if b.failAt == b.reads {
		return serviceStateSnapshot{}, errors.New("injected read failure")
	}
	return b.serviceStateBackend.Meta()
}
