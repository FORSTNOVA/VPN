//go:build windows && amd64 && rustcore

package main

import (
	"errors"
	"fmt"
	"runtime"
	"testing"
)

type chinaInjectedSession struct {
	chinaFlowSession
	kind, target                 byte
	nextFailure, pending, failed bool
}

func (s *chinaInjectedSession) next() ([]byte, uint64, error) {
	if s.pending {
		s.pending = false
		s.failed = true
		return nil, 0, errors.New("synthetic next")
	}
	b, n, e := s.chinaFlowSession.next()
	if e == nil {
		f, er := decodeChinaFrame(b)
		if er == nil {
			s.kind = f.kind
		}
	}
	return b, n, e
}
func (s *chinaInjectedSession) advance(n uint64, b []byte) error {
	if !s.failed && s.kind == s.target {
		if s.nextFailure {
			s.pending = true
		} else {
			s.failed = true
			return errors.New("synthetic advance")
		}
	}
	return s.chinaFlowSession.advance(n, b)
}

func chinaFaultCase(k byte) (chinaServiceRequest, chinaTestSpec) {
	s := chinaTestBase()
	q := chinaServiceRequest{Op: "ensure", Mode: chinaListNow}
	switch k {
	case 1, 2, 23:
		q.Op = "start-refresh"
	case 3, 4:
		q.Op = "supported"
	case 7, 8:
		q.Op = "parse"
		q.Body = s.Cache
	case 9, 10:
		q.Op = "download"
	case 11:
		q.Mode = chinaListIfOverdue
	case 12, 13, 14:
		q.Op = "write"
		q.Ranges = []string{"1.0.1.0/24"}
	case 16, 17, 18, 19, 20, 21, 22:
		q.Op = "probe"
	case 24:
		q.Op = "probe"
		s.Fail["probe-check"] = true
	case 25, 26, 27, 28, 29:
		q.Op = "options"
		s.Import = true
		s.Options.TUN = true
	case 31:
		q.Op = "parse"
		q.Body = []byte("invalid")
	}
	return q, s
}
func TestChinaFlowActualBoundaryFaults(t *testing.T) {
	for _, k := range []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 31, 32, 33} {
		for _, next := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/%v", k, next), func(t *testing.T) {
				q, sp := chinaFaultCase(k)
				var inj *chinaInjectedSession
				chinaTestParity(t, q, sp, func(q chinaServiceRequest, fx chinaServiceEffects) chinaServiceResult {
					r, used := runRustChinaServiceWithStart(q, fx, func(b []byte) (chinaFlowSession, error) {
						s, e := startRustChinaFlow(b)
						if e != nil {
							return nil, e
						}
						inj = &chinaInjectedSession{chinaFlowSession: s, target: k, nextFailure: next}
						return inj, nil
					})
					if !used {
						t.Fatal("not used")
					}
					return r
				}, func() (serviceStateBackend, error) { return newGoServiceStateBackend(), nil })
				if inj == nil || !inj.failed {
					t.Fatal("boundary not reached", chinaFlowRecoveryReason.Load())
				}
			})
		}
	}
}

type chinaPanicFX struct {
	*chinaTestFX
	port string
}

func (f *chinaPanicFX) Call(q chinaServiceEffect) chinaServiceReply {
	r := f.chinaTestFX.Call(q)
	if q.Kind == f.port {
		panic("after fake china port")
	}
	return r
}
func TestChinaFlowActualPanicNoReplay(t *testing.T) {
	for _, port := range []string{"lock", "command", "spawn", "fetch", "write-temp", "rename", "probe-mkdir", "probe-list-write", "probe-config-write", "probe-check", "probe-remove-list", "probe-remove-config"} {
		t.Run(port, func(t *testing.T) {
			sp := chinaTestBase()
			q := chinaServiceRequest{Op: "ensure", Mode: chinaListNow}
			if port == "lock" || port == "command" || port == "spawn" {
				q.Op = "start-refresh"
			}
			if len(port) >= 6 && port[:6] == "probe-" {
				q.Op = "probe"
			}
			f := chinaTestNew(t, sp, func() (serviceStateBackend, error) { return newGoServiceStateBackend(), nil })
			r, used := runRustChinaService(q, &chinaPanicFX{f, port})
			if !used || r.Err == nil || f.Owned {
				t.Fatal("panic false success/lock")
			}
			n := 0
			for _, e := range f.Trace {
				if e.Kind == port {
					n++
				}
			}
			if n != 1 {
				t.Fatal("replayed", n)
			}
		})
	}
}
func TestChinaFlowActualCapacityCancel(t *testing.T) {
	q := chinaServiceRequest{Op: "probe"}
	input, _ := chinaStartInput(q)
	var all []chinaFlowSession
	for i := 0; i < 64; i++ {
		s, e := startRustChinaFlow(input)
		if e != nil {
			t.Fatal(e)
		}
		all = append(all, s)
	}
	f := chinaTestNew(t, chinaTestBase(), func() (serviceStateBackend, error) { return newGoServiceStateBackend(), nil })
	_, used := runRustChinaService(q, f)
	if used || len(f.Trace) != 0 {
		t.Fatal("capacity executed effects")
	}
	for _, s := range all {
		s.close()
	}
	before := runtime.NumGoroutine()
	for i := 0; i < 1200; i++ {
		m := newChinaMirror(q)
		m.next()
		m.close()
	}
	runtime.GC()
	if runtime.NumGoroutine() > before+4 {
		t.Fatal("mirror leaked")
	}
}

func TestChinaFlowBodyArenaBound(t *testing.T) {
	a := newChinaArena(chinaServiceRequest{})
	body := make([]byte, 4096)
	for i := 0; i < 15000; i++ {
		if _, e := a.replyBytes(chinaFrame{kind: 9}, chinaServiceReply{Body: body}); e != nil {
			t.Fatal(e)
		}
	}
	if len(a.bodies) > 2 || a.bodyBytes != len(body) {
		t.Fatal("body arena accumulated retries", len(a.bodies), a.bodyBytes)
	}
}
