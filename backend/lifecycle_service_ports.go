package main

import (
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

type lifecycleListener struct {
	net.Listener
	once sync.Once
	err  error
}

func (l *lifecycleListener) Close() error {
	l.once.Do(func() { l.err = l.Listener.Close() })
	return l.err
}

type appLifecycleEffects struct {
	a                                          *app
	q                                          lifecycleServiceRequest
	w                                          http.ResponseWriter
	request                                    *http.Request
	redactor                                   *logRedactor
	token                                      string
	locked, storeOwned, serverOwned, responded bool
	authorityOwned                             bool
	settingsBody, cacheBody, bundleBody        []byte
	plain                                      string
	cache                                      cachedNodeList
	params                                     healthParams
	health                                     map[string]nodeHealth
	regions                                    map[string]regionRecord
	matches                                    []string
	listeners                                  map[int]*lifecycleListener
	server                                     *http.Server
	hooks                                      map[string]func(lifecycleEffect) lifecycleReply
	readFile                                   func(string) ([]byte, error)
	writeFile                                  func(string, []byte, os.FileMode) error
	remove                                     func(string) error
	stat                                       func(string) (os.FileInfo, error)
	glob                                       func(string) ([]string, error)
	openSecret                                 func(string) (string, error)
	openStore                                  func(string) (*store, error)
	listen                                     func() (net.Listener, error)
	serve                                      func(*http.Server, net.Listener)
	closeServer                                func(*http.Server) error
	signal                                     func()
	proxy                                      func(bool) error
	proxyOther                                 func() bool
}

func newAppLifecycleEffects(a *app, q lifecycleServiceRequest) *appLifecycleEffects {
	e := &appLifecycleEffects{a: a, q: q, listeners: map[int]*lifecycleListener{}, readFile: os.ReadFile, writeFile: os.WriteFile, remove: os.Remove, stat: os.Stat, glob: filepath.Glob, openSecret: openSecret, openStore: openStore}
	e.listen = func() (net.Listener, error) { return net.Listen("tcp4", "127.0.0.1:0") }
	e.serve = func(s *http.Server, l net.Listener) {
		handler, closeRead, _ := a.openHostAPI(e.token, s.Handler.(*http.ServeMux))
		s.Handler = handler
		go func() {
			if err := s.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Printf("local API stopped: %v", err)
			}
			_ = drainLocalServer(s)
			closeRead()
		}()
	}
	e.closeServer = func(s *http.Server) error { return s.Close() }
	e.signal = func() { a.shutdown <- struct{}{} }
	e.proxy = a.setSystemProxy
	e.proxyOther = a.proxyServedByAnotherInstance
	return e
}
func (e *appLifecycleEffects) snapshot() *lifecycleServiceState {
	a := e.a
	path := a.pathConfigLocked()
	chains := a.borrowChainsLocked()
	return &lifecycleServiceState{Portable: a.portable, Android: runtime.GOOS == "android", ChainsNil: chains.Chains == nil, Hops: len(chains.Chain.Hops), StrictPrivacy: a.controlMetaLocked().StrictPrivacy, HasURL: a.controlMetaLocked().SubscriptionURL != "", SelectionEmpty: a.controlMetaLocked().SelectionMode == "", PathDefault: path.IntervalSec == 0 && len(path.Targets) == 0, Store: a.store != nil, ProxyOn: a.proxyOn, SocksPort: a.controlMetaLocked().SocksPort, Selected: a.controlMetaLocked().SelectedNode, ConnectTimeout: e.params.ConnectTimeoutMs, FailureThreshold: e.params.FailureThreshold}
}
func (e *appLifecycleEffects) closeListener(i int) {
	if l := e.listeners[i]; l != nil {
		delete(e.listeners, i)
		_ = l.Close()
	}
}
func (e *appLifecycleEffects) Release() {
	defer func() {
		if e.authorityOwned {
			e.authorityOwned = false
			_ = e.a.service.Close()
		}
	}()
	defer func() {
		if e.locked {
			e.locked = false
			e.a.mu.Unlock()
		}
	}()
	defer func() {
		if e.storeOwned {
			e.storeOwned = false
			_ = e.a.store.Close()
		}
	}()
	defer e.closeListener(0)
	defer e.closeListener(1)
	if e.serverOwned {
		e.serverOwned = false
		_ = e.closeServer(e.server)
	}
}
func (e *appLifecycleEffects) FinalizeLifecycleResult(q lifecycleServiceRequest, r lifecycleServiceResult) lifecycleServiceResult {
	if r.Err == nil {
		if q.Op == "new" {
			e.authorityOwned = false
			e.storeOwned = false
		}
		if q.Op == "serve" {
			r.Server = e.server
			e.serverOwned = false
			clear(e.listeners)
		}
	}
	return r
}
func (e *appLifecycleEffects) Call(q lifecycleEffect) (r lifecycleReply) {
	if q.Kind == "response" || q.Kind == "disconnect" {
		e.responded = true
	}
	if h := e.hooks[q.Kind]; h != nil {
		return h(q)
	}
	a := e.a
	switch q.Kind {
	case "init":
		b, err := newSelectedServiceStateBackend()
		if err != nil {
			panic(err)
		}
		a.service = newServiceAuthority(b)
		e.authorityOwned = true
		a.shutdown = make(chan struct{}, 1)
		a.replaceHealthParamsLocked(defaultHealthParams())
		a.restoreHealthRecordsLocked(map[string]nodeHealth{})
		a.restoreRegionRecordsLocked(map[string]regionRecord{})
	case "snapshot":
		r.State = e.snapshot()
	case "read-settings":
		e.settingsBody, r.Err = e.readFile(filepath.Join(a.home, "settings.json"))
	case "decode-settings":
		r.Err = a.decodeSettingsDocumentLocked(e.settingsBody)
		r.State = e.snapshot()
	case "open-secret":
		e.plain, r.Err = e.openSecret(a.controlMetaLocked().SubscriptionURL)
	case "set":
		switch q.Name {
		case "legacy-chain":
			s := a.borrowChainsLocked()
			s.Chain.Name = lifecycleLegacyChainName()
			s.Chains = map[string][]string{s.Chain.Name: s.Chain.Hops}
			a.replaceChainsLocked(s)
		case "strict":
			a.patchControlLocked(controlWebRTCProtection|controlDNSProtection|controlStrictPrivacy, serviceControlState{WebRTCProtection: true, DNSProtection: true})
		case "url":
			if q.Flag {
				a.patchControlLocked(controlSubscriptionURL, serviceControlState{})
			} else {
				a.patchControlLocked(controlSubscriptionURL, serviceControlState{SubscriptionURL: e.plain})
			}
		case "selection":
			a.patchControlLocked(controlSelectionMode, serviceControlState{SelectionMode: "auto"})
		case "path-default":
			a.replacePathConfigLocked(defaultPathChecks())
		case "locks":
			c := a.controlMetaLocked()
			a.patchControlLocked(controlRuntimeLockedRegion|controlRuntimeLockedNode, serviceControlState{RuntimeLockedRegion: c.LockedRegion, RuntimeLockedNode: c.LockedNode})
		case "timeout":
			e.params.ConnectTimeoutMs = q.Value
		case "threshold":
			e.params.FailureThreshold = q.Value
		case "params":
			a.replaceHealthParamsLocked(e.params)
		case "health":
			a.restoreHealthRecordsLocked(e.health)
		case "regions":
			a.restoreRegionRecordsLocked(e.regions)
		case "proxy":
			a.proxyOn = q.Flag
		case "api-port":
			a.apiPort = q.Value
		}
	case "cache-read":
		e.cacheBody, r.Err = e.readFile(a.cachedNodesPath())
	case "cache-decode":
		e.cache = cachedNodeList{}
		r.Err = json.Unmarshal(e.cacheBody, &e.cache)
		r.Nodes = e.cache.Nodes
	case "cache-hash":
		r.Bool = e.cache.SubscriptionHash == subscriptionHash(a.controlMetaLocked().SubscriptionURL)
	case "cache-remove":
		r.Err = e.remove(a.cachedNodesPath())
	case "cache-apply":
		nodes := e.cache.Nodes
		for i := range nodes {
			nodes[i].Selected = false
		}
		for _, i := range q.Indices {
			nodes[i].Selected = true
		}
		a.replaceCachedNodesLocked(nodes, false)
	case "glob":
		e.matches, r.Err = e.glob(filepath.Join(a.home, "config-*.yaml"))
		r.Names = append([]string(nil), e.matches...)
	case "own-config":
		r.Text = a.configPath()
	case "remove-config":
		r.Err = e.remove(e.matches[q.Index])
	case "bundle-source":
		r.Bool = a.bundleDir != ""
	case "bundle-target-stat":
		_, r.Err = e.stat(filepath.Join(a.home, q.Name))
	case "bundle-source-stat":
		_, r.Err = e.stat(filepath.Join(a.bundleDir, q.Name))
	case "bundle-read":
		e.bundleBody, r.Err = e.readFile(filepath.Join(a.bundleDir, q.Name))
	case "bundle-write":
		r.Err = e.writeFile(filepath.Join(a.home, q.Name), e.bundleBody, 0700)
	case "store-open":
		var s *store
		s, r.Err = e.openStore(filepath.Join(a.home, "smartvpn.db"))
		if r.Err == nil {
			a.store = s
			e.storeOwned = true
		}
	case "load-params":
		e.params, r.Err = a.store.loadHealthParams()
		r.State = e.snapshot()
	case "load-health":
		e.health, r.Err = a.store.loadHealth()
	case "load-regions":
		e.regions, r.Err = a.store.loadRegions()
	case "manual":
		a.loadManualNodesLocked()
	case "subscriptions":
		a.loadSubscriptionsLocked()
	case "source-labels":
		a.restoreSourceLabels()
	case "proxy-other":
		r.Bool = e.proxyOther()
	case "proxy":
		r.Err = e.proxy(q.Flag)
	case "redactor":
		a.redactor = e.redactor
		e.redactor.Set(a.subscriptionSecrets())
	case "scrub":
		a.scrubKernelLog()
	case "china-refresh":
		a.startChinaIPRefresh(chinaListIfOverdue)
	case "listen":
		var l net.Listener
		l, r.Err = e.listen()
		if r.Err == nil {
			e.listeners[q.Index] = &lifecycleListener{Listener: l}
			r.Value = l.Addr().(*net.TCPAddr).Port
		}
	case "listener-close":
		e.closeListener(q.Index)
	case "server-new":
		e.server = &http.Server{Handler: a.routes(e.token), ReadHeaderTimeout: 3 * time.Second}
		e.serverOwned = true
	case "server-serve":
		e.serve(e.server, e.listeners[q.Index])
	case "lock":
		a.mu.Lock()
		e.locked = true
	case "unlock":
		e.locked = false
		a.mu.Unlock()
	case "stop-auto":
		a.stopAutoMergeLoopLocked()
	case "stop-kernel":
		r.Err = a.stopMihomoLocked()
	case "store-close":
		r.Err = a.store.Close()
	case "disconnect":
		a.disconnect(e.w, e.request)
		e.responded = true
	case "signal":
		e.signal()
	case "running":
		r.Bool = a.kernelRunningLocked()
	case "mode":
		r.Text = a.connectionModeLocked()
	case "response":
		if e.w != nil {
			if q.Value != 0 {
				http.Error(e.w, q.Text, q.Value)
			} else if q.Name == "identify" {
				writeIdentityJSON(e.w, q.Flag)
			} else {
				writeJSON(e.w, map[string]bool{"ok": true})
			}
		}
	case "log":
		log.Print(lifecycleLog(q.Index, q.Name, q.Err))
	}
	return
}
func runLifecycleWithEffects(q lifecycleServiceRequest, e *appLifecycleEffects) lifecycleServiceResult {
	defer e.Release()
	return runSelectedLifecycleService(q, e)
}

func runLifecycleHTTP(e *appLifecycleEffects) {
	r := runLifecycleWithEffects(e.q, e)
	if r.Err != nil && !e.responded {
		e.Call(lifecycleEffect{Kind: "response", Value: 500, Text: "service operation interrupted"})
	}
}
