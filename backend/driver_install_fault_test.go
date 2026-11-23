package main

import (
	"encoding/binary"
	"errors"
	"reflect"
	"testing"
)

type driverInstallFaultSession struct{ advanced, failAdvance, closed bool }

func (s *driverInstallFaultSession) next() ([]byte, uint64, error) {
	if s.advanced {
		return nil, 0, errors.New("synthetic next")
	}
	b := tunReportBytes(tunReportBytes(tunReportBytes(nil, "decode"), ""), "")
	b = binary.LittleEndian.AppendUint64(b, 0)
	return tunReportBytes(b, ""), 1, nil
}
func (s *driverInstallFaultSession) advance(_ uint64, _ []byte) error {
	s.advanced = true
	if s.failAdvance {
		return errors.New("synthetic uncertain advance")
	}
	return nil
}
func (s *driverInstallFaultSession) close() { s.closed = true }
func TestDriverInstallContinuationNeverReplaysObservedDecode(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		q := driverInstallRequest{Op: "install"}
		spec := driverIndependentSpec{Source: "local", Body: driverIndependentPE(0x8664), Signature: "valid:WireGuard LLC"}
		wfx := &driverIndependentFX{s: spec}
		want := runGoDriverInstall(q, wfx)
		fx := &driverIndependentFX{s: spec}
		session := &driverInstallFaultSession{failAdvance: uncertain}
		got, used := runRustDriverInstallWithStart(q, fx, func([]byte) (driverInstallSession, error) { return session, nil })
		if !used || !session.closed || !reflect.DeepEqual(driverInstallResultJSON(want), driverInstallResultJSON(got)) || !reflect.DeepEqual(wfx.trace, fx.trace) {
			t.Fatal("driver continuation replay/result mismatch")
		}
		fx.Release()
	}
}
