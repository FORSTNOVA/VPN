//go:build windows && amd64 && rustcore

package main

import (
	"context"
	"errors"
	"net"
	"reflect"
	"testing"
	"time"
)

type androidResolverObservedFX struct {
	base  *appAndroidResolverEffects
	trace []androidResolverEffect
}

func (f *androidResolverObservedFX) Release() {}
func (f *androidResolverObservedFX) Call(e androidResolverEffect) androidResolverReply {
	f.trace = append(f.trace, e)
	return f.base.Call(e)
}

type androidResolverInjectedSession struct {
	androidResolverSession
	nextAt, advanceAt, nextCalls, advanceCalls, closed int
}

func (s *androidResolverInjectedSession) next() ([]byte, uint64, error) {
	s.nextCalls++
	if s.nextCalls == s.nextAt {
		return nil, 0, errors.New("injected next")
	}
	return s.androidResolverSession.next()
}
func (s *androidResolverInjectedSession) advance(n uint64, b []byte) error {
	s.advanceCalls++
	if s.advanceCalls == s.advanceAt {
		_ = s.androidResolverSession.advance(n, b)
		return errors.New("uncertain advance")
	}
	return s.androidResolverSession.advance(n, b)
}
func (s *androidResolverInjectedSession) close() { s.closed++; s.androidResolverSession.close() }
func TestAndroidResolverFlowEveryBoundaryPreservesConn(t *testing.T) {
	if !rustAndroidResolverAvailable() {
		t.Fatal("native resolver unavailable")
	}
	for _, success := range []int{-1, 0, 2} {
		conn := &androidResolverTestConn{}
		last := errors.New("original sentinel")
		newFX := func() (*androidResolverObservedFX, *uint32, *int) {
			counter := new(uint32)
			calls := new(int)
			base := &appAndroidResolverEffects{ctx: context.Background(), network: "udp", servers: []string{"a", "b", "c", "d", "e"}, counter: counter}
			base.dial = func(context.Context, string, string, time.Duration) (net.Conn, error) {
				i := *calls
				*calls++
				if i == success {
					return conn, nil
				}
				return conn, last
			}
			return &androidResolverObservedFX{base: base}, counter, calls
		}
		wantFX, _, _ := newFX()
		q := androidResolverRequest{Op: "dial"}
		want := runGoAndroidResolver(q, wantFX)
		for _, phase := range []string{"next", "advance"} {
			count := len(wantFX.trace)
			if phase == "next" {
				count++
			}
			for at := 1; at <= count; at++ {
				fx, counter, calls := newFX()
				var s *androidResolverInjectedSession
				n := androidResolverRecoveries.Load()
				got, used := runRustAndroidResolverWithStart(q, fx, func(b []byte) (androidResolverSession, error) {
					v, e := startRustAndroidResolver(b)
					if e != nil {
						return nil, e
					}
					s = &androidResolverInjectedSession{androidResolverSession: v}
					if phase == "next" {
						s.nextAt = at
					} else {
						s.advanceAt = at
					}
					return s, nil
				})
				expectedCalls := 5
				if success >= 0 {
					expectedCalls = success + 1
				}
				if !used || s == nil || s.closed != 1 || *counter != 1 || *calls != expectedCalls || !reflect.DeepEqual(fx.trace, wantFX.trace) || got.Status != want.Status || got.Err != want.Err || fx.base.conn != wantFX.base.conn || conn.closed != 0 || androidResolverRecoveries.Load() != n+1 {
					t.Fatalf("%d/%s/%d replay or identity mismatch", success, phase, at)
				}
			}
		}
	}
}
func TestAndroidResolverFlowNativeArgumentsWithoutRecovery(t *testing.T) {
	if !rustAndroidResolverAvailable() {
		t.Fatal("native resolver unavailable")
	}
	t.Setenv("SMARTVPN_RUST_ANDROID_RESOLVER", "1")
	n := androidResolverRecoveries.Load()
	TestAndroidResolverServiceOriginalOrderAndIdentity(t)
	TestAndroidResolverServiceWrapAndNilConnection(t)
	if androidResolverRecoveries.Load() != n {
		t.Fatalf("normal path recovered: %v", androidResolverRecoveryReason.Load())
	}
}
