package main

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func lifecycleSafeFX(a *app, q lifecycleServiceRequest) *appLifecycleEffects {
	e := newAppLifecycleEffects(a, q)
	e.readFile = func(string) ([]byte, error) { return nil, os.ErrNotExist }
	e.glob = func(string) ([]string, error) { return nil, nil }
	e.openStore = func(string) (*store, error) { return nil, errors.New("synthetic store unavailable") }
	e.hooks = map[string]func(lifecycleEffect) lifecycleReply{"log": func(lifecycleEffect) lifecycleReply { return lifecycleReply{} }}
	return e
}
func TestLifecycleServicePartialSettingsAndAlias(t *testing.T) {
	a := &app{home: "synthetic"}
	q := lifecycleServiceRequest{Op: "load-settings"}
	e := lifecycleSafeFX(a, q)
	e.readFile = func(p string) ([]byte, error) {
		if strings.HasSuffix(p, "settings.json") {
			return []byte(`{"socksPort":"wrong-type","chain":{"hops":["a","b"]},"strictPrivacy":true,"selectedNode":"n"}`), nil
		}
		return nil, os.ErrNotExist
	}
	runGoLifecycleService(q, e)
	if !a.settingsLocked().WebRTCProtection || !a.settingsLocked().DNSProtection || a.settingsLocked().StrictPrivacy || a.settingsLocked().SelectionMode != "auto" || a.pathConfigLocked().IntervalSec != 120 {
		t.Fatal(a.settingsLocked())
	}
	h := a.settings.Chains[lifecycleLegacyChainName()]
	if len(h) != 2 || &h[0] != &a.settings.Chain.Hops[0] {
		t.Fatal("legacy alias changed")
	}
}
func TestLifecycleServiceReadFailureKeepsLegacyFlags(t *testing.T) {
	a := &app{}
	testEditSettings(a, func(s *settings) { s.StrictPrivacy = true })
	testEditSettings(a, func(s *settings) { s.SubscriptionURL = "synthetic" })
	q := lifecycleServiceRequest{Op: "load-settings"}
	e := lifecycleSafeFX(a, q)
	e.openSecret = func(string) (string, error) { return "partial", errors.New("synthetic") }
	runGoLifecycleService(q, e)
	if !a.settingsLocked().StrictPrivacy || a.settingsLocked().SubscriptionURL != "" {
		t.Fatal("read failure migrated/secret retained")
	}
}

type lifecycleFakeListener struct{ port, closes int }

func (l *lifecycleFakeListener) Accept() (net.Conn, error) { return nil, io.EOF }
func (l *lifecycleFakeListener) Close() error              { l.closes++; return nil }
func (l *lifecycleFakeListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: l.port}
}
func TestLifecycleServiceListenCollisionOwnership(t *testing.T) {
	for _, fail := range []bool{false, true} {
		a := &app{}
		testEditSettings(a, func(s *settings) { s.SocksPort = 10 })
		q := lifecycleServiceRequest{Op: "serve"}
		e := lifecycleSafeFX(a, q)
		first := &lifecycleFakeListener{port: 10}
		second := &lifecycleFakeListener{port: 11}
		calls := 0
		e.listen = func() (net.Listener, error) {
			calls++
			if calls == 1 {
				return first, nil
			}
			if first.closes != 0 {
				t.Fatal("closed reservation before next listen")
			}
			if fail {
				return nil, errors.New("second listen failed")
			}
			return second, nil
		}
		served := 0
		e.serve = func(s *http.Server, l net.Listener) {
			served++
			if s.ReadHeaderTimeout.Seconds() != 3 || l.Addr().(*net.TCPAddr).Port != 11 {
				t.Fatal("serve contract")
			}
		}
		e.closeServer = func(*http.Server) error { t.Fatal("closed returned server"); return nil }
		r := runGoLifecycleService(q, e)
		e.Release()
		if first.closes != 1 {
			t.Fatal(first.closes)
		}
		if fail {
			if r.Err == nil || served != 0 {
				t.Fatal(r)
			}
		} else {
			if r.Err != nil || r.Port != 11 || r.Server == nil || second.closes != 0 || served != 1 || a.apiPort != 11 {
				t.Fatal(r, second.closes)
			}
		}
	}
}

