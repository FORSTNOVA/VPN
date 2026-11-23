package main

import (
	"errors"
	"reflect"
	"testing"
)

type healthFlowFakeSession struct {
	frames     [][]byte
	at         int
	advanceErr bool
	failStep   int
	repeat     bool
	closed     bool
}

func (f *healthFlowFakeSession) next() ([]byte, uint64, error) {
	if f.at >= len(f.frames) {
		return nil, 0, errors.New("fake eof")
	}
	step := uint64(f.at + 1)
	if f.repeat {
		step = 1
	}
	return f.frames[f.at], step, nil
}
func (f *healthFlowFakeSession) advance(_ uint64, _ []byte) error {
	if f.advanceErr || f.failStep == f.at+1 {
		return errors.New("synthetic continuation failure")
	}
	f.at++
	return nil
}
func (f *healthFlowFakeSession) close() { f.closed = true }
func TestHealthFlowNoReplayAfterEffect(t *testing.T) {
	s, now := healthServiceFixture()
	s.Blocked = true
	fx := &healthServiceMemoryEffects{}
	session := &healthFlowFakeSession{frames: [][]byte{healthFlowInt([]byte{5}, 1)}, advanceErr: true}
	used := runRustHealthServiceWithStart(&s, healthServiceRequest{Kind: "unblock", Now: now}, fx, func([]byte) (healthFlowSession, error) { return session, nil })
	if !used || !session.closed {
		t.Fatal("must remain selected")
	}
	if got := healthTraceWithoutLogs(fx.Trace); !reflect.DeepEqual(got, []string{"switch:b", "settings:b", "event:region_recovered"}) {
		t.Fatal(got)
	}
	if s.Blocked || s.LockedNode != "b" || s.LastSwitch != now {
		t.Fatal("confirmed switch must finish local completion")
	}
}
func TestHealthFlowRejectsInvalidSequence(t *testing.T) {
	for _, tc := range []struct {
		name       string
		frames     [][]byte
		chain      bool
		failSwitch bool
	}{
		{name: "commit before switch", frames: [][]byte{healthFlowInt([]byte{7, 1}, 1)}},
		{name: "event before commit", frames: [][]byte{healthFlowInt(healthFlowInt(healthFlowInt([]byte{8, 1}, 0), 1), 10)}},
		{name: "chain switch", chain: true, frames: [][]byte{healthFlowInt([]byte{5}, 1)}},
		{name: "failed switch cannot commit", failSwitch: true, frames: [][]byte{flowRecordFrame(0, 3, 1, 0, 0, false), healthFlowInt([]byte{5}, 1), healthFlowInt([]byte{7, 1}, 1)}},
		{name: "reject without confirm", frames: [][]byte{healthFlowInt([]byte{5}, -1)}},
		{name: "trailing response", frames: [][]byte{{1, 0}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, now := healthServiceFixture()
			s.ChainEnabled = tc.chain
			fx := &healthServiceMemoryEffects{Fail: map[string]bool{"switch:b": tc.failSwitch}}
			f := &healthFlowFakeSession{frames: tc.frames}
			runRustHealthServiceWithStart(&s, healthServiceRequest{Kind: "failed", Active: "a", Now: now}, fx, func([]byte) (healthFlowSession, error) { return f, nil })
			if s.LockedNode != "a" || !s.LastSwitch.IsZero() || len(fx.Events) != 0 {
				t.Fatal("malformed plan changed selection")
			}
			if tc.failSwitch && !reflect.DeepEqual(healthTraceWithoutLogs(fx.Trace), []string{"health:a:UNHEALTHY", "switch:b"}) {
				t.Fatal("failed switch path not exercised", fx.Trace)
			}
			if !tc.failSwitch && len(healthTraceWithoutLogs(fx.Trace)) != 0 {
				t.Fatal(fx.Trace)
			}
		})
	}
}
func TestHealthFlowRejectsRepeatedStep(t *testing.T) {
	s, now := healthServiceFixture()
	s.Blocked = true
	fx := &healthServiceMemoryEffects{}
	f := &healthFlowFakeSession{frames: [][]byte{healthFlowInt([]byte{5}, 1), healthFlowInt([]byte{7, 3}, 1)}, repeat: true}
	used := runRustHealthServiceWithStart(&s, healthServiceRequest{Kind: "unblock", Now: now}, fx, func([]byte) (healthFlowSession, error) { return f, nil })
	if !used || len(fx.Saved) != 1 || len(fx.Events) != 1 || s.Blocked || s.LockedNode != "b" {
		t.Fatal("duplicate step lost/repeated completion", fx.Trace)
	}
}
func flowRecordFrame(node int64, state, failures, successes, latency int64, cooling bool) []byte {
	b := healthFlowInt([]byte{2}, node)
	for _, v := range []int64{state, failures, successes, latency} {
		b = healthFlowInt(b, v)
	}
	b = healthFlowBool(b, false)
	return healthFlowBool(b, cooling)
}
func TestHealthFlowFailoverCompletionEveryBoundary(t *testing.T) {
	// Each prefix stops transport after the indicated already-executed effect.
	frames := [][]byte{flowRecordFrame(0, 3, 1, 0, 0, false), healthFlowInt([]byte{5}, 1), flowRecordFrame(0, 4, 1, 0, 0, true), {6}, healthFlowInt([]byte{7, 1}, 1), healthFlowInt(healthFlowInt(healthFlowInt([]byte{8, 1}, 0), 1), 10)}
	for cutoff := 2; cutoff <= len(frames); cutoff++ {
		s, now := healthServiceFixture()
		fx := &healthServiceMemoryEffects{}
		f := &healthFlowFakeSession{frames: frames[:cutoff]}
		used := runRustHealthServiceWithStart(&s, healthServiceRequest{Kind: "failed", Active: "a", Now: now}, fx, func([]byte) (healthFlowSession, error) { return f, nil })
		want := []string{"health:a:UNHEALTHY", "switch:b", "health:a:COOLDOWN", "settings:a", "event:unhealthy"}
		if !used || !reflect.DeepEqual(healthTraceWithoutLogs(fx.Trace), want) || len(fx.Saved) != 1 || fx.Saved[0].LockedNode != "a" || s.LockedNode != "b" || s.Health["a"].State != healthCooldown {
			t.Fatalf("cutoff %d: %v %+v", cutoff, fx.Trace, s)
		}
	}
}
func TestHealthFlowPreludeCannotBeSkipped(t *testing.T) {
	for _, frame := range [][]byte{{10}, {3}, {4}, healthFlowInt([]byte{5}, 1)} {
		s, now := healthServiceFixture()
		fx := &healthServiceMemoryEffects{}
		f := &healthFlowFakeSession{frames: [][]byte{frame}}
		if runRustHealthServiceWithStart(&s, healthServiceRequest{Kind: "failed", Active: "a", Now: now}, fx, func([]byte) (healthFlowSession, error) { return f, nil }) || len(fx.Trace) != 0 {
			t.Fatal("untrusted prelude accepted")
		}
	}
}

func TestHealthFlowRejectCompletionAfterAdvanceFailure(t *testing.T) {
	s, now := healthServiceFixture()
	s.Regions = map[string]regionRecord{}
	fx := &healthServiceMemoryEffects{}
	f := &healthFlowFakeSession{frames: [][]byte{flowRecordFrame(0, 3, 1, 0, 0, false), {4}, healthFlowInt([]byte{5}, -1)}, failStep: 3}
	used := runRustHealthServiceWithStart(&s, healthServiceRequest{Kind: "failed", Active: "a", Now: now}, fx, func([]byte) (healthFlowSession, error) { return f, nil })
	want := []string{"health:a:UNHEALTHY", "confirm", "switch:" + blockedChoice, "event:region_exhausted"}
	if !used || !s.Blocked || s.LastSwitch != now || s.BlockReason != healthFlowBlockReason(2) || !reflect.DeepEqual(healthTraceWithoutLogs(fx.Trace), want) || len(fx.Events) != 1 {
		t.Fatal("REJECT completion lost/replayed", fx.Trace, s)
	}
}
