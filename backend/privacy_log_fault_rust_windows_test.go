//go:build windows && amd64 && rustcore

package main

import (
	"bytes"
	"encoding/hex"
	"errors"
	"reflect"
	"testing"
)

type privacyLogInjectedSession struct {
	privacyLogSession
	nextAt, advanceAt, nextCalls, advanceCalls int
	closed                                     bool
}

func (s *privacyLogInjectedSession) next() ([]byte, uint64, error) {
	s.nextCalls++
	if s.nextCalls == s.nextAt {
		return nil, 0, errors.New("injected next")
	}
	return s.privacyLogSession.next()
}
func (s *privacyLogInjectedSession) advance(n uint64, b []byte) error {
	s.advanceCalls++
	if s.advanceCalls == s.advanceAt {
		_ = s.privacyLogSession.advance(n, b)
		return errors.New("uncertain ack")
	}
	return s.privacyLogSession.advance(n, b)
}
func (s *privacyLogInjectedSession) close() {
	if s.closed {
		panic("double close")
	}
	s.closed = true
	s.privacyLogSession.close()
}

type privacyLogFaultFX struct {
	trace []privacyLogEffect
	fail  string
}

func (f *privacyLogFaultFX) Release() {}
func (f *privacyLogFaultFX) Call(e privacyLogEffect) privacyLogReply {
	f.trace = append(f.trace, e)
	if e.Kind == f.fail {
		return privacyLogReply{Err: errors.New("synthetic")}
	}
	switch e.Kind {
	case "secrets":
		return privacyLogReply{Names: []string{hex.EncodeToString([]byte(" https://synthetic.invalid/secret "))}}
	case "read":
		return privacyLogReply{Bytes: []byte("https://synthetic.invalid/secret\n")}
	case "write-source":
		return privacyLogReply{Bytes: []byte{'x', 255, 'z'}, Names: []string{hex.EncodeToString([]byte{255})}}
	case "write-temp", "rename", "sink", "log-error", "log-summary":
		return privacyLogReply{}
	}
	panic("unexpected privacy fake")
}
func TestPrivacyLogFlowFaultEveryBoundary(t *testing.T) {
	if !rustPrivacyLogAvailable() {
		t.Fatal("native privacy unavailable")
	}
	for _, op := range []string{"secrets", "scrub", "write"} {
		for _, failure := range []string{"", "write-temp", "rename", "sink"} {
			q := privacyLogRequest{Op: op}
			wantFX := &privacyLogFaultFX{fail: failure}
			want := runGoPrivacyLog(q, wantFX)
			for _, phase := range []string{"next", "advance"} {
				count := len(wantFX.trace)
				if phase == "next" {
					count++
				}
				for at := 1; at <= count; at++ {
					fx := &privacyLogFaultFX{fail: failure}
					var session *privacyLogInjectedSession
					n := privacyLogRecoveries.Load()
					got, used := runRustPrivacyLogWithStart(q, fx, func(b []byte) (privacyLogSession, error) {
						v, e := startRustPrivacyLog(b)
						if e != nil {
							return nil, e
						}
						session = &privacyLogInjectedSession{privacyLogSession: v}
						if phase == "next" {
							session.nextAt = at
						} else {
							session.advanceAt = at
						}
						return session, nil
					})
					if !used || session == nil || !session.closed || !reflect.DeepEqual(fx.trace, wantFX.trace) || !reflect.DeepEqual(privacyLogResultJSON(got), privacyLogResultJSON(want)) || privacyLogRecoveries.Load() != n+1 {
						t.Fatalf("%s/%s/%s/%d continuation changed effects: %v", op, failure, phase, at, privacyLogRecoveryReason.Load())
					}
				}
			}
		}
	}
}
func TestPrivacyLogFlowOwnedAndLargeTailNoRecovery(t *testing.T) {
	if !rustPrivacyLogAvailable() {
		t.Fatal("native privacy unavailable")
	}
	t.Setenv("SMARTVPN_RUST_PRIVACY_LOG", "1")
	n := privacyLogRecoveries.Load()
	TestPrivacyLogServiceTempCommitAndFailures(t)
	TestPrivacyLogServiceUnicodeTrimPreservesInvalidBytes(t)
	TestPrivacyLogServiceSinkOriginalShortWriteSemantics(t)
	TestPrivacyLogServiceTailBeforeOrderedReplacement(t)
	if privacyLogRecoveries.Load() != n {
		t.Fatalf("normal owned native recovered: %v", privacyLogRecoveryReason.Load())
	}
}
func TestPrivacyLogFlowEmptyNeedleContinuation(t *testing.T) {
	if !rustPrivacyLogAvailable() {
		t.Fatal("native privacy unavailable")
	}
	t.Setenv("SMARTVPN_RUST_PRIVACY_LOG", "1")
	n := privacyLogRecoveries.Load()
	TestPrivacyLogServiceRawBytesAndWriterCount(t)
	if privacyLogRecoveries.Load() != n+1 {
		t.Fatal("empty needle must resume Go rune semantics exactly once")
	}
}

