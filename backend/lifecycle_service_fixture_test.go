package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type lifeDiffSpec struct {
	Fail                                                                           string
	Settings                                                                       string
	Portable, Android, Other, Running, ProxyOn, HashMatch, BundleExists, Collision bool
	Mode                                                                           string
	Timeout, Threshold                                                             int
}

func lifeDiffBase() lifeDiffSpec {
	return lifeDiffSpec{Settings: `{"subscriptionUrl":"sealed","selectedNode":"a","strictPrivacy":true,"chain":{"hops":["a","b"]}}`, HashMatch: true, Mode: modeSystemProxy, Timeout: 1000, Threshold: 1}
}

type lifeDiffListener struct {
	port   int
	closed *int
}

func (l *lifeDiffListener) Accept() (net.Conn, error) { panic("fake listener cannot Accept") }
func (l *lifeDiffListener) Close() error              { *l.closed++; return nil }
func (l *lifeDiffListener) Addr() net.Addr            { return &net.TCPAddr{Port: l.port} }

type lifeDiffFrame struct {
	Effect               lifecycleEffect
	Settings             string
	Nodes                []proxyNode
	Locked, Proxy, Store bool
	Port                 int
	Extra                []string
}
type lifeDiffFX struct {
	e                                 *appLifecycleEffects
	spec                              lifeDiffSpec
	trace                             []lifeDiffFrame
	extra                             []string
	releases, closed, served, signals int
	server                            *http.Server
	w                                 *httptest.ResponseRecorder
}

func lifeDiffNew(s lifeDiffSpec, q lifecycleServiceRequest) *lifeDiffFX {
	a := &app{home: "synthetic-home", bundleDir: "synthetic-bundle", portable: s.Portable, proxyOn: s.ProxyOn}
	testEditSettings(a, func(s *settings) { s.SocksPort = 1080 })
	if q.Op == "cleanup" {
		a.store = &store{}
	}
	f := &lifeDiffFX{spec: s, w: httptest.NewRecorder()}
	e := newAppLifecycleEffects(a, q)
	f.e = e
	e.w = f.w
	e.hooks = map[string]func(lifecycleEffect) lifecycleReply{}
	fail := func(k string) error {
		if s.Fail == k {
			return errors.New("synthetic " + k)
		}
		return nil
	}
	e.readFile = func(p string) ([]byte, error) {
		if filepath.Base(p) == "settings.json" {
			return []byte(s.Settings), fail("read-settings")
		}
		if filepath.Base(p) == "nodes-cache.json" {
			hash := subscriptionHash(a.settingsLocked().SubscriptionURL)
			if !s.HashMatch {
				hash = "wrong"
			}
			b, _ := json.Marshal(cachedNodeList{SubscriptionHash: hash, Nodes: []proxyNode{{Name: "a"}, {Name: "b", Selected: true}}})
			return b, fail("cache-read")
		}
		return []byte("bundle"), fail("bundle-read")
	}
	e.remove = func(p string) error { f.extra = append(f.extra, "remove:"+p); return fail("remove") }
	e.writeFile = func(p string, b []byte, m os.FileMode) error {
		f.extra = append(f.extra, fmt.Sprintf("write:%s:%o", p, m))
		return fail("bundle-write")
	}
	e.stat = func(p string) (os.FileInfo, error) {
		if filepath.Dir(p) == a.home && !s.BundleExists {
			return nil, os.ErrNotExist
		}
		return nil, fail("stat")
	}
	e.glob = func(string) ([]string, error) {
		return []string{a.configPath(), filepath.Join(a.home, "config-old.yaml")}, fail("glob")
	}
	e.openSecret = func(string) (string, error) { return "plain", fail("open-secret") }
	e.proxy = func(on bool) error { f.extra = append(f.extra, fmt.Sprint("proxy:", on)); return fail("proxy") }
	e.proxyOther = func() bool { return s.Other }
	e.signal = func() { f.signals++; f.extra = append(f.extra, "signal") }
	calls := 0
	e.listen = func() (net.Listener, error) {
		calls++
		if calls == 2 && f.closed != 0 {
			panic("reserved listener closed before second allocation")
		}
		if er := fail(fmt.Sprintf("listen-%d", calls)); er != nil {
			return nil, er
		}
		port := 8000 + calls
		if s.Collision && calls == 1 {
			port = a.settingsLocked().SocksPort
		}
		return &lifeDiffListener{port: port, closed: &f.closed}, nil
	}
	e.serve = func(server *http.Server, l net.Listener) {
		f.server = server
		f.served++
		f.extra = append(f.extra, fmt.Sprint("serve:", l.Addr()))
	}
	e.closeServer = func(*http.Server) error { f.extra = append(f.extra, "close-server"); return nil }
	e.hooks["snapshot"] = func(lifecycleEffect) lifecycleReply {
		v := e.snapshot()
		v.Android = s.Android
		return lifecycleReply{State: v}
	}
	e.hooks["store-open"] = func(lifecycleEffect) lifecycleReply {
		er := fail("store-open")
		if er == nil {
			a.store = &store{}
		}
		return lifecycleReply{Err: er}
	}
	e.hooks["load-params"] = func(lifecycleEffect) lifecycleReply {
		e.params = healthParams{ConnectTimeoutMs: s.Timeout, FailureThreshold: s.Threshold}
		v := e.snapshot()
		v.Android = s.Android
		return lifecycleReply{Err: fail("load-params"), State: v}
	}
	e.hooks["load-health"] = func(lifecycleEffect) lifecycleReply {
		e.health = map[string]nodeHealth{"a": {}}
		return lifecycleReply{Err: fail("load-health")}
	}
	e.hooks["load-regions"] = func(lifecycleEffect) lifecycleReply {
		e.regions = map[string]regionRecord{"a": {}}
		return lifecycleReply{Err: fail("load-regions")}
	}
	for _, k := range []string{"manual", "subscriptions", "source-labels", "redactor", "scrub", "china-refresh", "stop-auto", "stop-kernel", "store-close", "log"} {
		key := k
		e.hooks[key] = func(lifecycleEffect) lifecycleReply { return lifecycleReply{Err: fail(key)} }
	}
	e.hooks["disconnect"] = func(lifecycleEffect) lifecycleReply {
		f.w.WriteHeader(200)
		_, _ = f.w.Write([]byte(`{"ok":true}`))
		f.extra = append(f.extra, "disconnect-response")
		return lifecycleReply{Err: fail("disconnect")}
	}
	e.hooks["running"] = func(lifecycleEffect) lifecycleReply { return lifecycleReply{Bool: s.Running} }
	e.hooks["mode"] = func(lifecycleEffect) lifecycleReply { return lifecycleReply{Text: s.Mode} }
	return f
}
func (f *lifeDiffFX) Call(q lifecycleEffect) lifecycleReply {
	if f.e.hooks[q.Kind] == nil {
		switch q.Kind {
		case "init", "read-settings", "decode-settings", "open-secret", "set", "cache-read", "cache-decode", "cache-hash", "cache-remove", "cache-apply", "glob", "own-config", "remove-config", "bundle-source", "bundle-target-stat", "bundle-source-stat", "bundle-read", "bundle-write", "proxy-other", "proxy", "listen", "listener-close", "server-new", "server-serve", "lock", "unlock", "signal", "response":
		default:
			panic("unmocked lifecycle port " + q.Kind)
		}
	}
	if q.Kind == "response" && f.e.q.Op == "identify" && f.e.locked {
		panic("identify response under lock")
	}
	if q.Kind == "response" && f.e.q.Op == "reapply" && !f.e.locked {
		panic("reapply response without lock")
	}
	r := f.e.Call(q)
	b, _ := json.Marshal(f.e.a.settingsDocumentLocked())
	q.Indices = append([]int(nil), q.Indices...)
	f.trace = append(f.trace, lifeDiffFrame{q, string(b), append([]proxyNode(nil), f.e.a.cachedNodes...), f.e.locked, f.e.a.proxyOn, f.e.a.store != nil, f.e.a.apiPort, append([]string(nil), f.extra...)})
	return r
}
func (f *lifeDiffFX) Release() { f.releases++; f.e.Release() }
func (f *lifeDiffFX) FinalizeLifecycleResult(q lifecycleServiceRequest, r lifecycleServiceResult) lifecycleServiceResult {
	return f.e.FinalizeLifecycleResult(q, r)
}

