package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"reflect"
	"testing"
)

func driverIndependentPE(machine uint16) []byte {
	b := make([]byte, 128)
	copy(b, "MZ")
	binary.LittleEndian.PutUint32(b[0x3c:], 64)
	copy(b[64:], "PE\x00\x00")
	binary.LittleEndian.PutUint16(b[68:], machine)
	return b
}

type driverIndependentSpec struct {
	Fail, Source, Signature, ZipHash, DLLHash string
	HTTP                                      int
	Body                                      []byte
	Entries                                   []string
	Elevated, Dir, EOF                        bool
}
type driverIndependentFX struct {
	s        driverIndependentSpec
	trace    []driverInstallEffect
	owned    bool
	releases int
}

func (f *driverIndependentFX) Call(e driverInstallEffect) driverInstallReply {
	f.trace = append(f.trace, e)
	if e.Kind == f.s.Fail {
		return driverInstallReply{Err: errors.New("synthetic-" + e.Kind), Text: f.s.Signature}
	}
	switch e.Kind {
	case "decode":
		if f.s.EOF {
			return driverInstallReply{Flag: true, Err: io.EOF}
		}
		return driverInstallReply{Text: f.s.Source}
	case "read-local":
	case "http-open":
		return driverInstallReply{Number: int64(f.s.HTTP)}
	case "http-read", "http-close", "zip-entry-open", "zip-entry-read", "zip-entry-close":
	case "hash-zip":
		return driverInstallReply{Text: f.s.ZipHash}
	case "hash-dll":
		return driverInstallReply{Text: f.s.DLLHash}
	case "zip-open":
		return driverInstallReply{Names: f.s.Entries}
	case "pe-header":
		b := f.s.Body
		if len(b) > 64 {
			b = b[:64]
		}
		return driverInstallReply{Bytes: b, Number: int64(len(f.s.Body))}
	case "pe-at":
		if e.Number < 0 || e.Number+6 > int64(len(f.s.Body)) {
			return driverInstallReply{}
		}
		return driverInstallReply{Bytes: f.s.Body[e.Number : e.Number+6]}
	case "lock":
		if f.owned {
			panic("double lock")
		}
		f.owned = true
	case "target":
		return driverInstallReply{Text: "synthetic/wintun.dll"}
	case "signature":
		return driverInstallReply{Text: f.s.Signature}
	case "write-temp", "remove-temp", "rename", "log":
	case "stat":
		return driverInstallReply{Flag: f.s.Dir}
	case "hash-file":
		return driverInstallReply{Text: "synthetic-hash"}
	case "elevated":
		return driverInstallReply{Flag: f.s.Elevated}
	default:
		panic("unknown fake driver port: " + e.Kind)
	}
	return driverInstallReply{}
}
func (f *driverIndependentFX) Release() { f.releases++; f.owned = false }

type driverIndependentRun func(*testing.T, driverInstallRequest, driverInstallEffects) driverInstallResult

func driverIndependentBase() driverIndependentSpec {
	return driverIndependentSpec{HTTP: 200, Body: driverIndependentPE(0x8664), Signature: "valid:CN=WireGuard LLC", ZipHash: driverWintunZipSHA256, DLLHash: driverWintunDLLSHA256, Entries: []string{"other", driverWintunEntry}, Elevated: true}
}
func driverIndependentKinds(f *driverIndependentFX) []string {
	a := make([]string, len(f.trace))
	for i, e := range f.trace {
		a[i] = e.Kind
	}
	return a
}
func driverIndependentError(e error) (string, bool, bool) {
	if e == nil {
		return "", false, false
	}
	return e.Error(), errors.Is(e, driverBadWintun), errors.Is(e, driverBadArch)
}
func driverIndependentMatrix(t *testing.T, run driverIndependentRun) {
	ops := []string{"install", "download", "pe", "signature"}
	fails := []string{"", "decode", "read-local", "http-open", "http-read", "zip-open", "zip-entry-open", "zip-entry-read", "write-temp", "signature", "rename", "stat", "hash-file"}
	for i := 0; i < 1200; i++ {
		i := i
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			s := driverIndependentBase()
			s.Fail = fails[(i/4)%len(fails)]
			s.Source = []string{"", " synthetic/source.dll "}[(i/7)%2]
			s.HTTP = []int{200, 503}[(i/13)%2]
			s.Signature = []string{"valid:CN=WireGuard LLC", "valid:Other", "invalid:NotSigned", "VALID:WireGuard LLC", "  valid:%s WireGuard LLC %d  "}[(i/11)%5]
			s.Elevated = i&16 != 0
			s.Dir = i%17 == 0
			s.EOF = i%19 == 0
			if i%23 == 0 {
				s.ZipHash = "bad"
			}
			if i%29 == 0 {
				s.DLLHash = "bad"
			}
			if i%31 == 0 {
				s.Body = driverIndependentPE(0x14c)
			}
			if i%37 == 0 {
				s.Body = nil
			}
			if i%41 == 0 {
				s.Entries = []string{driverWintunEntry, driverWintunEntry}
			}
			q := driverInstallRequest{Op: ops[i%4], Path: "signature/path", Body: s.Body}
			a, b := &driverIndependentFX{s: s}, &driverIndependentFX{s: s}
			g, r := runGoDriverInstall(q, a), run(t, q, b)
			a.Release()
			b.Release()
			ge, gb, ga := driverIndependentError(g.Err)
			re, rb, ra := driverIndependentError(r.Err)
			g.Err = nil
			r.Err = nil
			if ge != re || gb != rb || ga != ra || !reflect.DeepEqual(g, r) || !reflect.DeepEqual(a.trace, b.trace) || a.releases != b.releases || a.owned || b.owned {
				t.Fatalf("q=%+v s=%+v\nGo=%+v err=%s/%v/%v\nRust=%+v err=%s/%v/%v\nGo trace=%v\nRust trace=%v", q, s, g, ge, gb, ga, r, re, rb, ra, a.trace, b.trace)
			}
		})
	}
}
