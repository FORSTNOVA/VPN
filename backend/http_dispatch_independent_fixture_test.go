package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// Frozen Phase41 registration order. Endpoints below are synthetic only.
var dispatchIndependentPatterns = []string{
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
	"POST /api/tun/wintun",
}

func dispatchIndependentMux(native bool, token string, calls *int) *http.ServeMux {
	mux := http.NewServeMux()
	for i, pattern := range dispatchIndependentPatterns {
		id := uint32(i + 1)
		endpoint := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			*calls++
			w.Header().Set("X-Synthetic-Route", fmt.Sprint(id))
			w.WriteHeader(202)
			fmt.Fprintf(w, "route=%d method=%s", id, r.Method)
		})
		if native {
			endpoint = httpDispatchWrapper(pattern, token, id, i == 0, endpoint)
		} else if i != 0 {
			next := endpoint
			endpoint = func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer "+token {
					http.Error(w, "unauthorized", 401)
					return
				}
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				next(w, r)
			}
		}
		mux.HandleFunc(pattern, endpoint)
	}
	return mux
}
func dispatchIndependentMatrix(t *testing.T) {
	t.Helper()
	count := 0
	for _, pattern := range dispatchIndependentPatterns {
		_, path, _ := strings.Cut(pattern, " ")
		for _, method := range []string{"GET", "HEAD", "POST", "PUT", "DELETE", "OPTIONS", "PATCH", "TRACE", "get"} {
			for _, auth := range []string{"", "Bearer synthetic", "bearer synthetic", "Bearer  synthetic", "Bearer synthetic "} {
				name := fmt.Sprintf("%d", count)
				count++
				t.Run(name, func(t *testing.T) {
					gc, rc := 0, 0
					g, r := dispatchIndependentMux(false, "synthetic", &gc), dispatchIndependentMux(true, "synthetic", &rc)
					req := httptest.NewRequest(method, path+"?synthetic=1", nil)
					req.Header.Set("Authorization", auth)
					gw, rw := httptest.NewRecorder(), httptest.NewRecorder()
					g.ServeHTTP(gw, req.Clone(req.Context()))
					r.ServeHTTP(rw, req.Clone(req.Context()))
					if gw.Code != rw.Code || gw.Body.String() != rw.Body.String() || !reflect.DeepEqual(gw.Header(), rw.Header()) || gc != rc {
						t.Fatalf("%s %s auth%q old=%d %s %v calls%d new=%d %s %v calls%d", method, path, auth, gw.Code, gw.Body, gw.Header(), gc, rw.Code, rw.Body, rw.Header(), rc)
					}
				})
			}
		}
	}
	for _, path := range []string{"/missing", "/api/state/", "//api/state", "/api/../api/state", "/api/./state", "/api/%73tate", "/api%2fstate", "/api/state//", "/api/subscriptions/../state"} {
		for _, method := range []string{"GET", "HEAD", "POST", "OPTIONS", "CONNECT"} {
			gc, rc := 0, 0
			g, r := dispatchIndependentMux(false, "synthetic", &gc), dispatchIndependentMux(true, "synthetic", &rc)
			req := httptest.NewRequest(method, path, nil)
			gw, rw := httptest.NewRecorder(), httptest.NewRecorder()
			g.ServeHTTP(gw, req)
			r.ServeHTTP(rw, req)
			if gw.Code != rw.Code || gw.Body.String() != rw.Body.String() || !reflect.DeepEqual(gw.Header(), rw.Header()) || gc != rc {
				t.Fatalf("canonical %s %s mismatch", method, path)
			}
		}
	}
}
func TestHTTPDispatchIndependentMatrix(t *testing.T) { dispatchIndependentMatrix(t) }

var dispatchIndependentHandlerNames = []string{"identify", "state", "subscriptionPolicy", "updateSettings", "connect", "disconnect", "reapplyProxy", "nodes", "nodeLatency", "fetchNodes", "subscriptionDiagnostics", "refreshNodes", "selectNode", "shutdownService", "refreshChinaDirect", "latency", "diagnose", "speedTest", "pathChecks", "updatePathChecks", "runPathChecks", "cancelPathRecovery", "traffic", "trafficHistory", "listManualNodes", "saveChainProfile", "listProbeResults", "probeChain", "updateChain", "addManualNode", "deleteManualNode", "listSubscriptions", "activateSubscription", "addSubscription", "updateSubscription", "mergeSubscriptions", "refreshSingleSubscription", "refreshSubscriptionQuota", "setAutoMergeHours", "renameSubscription", "deleteSubscription", "regionReport", "startRegionVerification", "lockRegion", "switchEvents", "updateHealthParams", "patrolNow", "tunStatus", "runTunChecks", "installWintun"}
