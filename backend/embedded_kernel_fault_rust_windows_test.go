//go:build windows && amd64 && rustcore

package main

import (
	"errors"
	"reflect"
	"testing"
)

type embeddedKernelInjectedSession struct {
	embeddedKernelSession
	nextAt, advanceAt, nextCalls, advanceCalls, closed int
}

func (s *embeddedKernelInjectedSession) next() ([]byte, uint64, error) {
	s.nextCalls++
	if s.nextCalls == s.nextAt {
		return nil, 0, errors.New("injected next")
	}
	return s.embeddedKernelSession.next()
}
func (s *embeddedKernelInjectedSession) advance(n uint64, b []byte) error {
	s.advanceCalls++
	if s.advanceCalls == s.advanceAt {
		_ = s.embeddedKernelSession.advance(n, b)
		return errors.New("uncertain acknowledgement")
	}
	return s.embeddedKernelSession.advance(n, b)
}
func (s *embeddedKernelInjectedSession) close() { s.closed++; s.embeddedKernelSession.close() }

type embeddedKernelTraceFX struct {
	base  *embeddedKernelSafeFX
	trace []embeddedKernelEffect
}

func (f *embeddedKernelTraceFX) Release() { f.base.Release() }
func (f *embeddedKernelTraceFX) Call(e embeddedKernelEffect) embeddedKernelReply {
	f.trace = append(f.trace, e)
	return f.base.Call(e)
}
func TestEmbeddedKernelFlowEveryBoundaryNoReplay(t *testing.T) {
	if !rustEmbeddedKernelAvailable() {
		t.Fatal("native embedded unavailable")
	}
	for _, op := range []string{"start", "stop", "running", "refusal", "start-logging", "logs", "event", "stop-logging", "check", "apply"} {
		factory := func() *embeddedKernelTraceFX {
			s := &embeddedKernelSafeState{tun: "original TUN listening error", file: new(int), sub: new(int), events: []string{"one", "two TUN listening error"}}
			return &embeddedKernelTraceFX{base: &embeddedKernelSafeFX{s: s, op: op, event: "new TUN listening error"}}
		}
		wantFX := factory()
		q := embeddedKernelRequest{Op: op}
		want := runGoEmbeddedKernel(q, wantFX)
		wantFX.Release()
		for _, phase := range []string{"next", "advance"} {
			count := len(wantFX.trace)
			if phase == "next" {
				count++
			}
			for at := 1; at <= count; at++ {
				fx := factory()
				var session *embeddedKernelInjectedSession
				n := embeddedKernelRecoveries.Load()
				got, used := runRustEmbeddedKernelWithStart(q, fx, func(b []byte) (embeddedKernelSession, error) {
					v, e := startRustEmbeddedKernel(b)
					if e != nil {
						return nil, e
					}
					session = &embeddedKernelInjectedSession{embeddedKernelSession: v}
					if phase == "next" {
						session.nextAt = at
					} else {
						session.advanceAt = at
					}
					return session, nil
				})
				fx.Release()
				gs, ws := fx.base.s, wantFX.base.s
				if !used || session == nil || session.closed != 1 || !reflect.DeepEqual(fx.trace, wantFX.trace) || !reflect.DeepEqual(embeddedKernelResultJSON(got), embeddedKernelResultJSON(want)) || !reflect.DeepEqual(gs.trace, ws.trace) || !reflect.DeepEqual(gs.writes, ws.writes) || gs.running != ws.running || gs.tun != ws.tun || gs.handover != ws.handover || gs.spawned != ws.spawned || embeddedKernelRecoveries.Load() != n+1 {
					t.Fatalf("%s/%s/%d replay/partial state changed", op, phase, at)
				}
			}
		}
	}
}
func TestEmbeddedKernelFlowNormalResourceClosureNoRecovery(t *testing.T) {
	if !rustEmbeddedKernelAvailable() {
		t.Fatal("native embedded unavailable")
	}
	t.Setenv("SMARTVPN_RUST_EMBEDDED_KERNEL", "1")
	n := embeddedKernelRecoveries.Load()
	TestEmbeddedKernelServiceStartFailureOrder(t)
	TestEmbeddedKernelServiceLoggingAndRefusalQuirks(t)
	TestEmbeddedKernelServiceFirstTunEventAndStream(t)
	TestEmbeddedKernelServiceConfigAdmission(t)
	TestEmbeddedKernelServiceSnapshotMasksAndRunning(t)
	if embeddedKernelRecoveries.Load() != n {
		t.Fatalf("normal path recovered: %v", embeddedKernelRecoveryReason.Load())
	}
}

