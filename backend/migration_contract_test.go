package main

// Offline contracts: no listeners, service startup, real profiles or handler side effects.
import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestMigrationRouteContract(t *testing.T) {
	a := &app{}
	mux := a.routes("synthetic-contract-token")
	routes := []string{
		"GET /api/identify",
		"GET /api/state",
		"GET /api/subscription/policy",
		"PUT /api/settings",
		"POST /api/connect",
		"POST /api/disconnect",
		"POST /api/proxy/reapply",
		"GET /api/nodes",
		"GET /api/nodes/latency",
		"POST /api/nodes/fetch",
		"GET /api/diagnostics/subscription",
		"POST /api/nodes/refresh",
		"PUT /api/nodes/select",
		"POST /api/shutdown",
		"POST /api/direct/refresh",
		"GET /api/latency",
		"POST /api/diagnose",
		"POST /api/speedtest",
		"GET /api/path-checks",
		"PUT /api/path-checks",
		"POST /api/path-checks/run",
		"POST /api/path-checks/recovery/cancel",
		"GET /api/traffic",
		"GET /api/traffic/history",
		"GET /api/nodes/manual",
		"PUT /api/nodes/chain/profile",
		"GET /api/nodes/probes",
		"POST /api/nodes/chain/probe",
		"PUT /api/nodes/chain",
		"POST /api/nodes/manual",
		"DELETE /api/nodes/manual",
		"GET /api/subscriptions",
		"POST /api/subscriptions/activate",
		"POST /api/subscriptions/add",
		"POST /api/subscriptions/update",
		"POST /api/subscriptions/merge",
		"POST /api/subscriptions/refresh",
		"POST /api/subscriptions/quota",
		"POST /api/subscriptions/auto-merge",
		"PUT /api/subscriptions",
		"DELETE /api/subscriptions",
		"GET /api/regions",
		"POST /api/regions/verify",
		"POST /api/region/lock",
		"GET /api/events",
		"PUT /api/health/params",
		"POST /api/health/patrol",
		"GET /api/tun",
		"POST /api/tun/check",
	}
	if runtime.GOOS == "windows" {
		routes = append(routes, "POST /api/tun/wintun")
	}
	for _, want := range routes {
		method, path, _ := strings.Cut(want, " ")
		// Handler resolves routing only. Never invoke any registered business handler.
		_, got := mux.Handler(httptest.NewRequest(method, path, nil))
		if got != want {
			t.Errorf("route %q resolved to %q", want, got)
		}
		if method == "GET" {
			_, got = mux.Handler(httptest.NewRequest("HEAD", path, nil))
			if got != want {
				t.Errorf("HEAD alias %q resolved to %q", want, got)
			}
		}
	}
	_, pattern := mux.Handler(httptest.NewRequest("GET", "/api/unknown-contract-route", nil))
	if pattern != "" {
		t.Fatalf("unexpected catch-all %q", pattern)
	}
}

func TestMigrationAuthorizationContract(t *testing.T) {
	a := &app{}
	for _, header := range []string{"", "contract-token", "bearer contract-token", "Bearer wrong", "Bearer contract-token ", "Bearer contract-token"} {
		called := false
		handler := a.authorize("contract-token", func(w http.ResponseWriter, r *http.Request) {
			called = true
			writeJSON(w, map[string]bool{"ok": true})
		})
		req := httptest.NewRequest("GET", "/synthetic", nil)
		req.Header.Set("Authorization", header)
		out := httptest.NewRecorder()
		handler(out, req)
		valid := header == "Bearer contract-token"
		if called != valid {
			t.Fatalf("authorization behavior changed for %q", header)
		}
		if valid {
			if out.Code != 200 || out.Header().Get("Content-Type") != "application/json; charset=utf-8" || out.Body.String() != "{\"ok\":true}\n" {
				t.Fatalf("success contract: %#v", out)
			}
		} else if out.Code != 401 || out.Body.String() != "unauthorized\n" || out.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
			t.Fatalf("rejection contract: %#v", out)
		}
	}
}

func TestMigrationJSONContract(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}
	for _, tc := range []struct {
		input, want string
		ok          bool
	}{
		{`{"name":"sample"}`, "sample", true},
		{`{"unknown":1}`, "", false},
		{`{"name":3}`, "", false},
		{`{"name":"first","name":"last"}`, "last", true},
		{`{"name":"first"} {"name":"second"}`, "first", true},
		{`null`, "", true},
		{`{"name":"` + strings.Repeat("x", 65536) + `"}`, "", false},
	} {
		var got payload
		err := decodeJSON(strings.NewReader(tc.input), &got)
		if (err == nil) != tc.ok || (tc.ok && got.Name != tc.want) {
			t.Fatalf("JSON contract mismatch: len=%d err=%v", len(tc.input), err)
		}
	}
}

func TestMigrationDTOContract(t *testing.T) {
	cases := []struct {
		value any
		want  string
	}{
		{bootstrap{}, `{"port":0,"token":""}`},
		{bootstrap{Error: "synthetic"}, `{"port":0,"token":"","error":"synthetic"}`},
		{proxyNode{}, `{"name":"","type":"","tls":false,"wsHostConfigured":false,"wsPathConfigured":false,"alive":false,"selected":false}`},
		{cachedNodeList{}, `{"subscriptionHash":"","nodes":null}`},
		{cachedNodeList{Nodes: []proxyNode{}}, `{"subscriptionHash":"","nodes":[]}`},
	}
	for _, tc := range cases {
		body, err := json.Marshal(tc.value)
		if err != nil || string(body) != tc.want {
			t.Fatalf("DTO contract mismatch: %s, %v", body, err)
		}
	}
	out := httptest.NewRecorder()
	(&app{}).identify(out, httptest.NewRequest("GET", "/api/identify", nil))
	var got map[string]any
	if err := json.Unmarshal(out.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if out.Code != 200 || !reflect.DeepEqual(got, map[string]any{"app": "SmartVPN", "connected": false}) {
		t.Fatalf("identity contract: %#v", got)
	}
}
