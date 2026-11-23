//go:build rustcore && ((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

import (
	"errors"
	"os"
	"runtime"
	"sync"
	"unsafe"
)

var policyDnsRust struct {
	once                        sync.Once
	start, next, advance, close uintptr
}

func rustPolicyDnsAvailable() bool {
	policyDnsRust.once.Do(func() {
		if !rustCoreAvailable() {
			return
		}
		version, _ := nativeLookup(rustCore.handle, "smartvpn_policy_dns_version_v1")
		if version == 0 {
			return
		}
		v, _, _ := nativeCall(version)
		if v != 1 {
			return
		}
		policyDnsRust.start, _ = nativeLookup(rustCore.handle, "smartvpn_policy_dns_start_v1")
		policyDnsRust.next, _ = nativeLookup(rustCore.handle, "smartvpn_policy_dns_next_v1")
		policyDnsRust.advance, _ = nativeLookup(rustCore.handle, "smartvpn_policy_dns_advance_v1")
		policyDnsRust.close, _ = nativeLookup(rustCore.handle, "smartvpn_policy_dns_close_v1")
	})
	return policyDnsRust.start != 0 && policyDnsRust.next != 0 && policyDnsRust.advance != 0 && policyDnsRust.close != 0
}

type rustPolicyDnsSession struct{ id uint64 }

func startRustPolicyDns(input []byte) (policyDnsSession, error) {
	if !rustPolicyDnsAvailable() || len(input) == 0 || len(input) > 8<<20 {
		return nil, errors.New("policy DNS unavailable")
	}
	var id uint64
	status, _, _ := nativeCall(policyDnsRust.start, uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)), uintptr(unsafe.Pointer(&id)))
	runtime.KeepAlive(input)
	runtime.KeepAlive(&id)
	if int32(status) != 0 || id == 0 {
		return nil, errors.New("policy DNS start failed")
	}
	return &rustPolicyDnsSession{id}, nil
}
func (s *rustPolicyDnsSession) next() ([]byte, uint64, error) {
	var written uintptr
	var step uint64
	status, _, _ := nativeCall(policyDnsRust.next, uintptr(s.id), 0, 0, uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	if int32(status) != -3 || written == 0 || written > 8<<20 {
		return nil, 0, errors.New("policy DNS frame size rejected")
	}
	out := make([]byte, int(written))
	status, _, _ = nativeCall(policyDnsRust.next, uintptr(s.id), uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)), uintptr(unsafe.Pointer(&written)), uintptr(unsafe.Pointer(&step)))
	runtime.KeepAlive(out)
	runtime.KeepAlive(&written)
	runtime.KeepAlive(&step)
	if int32(status) != 0 || written > uintptr(len(out)) || step == 0 {
		return nil, 0, errors.New("policy DNS next rejected")
	}
	return out[:written], step, nil
}
func (s *rustPolicyDnsSession) advance(step uint64, input []byte) error {
	if len(input) == 0 || len(input) > 8<<20 {
		return errors.New("policy DNS reply too large")
	}
	status, _, _ := nativeCall(policyDnsRust.advance, uintptr(s.id), uintptr(step), uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)))
	runtime.KeepAlive(input)
	if int32(status) != 0 {
		return errors.New("policy DNS advance rejected")
	}
	return nil
}
func (s *rustPolicyDnsSession) close() {
	if s.id != 0 {
		nativeCall(policyDnsRust.close, uintptr(s.id))
		s.id = 0
	}
}
func runRustPolicyDns(r policyDnsRequest, fx policyDnsEffects) (policyDnsResult, bool) {
	return runRustPolicyDnsWithStart(r, fx, startRustPolicyDns)
}
func runSelectedPolicyDns(r policyDnsRequest, fx policyDnsEffects) policyDnsResult {
	if os.Getenv("SMARTVPN_RUST_POLICY_DNS") == "1" {
		if result, used := runRustPolicyDns(r, fx); used {
			return result
		}
	}
	return runGoPolicyDns(r, fx)
}
