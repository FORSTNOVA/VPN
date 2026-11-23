package main

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"time"
)

type apiRoute struct {
	method string
	id     uint32
	path   string
	handle http.HandlerFunc
}

func (a *app) apiRoutes() []apiRoute {
	routes := []apiRoute{
		{"GET", 1, "/api/identify", a.identify},
		{"GET", 2, "/api/state", a.state},
		{"GET", 3, "/api/subscription/policy", a.subscriptionPolicy},
		{"GET", 8, "/api/nodes", a.nodes},
		{"GET", 9, "/api/nodes/latency", a.monitored("/api/nodes/latency", a.nodeLatency)},
		{"GET", 11, "/api/diagnostics/subscription", a.subscriptionDiagnostics},
		{"GET", 16, "/api/latency", a.monitored("/api/latency", a.latency)},
		{"GET", 19, "/api/path-checks", a.pathChecks},
		{"GET", 23, "/api/traffic", a.monitored("/api/traffic", a.traffic)},
		{"GET", 24, "/api/traffic/history", a.trafficHistory},
		{"GET", 25, "/api/nodes/manual", a.listManualNodes},
		{"GET", 27, "/api/nodes/probes", a.listProbeResults},
		{"GET", 32, "/api/subscriptions", a.listSubscriptions},
		{"GET", 42, "/api/regions", a.regionReport},
		{"GET", 45, "/api/events", a.switchEvents},
		{"GET", 48, "/api/tun", a.tunStatus},
		{"PUT", 4, "/api/settings", a.updateSettings},
		{"POST", 5, "/api/connect", a.connect},
		{"POST", 6, "/api/disconnect", a.disconnect},
		{"POST", 7, "/api/proxy/reapply", a.reapplyProxy},
		{"POST", 10, "/api/nodes/fetch", a.fetchNodes},
		{"POST", 12, "/api/nodes/refresh", a.refreshNodes},
		{"PUT", 13, "/api/nodes/select", a.selectNode},
		{"POST", 14, "/api/shutdown", a.shutdownService},
		{"POST", 15, "/api/direct/refresh", a.refreshChinaDirect},
		{"POST", 17, "/api/diagnose", a.diagnose},
		{"POST", 18, "/api/speedtest", a.speedTest},
		{"PUT", 20, "/api/path-checks", a.updatePathChecks},
		{"POST", 21, "/api/path-checks/run", a.runPathChecks},
		{"POST", 22, "/api/path-checks/recovery/cancel", a.cancelPathRecovery},
		{"PUT", 26, "/api/nodes/chain/profile", a.saveChainProfile},
		{"POST", 28, "/api/nodes/chain/probe", a.probeChain},
		{"PUT", 29, "/api/nodes/chain", a.updateChain},
		{"POST", 30, "/api/nodes/manual", a.addManualNode},
		{"DELETE", 31, "/api/nodes/manual", a.deleteManualNode},
		{"POST", 33, "/api/subscriptions/activate", a.activateSubscription},
		{"POST", 34, "/api/subscriptions/add", a.addSubscription},
		{"POST", 35, "/api/subscriptions/update", a.updateSubscription},
		{"POST", 36, "/api/subscriptions/merge", a.mergeSubscriptions},
		{"POST", 37, "/api/subscriptions/refresh", a.refreshSingleSubscription},
		{"POST", 38, "/api/subscriptions/quota", a.refreshSubscriptionQuota},
		{"POST", 39, "/api/subscriptions/auto-merge", a.setAutoMergeHours},
		{"PUT", 40, "/api/subscriptions", a.renameSubscription},
		{"DELETE", 41, "/api/subscriptions", a.deleteSubscription},
		{"POST", 43, "/api/regions/verify", a.startRegionVerification},
		{"POST", 44, "/api/region/lock", a.lockRegion},
		{"PUT", 46, "/api/health/params", a.updateHealthParams},
		{"POST", 47, "/api/health/patrol", a.patrolNow},
		{"POST", 49, "/api/tun/check", a.runTunChecks},
	}
	return a.platformAPIRoutes(routes)
}

// The adapter exposes only the operations negotiated at startup. The app lock and time-sensitive
// controller/SQLite resources remain with their existing owners.
func (a *app) openHostAPI(token string, fallback *http.ServeMux) (http.Handler, func(), bool) {
	if !readHostSupported() || token == "" {
		return fallback, func() {}, false
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fallback, func() {}, false
	}
	key := hex.EncodeToString(raw[:])
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return fallback, func() {}, false
	}
	writes := writeHostSupported()
	addr, stop, err := startAPIHost(l.Addr().(*net.TCPAddr).Port, key, token, writes)
	if err != nil && writes {
		writes = false
		addr, stop, err = startAPIHost(l.Addr().(*net.TCPAddr).Port, key, token, false)
	}
	if err != nil {
		l.Close()
		log.Print("Rust API host unavailable, using local handlers")
		return fallback, func() {}, false
	}
	routes := a.apiRoutes()
	adapter := http.NewServeMux()
	for _, route := range routes {
		if !writes && route.method != "GET" {
			continue
		}
		adapter.HandleFunc(route.method+" /"+strconv.Itoa(int(route.id)), func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+key {
				http.Error(w, "unauthorized", 401)
				return
			}
			if route.id != 1 {
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
			}
			r.URL.Path = route.path
			route.handle(w, r)
		})
	}
	srv := &http.Server{Handler: adapter, ReadHeaderTimeout: 3 * time.Second}
	go srv.Serve(l)
	transport := &http.Transport{MaxIdleConnsPerHost: 8, IdleConnTimeout: 2 * time.Second}
	readAPIClient := &http.Client{Transport: transport, CheckRedirect: localMonitorClient.CheckRedirect}
	ingress := http.NewServeMux()
	for _, route := range routes {
		if !writes && route.method != "GET" {
			continue
		}
		ingress.HandleFunc(route.method+" "+route.path, func(w http.ResponseWriter, r *http.Request) {
			var body io.Reader
			if route.method != "GET" {
				body = r.Body
			}
			req, err := http.NewRequestWithContext(r.Context(), r.Method, "http://"+addr+route.path, body)
			if err != nil {
				http.Error(w, "invalid read endpoint", 502)
				return
			}
			if body != nil {
				req.ContentLength = r.ContentLength
				req.Header.Set("Content-Type", r.Header.Get("Content-Type"))
			}
			req.URL.RawQuery = r.URL.RawQuery
			req.Header.Set("Authorization", r.Header.Get("Authorization"))
			res, err := readAPIClient.Do(req)
			if err != nil {
				http.Error(w, "Rust API host unavailable", 502)
				return
			}
			defer res.Body.Close()
			for _, k := range []string{"Content-Type", "Content-Length", "X-Content-Type-Options", "Allow", "Cache-Control"} {
				if v := res.Header.Get(k); v != "" {
					w.Header().Set(k, v)
				}
			}
			w.WriteHeader(res.StatusCode)
			if r.Method != "HEAD" {
				_, _ = io.Copy(w, res.Body)
			}
		})
	}
	if !writes {
		ingress.Handle("/", fallback)
	}
	return ingress, func() { transport.CloseIdleConnections(); stop(); srv.Close(); l.Close() }, true
}
