//go:build windows && amd64 && rustcore

package main

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
)

func TestTunReportNativeInvalidUTF8AndCapacityContinueOnce(t *testing.T) {
	if !rustTunReportAvailable() {
		t.Skip("native TUN report unavailable")
	}
	for _, name := range []string{"invalid-utf8", "reply-capacity"} {
		t.Run(name, func(t *testing.T) {
			answers := []string{string([]byte{0xff, 'x'})}
			if name == "reply-capacity" {
				answers = make([]string, 300)
				for i := range answers {
					answers[i] = "203.0.113.1"
				}
			}
			q := tunReportRequest{Op: "dns"}
			spec := tunIndependentSpec{Answers: answers}
			wfx := &tunIndependentFX{s: spec}
			want := runGoTunReport(q, wfx)
			fx := &tunIndependentFX{s: spec}
			before := tunReportRecoveries.Load()
			got, used := runRustTunReport(q, fx)
			if !used || got.Err != nil || !reflect.DeepEqual(got.Data, want.Data) || !reflect.DeepEqual(fx.trace, wfx.trace) {
				t.Fatal("native continuation changed result or replayed resource")
			}
			if tunReportRecoveries.Load() != before+1 {
				t.Fatal("expected one controlled continuation")
			}
		})
	}
}

type tunReportInjectedSession struct {
	tunReportSession
	nextAt, advanceAt, nextCalls, advanceCalls int
	closed                                     bool
}

func (s *tunReportInjectedSession) next() ([]byte, uint64, error) {
	s.nextCalls++
	if s.nextCalls == s.nextAt {
		return nil, 0, errors.New("injected native next")
	}
	return s.tunReportSession.next()
}
func (s *tunReportInjectedSession) advance(step uint64, b []byte) error {
	s.advanceCalls++
	if s.advanceCalls == s.advanceAt { // The native advance may already have consumed its acknowledgement.
		_ = s.tunReportSession.advance(step, b)
		return errors.New("injected uncertain native advance")
	}
	return s.tunReportSession.advance(step, b)
}
func (s *tunReportInjectedSession) close() {
	if s.closed {
		panic("session closed twice")
	}
	s.closed = true
	s.tunReportSession.close()
}
func TestTunReportNativeFaultEveryEffectBoundary(t *testing.T) {
	if !rustTunReportAvailable() {
		t.Skip("native TUN report unavailable")
	}
	spec := tunIndependentSpec{Elevated: true, Installed: true, Running: true, Authorized: true, Alive: true, Known: true, FD: 3, Mode: modeTUN, Host: "synthetic.invalid", Lock: "JP", Routes: []string{"0.0.0.0/0"}, DNS: []string{"192.0.2.1"}, Answers: []string{"203.0.113.1"}, Adapter: adapterReport{Present: true, Status: "Up", IPv4Route: true, IPv6Route: true}, Tunnel: exitIPResult{Address: "203.0.113.2"}, System: exitIPResult{Address: "203.0.113.2"}, Region: regionRecord{Country: "JP", ExitIP: "203.0.113.2"}}
	for _, op := range []string{"windows-checks", "android-checks", "windows-status", "android-status"} {
		q := tunReportRequest{Op: op}
		wfx := &tunIndependentFX{s: spec}
		want := runGoTunReport(q, wfx)
		for _, phase := range []string{"next", "advance"} {
			count := len(wfx.trace)
			if phase == "next" {
				count++
			}
			for at := 1; at <= count; at++ {
				t.Run(fmt.Sprintf("%s/%s/%d", op, phase, at), func(t *testing.T) {
					fx := &tunIndependentFX{s: spec}
					var wrapped *tunReportInjectedSession
					before := tunReportRecoveries.Load()
					got, used := runRustTunReportWithStart(q, fx, func(b []byte) (tunReportSession, error) {
						base, e := startRustTunReport(b)
						if e != nil {
							return nil, e
						}
						wrapped = &tunReportInjectedSession{tunReportSession: base}
						if phase == "next" {
							wrapped.nextAt = at
						} else {
							wrapped.advanceAt = at
						}
						return wrapped, nil
					})
					if !used || got.Err != nil || wrapped == nil || !wrapped.closed || !reflect.DeepEqual(got.Data, want.Data) || !reflect.DeepEqual(fx.trace, wfx.trace) {
						t.Fatalf("fault replay/result mismatch: %v", got.Err)
					}
					if tunReportRecoveries.Load() != before+1 {
						t.Fatal("fault did not use one controlled continuation")
					}
					fx.Release()
				})
			}
		}
	}
}
