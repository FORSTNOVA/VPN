//go:build windows && amd64 && rustcore

package main

import (
	"encoding/binary"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

func trafficProjectionStrict(t *testing.T, q trafficProjectionRequest) trafficProjectionResult {
	t.Helper()
	if !rustTrafficProjectionAvailable() {
		t.Fatal("projection export unavailable")
	}
	r, ok := runRustTrafficProjection(q)
	if !ok || !reflect.DeepEqual(r, runGoTrafficProjection(q)) {
		t.Fatalf("native differs op=%d ok=%v got=%#v want=%#v", q.Op, ok, r, runGoTrafficProjection(q))
	}
	return r
}
func TestTrafficProjectionFlowStrict(t *testing.T) {
	for op := byte(0); op <= 8; op++ {
		for i := 0; i < 80; i++ {
			f := trafficProjectionFacts{Process: string([]byte{255, byte(i)}), Path: " C:\\p ", Base: "p", Host: "::1", Port: "443", HostPort: "[::1]:443", UID: " -1 ", Rule: "RULE", Payload: "x", Chains: []string{"", "DIRECT"}, Upload: int64(i), Download: -int64(i), Elapsed: int64(i) * 999999, Parsed: i%2 == 0}
			q := trafficProjectionRequest{Op: op, Rows: []trafficProjectionFacts{f}, Period: []string{"hour", "day", "month", "year", "bad"}[i%5], Date: "2026-01", Formatted: "2026-01", Parsed: i%2 == 0, Offset: "+2", Limit: "200", OffsetValue: 2, LimitValue: int64(i * 4), OffsetOK: true, LimitOK: i%3 != 0}
			trafficProjectionStrict(t, q)
		}
	}
}
func projectionRawTest(in, out []byte) (int32, uintptr) {
	var n uintptr
	var p, op uintptr
	if len(in) > 0 {
		p = uintptr(unsafe.Pointer(&in[0]))
	}
	if len(out) > 0 {
		op = uintptr(unsafe.Pointer(&out[0]))
	}
	s, _, _ := syscall.SyscallN(trafficProjectionRust.call, p, uintptr(len(in)), op, uintptr(len(out)), uintptr(unsafe.Pointer(&n)))
	runtime.KeepAlive(in)
	runtime.KeepAlive(out)
	return int32(s), n
}
func TestTrafficProjectionFlowABI(t *testing.T) {
	if !rustTrafficProjectionAvailable() {
		t.Fatal("missing native")
	}
	q := trafficProjectionRequest{Op: trafficProjectionRows, Rows: []trafficProjectionFacts{{Process: "synthetic", Parsed: true, Elapsed: 1}}}
	in, err := trafficProjectionEncode(q)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < len(in); i++ {
		if s, _ := projectionRawTest(in[:i], make([]byte, 1024)); s != -1 {
			t.Fatalf("truncated %d accepted %d", i, s)
		}
	}
	if s, _ := projectionRawTest(append(append([]byte(nil), in...), 0), make([]byte, 1024)); s != -1 {
		t.Fatal("trailing accepted")
	}
	out := []byte{0x55}
	s, n := projectionRawTest(in, out)
	if s != 1 || n <= 1 || out[0] != 0x55 {
		t.Fatal("capacity handshake modified output", s, n)
	}
	out = make([]byte, n)
	s, w := projectionRawTest(in, out)
	r, e := trafficProjectionDecode(out[:w])
	if s != 0 || e != nil || !reflect.DeepEqual(r, runGoTrafficProjection(q)) {
		t.Fatal("capacity retry mismatch")
	}
	bad := append([]byte(nil), in...)
	bad[0] = 255
	if s, _ := projectionRawTest(bad, make([]byte, 1024)); s != -1 {
		t.Fatal("unknown op accepted")
	}
	for _, count := range []uint32{100001, 0xffffffff} {
		evil := append([]byte(nil), in...)
		binary.LittleEndian.PutUint32(evil[1:5], count)
		if status, _ := projectionRawTest(evil, make([]byte, 1024)); status != -1 {
			t.Fatalf("malicious row count accepted %d", count)
		}
	}
	// Length/cap validation happens before dereference; pointers here only name
	// one real byte, never a fictitious allocation of the advertised extent.
	one := byte(0)
	var needed uintptr
	status, _, _ := syscall.SyscallN(trafficProjectionRust.call, uintptr(unsafe.Pointer(&one)), trafficProjectionMaxBytes+1, uintptr(unsafe.Pointer(&one)), 1, uintptr(unsafe.Pointer(&needed)))
	if int32(status) != -1 {
		t.Fatal("oversized input length accepted")
	}
	status, _, _ = syscall.SyscallN(trafficProjectionRust.call, uintptr(unsafe.Pointer(&one)), 1, uintptr(unsafe.Pointer(&one)), trafficProjectionMaxBytes+1, uintptr(unsafe.Pointer(&needed)))
	if int32(status) != -1 {
		t.Fatal("oversized output capacity accepted")
	}
	var unaligned [32]byte
	addr := uintptr(unsafe.Pointer(&unaligned[0]))
	if addr%unsafe.Alignof(needed) == 0 {
		addr++
	}
	status, _, _ = syscall.SyscallN(trafficProjectionRust.call, uintptr(unsafe.Pointer(&one)), 1, uintptr(unsafe.Pointer(&one)), 1, addr)
	if int32(status) != -1 {
		t.Fatal("unaligned written pointer accepted")
	}
	runtime.KeepAlive(unaligned)
	runtime.KeepAlive(one)

}
func TestTrafficProjectionFlowLargeBytes(t *testing.T) {
	text := strings.Repeat("x", (8<<20)+7) + string([]byte{255, 128})
	q := trafficProjectionRequest{Op: trafficProjectionProcess, Rows: []trafficProjectionFacts{{Process: text}}}
	r := trafficProjectionStrict(t, q)
	if r.Text != text {
		t.Fatal("rawbytes changed")
	}
	q.Rows = make([]trafficProjectionFacts, 100001)
	if _, ok := runRustTrafficProjection(q); ok {
		t.Fatal("input bound ignored")
	}
}