type lifecycleFakeBackend struct {
	storeBackend
	closes int
}

func (b *lifecycleFakeBackend) Close() error { b.closes++; return nil }
func TestLifecycleServiceStoreAbortCleanup(t *testing.T) {
	a := &app{portable: true}
	q := lifecycleServiceRequest{Op: "new"}
	e := lifecycleSafeFX(a, q)
	b := &lifecycleFakeBackend{}
	e.openStore = func(string) (*store, error) { return &store{backend: b}, nil }
	e.hooks["load-params"] = func(lifecycleEffect) lifecycleReply { panic("synthetic after open") }
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("no panic")
			}
		}()
		runGoLifecycleService(q, e)
	}()
	e.Release()
	if b.closes != 1 {
		t.Fatal("lost/double resource", b.closes)
	}
}
func TestLifecycleServicePreparePreservesOtherProxy(t *testing.T) {
	a := &app{}
	q := lifecycleServiceRequest{Op: "prepare"}
	e := lifecycleSafeFX(a, q)
	e.proxyOther = func() bool { return true }
	e.proxy = func(bool) error { t.Fatal("changed other instance proxy"); return nil }
	order := ""
	for _, kind := range []string{"redactor", "scrub", "china-refresh"} {
		k := kind
		e.hooks[k] = func(lifecycleEffect) lifecycleReply { order += k + ";"; return lifecycleReply{} }
	}
	runGoLifecycleService(q, e)
	if order != "redactor;scrub;china-refresh;" {
		t.Fatal(order)
	}
}
func TestLifecycleServiceShutdownResponseBeforeSignal(t *testing.T) {
	q := lifecycleServiceRequest{Op: "shutdown"}
	e := lifecycleSafeFX(&app{}, q)
	order := ""
	e.hooks["disconnect"] = func(lifecycleEffect) lifecycleReply {
		order += "response;"
		return lifecycleReply{Err: errors.New("disconnect failed")}
	}
	e.signal = func() { order += "signal;" }
	runGoLifecycleService(q, e)
	if order != "response;signal;" || !e.responded {
		t.Fatal(order)
	}
}
func TestLifecycleServiceReapplyFailurePartial(t *testing.T) {
	a := &app{proxyOn: true}
	q := lifecycleServiceRequest{Op: "reapply"}
	e := lifecycleSafeFX(a, q)
	e.w = httptest.NewRecorder()
	e.hooks["running"] = func(lifecycleEffect) lifecycleReply { return lifecycleReply{Bool: true} }
	e.hooks["mode"] = func(lifecycleEffect) lifecycleReply { return lifecycleReply{Text: modeSystemProxy} }
	e.proxy = func(bool) error {
		if a.mu.TryLock() {
			a.mu.Unlock()
			t.Fatal("proxy call unlocked")
		}
		return errors.New("synthetic")
	}
	r := runGoLifecycleService(q, e)
	if r.Status != 500 || !a.proxyOn {
		t.Fatal(r)
	}
	if !a.mu.TryLock() {
		t.Fatal("lock leak")
	}
	a.mu.Unlock()
}

func TestLifecycleServiceStoreNormalHandoff(t *testing.T) {
	a := &app{portable: true}
	q := lifecycleServiceRequest{Op: "new"}
	e := lifecycleSafeFX(a, q)
	b := &lifecycleFakeBackend{}
	e.openStore = func(string) (*store, error) { return &store{backend: b}, nil }
	e.hooks["load-params"] = func(lifecycleEffect) lifecycleReply { return lifecycleReply{State: e.snapshot()} }
	for _, kind := range []string{"load-health", "load-regions", "manual", "subscriptions", "source-labels"} {
		e.hooks[kind] = func(lifecycleEffect) lifecycleReply { return lifecycleReply{} }
	}
	r := runGoLifecycleService(q, e)
	e.Release()
	if r.Err != nil || b.closes != 0 || a.store == nil {
		t.Fatal(r, b.closes)
	}
	a.store.Close()
	e.Release()
	if b.closes != 1 {
		t.Fatal("handoff replay", b.closes)
	}
}
