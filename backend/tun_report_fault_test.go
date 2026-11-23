package main

import (
	"errors"
	"reflect"
	"testing"
)

type tunReportFaultSession struct {
	kind                          string
	advanced, failAdvance, closed bool
}

func (s *tunReportFaultSession) next() ([]byte, uint64, error) {
	if s.advanced {
		return nil, 0, errors.New("synthetic next failure")
	}
	return tunReportBytes(tunReportBytes(tunReportBytes(nil, s.kind), ""), ""), 1, nil
}
func (s *tunReportFaultSession) advance(_ uint64, _ []byte) error {
	s.advanced = true
	if s.failAdvance {
		return errors.New("synthetic uncertain advance")
	}
	return nil
}
func (s *tunReportFaultSession) close() { s.closed = true }
func TestTunReportContinuationNeverReplaysObservedEffect(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		q := tunReportRequest{Op: "windows-status"}
		wantfx := &tunIndependentFX{}
		want := runGoTunReport(q, wantfx)
		fx := &tunIndependentFX{}
		session := &tunReportFaultSession{kind: "lock", failAdvance: uncertain}
		got, used := runRustTunReportWithStart(q, fx, func([]byte) (tunReportSession, error) { return session, nil })
		if !used || got.Err != nil || !session.closed || !reflect.DeepEqual(want.Data, got.Data) || !reflect.DeepEqual(wantfx.trace, fx.trace) {
			t.Fatalf("continuation replay or mismatch uncertain=%v got=%#v trace=%#v", uncertain, got, fx.trace)
		}
		fx.Release()
		if fx.releases != 1 {
			t.Fatal("fixture owner release mismatch")
		}
	}
}
