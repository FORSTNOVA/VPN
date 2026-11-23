package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
)

// Exercise the same read listener across a real controller start and stop.
func TestLiveReadAPIParity(t *testing.T) {
	bin := os.Getenv("SMARTVPN_TEST_MIHOMO")
	mode := os.Getenv("SMARTVPN_TEST_READ_API")
	if bin == "" || mode == "" {
		t.Skip("requires local Mihomo and an expected host mode")
	}
	a := storeApp(t)
	testEditSettings(a, func(s *settings) { s.ConnectionMode = modeSystemProxy; s.MihomoPath = bin })
	a.replaceManualLocked([]manualNode{{Name: "read-fixture", Type: "http", Server: "127.0.0.1", Port: 9, Proxy: map[string]any{"name": "read-fixture", "type": "http", "server": "127.0.0.1", "port": 9}}})
	const token = "parity-service-token"
	local := a.routes(token)
	handler, closeAPI, enabled := a.openHostAPI(token, local)
	defer closeAPI()
	if enabled != (mode == "current") {
		t.Fatalf("read host active=%v, expected %s", enabled, mode)
	}
	legacy := httptest.NewServer(local)
	defer legacy.Close()
	current := httptest.NewServer(handler)
	defer current.Close()
	type reply struct {
		status int
		mime   string
		body   []byte
	}
	fetch := func(t *testing.T, addr, method, path, auth string) reply {
		t.Helper()
		req, err := http.NewRequest(method, addr+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", auth)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		return reply{res.StatusCode, res.Header.Get("Content-Type"), b}
	}
	compare := func(t *testing.T, method, path, key string) reply {
		t.Helper()
		want, got := fetch(t, legacy.URL, method, path, key), fetch(t, current.URL, method, path, key)
		if want.status != got.status || want.mime != got.mime {
			t.Fatalf("%s %s status/MIME Go=%d %q host=%d %q", method, path, want.status, want.mime, got.status, got.mime)
		}
		if !bytes.Equal(want.body, got.body) {
			var x, y any
			if json.Unmarshal(want.body, &x) != nil || json.Unmarshal(got.body, &y) != nil || !reflect.DeepEqual(x, y) {
				t.Fatalf("%s %s response differs:\n%s\n%s", method, path, want.body, got.body)
			}
		}
		return got
	}
	for _, state := range []string{"idle", "connected"} {
		if state == "connected" {
			// Keep the reference handlers on their direct controller path.
			t.Setenv("SMARTVPN_MANAGED_HOST", "off")
			a.mu.Lock()
			err := a.startMihomoLocked()
			if err == nil {
				t.Cleanup(func() {
					a.mu.Lock()
					defer a.mu.Unlock()
					if err := a.stopMihomoLocked(); err != nil {
						t.Error(err)
					}
				})
				err = a.switchGroupLocked("read-fixture")
			}
			a.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
		}
		for _, r := range a.apiRoutes() {
			if r.method != "GET" {
				continue
			}
			t.Run(state+r.path, func(t *testing.T) {
				for _, method := range []string{"GET", "HEAD"} {
					for _, key := range []string{"", "Bearer wrong", "Bearer " + token} {
						got := compare(t, method, r.path, key)
						if key != "Bearer "+token && r.id != 1 && got.status != 401 {
							t.Fatal("missing auth denial")
						}
						if method == "HEAD" && len(got.body) != 0 {
							t.Fatal("HEAD body")
						}
						if method == "GET" && r.id == 1 {
							var identity struct{ Connected bool }
							if err := json.Unmarshal(got.body, &identity); err != nil || identity.Connected != (state == "connected") {
								t.Fatalf("identity %s", got.body)
							}
						}
					}
				}
			})
		}
		compare(t, "GET", "/api/traffic/history?period=invalid&app=x%26y", "Bearer "+token)
		compare(t, "GET", "/api/does-not-exist", "Bearer "+token)
		compare(t, "GET", "/api/connect", "Bearer "+token)
	}
}
