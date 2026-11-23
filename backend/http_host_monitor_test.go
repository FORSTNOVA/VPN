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
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func startRustMonitor(t *testing.T, port int) string {
	t.Helper()
	host := os.Getenv("SMARTVPN_TEST_OFFLINE_HOST")
	if host == "" {
		t.Skip("standalone Rust host binary was not supplied")
	}
	args := []string{"--http"}
	if port != 0 {
		args = append(args, strconv.Itoa(port))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	cmd := exec.CommandContext(ctx, host, args...)
	cmd.Env = append(os.Environ(), "SMARTVPN_MONITOR_TOKEN=test-only-monitor", "SMARTVPN_MONITOR_CONTROLLER_SECRET=controller-secret", "SMARTVPN_MONITOR_NODE_TIMEOUT_MS=3000")
	out, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = cmd.Wait() })
	addr, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	addr = strings.TrimSpace(addr)
	ip, _, err := net.SplitHostPort(addr)
	if err != nil || ip != "127.0.0.1" {
		t.Fatalf("unexpected listener %q", addr)
	}
	return "http://" + addr
}

func TestRustHTTPMonitor(t *testing.T) {
	var bad atomic.Bool
	var busy atomic.Bool
	var upload atomic.Int64
	upload.Store(100)
	stub := newControllerStub(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/version":
			fmt.Fprint(w, `{"version":"test"}`)
		case "/group/SmartVPN/delay":
			if r.URL.Query().Get("url") != patrolTargetPrimary {
				t.Error("wrong probe target")
			}
			switch r.URL.Query().Get("timeout") {
			case "3000", "8000":
			default:
				t.Error("wrong probe timeout")
			}
			if bad.Load() {
				http.Error(w, "synthetic failure", 502)
				return
			}
			fmt.Fprint(w, `{"测试 A":17,"B":22}`)
		case "/proxies/SmartVPN":
			fmt.Fprint(w, `{"now":"SmartVPNAuto"}`)
		case "/proxies/SmartVPNAuto":
			fmt.Fprint(w, `{"now":"测试 A"}`)
		case "/connections":
			if bad.Load() {
				fmt.Fprint(w, "bad JSON")
				return
			}
			if busy.Load() {
				rows := make([]map[string]any, 82)
				for i := range rows {
					rows[i] = map[string]any{"upload": i, "rule": nil, "start": "invalid", "chains": []any{nil, " A "}, "metadata": map[string]any{"host": "::1", "destinationPort": 80, "process": nil}}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"connections": rows})
				return
			}
			fmt.Fprintf(w, `{"uploadTotal":%d,"downloadTotal":200,"connections":[{"upload":9,"chains":[" A "],"rule":"Domain","rulePayload":"abc","metadata":{"processPath":"C:\\apps\\sample.exe","destinationIP":"2001:db8::1","destinationPort":443,"network":"tcp","type":"HTTP"}}]}`, upload.Load())
		default:
			t.Errorf("unexpected controller path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	a := stub.app(t)
	runningKernel(t, a)
	addr := startRustMonitor(t, a.ctrlPort)
	client := http.Client{Transport: &http.Transport{}, Timeout: 6 * time.Second}
	defer client.CloseIdleConnections()
	call := func(base, method, path, token string, status int) string {
		t.Helper()
		r, err := http.NewRequest(method, base+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode != status {
			t.Fatalf("%s %s: %d %q %v", method, path, resp.StatusCode, b, err)
		}
		return string(b)
	}
	token := "test-only-monitor"
	call(addr, "GET", "/api/latency", "", 401)
	call(addr, "POST", "/api/traffic", token, 405)
	call(addr, "POST", "/api/connect", token, 501)
	call(addr, "GET", "/unknown", token, 404)
	if got := call(addr, "GET", "/api/identify", "", 200); !strings.Contains(got, `"connected":true`) {
		t.Fatal(got)
	}
	for path, handler := range map[string]http.HandlerFunc{"/api/latency": a.latency, "/api/nodes/latency": a.nodeLatency, "/api/traffic": a.traffic} {
		want := httptest.NewRecorder()
		handler(want, httptest.NewRequest("GET", path, nil))
		var expected, actual any
		got := call(addr, "GET", path, token, want.Code)
		if json.Unmarshal([]byte(got), &actual) != nil || json.Unmarshal(want.Body.Bytes(), &expected) != nil || !reflect.DeepEqual(actual, expected) {
			t.Fatalf("%s: Rust %s, Go %s", path, got, want.Body.String())
		}
	}
	if body := call(addr, "HEAD", "/api/latency", token, 200); body != "" {
		t.Fatal(body)
	}
	upload.Store(300)
	var rates struct{ UploadRate int64 }
	if err := json.Unmarshal([]byte(call(addr, "GET", "/api/traffic", token, 200)), &rates); err != nil || rates.UploadRate <= 0 {
		t.Fatalf("missing rate: %+v %v", rates, err)
	}
	bad.Store(true)
	call(addr, "GET", "/api/latency", token, 502)
	if got := call(addr, "GET", "/api/traffic", token, 200); !strings.Contains(got, `"available":false`) {
		t.Fatal(got)
	}
	bad.Store(false)
	if got := call(addr, "GET", "/api/traffic", token, 200); !strings.Contains(got, `"uploadRate":0`) {
		t.Fatal(got)
	}
	busy.Store(true)
	var projected struct {
		ConnectionCount, ShownCount int
		Connections                 []connectionRow
	}
	if err := json.Unmarshal([]byte(call(addr, "GET", "/api/traffic", token, 200)), &projected); err != nil {
		t.Fatal(err)
	}
	if projected.ConnectionCount != 82 || projected.ShownCount != 80 || len(projected.Connections) != 80 {
		t.Fatalf("invalid row counts: %+v", projected)
	}
	for i, row := range projected.Connections {
		if row.Upload != int64(81-i) || row.Target != "[::1]:80" || row.Chain != "A" {
			t.Fatalf("row %d: %+v", i, row)
		}
	}
	idle := startRustMonitor(t, 0)
	call(idle, "GET", "/api/latency", token, 409)
	if got := call(idle, "GET", "/api/identify", "", 200); !strings.Contains(got, `"connected":false`) {
		t.Fatal(got)
	}
}
