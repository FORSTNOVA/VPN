//go:build windows && amd64 && rustcore

package main

import (
	"errors"
	"fmt"
	"runtime"
	"testing"
)

type labelsInjectedSession struct {
	labelsFlowSession
	kind, target                 byte
	nextFailure, pending, failed bool
}

func (s *labelsInjectedSession) next() ([]byte, uint64, error) {
	if s.pending {
		s.pending = false
		s.failed = true
		return nil, 0, errors.New("synthetic next")
	}
	b, n, e := s.labelsFlowSession.next()
	if e == nil {
		f, er := decodeLabelsFrame(b)
		if er == nil {
			s.kind = f.kind
		}
	}
	return b, n, e
}
func (s *labelsInjectedSession) advance(n uint64, b []byte) error {
	if !s.failed && s.kind == s.target {
		if s.nextFailure {
			s.pending = true
		} else {
			s.failed = true
			return errors.New("synthetic advance")
		}
	}
	return s.labelsFlowSession.advance(n, b)
}
func TestLabelsFlowActualBoundaryFaults(t *testing.T) {
	for k := byte(1); k <= 20; k++ {
		for _, next := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/%v", k, next), func(t *testing.T) {
				sp := labelsDiffBase()
				if k == 2 {
					sp.State.Merged = false
				}
				if k == 17 {
					sp.Fail = "rename"
				}
				var inj *labelsInjectedSession
				labelsDiffCompare(t, sp, func(q labelsServiceRequest, fx labelsServiceEffects) labelsServiceResult {
					r, used := runRustLabelsServiceWithStart(q, fx, func(b []byte) (labelsFlowSession, error) {
						s, e := startRustLabelsFlow(b)
						if e != nil {
							return nil, e
						}
						inj = &labelsInjectedSession{labelsFlowSession: s, target: k, nextFailure: next}
						return inj, nil
					})
					if !used {
						t.Fatal("not used")
					}
					return r
				})
				if inj == nil || !inj.failed {
					t.Fatal("boundary not reached", labelsFlowRecoveryReason.Load())
				}
			})
		}
	}
}

type labelsPanicFX struct {
	*labelsDiffFX
	port string
}

func (f *labelsPanicFX) Call(q labelsEffect) labelsReply {
	r := f.labelsDiffFX.Call(q)
	if q.Kind == f.port {
		panic("after fake labels port")
	}
	return r
}
func TestLabelsFlowActualPanicNoReplay(t *testing.T) {
	for _, port := range []string{"read-original", "rebuild", "patch-key", "write-temp", "rename", "remove-temp", "set-label", "save-cache"} {
		t.Run(port, func(t *testing.T) {
			s := labelsDiffBase()
			if port == "remove-temp" {
				s.Fail = "rename"
			}
			f := labelsDiffNew(s)
			r, used := runRustLabelsService(labelsServiceRequest{Op: "restore"}, &labelsPanicFX{f, port})
			if !used || r.Err == nil || f.releases != 1 {
				t.Fatal("panic false success/cleanup", r)
			}
			n := 0
			for _, e := range f.trace {
				if e.Effect.Kind == port {
					n++
				}
			}
			if n != 1 {
				t.Fatal("port replayed", n)
			}
		})
	}
}
func TestLabelsFlowActualCapacityAndCancel(t *testing.T) {
	q := labelsServiceRequest{Op: "restore"}
	input, _ := labelsStartInput(q)
	var all []labelsFlowSession
	for i := 0; i < 64; i++ {
		s, e := startRustLabelsFlow(input)
		if e != nil {
			t.Fatal(e)
		}
		all = append(all, s)
	}
	f := labelsDiffNew(labelsDiffBase())
	_, used := runRustLabelsService(q, f)
	if used || len(f.trace) != 0 {
		t.Fatal("capacity performed effects")
	}
	for _, s := range all {
		s.close()
	}
	s, e := startRustLabelsFlow(input)
	if e != nil {
		t.Fatal(e)
	}
	s.close()
	before := runtime.NumGoroutine()
	for i := 0; i < 1200; i++ {
		m := newLabelsMirror(q)
		m.next()
		m.close()
	}
	runtime.GC()
	if runtime.NumGoroutine() > before+4 {
		t.Fatal("mirror leaked")
	}
}