type embeddedKernelLongStreamFX struct{ remaining, next, handled int }

func (f *embeddedKernelLongStreamFX) Release() {}
func (f *embeddedKernelLongStreamFX) Call(e embeddedKernelEffect) embeddedKernelReply {
	switch e.Kind {
	case "next-event":
		f.next++
		if f.remaining > 0 {
			f.remaining--
			return embeddedKernelReply{Flag: true}
		}
		return embeddedKernelReply{}
	case "handle-event":
		f.handled++
		return embeddedKernelReply{}
	}
	panic("unexpected streaming effect")
}
func TestEmbeddedKernelFlowLongStreamRemainsNative(t *testing.T) {
	if !rustEmbeddedKernelAvailable() {
		t.Fatal("native embedded unavailable")
	}
	const events = 230000
	fx := &embeddedKernelLongStreamFX{remaining: events}
	n := embeddedKernelRecoveries.Load()
	r, used := runRustEmbeddedKernel(embeddedKernelRequest{Op: "logs"}, fx)
	if !used || r.Err != nil || fx.handled != events || fx.next != events+1 || embeddedKernelRecoveries.Load() != n {
		t.Fatalf("long stream truncated or fell back: handled=%d next=%d reason=%v", fx.handled, fx.next, embeddedKernelRecoveryReason.Load())
	}
}
func TestEmbeddedKernelFlowInvalidUTF8RefusalContinuesAfterSingleSleep(t *testing.T) {
	if !rustEmbeddedKernelAvailable() {
		t.Fatal("native embedded unavailable")
	}
	s := &embeddedKernelSafeState{tun: "TUN " + string([]byte{255})}
	fx := &embeddedKernelTraceFX{base: &embeddedKernelSafeFX{s: s, op: "refusal"}}
	n := embeddedKernelRecoveries.Load()
	r, used := runRustEmbeddedKernel(embeddedKernelRequest{Op: "refusal"}, fx)
	if !used || r.Err == nil || !reflect.DeepEqual(s.trace, []string{"sleep", "tun-error"}) || embeddedKernelRecoveries.Load() != n+1 {
		t.Fatal("invalid text replayed refusal wait")
	}
}
func TestEmbeddedKernelFlowLongStreamContinuesAfterFault(t *testing.T) {
	if !rustEmbeddedKernelAvailable() {
		t.Fatal("native embedded unavailable")
	}
	const events = 230000
	fx := &embeddedKernelLongStreamFX{remaining: events}
	n := embeddedKernelRecoveries.Load()
	var session *embeddedKernelInjectedSession
	r, used := runRustEmbeddedKernelWithStart(embeddedKernelRequest{Op: "logs"}, fx, func(b []byte) (embeddedKernelSession, error) {
		v, e := startRustEmbeddedKernel(b)
		if e != nil {
			return nil, e
		}
		session = &embeddedKernelInjectedSession{embeddedKernelSession: v, nextAt: 2}
		return session, nil
	})
	if !used || r.Err != nil || fx.handled != events || fx.next != events+1 || session == nil || session.closed != 1 || embeddedKernelRecoveries.Load() != n+1 {
		t.Fatal("fallback stream truncated or replayed")
	}
}
