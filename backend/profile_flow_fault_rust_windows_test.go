//go:build windows && amd64 && rustcore

package main

import (
	"errors"
	"reflect"
	"testing"
)

type profileInjectedSession struct {
	profileFlowSession
	kind                         byte
	at                           byte
	nextFailure, pending, failed bool
}

func (s *profileInjectedSession) next() ([]byte, uint64, error) {
	if s.pending {
		s.pending = false
		s.failed = true
		return nil, 0, errors.New("synthetic next failure")
	}
	b, n, e := s.profileFlowSession.next()
	if e == nil {
		f, err := decodeProfileFrame(b)
		if err == nil {
			s.kind = f.kind
		}
	}
	return b, n, e
}
func (s *profileInjectedSession) advance(n uint64, b []byte) error {
	if !s.failed && s.kind == s.at {
		if s.nextFailure {
			s.pending = true
		} else {
			s.failed = true
			return errors.New("synthetic advance failure")
		}
	}
	return s.profileFlowSession.advance(n, b)
}
func TestProfileFlowApplyFaultPreservesCompensationExactlyOnce(t *testing.T) {
	if !rustProfileFlowAvailable() {
		t.Fatal("actual DLL required")
	}
	for _, failure := range []string{"", "apply-live-config"} {
		for _, next := range []bool{false, true} {
			a := profileFaultState()
			b := profileCloneState(a)
			x := &profileFaultEffects{running: true, fail: failure}
			y := &profileFaultEffects{running: true, fail: failure}
			q := profileServiceRequest{Op: "write-merged", Sources: []SubscriptionSource{{ID: "old", Body: []byte("source")}}}
			want := runGoProfileService(&a, q, x)
			var injected *profileInjectedSession
			got, used := runRustProfileServiceWithStart(&b, q, y, func(input []byte) (profileFlowSession, error) {
				session, err := startRustProfileFlow(input)
				if err != nil {
					return nil, err
				}
				injected = &profileInjectedSession{profileFlowSession: session, at: 10, nextFailure: next}
				return injected, nil
			})
			if !used || injected == nil || !injected.failed || !reflect.DeepEqual(a, b) || !reflect.DeepEqual(x.trace, y.trace) || (got.Err != nil) != (want.Err != nil) {
				t.Fatal(failure, next, got, want, x.trace, y.trace)
			}
			applies := 0
			for _, e := range y.trace {
				if e.Kind == "apply-live-config" {
					applies++
				}
			}
			if applies != 1 {
				t.Fatal("live apply replayed", applies)
			}
		}
	}
}

func TestProfileFlowRealCapacityFallback(t *testing.T) {
	if !rustProfileFlowAvailable() {
		t.Fatal("actual DLL required")
	}
	s := profileFaultState()
	input, err := profileStartInput(s, profileServiceRequest{Op: "cache-id"}, &profileArena{})
	if err != nil {
		t.Fatal(err)
	}
	var held []profileFlowSession
	defer func() {
		for _, h := range held {
			h.close()
		}
	}()
	for i := 0; i < 512; i++ {
		h, err := startRustProfileFlow(input)
		if err != nil {
			t.Fatal(i, err)
		}
		held = append(held, h)
	}
	if h, err := startRustProfileFlow(input); err == nil {
		h.close()
		t.Fatal("capacity missing")
	}
	t.Setenv("SMARTVPN_RUST_PROFILE_FLOW", "1")
	fx := &profileFaultEffects{}
	out := runSelectedProfileService(&s, profileServiceRequest{Op: "replace", Path: s.ProfilePath, Body: []byte("synthetic")}, fx)
	if out.Err != nil || len(fx.trace) != 2 || fx.trace[0].Kind != "write" || fx.trace[1].Kind != "rename" {
		t.Fatal(out, fx.trace)
	}
	for _, h := range held {
		h.close()
	}
	held = nil
	fx = &profileFaultEffects{}
	_, used := runRustProfileService(&s, profileServiceRequest{Op: "replace", Path: s.ProfilePath, Body: []byte("synthetic")}, fx)
	if !used || len(fx.trace) != 2 {
		t.Fatal("capacity did not recover")
	}
}
func TestProfileFlowBatchFaultKeepsOrderedWrites(t *testing.T) {
	if !rustProfileFlowAvailable() {
		t.Fatal("actual DLL required")
	}
	for _, next := range []bool{false, true} {
		state, base := pipelineDiffBase()
		a, b := pipelineClone(state), pipelineClone(state)
		x, y := pipelineFakeCopy(base), pipelineFakeCopy(base)
		q := profileServiceRequest{Op: "merge-all"}
		want := runGoProfileService(&a, q, x)
		var injected *profileInjectedSession
		got, used := runRustProfileServiceWithStart(&b, q, y, func(input []byte) (profileFlowSession, error) {
			session, err := startRustProfileFlow(input)
			if err != nil {
				return nil, err
			}
			injected = &profileInjectedSession{profileFlowSession: session, at: 24, nextFailure: next}
			return injected, nil
		})
		if !used || !injected.failed || !reflect.DeepEqual(a, b) || !reflect.DeepEqual(x.Trace, y.Trace) || !reflect.DeepEqual(x.WorkerTraces, y.WorkerTraces) || (got.Err != nil) != (want.Err != nil) {
			t.Fatal(next, got, want)
		}
		batches := 0
		for _, e := range y.Trace {
			if e.Effect.Kind == "acquire-batch" {
				batches++
			}
		}
		if batches != 1 {
			t.Fatal("batch replayed", batches)
		}
	}
}

type profileCapacityBatchEffects struct {
	base                         *pipelineDiffFX
	input                        []byte
	heldCount, children, batches int
}

func (f *profileCapacityBatchEffects) Call(q profileEffect, s *profileServiceState) profileReply {
	if q.Kind != "acquire-batch" {
		return f.base.Call(q, s)
	}
	f.batches++
	var held []profileFlowSession
	defer func() {
		for _, h := range held {
			h.close()
		}
	}()
	for {
		h, err := startRustProfileFlow(f.input)
		if err != nil {
			break
		}
		held = append(held, h)
	}
	f.heldCount = len(held)
	var out profileReply
	for _, entry := range q.Entries {
		child := *s
		fx := pipelineFakeCopy(f.base)
		result := runSelectedProfileService(&child, profileServiceRequest{Op: "acquire-entry", Entry: entry}, fx)
		if result.Err != nil {
			return profileReply{Err: result.Err}
		}
		out.Results = append(out.Results, result.Acquisition)
		f.children++
	}
	return out
}
func TestProfileFlowParentCapacityChildFallbackDoesNotReplayBatch(t *testing.T) {
	if !rustProfileFlowAvailable() {
		t.Fatal("actual DLL required")
	}
	t.Setenv("SMARTVPN_RUST_PROFILE_FLOW", "1")
	s, base := pipelineDiffBase()
	input, err := profileStartInput(s, profileServiceRequest{Op: "cache-id"}, &profileArena{})
	if err != nil {
		t.Fatal(err)
	}
	fx := &profileCapacityBatchEffects{base: base, input: input}
	before := profileFlowRecoveries.Load()
	result, used := runRustProfileService(&s, profileServiceRequest{Op: "merge-all"}, fx)
	if !used || result.Err != nil || fx.heldCount != 511 || fx.children != 1 || fx.batches != 1 || before != profileFlowRecoveries.Load() {
		t.Fatal(result, fx.heldCount, fx.children, fx.batches)
	}
}
