package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestChainSettingsRoundTrip(t *testing.T) {
	a := storeApp(t)
	for _, name := range []string{"入口", "出口"} {
		if _, err := a.addManualNodeLocked("trojan://fixture@127.0.0.1:9", name); err != nil {
			t.Fatal(err)
		}
	}
	put := func(body string) int {
		w := httptest.NewRecorder()
		a.updateChain(w, httptest.NewRequest("PUT", "/api/nodes/chain", strings.NewReader(body)))
		return w.Code
	}
	if got := put(`{"enabled":true,"hops":["入口","出口"]}`); got != 200 {
		t.Fatal(got)
	}
	if got := put(`{"enabled":true,"hops":["出口","出口"]}`); got != 400 {
		t.Fatal(got)
	}
	a.settings = settings{}
	a.loadSettings()
	if !a.settings.Chain.Enabled || strings.Join(a.settings.Chain.Hops, ",") != "入口,出口" {
		t.Fatal("saved order lost")
	}
	runningKernel(t, a)
	if got := put(`{"enabled":false}`); got != 409 {
		t.Fatal(got)
	}
	a.kernel = nil
	if got := put(`{"enabled":false}`); got != 200 || a.selectedChoice() == chainExit {
		t.Fatal("could not restore normal route")
	}
}

func TestChainOrderAndValidation(t *testing.T) {
	profile := []byte("proxies:\n  - {name: first, type: socks5, server: a, port: 1}\n  - {name: last, type: socks5, server: c, port: 3}\n")
	middle := map[string]any{"name": "mid", "type": "http", "server": "b", "port": 2}
	manual := []manualNode{{Proxy: middle}}
	proxies, err := chainProxies([]string{"first", "mid", "last"}, profile, manual)
	if err != nil {
		t.Fatal(err)
	}
	if proxies[0]["dialer-proxy"] != nil || proxies[1]["dialer-proxy"] != proxies[0]["name"] || proxies[2]["dialer-proxy"] != proxies[1]["name"] || proxies[2]["name"] != chainExit || middle["dialer-proxy"] != nil {
		t.Fatal("wrong dialer order or mutated source")
	}
	for _, hops := range [][]string{{"first"}, {"mid", "mid"}, {"first", "removed"}} {
		if _, err := chainProxies(hops, profile, manual); err == nil {
			t.Fatal("accepted invalid path", hops)
		}
	}
}

