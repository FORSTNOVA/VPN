package main

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func driverIndependentGolden(t *testing.T, direct driverIndependentRun) {
	run := func(t *testing.T, q driverInstallRequest, f driverInstallEffects) driverInstallResult {
		defer f.Release()
		return direct(t, q, f)
	}
	t.Run("local-success-two-status-reads", func(t *testing.T) {
		s := driverIndependentBase()
		s.Source = " synthetic/source.dll "
		f := &driverIndependentFX{s: s}
		r := run(t, driverInstallRequest{Op: "install"}, f)
		if r.Err != nil || r.Status != 200 || r.Data["available"] != true {
			t.Fatal(r)
		}
		want := []string{"decode", "read-local", "pe-header", "pe-at", "lock", "target", "write-temp", "signature", "rename", "log", "target", "stat", "hash-file", "elevated", "target", "stat", "hash-file"}
		if !reflect.DeepEqual(driverIndependentKinds(f), want) || f.trace[1].Text != "synthetic/source.dll" || f.trace[6].Number != 0600 {
			t.Fatal(f.trace)
		}
	})
	t.Run("write-failure-does-not-remove", func(t *testing.T) {
		s := driverIndependentBase()
		s.Source = "x"
		s.Fail = "write-temp"
		f := &driverIndependentFX{s: s}
		r := run(t, driverInstallRequest{Op: "install"}, f)
		if r.Status != 500 || r.Err == nil || len(f.trace) != 7 || f.trace[6].Kind != "write-temp" {
			t.Fatal(r, f.trace)
		}
	})
	t.Run("signature-before-rename-and-cleanup", func(t *testing.T) {
		s := driverIndependentBase()
		s.Source = "x"
		s.Signature = "invalid:NotSigned"
		f := &driverIndependentFX{s: s}
		r := run(t, driverInstallRequest{Op: "install"}, f)
		if r.Status != 502 || !errors.Is(r.Err, driverBadWintun) || f.trace[len(f.trace)-1].Kind != "remove-temp" {
			t.Fatal(r, f.trace)
		}
		for _, e := range f.trace {
			if e.Kind == "rename" {
				t.Fatal("rename before successful signature")
			}
		}
	})
	t.Run("rename-error-removes-temp", func(t *testing.T) {
		s := driverIndependentBase()
		s.Source = "x"
		s.Fail = "rename"
		f := &driverIndependentFX{s: s}
		r := run(t, driverInstallRequest{Op: "install"}, f)
		if r.Status != 500 || r.Err == nil || f.trace[len(f.trace)-1].Kind != "remove-temp" {
			t.Fatal(r, f.trace)
		}
	})
	t.Run("official-closes-entry-before-response-before-lock", func(t *testing.T) {
		f := &driverIndependentFX{s: driverIndependentBase()}
		r := run(t, driverInstallRequest{Op: "install"}, f)
		if r.Err != nil {
			t.Fatal(r.Err)
		}
		k := driverIndependentKinds(f)
		want := []string{"decode", "http-open", "http-read", "hash-zip", "zip-open", "zip-entry-open", "zip-entry-read", "hash-dll", "pe-header", "pe-at", "zip-entry-close", "http-close", "lock"}
		if len(k) < len(want) || !reflect.DeepEqual(k[:len(want)], want) {
			t.Fatal(k)
		}
		if f.trace[2].Number != 8*1024*1024 || f.trace[6].Number != 2*1024*1024 {
			t.Fatal("read limits", f.trace)
		}
	})
	t.Run("first-matching-entry-failure-terminal", func(t *testing.T) {
		s := driverIndependentBase()
		s.Entries = []string{driverWintunEntry, driverWintunEntry}
		s.Fail = "zip-entry-open"
		f := &driverIndependentFX{s: s}
		r := run(t, driverInstallRequest{Op: "download"}, f)
		if !errors.Is(r.Err, driverBadWintun) || !reflect.DeepEqual(driverIndependentKinds(f), []string{"http-open", "http-read", "hash-zip", "zip-open", "zip-entry-open", "http-close"}) {
			t.Fatal(r, f.trace)
		}
	})
	t.Run("hash-precedes-archive", func(t *testing.T) {
		s := driverIndependentBase()
		s.ZipHash = "bad"
		s.Fail = "zip-open"
		f := &driverIndependentFX{s: s}
		r := run(t, driverInstallRequest{Op: "download"}, f)
		if !errors.Is(r.Err, driverBadWintun) || !reflect.DeepEqual(driverIndependentKinds(f), []string{"http-open", "http-read", "hash-zip", "http-close"}) {
			t.Fatal(r, f.trace)
		}
	})
	t.Run("signature-contains-not-exact-and-error-identity", func(t *testing.T) {
		for _, v := range []struct {
			sig, fail string
			ok, bad   bool
		}{{" valid:prefix WireGuard LLC suffix ", "", true, false}, {"VALID:WireGuard LLC", "", false, true}, {"valid:Other%s%d%q", "", false, true}, {" %s output %d ", "signature", false, false}} {
			s := driverIndependentBase()
			s.Signature = v.sig
			s.Fail = v.fail
			f := &driverIndependentFX{s: s}
			r := run(t, driverInstallRequest{Op: "signature", Path: "x"}, f)
			if (r.Err == nil) != v.ok || errors.Is(r.Err, driverBadWintun) != v.bad {
				t.Fatal(v, r)
			}
			if v.fail != "" && !strings.Contains(r.Err.Error(), "%s output %d") {
				t.Fatal("output tokens changed", r.Err)
			}
		}
	})
}
func TestWintunServiceIndependentSelectedGolden(t *testing.T) {
	driverIndependentGolden(t, func(_ *testing.T, q driverInstallRequest, fx driverInstallEffects) driverInstallResult {
		return runSelectedDriverInstall(q, fx)
	})
}
