//go:build windows && amd64 && rustcore

package main

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

type driverInjectedSession struct {
	driverInstallSession
	nextAt, advanceAt, nextCalls, advanceCalls int
	closed                                     bool
}

func (s *driverInjectedSession) next() ([]byte, uint64, error) {
	s.nextCalls++
	if s.nextCalls == s.nextAt {
		return nil, 0, errors.New("injected next")
	}
	return s.driverInstallSession.next()
}
func (s *driverInjectedSession) advance(step uint64, b []byte) error {
	s.advanceCalls++
	if s.advanceCalls == s.advanceAt {
		_ = s.driverInstallSession.advance(step, b)
		return errors.New("uncertain acknowledged advance")
	}
	return s.driverInstallSession.advance(step, b)
}
func (s *driverInjectedSession) close() {
	if s.closed {
		panic("doubleclose")
	}
	s.closed = true
	s.driverInstallSession.close()
}
func TestDriverInstallActualFaultEveryEffectBoundary(t *testing.T) {
	if !rustDriverInstallAvailable() {
		t.Fatal("native driver install unavailable")
	}
	base := driverIndependentSpec{Source: "local", Body: driverIndependentPE(0x8664), Signature: "valid:WireGuard LLC", HTTP: 200, ZipHash: driverWintunZipSHA256, DLLHash: driverWintunDLLSHA256, Entries: []string{driverWintunEntry}, Elevated: true}
	for _, scenario := range []string{"local", "official", "signaturefail", "renamefail", "download", "pe", "signature"} {
		spec := base
		q := driverInstallRequest{Op: "install", Path: "synthetic.tmp"}
		switch scenario {
		case "official":
			spec.Source = ""
		case "signaturefail":
			spec.Signature = "invalid:test"
		case "renamefail":
			spec.Fail = "rename"
		case "download", "pe", "signature":
			q.Op = scenario
		}
		wfx := &driverIndependentFX{s: spec}
		want := runGoDriverInstall(q, wfx)
		for _, phase := range []string{"next", "advance"} {
			count := len(wfx.trace)
			if phase == "next" {
				count++
			}
			for at := 1; at <= count; at++ {
				t.Run(fmt.Sprintf("%s/%s/%d", scenario, phase, at), func(t *testing.T) {
					fx := &driverIndependentFX{s: spec}
					var session *driverInjectedSession
					before := driverInstallRecoveries.Load()
					got, used := runRustDriverInstallWithStart(q, fx, func(b []byte) (driverInstallSession, error) {
						native, e := startRustDriverInstall(b)
						if e != nil {
							return nil, e
						}
						session = &driverInjectedSession{driverInstallSession: native}
						if phase == "next" {
							session.nextAt = at
						} else {
							session.advanceAt = at
						}
						return session, nil
					})
					if !used || session == nil || !session.closed || !reflect.DeepEqual(driverInstallResultJSON(got), driverInstallResultJSON(want)) || !reflect.DeepEqual(fx.trace, wfx.trace) {
						t.Fatal("resource replay/result mismatch")
					}
					if driverInstallRecoveries.Load() != before+1 {
						t.Fatal("expected one controlled continuation")
					}
					fx.Release()
				})
			}
		}
	}
}
func TestDriverInstallActualInvalidUTF8AndCapacityNoReplay(t *testing.T) {
	if !rustDriverInstallAvailable() {
		t.Fatal("native driver install unavailable")
	}
	for _, capacity := range []int{0, 1, 2} {
		spec := driverIndependentSpec{Source: "local", Body: driverIndependentPE(0x8664), Signature: "valid:WireGuard LLC" + string([]byte{255}), HTTP: 200, ZipHash: driverWintunZipSHA256}
		if capacity > 0 {
			spec.Source = ""
			spec.Entries = make([]string, 100001)
			for i := range spec.Entries {
				spec.Entries[i] = "not-driver"
			}
		}
		if capacity == 2 {
			spec.Entries = make([]string, 90000)
			for i := range spec.Entries {
				spec.Entries[i] = strings.Repeat("x", 80)
			}
		}
		q := driverInstallRequest{Op: "install"}
		wfx := &driverIndependentFX{s: spec}
		want := runGoDriverInstall(q, wfx)
		fx := &driverIndependentFX{s: spec}
		before := driverInstallRecoveries.Load()
		got, used := runRustDriverInstall(q, fx)
		if !used || !reflect.DeepEqual(driverInstallResultJSON(got), driverInstallResultJSON(want)) || !reflect.DeepEqual(fx.trace, wfx.trace) || driverInstallRecoveries.Load() != before+1 {
			t.Fatal("invalid reply continuation changed effects")
		}
		fx.Release()
	}
}

func TestDriverInstallActualOwnedAdapterNoRecovery(t *testing.T) {
	if !rustDriverInstallAvailable() {
		t.Fatal("native driver install unavailable")
	}
	t.Setenv("SMARTVPN_RUST_DRIVER_INSTALL", "1")
	before := driverInstallRecoveries.Load()
	TestDriverInstallSafeTempFilesAndFailureCleanup(t)
	TestDriverInstallSafeFakeHTTPRealArchiveClosesInOrder(t)
	TestDriverInstallSafeHTTPBoundAndHashPriority(t)
	TestDriverInstallOwnedHTTPResponseThenUnlock(t)
	if driverInstallRecoveries.Load() != before {
		t.Fatalf("adapter native fallback: %v", driverInstallRecoveryReason.Load())
	}
}
