package main

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

type nativeFakeEnvironment struct {
	values        map[string]string
	calls, failAt int
}

var nativeFakeFailure = errors.New("injected environment failure")

func (e *nativeFakeEnvironment) Lookup(k string) (string, bool) { v, ok := e.values[k]; return v, ok }
func (e *nativeFakeEnvironment) Set(k, v string) error {
	e.calls++
	if e.calls == e.failAt {
		return nativeFakeFailure
	}
	e.values[k] = v
	return nil
}
func (e *nativeFakeEnvironment) Unset(k string) error { delete(e.values, k); return nil }
func nativeCloneEnvironment(m map[string]string) map[string]string {
	r := map[string]string{}
	for k, v := range m {
		r[k] = v
	}
	return r
}

func TestNativeBridgeContractArguments(t *testing.T) {
	for n := 0; n <= nativeMaxArguments; n++ {
		if !nativeInvocationAllowed(1, n) {
			t.Fatalf("arity %d", n)
		}
	}
	for _, n := range []int{-1, 8, 100} {
		if nativeInvocationAllowed(1, n) {
			t.Fatalf("accepted arity %d", n)
		}
	}
	if nativeInvocationAllowed(0, 0) {
		t.Fatal("null proc accepted")
	}
	for _, s := range []string{"", "a\x00b"} {
		if nativeSymbolAllowed(s) {
			t.Fatal("invalid symbol accepted")
		}
	}
	if !nativeSymbolAllowed("smartvpn_service_state_version_v6") {
		t.Fatal("symbol rejected")
	}
}
func TestNativeBridgeContractPath(t *testing.T) {
	p, err := filepath.Abs("synthetic/libsmartvpn_core.so")
	if err != nil {
		t.Fatal(err)
	}
	if !nativeLibraryPathAllowed(p) {
		t.Fatal("absolute path rejected")
	}
	for _, v := range []string{"", "relative.so", p + "\x00", p + "\xff"} {
		if nativeLibraryPathAllowed(v) {
			t.Fatal("invalid path accepted")
		}
	}
	m := map[string]string{}
	get := func(k string) string { return m[k] }
	if nativeCoreSelectedPath(get) != "" {
		t.Fatal("default not empty")
	}
	m["SMARTVPN_RUST_CORE_DLL"] = "legacy"
	if nativeCoreSelectedPath(get) != "legacy" {
		t.Fatal("legacy missing")
	}
	m["SMARTVPN_RUST_CORE_LIB"] = "canonical"
	if nativeCoreSelectedPath(get) != "canonical" {
		t.Fatal("canonical precedence")
	}
}
func TestNativeBridgeContractConfiguration(t *testing.T) {
	p, _ := filepath.Abs("synthetic/libsmartvpn_core.so")
	for _, enabled := range []bool{false, true} {
		env := &nativeFakeEnvironment{values: map[string]string{"SMARTVPN_RUST_STORE": "untouched", "SMARTVPN_RUST_CORE_DLL": "old"}}
		state := nativeCoreConfiguration{}
		if err := nativeApplyConfiguration(&state, p, enabled, false, env); err != nil {
			t.Fatal(err)
		}
		want := "0"
		wantPath := ""
		if enabled {
			want = "1"
			wantPath = p
		}
		seen := map[string]bool{}
		for _, key := range nativeCoreSwitches {
			if seen[key] {
				t.Fatalf("duplicate switch %s", key)
			}
			seen[key] = true
			if env.values[key] != want {
				t.Fatalf("switch %s", key)
			}
		}
		if len(seen) != 39 || seen["SMARTVPN_RUST_STORE"] {
			t.Fatalf("switch inventory %d", len(seen))
		}
		if env.values["SMARTVPN_RUST_CORE_LIB"] != wantPath || env.values["SMARTVPN_RUST_CORE_DLL"] != "" || env.values["SMARTVPN_RUST_STORE"] != "untouched" || !state.Configured {
			t.Fatal("configuration result")
		}
		calls := env.calls
		if !errors.Is(nativeApplyConfiguration(&state, p, enabled, false, env), errNativeConfigurationFrozen) || env.calls != calls {
			t.Fatal("one-shot violated")
		}
	}
}
func TestNativeBridgeContractRollbackRetry(t *testing.T) {
	p, _ := filepath.Abs("synthetic/libsmartvpn_core.so")
	before := map[string]string{"SMARTVPN_RUST_CORE_DLL": "legacy", "SMARTVPN_RUST_CORE_LIB": "", nativeCoreSwitches[0]: "old", "SMARTVPN_RUST_STORE": "untouched"}
	for fail := 1; fail <= len(nativeCoreSwitches)+2; fail++ {
		env := &nativeFakeEnvironment{values: nativeCloneEnvironment(before), failAt: fail}
		state := nativeCoreConfiguration{}
		if !errors.Is(nativeApplyConfiguration(&state, p, true, false, env), nativeFakeFailure) {
			t.Fatalf("failure point %d", fail)
		}
		if state.Configured || !reflect.DeepEqual(before, env.values) {
			t.Fatalf("rollback mismatch %d", fail)
		}
		env.failAt = 0
		if err := nativeApplyConfiguration(&state, p, true, false, env); err != nil || !state.Configured {
			t.Fatalf("retry %d: %v", fail, err)
		}
	}
}
func TestNativeBridgeContractFrozenAndInvalid(t *testing.T) {
	p, _ := filepath.Abs("synthetic/libsmartvpn_core.so")
	for _, s := range []nativeCoreConfiguration{{Configured: true}, {LoadStarted: true}} {
		env := &nativeFakeEnvironment{values: map[string]string{}}
		if !errors.Is(nativeApplyConfiguration(&s, p, true, false, env), errNativeConfigurationFrozen) || env.calls != 0 {
			t.Fatal("frozen mutated")
		}
	}
	env := &nativeFakeEnvironment{values: map[string]string{}}
	s := nativeCoreConfiguration{}
	if !errors.Is(nativeApplyConfiguration(&s, p, true, true, env), errNativeConfigurationFrozen) || env.calls != 0 {
		t.Fatal("active mutated")
	}
	for _, v := range []string{"", "relative", p + "\x00", p + "\xff"} {
		if !errors.Is(nativeApplyConfiguration(&s, v, true, false, env), errNativeConfigurationInvalid) || env.calls != 0 {
			t.Fatal("invalid mutated")
		}
	}
	if err := nativeApplyConfiguration(&s, "", false, false, env); err != nil {
		t.Fatal(err)
	}
}
