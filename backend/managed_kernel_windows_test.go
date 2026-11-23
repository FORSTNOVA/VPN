package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// The test binary doubles as a synthetic Mihomo only when the parent supplies
// the exact temporary config and the supervisor invokes the kernel CLI.
func init() {
	if len(os.Args) != 5 || os.Args[1] != "-d" || os.Args[3] != "-f" || os.Getenv("SMARTVPN_TEST_KERNEL_CONFIG") == "" || os.Args[4] != os.Getenv("SMARTVPN_TEST_KERNEL_CONFIG") {
		return
	}
	if os.Getenv("SMARTVPN_MONITOR_CONTROLLER_SECRET") != "" || os.Getenv("SMARTVPN_MONITOR_TOKEN") != "" {
		os.Exit(7)
	}
	b, err := os.ReadFile(os.Args[4])
	if err != nil {
		os.Exit(2)
	}
	ln, err := net.Listen("tcp4", "127.0.0.1:"+strings.TrimSpace(string(b)))
	if err != nil {
		os.Exit(3)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-controller" {
			http.Error(w, "denied", 401)
			return
		}
		switch r.URL.Path {
		case "/version":
			fmt.Fprintf(w, `{"version":"test","pid":%d}`, os.Getpid())
		case "/connections":
			fmt.Fprint(w, `{"uploadTotal":17,"connections":[]}`)
		case "/group/SmartVPN/delay":
			ms, _ := strconv.Atoi(r.URL.Query().Get("timeout"))
			fmt.Fprintf(w, `{"node":19,"timeout":%d}`, ms)
		case "/proxies/SmartVPN":
			fmt.Fprint(w, `{"now":"node"}`)
		default:
			http.NotFound(w, r)
		}
	})
	_ = http.Serve(ln, mux)
	os.Exit(0)
}

func TestManagedKernelLifecycle(t *testing.T) {
	host := os.Getenv("SMARTVPN_TEST_OFFLINE_HOST")
	if host == "" {
		t.Skip("Rust host binary not supplied")
	}
	t.Setenv("SMARTVPN_MANAGED_HOST", host)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"stop", "supervisor-exit", "kernel-exit"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			config := filepath.Join(home, "fake-config")
			t.Setenv("SMARTVPN_TEST_KERNEL_CONFIG", config)
			port, err := freeLoopbackPort()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(config, []byte(strconv.Itoa(port)), 0600); err != nil {
				t.Fatal(err)
			}
			// This synthetic process serves both the TCP readiness probe and its
			// HTTP controller on one private port.
			a := testHealthApp(&app{home: home, mixedPort: port, ctrlPort: port, ctrlSecret: "synthetic-controller"}, func(p *healthDomainProjection) { p.Params = defaultHealthParams() })
			k := &execKernel{app: a, path: exe, config: config}
			a.mu.Lock()
			err = k.Start()
			if err == nil {
				a.kernel = k
			}
			a.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = k.Stop() })
			// Retain the child process handle so a reused PID can never satisfy this test.
			child, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, uint32(k.managed.pid))
			if err != nil {
				t.Fatal(err)
			}
			defer windows.CloseHandle(child)
			if k.managed.pid == k.cmd.Process.Pid {
				t.Fatal("published supervisor PID as kernel owner")
			}
			if err := a.waitForMihomo(5 * time.Second); err != nil {
				t.Fatal(err)
			}
			if err := k.verifyPortOwner(port); err != nil {
				t.Fatal(err)
			}
			mux := a.routes("synthetic-ui")
			for _, path := range []string{"/api/latency", "/api/nodes/latency", "/api/traffic"} {
				req := httptest.NewRequest("GET", path, nil)
				res := httptest.NewRecorder()
				mux.ServeHTTP(res, req)
				if res.Code != 401 {
					t.Fatalf("%s without client token: %d", path, res.Code)
				}
				req.Header.Set("Authorization", "Bearer synthetic-ui")
				res = httptest.NewRecorder()
				mux.ServeHTTP(res, req)
				var v map[string]any
				if res.Code != 200 || json.NewDecoder(res.Body).Decode(&v) != nil {
					t.Fatalf("%s: %d %s", path, res.Code, res.Body.String())
				}
				if path == "/api/traffic" && v["uploadTotal"] != float64(17) {
					t.Fatalf("unexpected traffic: %v", v)
				}
			}
			a.mu.Lock()
			testEditHealth(a, func(p *healthDomainProjection) { p.Params.ConnectTimeoutMs = 7123 })
			a.mu.Unlock()
			updated := httptest.NewRecorder()
			nodesReq := httptest.NewRequest("GET", "/api/nodes/latency", nil)
			nodesReq.Header.Set("Authorization", "Bearer synthetic-ui")
			nodesReq.Header.Set("X-SmartVPN-Node-Timeout-Ms", "1234")
			mux.ServeHTTP(updated, nodesReq)
			if updated.Code != 200 || !strings.Contains(updated.Body.String(), `"timeout":7123`) {
				t.Fatalf("saved timeout not applied: %d %s", updated.Code, updated.Body.String())
			}
			switch mode {
			case "stop":
				// Prove the client route actually chose the host: on a canceled
				// generation it must not issue a fresh Go latency measurement.
				k.managed.cancel()
				req := httptest.NewRequest("GET", "/api/latency", nil)
				req.Header.Set("Authorization", "Bearer synthetic-ui")
				res := httptest.NewRecorder()
				mux.ServeHTTP(res, req)
				if res.Code != 502 || !strings.Contains(res.Body.String(), "Rust monitor unavailable") {
					t.Fatalf("unexpected host loss response: %d %s", res.Code, res.Body.String())
				}
				err = k.Stop()
			case "supervisor-exit":
				err = k.cmd.Process.Kill()
			case "kernel-exit":
				err = windows.TerminateProcess(child, 9)
			}
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-k.exited:
			case <-time.After(5 * time.Second):
				t.Fatal("supervisor did not exit")
			}
			state, err := windows.WaitForSingleObject(child, 5000)
			if err != nil || state != windows.WAIT_OBJECT_0 {
				t.Fatalf("child remained alive: %d %v", state, err)
			}
			until := time.Now().Add(3 * time.Second)
			for {
				a.mu.Lock()
				cleared := a.kernel == nil
				a.mu.Unlock()
				if cleared {
					break
				}
				if time.Now().After(until) {
					t.Fatal("stale runner after exit")
				}
				time.Sleep(10 * time.Millisecond)
			}
			res := httptest.NewRecorder()
			req := httptest.NewRequest("GET", "/api/latency", nil)
			req.Header.Set("Authorization", "Bearer synthetic-ui")
			mux.ServeHTTP(res, req)
			if res.Code != 409 {
				t.Fatalf("idle response after exit: %d", res.Code)
			}
		})
	}
}

