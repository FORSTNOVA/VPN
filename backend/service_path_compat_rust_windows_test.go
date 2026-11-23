//go:build windows && amd64 && rustcore

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestServiceStatePathOldDLLChild(t *testing.T) {
	if os.Getenv("SMARTVPN_TEST_OLD_STATE_DLL") != "1" {
		return
	}
	if rustServiceStateAvailable() {
		t.Fatal("old DLL accepted full authority")
	}
	b, e := newSelectedServiceStateBackend()
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	if _, ok := b.(*goServiceStateBackend); !ok {
		t.Fatal("partial native authority selected")
	}
}
func TestServiceStatePathOldDLLSelection(t *testing.T) {
	old := filepath.Join("C:\\Users\\21654\\SmartVPN-Rust-Phase32", "core-target", "release", "smartvpn_core.dll")
	if _, e := os.Stat(old); e != nil {
		t.Skip("previous-stage DLL not present")
	}
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	cmd := exec.Command(exe, "-test.run=^TestServiceStatePathOldDLLChild$", "-test.count=1")
	for _, v := range os.Environ() {
		key := strings.ToUpper(strings.SplitN(v, "=", 2)[0])
		if key != "SMARTVPN_RUST_CORE_DLL" && key != "SMARTVPN_RUST_SERVICE_STATE" && key != "SMARTVPN_TEST_OLD_STATE_DLL" {
			cmd.Env = append(cmd.Env, v)
		}
	}
	cmd.Env = append(cmd.Env, "SMARTVPN_RUST_CORE_DLL="+old, "SMARTVPN_RUST_SERVICE_STATE=1", "SMARTVPN_TEST_OLD_STATE_DLL=1")
	if out, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("old DLL selector: %v %s", e, out)
	}
}