// The first HTTP proxy may dial only the second; only the second can resolve
// the target. Stopping the first must break a subsequent, fresh connection.
func TestChainThroughMihomo(t *testing.T) {
	bin := os.Getenv("SMARTVPN_TEST_MIHOMO")
	if bin == "" {
		t.Skip("set SMARTVPN_TEST_MIHOMO to exercise the installed kernel")
	}
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "chain OK") }))
	defer target.Close()
	var firstHits, lastHits atomic.Int32
	relay := func(accept, forward string, hits *atomic.Int32) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "CONNECT" || r.Host != accept {
				http.Error(w, "wrong hop", 403)
				return
			}
			dest, err := net.DialTimeout("tcp", forward, 2*time.Second)
			if err != nil {
				http.Error(w, "connect failed", 502)
				return
			}
			defer dest.Close()
			src, rw, err := w.(http.Hijacker).Hijack()
			if err != nil {
				return
			}
			defer src.Close()
			hits.Add(1)
			fmt.Fprint(rw, "HTTP/1.1 200 Connection Established\r\n\r\n")
			rw.Flush()
			go io.Copy(dest, rw.Reader)
			io.Copy(src, dest)
		}))
	}
	exit := relay("chain-test.invalid:443", target.Listener.Addr().String(), &lastHits)
	defer exit.Close()
	entry := relay(exit.Listener.Addr().String(), exit.Listener.Addr().String(), &firstHits)
	defer entry.Close()
	source := []map[string]any{}
	for i, server := range []*httptest.Server{entry, exit} {
		host, port, _ := net.SplitHostPort(server.Listener.Addr().String())
		n, _ := strconv.Atoi(port)
		source = append(source, map[string]any{"type": "http", "name": []string{"entry", "exit"}[i], "server": host, "port": n})
	}
	probeTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer probeTarget.Close()
	var probeEntries, probeExits atomic.Int32
	probeExit := relay("probe.invalid:80", probeTarget.Listener.Addr().String(), &probeExits)
	defer probeExit.Close()
	probeEntry := relay(probeExit.Listener.Addr().String(), probeExit.Listener.Addr().String(), &probeEntries)
	configs := []map[string]any{}
	for i, srv := range []*httptest.Server{probeEntry, probeExit} {
		host, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
		n, _ := strconv.Atoi(port)
		configs = append(configs, map[string]any{"name": fmt.Sprint(i), "type": "http", "server": host, "port": n})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ms, err := measurePrefix(ctx, configs, "http://probe.invalid/")
	probeEntry.Close()
	if err != nil || ms < 1 || probeEntries.Load() != 1 || probeExits.Load() != 1 {
		t.Fatalf("prefix probe: %v, hits %d/%d", err, probeEntries.Load(), probeExits.Load())
	}
	if _, err = measurePrefix(ctx, configs, "http://probe.invalid/"); err == nil {
		t.Fatal("probe bypassed disconnected entry")
	}

	profile, _ := yaml.Marshal(map[string]any{"proxies": source})
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "subscription.yaml"), profile, 0600); err != nil {
		t.Fatal(err)
	}
	a := app{settings: settings{SubscriptionURL: "fixture"}}
	proxies, err := a.buildChain(proxyChain{Enabled: true, Hops: []string{"entry", "exit"}}, profile)
	if err != nil {
		t.Fatal(err)
	}
	mixed, _ := freeLoopbackPort()
	ctrl, _ := freeLoopbackPort()
	cfg := generatedConfig(configOptions{MixedPort: mixed, ControllerPort: ctrl, SubscriptionProvider: true, ProviderPath: "subscription.yaml", ChainProxies: proxies})
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "-d", dir, "-f", filepath.Join(dir, "config.yaml"))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	a.ctrlPort = ctrl
	a.mixedPort = mixed
	if err := a.waitForMihomo(10 * time.Second); err != nil {
		t.Fatal(err)
	}
	if err := a.waitForNodes(3 * time.Second); err != nil {
		t.Fatalf("chain cannot pass the app's node readiness check: %v", err)
	}
	if err := a.switchGroupLocked(chainExit); err != nil || a.activeNodeLocked() != chainExit {
		t.Fatalf("could not activate chain: %v", err)
	}
	proxyURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", mixed))
	tr := target.Client().Transport.(*http.Transport).Clone()
	tr.DisableKeepAlives = true
	// Trust the test server's certificate under its loopback name.
	tr.TLSClientConfig = tr.TLSClientConfig.Clone()
	tr.TLSClientConfig.ServerName = "127.0.0.1"
	tr.Proxy = http.ProxyURL(proxyURL)
	client := http.Client{Transport: tr, Timeout: 5 * time.Second}
	fetch := func() error {
		resp, err := client.Get("https://chain-test.invalid/")
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if string(b) != "chain OK" {
			return errors.New("target did not answer")
		}
		return nil
	}
	if err := fetch(); err != nil || firstHits.Load() != 1 || lastHits.Load() != 1 {
		t.Fatalf("request %v, hop counts %d/%d", err, firstHits.Load(), lastHits.Load())
	}
	entry.Close()
	if fetch() == nil || lastHits.Load() != 1 {
		t.Fatal("traffic bypassed disconnected entry")
	}
}

// Optional read-only check against the chain selected in an existing portable profile.
func TestPersonalChainProbe(t *testing.T) {
	dir := os.Getenv("SMARTVPN_TEST_CHAIN_HOME")
	if dir == "" {
		t.Skip("no portable profile selected")
	}
	a := app{home: dir}
	b, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil || json.Unmarshal(b, &a.settings) != nil || len(a.settings.Chain.Hops) < 2 {
		t.Fatal("cannot read selected chain")
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(dir, "smartvpn.db"))+"?mode=ro")
	if err != nil {
		t.Fatal("cannot open profile database")
	}
	defer db.Close()
	a.store = &store{db: db}
	a.loadManualNodesLocked()
	profile, err := os.ReadFile(a.profilePath())
	if err != nil {
		t.Fatal("subscription cache unavailable")
	}
	for i := range a.settings.Chain.Hops {
		t.Run(fmt.Sprintf("hop-%d", i+1), func(t *testing.T) {
			proxies, err := prefixProxies(a.settings.Chain.Hops[:i+1], profile, a.manualNodes)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			ms, err := measurePrefix(ctx, proxies, "https://www.gstatic.com/generate_204")
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("HTTP 204 in %d ms", ms)
		})
	}
}

func TestProbeRecordsPersistAndMatchConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "probe.db")
	st, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	a := app{store: st, manualNodes: []manualNode{{Proxy: map[string]any{"type": "http", "name": "down", "server": "127.0.0.1", "port": 1}}}}
	response := httptest.NewRecorder()
	a.probeChain(response, httptest.NewRequest("POST", "/", strings.NewReader(`{"hops":["down"]}`)))
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"error"`) {
		t.Fatal("failed probe was not recorded", response.Code)
	}
	st.Close()
	a.store, err = openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.store.Close()
	list := func() []probeResult {
		w := httptest.NewRecorder()
		a.listProbeResults(w, nil)
		var body struct{ Results []probeResult }
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil {
			t.Fatal("cannot list probe results")
		}
		return body.Results
	}
	if got := list(); len(got) != 1 || got[0].Measured == 0 || got[0].Error == "" {
		t.Fatal("lost failed measurement across restart")
	}
	a.manualNodes[0].Proxy["port"] = 2
	if len(list()) != 0 {
		t.Fatal("reused result after configuration change")
	}
}
