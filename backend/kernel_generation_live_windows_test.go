package main

import (
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// Run the production proxy helper against a unique HKCU test key, without
// broadcasting Internet Settings changes or touching the shared owner record.
func isolatedGenerationProxy(t *testing.T, a *app) (enable, check func()) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(filepath.Dir(os.Args[0]), "proxy.ps1")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("test executable must have no bundled proxy helper")
	}
	script, err := os.ReadFile("proxy.ps1")
	if err != nil {
		t.Fatal(err)
	}
	body, _, ok := strings.Cut(string(script), "if (-not ('SmartVPNProxyNotify'")
	const productionKey = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`
	if !ok || strings.Count(body, productionKey) != 1 {
		t.Fatal("unexpected proxy helper layout")
	}
	path := `Software\SmartVPN-Phase101-` + rand.Text()
	file := filepath.Join(a.home, "proxy.ps1")
	if err = os.WriteFile(file, []byte(strings.Replace(body, productionKey, path, 1)), 0600); err != nil {
		t.Fatal(err)
	}
	key, exists, err := registry.CreateKey(registry.CURRENT_USER, path, registry.ALL_ACCESS)
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		key.Close()
		t.Fatal("test key collision")
	}
	t.Cleanup(func() {
		key.Close()
		if err := registry.DeleteKey(registry.CURRENT_USER, path); err != nil {
			t.Error(err)
		}
	})
	a.proxyOwnerHook = func() (proxyOwner, bool) { return proxyOwner{Home: "foreign-fixture"}, true }
	backup := filepath.Join(a.home, "proxy-backup.json")
	enable = func() {
		t.Helper()
		if err := key.SetDWordValue("ProxyEnable", 0); err != nil {
			t.Fatal(err)
		}
		if err := key.SetStringValue("ProxyServer", "original"); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", file, "-Action", "enable", "-BackupPath", backup, "-ProxyAddress", fmt.Sprintf("127.0.0.1:%d", a.mixedPort))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("isolated proxy enable: %v %s", err, out)
		}
		if v, _, err := key.GetIntegerValue("ProxyEnable"); err != nil || v != 1 {
			t.Fatal("proxy fixture did not enable", err)
		}
		if err := key.SetStringValue("ProxyOverride", "external-edit"); err != nil {
			t.Fatal(err)
		}
		a.proxyOn = true
	}
	check = func() {
		t.Helper()
		if v, _, err := key.GetIntegerValue("ProxyEnable"); err != nil || v != 0 {
			t.Error("proxy enable flag not restored", err)
		}
		for name, want := range map[string]string{"ProxyServer": "original", "ProxyOverride": "external-edit"} {
			if v, _, err := key.GetStringValue(name); err != nil || v != want {
				t.Errorf("%s = %q, %v", name, v, err)
			}
		}
		if _, err := os.Stat(backup); !errors.Is(err, os.ErrNotExist) {
			t.Error("proxy backup not consumed", err)
		}
	}
	return
}

func TestLiveWindowsKernelGeneration(t *testing.T) {
	mode, bin := os.Getenv("SMARTVPN_TEST_KERNEL_MODE"), os.Getenv("SMARTVPN_TEST_MIHOMO")
	if mode == "" || bin == "" {
		t.Skip("requires local Mihomo and explicit kernel test mode")
	}
	a := storeApp(t)
	a.newToken()
	testEditSettings(a, func(s *settings) { s.ConnectionMode = modeSystemProxy; s.MihomoPath = bin })
	a.manualNodes = []manualNode{{Name: "fixture", Proxy: map[string]any{"name": "fixture", "type": "http", "server": "127.0.0.1", "port": 9}}}
	enable, check := isolatedGenerationProxy(t, a)
	t.Cleanup(func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		if err := a.stopMihomoLocked(); err != nil {
			t.Error(err)
		}
	})
	start := func() *execKernel {
		t.Helper()
		a.mu.Lock()
		defer a.mu.Unlock()
		if err := a.startMihomoLocked(); err != nil {
			t.Fatal(err)
		}
		k := a.kernel.(*execKernel)
		if (k.managed != nil) != (mode != "go") || (k.managed != nil && k.managed.reloadKey != "") != (mode == "current") {
			t.Fatal("unexpected negotiated supervisor mode", mode)
		}
		enable()
		return k
	}
	old := start()
	func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		if err := a.applyLiveConfig("[invalid"); err == nil {
			t.Fatal("invalid config accepted")
		}
		if err := a.switchGroupLocked("fixture"); err != nil {
			t.Fatal("old config unavailable", err)
		}
		if m := old.managed; m != nil && m.reloadKey != "" {
			req, err := http.NewRequest(http.MethodPut, "http://"+m.target.addr+"/managed/config", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+m.target.token)
			res, err := localMonitorClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			res.Body.Close()
			if res.StatusCode != 401 {
				t.Fatal("read credential on managed config", res.StatusCode)
			}
			m.cancel()
			if err := a.applyLiveConfig("mode: rule"); err == nil {
				t.Fatal("expired generation performed reload")
			}
		}
		pid := old.cmd.Process.Pid
		if old.managed != nil {
			pid = old.managed.pid
		}
		handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE, false, uint32(pid))
		if err != nil {
			t.Fatal(err)
		}
		defer windows.CloseHandle(handle)
		buf := make([]uint16, 32768)
		size := uint32(len(buf))
		if err := windows.QueryFullProcessImageName(handle, 0, &buf[0], &size); err != nil || !strings.EqualFold(filepath.Clean(windows.UTF16ToString(buf[:size])), filepath.Clean(bin)) {
			t.Fatal("unexpected kernel image", err)
		}
		if err := windows.TerminateProcess(handle, 3); err != nil {
			t.Fatal(err)
		}
	}()
	deadline := time.Now().Add(15 * time.Second)
	for {
		a.mu.Lock()
		done := a.kernel == nil && !a.proxyOn && a.pathCancel == nil
		a.mu.Unlock()
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("kernel exit cleanup timed out")
		}
		time.Sleep(30 * time.Millisecond)
	}
	if _, err := os.Stat(old.config); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("generation config survived", err)
	}
	check()
	next := start()
	a.kernelExited(old)
	func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.kernel != next || !a.proxyOn || !next.Running() {
			t.Fatal("old callback changed replacement")
		}
	}()
	helper := filepath.Join(a.home, "proxy.ps1")
	script, err := os.ReadFile(helper)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(helper, []byte("throw 'injected restore failure'"), 0600); err != nil {
		t.Fatal(err)
	}
	if r := a.runConnectionService(connectionServiceRequest{Op: "disconnect"}); r.Status != 500 {
		t.Fatal("missing restore failure", r.Status)
	}
	if err = os.WriteFile(helper, script, 0600); err != nil {
		t.Fatal(err)
	}
	if r := a.runConnectionService(connectionServiceRequest{Op: "disconnect"}); r.Status != 200 {
		t.Fatal("disconnect", r.Status, r.Err)
	}
	check()
}