type privacyLogLargeSink struct {
	want  []byte
	calls int
	t     *testing.T
}

func (w *privacyLogLargeSink) Write(b []byte) (int, error) {
	w.calls++
	if !bytes.Equal(b, w.want) {
		w.t.Fatal("large continuation changed raw bytes")
	}
	return len(b), nil
}

type privacyLogLargeFX struct {
	base         *appPrivacyLogEffects
	source, sink int
}

func (f *privacyLogLargeFX) Release() { f.base.Release() }
func (f *privacyLogLargeFX) Call(e privacyLogEffect) privacyLogReply {
	switch e.Kind {
	case "write-source":
		f.source++
	case "sink":
		f.sink++
	default:
		panic("unexpected large-write effect")
	}
	return f.base.Call(e)
}

type privacyLogObservedSession struct {
	privacyLogSession
	advanceOK, nextErrors, closes int
}

func (s *privacyLogObservedSession) advance(n uint64, b []byte) error {
	e := s.privacyLogSession.advance(n, b)
	if e == nil {
		s.advanceOK++
	}
	return e
}
func (s *privacyLogObservedSession) next() ([]byte, uint64, error) {
	b, n, e := s.privacyLogSession.next()
	if e != nil {
		s.nextErrors++
	}
	return b, n, e
}
func (s *privacyLogObservedSession) close() { s.closes++; s.privacyLogSession.close() }
func TestPrivacyLogFlowOversizedHexFrameContinuesSinkOnce(t *testing.T) {
	if !rustPrivacyLogAvailable() {
		t.Fatal("native privacy unavailable")
	}
	// Input fits the 64 MiB reply limit; its hex effect exceeds the same next-frame limit.
	body := bytes.Repeat([]byte{0, 255, 'a', 128}, (33<<20)/4)
	sink := &privacyLogLargeSink{want: body, t: t}
	fx := &privacyLogLargeFX{base: &appPrivacyLogEffects{line: body, out: sink}}
	before := privacyLogRecoveries.Load()
	var session *privacyLogObservedSession
	got, used := runRustPrivacyLogWithStart(privacyLogRequest{Op: "write"}, fx, func(b []byte) (privacyLogSession, error) {
		v, e := startRustPrivacyLog(b)
		if e != nil {
			return nil, e
		}
		session = &privacyLogObservedSession{privacyLogSession: v}
		return session, nil
	})
	if !used || got.Err != nil || got.Status != len(body) || got.Data != nil || got.Text != "" {
		t.Fatal("large continuation result changed")
	}
	if session == nil || session.advanceOK != 1 || session.nextErrors != 1 || session.closes != 1 {
		t.Fatalf("expected successful advance then rejected next and one close: %+v", session)
	}
	if fx.source != 1 || fx.sink != 1 || sink.calls != 1 || privacyLogRecoveries.Load() != before+1 {
		t.Fatal("large frame did not continue paused oracle exactly once")
	}
}
