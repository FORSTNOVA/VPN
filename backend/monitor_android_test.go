//go:build android && (arm64 || amd64) && rustcore

package main

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// Run in a standalone Android test process with the staged host beside the
// configured core path. All controller traffic stays on device loopback.
func TestAndroidHostAttachment(t *testing.T) {
	if os.Getenv("SMARTVPN_RUST_CORE_LIB") == "" {
		t.Skip("requires staged Android host")
	}
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer controller" || r.URL.Path != "/group/SmartVPN/delay" {
			t.Errorf("unexpected controller request %s", r.URL.Path)
		}
		io.WriteString(w, `{"fixture":19}`)
	}))
	defer controller.Close()
	k := &embeddedKernel{app: &app{ctrlPort: controller.Listener.Addr().(*net.TCPAddr).Port, ctrlSecret: "controller"}}
	k.app.home = t.TempDir()
	// Start changes the process working directory to the kernel home.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(cwd)
	service = k.app
	defer func() { service = nil }()
	if err := k.Start(); err == nil || k.monitor != nil {
		t.Fatal("missing config published a session")
	}
	route := k.app.monitored("/api/nodes/latency", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	query := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		route(w, httptest.NewRequest("GET", "/api/nodes/latency", nil))
		return w
	}
	var previous string
	for i := 0; i < 2; i++ {
		if err := os.WriteFile(k.app.configPath(), []byte("mode: direct\ntun: {enable: false}\ndns: {enable: false}\n"), 0600); err != nil {
			t.Fatal(err)
		}
		k.app.kernel = k
		if err := k.Start(); err != nil {
			t.Fatal(err)
		}
		target, ok := k.monitorEndpoint()
		if !ok || target.token == previous {
			t.Fatal("missing fresh session")
		}
		defer k.stopMonitor()
		previous = target.token
		if w := query(); w.Code != 200 || w.Body.String() != "{\"latency\":{\"fixture\":19}}\n" {
			t.Fatalf("client forwarding: %d %s", w.Code, w.Body.String())
		}
		for _, tc := range []struct {
			token  string
			status int
		}{{"wrong", 401}, {target.token, 200}} {
			req, _ := http.NewRequest("GET", "http://"+target.addr+"/api/nodes/latency", nil)
			req.Header.Set("Authorization", "Bearer "+tc.token)
			res, err := localMonitorClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(res.Body)
			res.Body.Close()
			if err != nil || res.StatusCode != tc.status || tc.status == 200 && string(body) != "{\"latency\":{\"fixture\":19}}\n" {
				t.Fatalf("response %d %s: %v", res.StatusCode, body, err)
			}
		}
		conn, err := net.DialTimeout("tcp", target.addr, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if err := k.Stop(); err != nil {
				t.Fatal(err)
			}
		} else {
			// The same Go tunnel-report entry used by CoreBridge.setTunnel(false).
			e := newBootstrapEffects(bootstrapServiceRequest{Op: "tunnel", Authorized: false})
			e.android().decode = func(*tunnelReport) error { return nil }
			if result := runBootstrapWithEffects(e.q, e); result.Err != nil {
				t.Fatal(result.Err)
			}
		}
		if _, ok := k.monitorEndpoint(); ok || target.lifetime.Err() == nil {
			t.Fatal("stopped session still published")
		}
		if w := query(); w.Code != http.StatusNoContent {
			t.Fatalf("local handler after stop: %d", w.Code)
		}
		conn.SetReadDeadline(time.Now().Add(time.Second))
		_, err = bufio.NewReader(conn).ReadByte()
		conn.Close()
		if err == nil {
			t.Fatal("connection survived stop")
		}
		if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
			t.Fatal("connection did not close")
		}
		if c, err := net.DialTimeout("tcp", target.addr, time.Second); err == nil {
			c.Close()
			t.Fatal("listener survived stop")
		}
	}
}
