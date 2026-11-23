//go:build windows

package main

import (
	"encoding/binary"
	"errors"
	"testing"
)

// Synthetic bytes only: none of these buffers is loaded or executed.
func wintunIndependentPE(machine uint16) []byte {
	b := make([]byte, 128)
	copy(b, "MZ")
	binary.LittleEndian.PutUint32(b[0x3c:], 64)
	copy(b[64:], "PE\x00\x00")
	binary.LittleEndian.PutUint16(b[68:], machine)
	return b
}
func TestWintunServiceIndependentLegacyPE(t *testing.T) {
	for _, c := range []struct {
		name string
		body []byte
		bad  bool
	}{
		{"amd64", wintunIndependentPE(0x8664), false},
		{"x86", wintunIndependentPE(0x14c), true},
		{"arm64", wintunIndependentPE(0xaa64), true},
		{"nil", nil, true},
		{"short", make([]byte, 63), true},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := verifyPEArch(c.body)
			if c.bad != errors.Is(e, errBadWintunArch) {
				t.Fatalf("error identity %v", e)
			}
		})
	}
	for _, offset := range []uint32{0, 123, 0xffffffff} {
		b := wintunIndependentPE(0x8664)
		binary.LittleEndian.PutUint32(b[0x3c:], offset)
		if e := verifyPEArch(b); !errors.Is(e, errBadWintunArch) {
			t.Fatalf("offset=%d error=%v", offset, e)
		}
	}
	// Exactly offset+6 bytes suffice; no optional-header or image-body validation
	// is part of this original gate.
	b := wintunIndependentPE(0x8664)[:70]
	if e := verifyPEArch(b); e != nil {
		t.Fatalf("minimal COFF header rejected: %v", e)
	}
	b[64] = 'X'
	if e := verifyPEArch(b); !errors.Is(e, errBadWintunArch) {
		t.Fatalf("bad signature: %v", e)
	}
}
func TestWintunServiceIndependentLegacyPSQuote(t *testing.T) {
	if got := psQuote("x'; %s\n$HOME"); got != "'x''; %s\n$HOME'" {
		t.Fatalf("literal altered: %q", got)
	}
}
