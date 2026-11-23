package main

import (
	"encoding/binary"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type settingsFlowFakeSession struct {
	frames       [][]byte
	at, failStep int
	repeat       bool
	closed       bool
}

func (f *settingsFlowFakeSession) next() ([]byte, uint64, error) {
	if f.at >= len(f.frames) {
		return nil, 0, errors.New("fake EOF")
	}
	step := uint64(f.at + 1)
	if f.repeat {
		step = 1
	}
	return f.frames[f.at], step, nil
}
func (f *settingsFlowFakeSession) advance(_ uint64, _ []byte) error {
	if f.failStep == f.at+1 {
		return errors.New("synthetic transport failure")
	}
	f.at++
	return nil
}
func (f *settingsFlowFakeSession) close() { f.closed = true }
func settingsTraceNoLogs(v []string) []string {
	out := []string{}
	for _, s := range v {
		if !strings.HasPrefix(s, "log:") {
			out = append(out, s)
		}
	}
	return out
}
func settingsFlowApply(f settingsFlowPlan) []byte {
	b := binary.LittleEndian.AppendUint16([]byte{2}, f.presence)
	for _, v := range []bool{f.next.DomesticWeb, f.next.WebRTCProtection, f.next.DNSProtection} {
		b = settingsFlowFlag(b, v)
	}
	return b
}
func TestSettingsFlowCommittedFaultsNeverReplay(t *testing.T) {
	for _, cache := range []bool{false, true} {
		for _, cacheFail := range []bool{false, true} {
			for _, saveFail := range []bool{false, true} {
				initial := settingsFixture()
				url := "https://new.invalid/sub"
				if cache {
					initial.Subscriptions = []subscriptionEntry{{ID: "cached", URL: url}}
				}
				r := prepareSettingsServiceRequest(settingsPatch{SubscriptionURL: &url}, true, false, 123, true)
				plan := prepareSettingsFlow(initial, r)
				frames := [][]byte{{1, 0}, settingsFlowApply(plan), {3}, {4}, {5}, {6}, {7, 0}, {8, 0}}
				if cache {
					frames = append(frames, []byte{9})
					if !cacheFail {
						frames = append(frames, []byte{8, 1})
					}
				}
				if !cache || cacheFail {
					frames = append(frames, []byte{10, 0}, []byte{10, 1}, []byte{10, 2})
				}
				if !cache {
					frames = append(frames, []byte{11}, []byte{12})
				}
				frames = append(frames, []byte{7, 1})
				for failStep := 2; failStep <= 2*len(frames)-1; failStep++ {
					wantState := settingsFixture()
					wantState.Subscriptions = append([]subscriptionEntry(nil), initial.Subscriptions...)
					wantFX := &settingsMemoryEffects{CacheError: cacheFail, CacheNodes: []proxyNode{{Name: "partial"}}, SaveFailures: []bool{true, saveFail}}
					want := runGoSettingsService(&wantState, r, wantFX)
					gotState := settingsFixture()
					gotState.Subscriptions = append([]subscriptionEntry(nil), initial.Subscriptions...)
					gotFX := &settingsMemoryEffects{CacheError: cacheFail, CacheNodes: []proxyNode{{Name: "partial"}}, SaveFailures: []bool{true, saveFail}}
					cutoff := 2 + (failStep-2)%(len(frames)-1)
					session := &settingsFlowFakeSession{frames: frames, failStep: cutoff}
					if failStep > len(frames) {
						session.frames = frames[:cutoff]
						session.failStep = 0
					}
					got, used := runRustSettingsServiceWithStart(&gotState, r, gotFX, func([]byte) (settingsFlowSession, error) { return session, nil })
					if !used || !session.closed || got != want || !reflect.DeepEqual(gotState, wantState) || !reflect.DeepEqual(settingsTraceNoLogs(gotFX.Trace), settingsTraceNoLogs(wantFX.Trace)) {
						t.Fatalf("cache=%v cacheFail=%v saveFail=%v step=%d got=%+v want=%+v\ngot trace%v\nwant trace%v", cache, cacheFail, saveFail, failStep, got, want, gotFX.Trace, wantFX.Trace)
					}
				}
			}
		}
	}
}
func TestSettingsFlowRejectsUntrustedPrelude(t *testing.T) {
	for _, frame := range [][]byte{{3}, {7, 1}, {13, 0}, {14}, {1, 0, 0}} {
		s := settingsFixture()
		before := s
		fx := &settingsMemoryEffects{}
		f := &settingsFlowFakeSession{frames: [][]byte{frame}}
		_, used := runRustSettingsServiceWithStart(&s, prepareSettingsServiceRequest(settingsPatch{}, true, false, 123, true), fx, func([]byte) (settingsFlowSession, error) { return f, nil })
		if used || len(fx.Trace) != 0 || !reflect.DeepEqual(s, before) {
			t.Fatal("untrusted prelude executed", frame)
		}
	}
}
func TestSettingsFlowFailureBeforeCommitDoesNotReplayKernel(t *testing.T) {
	s := settingsFixture()
	before := s
	fx := &settingsMemoryEffects{}
	f := &settingsFlowFakeSession{frames: [][]byte{{1, 0}}, failStep: 1}
	result, used := runRustSettingsServiceWithStart(&s, prepareSettingsServiceRequest(settingsPatch{}, true, false, 123, true), fx, func([]byte) (settingsFlowSession, error) { return f, nil })
	if !used || result.Status != 500 || !reflect.DeepEqual(fx.Trace, []string{"running"}) || !reflect.DeepEqual(s, before) {
		t.Fatal(result, fx.Trace)
	}
}

func TestSettingsFlowSecondKernelCheckWinsBeforeCommit(t *testing.T) {
	s := settingsFixture()
	before := s
	fx := &settingsMemoryEffects{Running: []bool{false, true}}
	f := &settingsFlowFakeSession{frames: [][]byte{{1, 1}, {1, 0}, {13, 9}}}
	result, used := runRustSettingsServiceWithStart(&s, prepareSettingsServiceRequest(settingsPatch{ImportPolicy: settingsBool(true)}, true, false, 123, true), fx, func([]byte) (settingsFlowSession, error) { return f, nil })
	if !used || result.Status != 409 || !reflect.DeepEqual(fx.Trace, []string{"running", "running"}) || !reflect.DeepEqual(s, before) {
		t.Fatal(result, fx.Trace, s)
	}
}