func TestManagedClientHelper(t *testing.T) {
	if os.Getenv("SMARTVPN_TEST_CLIENT_HELPER") != "1" {
		t.Skip("subprocess fixture")
	}
	config := os.Getenv("SMARTVPN_TEST_KERNEL_CONFIG")
	b, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(string(b))
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	a := testHealthApp(&app{home: filepath.Dir(config), ctrlPort: port, ctrlSecret: "synthetic-controller"}, func(p *healthDomainProjection) { p.Params = defaultHealthParams() })
	k := &execKernel{app: a, path: exe, config: config}
	if err = k.Start(); err != nil {
		t.Fatal(err)
	}
	defer k.Stop()
	if k.managed == nil {
		t.Fatal("bundled host was not discovered")
	}
	fmt.Printf("%d %d\n", k.cmd.Process.Pid, k.managed.pid)
	_, _ = io.Copy(io.Discard, os.Stdin)
}

func TestManagedClientParentExit(t *testing.T) {
	host := os.Getenv("SMARTVPN_TEST_OFFLINE_HOST")
	if host == "" {
		t.Skip("Rust host binary not supplied")
	}
	dir := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for dst, src := range map[string]string{"service.test.exe": exe, "smartvpn-host.exe": host} {
		from, err := os.Open(src)
		if err != nil {
			t.Fatal(err)
		}
		to, err := os.Create(filepath.Join(dir, dst))
		if err != nil {
			from.Close()
			t.Fatal(err)
		}
		_, err = io.Copy(to, from)
		from.Close()
		closeErr := to.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("copy fixture: %v %v", err, closeErr)
		}
	}
	port, err := freeLoopbackPort()
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dir, "fake-config")
	if err = os.WriteFile(config, []byte(strconv.Itoa(port)), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(dir, "service.test.exe"), "-test.run=^TestManagedClientHelper$")
	cmd.Env = append(os.Environ(), "SMARTVPN_TEST_CLIENT_HELPER=1", "SMARTVPN_MANAGED_HOST=", "SMARTVPN_TEST_KERNEL_CONFIG="+config)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	var supervisor, kernel uint32
	if _, err = fmt.Fscan(bufio.NewReader(out), &supervisor, &kernel); err != nil {
		t.Fatal(err)
	}
	var handles []windows.Handle
	for _, pid := range []uint32{supervisor, kernel} {
		h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, pid)
		if err != nil {
			t.Fatal(err)
		}
		defer windows.CloseHandle(h)
		handles = append(handles, h)
	}
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	for _, h := range handles {
		status, err := windows.WaitForSingleObject(h, 5000)
		if err != nil || status != windows.WAIT_OBJECT_0 {
			t.Fatalf("orphan after service death: %d %v", status, err)
		}
	}
}
