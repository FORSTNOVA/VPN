//go:build windows && amd64 && rustcore

package main

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

type metadataProjectionObserved struct {
	base    *appMetadataProjectionEffects
	trace   []metadataProjectionEffect
	panicAt int
}

func (f *metadataProjectionObserved) Release() {}
func (f *metadataProjectionObserved) Call(e metadataProjectionEffect) metadataProjectionReply {
	f.trace = append(f.trace, e)
	r := f.base.Call(e)
	if len(f.trace) == f.panicAt {
		panic("post effect")
	}
	return r
}

type metadataProjectionInjected struct {
	metadataProjectionSession
	nextAt, advanceAt, nextCalls, advanceCalls, closed int
}

func (s *metadataProjectionInjected) next() ([]byte, uint64, error) {
	s.nextCalls++
	if s.nextCalls == s.nextAt {
		return nil, 0, errors.New("next fault")
	}
	return s.metadataProjectionSession.next()
}
func (s *metadataProjectionInjected) advance(n uint64, b []byte) error {
	s.advanceCalls++
	if s.advanceCalls == s.advanceAt {
		_ = s.metadataProjectionSession.advance(n, b)
		return errors.New("uncertain acknowledgement")
	}
	return s.metadataProjectionSession.advance(n, b)
}
func (s *metadataProjectionInjected) close() { s.closed++; s.metadataProjectionSession.close() }
func metadataProjectionFactory(op string) *metadataProjectionObserved {
	f := &appMetadataProjectionEffects{body: []byte("proxies: [{name: same}, {name: same}]"), manual: []manualNode{{Name: string([]byte{255}), Type: "manual", Server: "a.example.com", Proxy: map[string]any{"name": "same", "tls": true, "ws-opts": map[string]any{"headers": nil}}}, {Name: "last", Proxy: nil}}, hooks: map[string]func(metadataProjectionEffect) metadataProjectionReply{}}
	f.hooks["subscription"] = func(metadataProjectionEffect) metadataProjectionReply { return metadataProjectionReply{Text: "active"} }
	f.hooks["read-profile"] = func(metadataProjectionEffect) metadataProjectionReply {
		return metadataProjectionReply{Err: errors.New("partial")}
	}
	f.hooks["manual-snapshot"] = func(metadataProjectionEffect) metadataProjectionReply { return metadataProjectionReply{} }
	f.hooks["mode-read"] = func(metadataProjectionEffect) metadataProjectionReply { return metadataProjectionReply{Text: "tun"} }
	f.hooks["platform"] = func(metadataProjectionEffect) metadataProjectionReply {
		return metadataProjectionReply{Text: "windows"}
	}
	return &metadataProjectionObserved{base: f}
}
func TestMetadataProjectionFlowEveryBoundaryAndPostEffectPanic(t *testing.T) {
	if !rustMetadataProjectionAvailable() {
		t.Fatal("native metadata unavailable")
	}
	for _, op := range []string{"details", "domains", "index", "current-index", "mode"} {
		q := metadataProjectionRequest{Op: op}
		wantFX := metadataProjectionFactory(op)
		want := runGoMetadataProjection(q, wantFX)
		for _, phase := range []string{"next", "advance"} {
			count := len(wantFX.trace)
			if phase == "next" {
				count++
			}
			for at := 1; at <= count; at++ {
				fx := metadataProjectionFactory(op)
				var s *metadataProjectionInjected
				n := metadataProjectionRecoveries.Load()
				got, used := runRustMetadataProjectionWithStart(q, fx, func(b []byte) (metadataProjectionSession, error) {
					v, e := startRustMetadataProjection(b)
					if e != nil {
						return nil, e
					}
					s = &metadataProjectionInjected{metadataProjectionSession: v}
					if phase == "next" {
						s.nextAt = at
					} else {
						s.advanceAt = at
					}
					return s, nil
				})
				if !used || s == nil || s.closed != 1 || !reflect.DeepEqual(fx.trace, wantFX.trace) || !reflect.DeepEqual(metadataProjectionResultJSON(got), metadataProjectionResultJSON(want)) || !reflect.DeepEqual(fx.base.index, wantFX.base.index) || !reflect.DeepEqual(fx.base.details, wantFX.base.details) || !reflect.DeepEqual(fx.base.domains, wantFX.base.domains) || metadataProjectionRecoveries.Load() != n+1 {
					t.Fatalf("%s/%s/%d changed effects: %v", op, phase, at, metadataProjectionRecoveryReason.Load())
				}
				if op == "index" || op == "current-index" {
					if reflect.ValueOf(fx.base.index["same"][2]).Pointer() != reflect.ValueOf(fx.base.manual[0].Proxy).Pointer() {
						t.Fatal("fault cloned manual graph")
					}
				}
			}
		}
		for at := 1; at <= len(wantFX.trace); at++ {
			fx := metadataProjectionFactory(op)
			fx.panicAt = at
			var s *metadataProjectionInjected
			panicked := false
			func() {
				defer func() { panicked = recover() != nil }()
				runRustMetadataProjectionWithStart(q, fx, func(b []byte) (metadataProjectionSession, error) {
					v, e := startRustMetadataProjection(b)
					if e != nil {
						return nil, e
					}
					s = &metadataProjectionInjected{metadataProjectionSession: v}
					return s, nil
				})
			}()
			if !panicked || s == nil || s.closed != 1 || !reflect.DeepEqual(fx.trace, wantFX.trace[:at]) {
				t.Fatalf("%s/panic/%d replayed", op, at)
			}
		}
	}
}
func TestMetadataProjectionFlowOwnedAndLongTraversal(t *testing.T) {
	if !rustMetadataProjectionAvailable() {
		t.Fatal("native metadata unavailable")
	}
	t.Setenv("SMARTVPN_RUST_METADATA_PROJECTION", "1")
	t.Setenv("SMARTVPN_RUST_POLICY_DNS", "1")
	child := policyDnsRecoveries.Load()
	n := metadataProjectionRecoveries.Load()
	TestMetadataProjectionServiceGraphIdentity(t)
	TestMetadataProjectionServiceDetailsExactTypesAndLastWins(t)
	TestMetadataProjectionServiceDomainsOrderedNotDeduplicated(t)
	TestMetadataProjectionServiceCurrentPartialReadThenSnapshot(t)
	TestMetadataProjectionServiceModeDoesNotTrim(t)
	TestMetadataProjectionServiceLargeGraphNoArtificialLimit(t)
	if policyDnsRecoveries.Load() != child {
		t.Fatal("suffix child recovered")
	}
	if metadataProjectionRecoveries.Load() != n {
		t.Fatalf("normal data recovered: %v", metadataProjectionRecoveryReason.Load())
	}
}
func TestMetadataProjectionFlowOversizedReplyResumesOnce(t *testing.T) {
	if !rustMetadataProjectionAvailable() {
		t.Fatal("native metadata unavailable")
	}
	name := strings.Repeat("x", 5<<20)
	f := &metadataProjectionObserved{base: &appMetadataProjectionEffects{manual: []manualNode{{Name: name}}}}
	n := metadataProjectionRecoveries.Load()
	r, used := runRustMetadataProjection(metadataProjectionRequest{Op: "details"}, f)
	if !used || r.Err != nil || metadataProjectionRecoveries.Load() != n+1 || len(f.base.details) != 1 {
		t.Fatal("oversize did not resume safely")
	}
	puts := 0
	for _, e := range f.trace {
		if e.Kind == "details-put" {
			puts++
		}
	}
	if puts != 1 || f.base.details[name].Name != name {
		t.Fatal("oversize write replayed or bytes lost")
	}
}
func TestMetadataProjectionFlowCapacityAndCancel(t *testing.T) {
	if !rustMetadataProjectionAvailable() {
		t.Fatal("native metadata unavailable")
	}
	input := metadataProjectionInput(metadataProjectionRequest{Op: "mode"})
	sessions := []metadataProjectionSession{}
	defer func() {
		for _, s := range sessions {
			s.close()
		}
	}()
	for i := 0; i < 64; i++ {
		s, e := startRustMetadataProjection(input)
		if e != nil {
			t.Fatal(e)
		}
		sessions = append(sessions, s)
	}
	if s, e := startRustMetadataProjection(input); e == nil {
		s.close()
		t.Fatal("capacity exceeded")
	}
	for _, s := range sessions {
		s.close()
	}
	sessions = nil
	for i := 0; i < 1200; i++ {
		s, e := startRustMetadataProjection(input)
		if e != nil {
			t.Fatal(e)
		}
		s.close()
	}
}