type lifeDiffRun func(lifecycleServiceRequest, lifecycleServiceEffects) lifecycleServiceResult

func lifeDiffCompare(t *testing.T, q lifecycleServiceRequest, s lifeDiffSpec, run lifeDiffRun) {
	t.Helper()
	g, r := lifeDiffNew(s, q), lifeDiffNew(s, q)
	a, b := runGoLifecycleService(q, g), run(q, r)
	ae, be := "", ""
	if a.Err != nil {
		ae = a.Err.Error()
	}
	if b.Err != nil {
		be = b.Err.Error()
	}
	a.Err = nil
	b.Err = nil
	a.Server = nil
	b.Server = nil
	if ae != be || !reflect.DeepEqual(a, b) || !reflect.DeepEqual(g.trace, r.trace) || !reflect.DeepEqual(g.extra, r.extra) || g.closed != r.closed || g.served != r.served || g.signals != r.signals || g.w.Code != r.w.Code || g.w.Body.String() != r.w.Body.String() {
		t.Fatalf("%s fail=%s result %v/%v err %q/%q\ngo=%+v\nrust=%+v", q.Op, s.Fail, a, b, ae, be, g.trace, r.trace)
	}
	if g.e.locked || r.e.locked || g.releases != 1 || r.releases != 1 {
		t.Fatalf("cleanup locks/release %v/%v %d/%d", g.e.locked, r.e.locked, g.releases, r.releases)
	}
}
func TestMigrationLifecycleSafeSelectedLifecycle(t *testing.T) {
	for _, op := range []string{"new", "load-settings", "prepare", "serve", "cleanup", "shutdown", "identify", "reapply"} {
		s := lifeDiffBase()
		s.Running = true
		lifeDiffCompare(t, lifecycleServiceRequest{Op: op}, s, runSelectedLifecycleService)
	}
}
