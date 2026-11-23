package main

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type bootstrapSafeBody struct {
	io.Reader
	closes int
	fail   bool
}

func (b *bootstrapSafeBody) Close() error {
	b.closes++
	if b.fail {
		panic("close fault")
	}
	return nil
}
func TestBootstrapServiceSafeProbeBody(t *testing.T) {
	for _, status := range []int{200, 503} {
		e := newBootstrapEffects(bootstrapServiceRequest{Op: "probe"})
		e.record = serviceRecord{Port: 1, Token: "fake"}
		b := &bootstrapSafeBody{Reader: strings.NewReader(`{"elevated":true,"connected":true} trailing`)}
		e.request = func(r serviceRecord, m, p string) (*http.Response, error) {
			if r.Token != "fake" || m != "GET" || p != "/api/state" {
				t.Fatal("request changed")
			}
			return &http.Response{StatusCode: status, Body: b}, nil
		}
		r := runBootstrapWithEffects(e.q, e)
		if r.Err != nil || r.Bool != (status == 200) || b.closes != 1 {
			t.Fatalf("result %+v closes %d", r, b.closes)
		}
		e.Release()
		if b.closes != 1 {
			t.Fatal("double close")
		}
	}
	e := newBootstrapEffects(bootstrapServiceRequest{Op: "probe"})
	e.record = serviceRecord{Port: 1, Token: "fake"}
	b := &bootstrapSafeBody{Reader: strings.NewReader("{}"), fail: true}
	e.request = func(serviceRecord, string, string) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: b}, nil
	}
	if r := runBootstrapWithEffects(e.q, e); r.Err == nil {
		t.Fatal("close panic swallowed")
	}
	e.Release()
	if b.closes != 1 {
		t.Fatal("close replay")
	}
}
func TestBootstrapServiceSafeIdentify(t *testing.T) {
	for _, status := range []int{200, 404} {
		e := newBootstrapEffects(bootstrapServiceRequest{Op: "identify"})
		b := &bootstrapSafeBody{Reader: strings.NewReader(`{"app":"` + appName + `"}`)}
		e.identifyDo = func(string) (*http.Response, error) {
			tr := e.client.Transport.(*http.Transport)
			if e.client.Timeout != 2*time.Second || tr.Proxy != nil || !tr.DisableKeepAlives {
				t.Fatal("client policy")
			}
			return &http.Response{StatusCode: status, Body: b}, nil
		}
		r := runBootstrapWithEffects(e.q, e)
		if r.Err != nil || r.Bool != (status == 200) || b.closes != 1 || e.idleClose != nil {
			t.Fatalf("result %+v", r)
		}
	}
}
func TestBootstrapServiceSafeRecordFiles(t *testing.T) {
	home := t.TempDir()
	e := newBootstrapEffects(bootstrapServiceRequest{Op: "write-record"})
	e.home = home
	e.record = serviceRecord{PID: 17, Port: 23, Token: "fake"}
	writes := 0
	e.writeFile = func(p string, b []byte, m os.FileMode) error {
		writes++
		if m != 0600 || p != serviceRecordPath(home)+".tmp" {
			t.Fatal("write policy")
		}
		return os.WriteFile(p, b, m)
	}
	e.rename = func(a, b string) error { return errors.New("rename") }
	if r := runBootstrapWithEffects(e.q, e); r.Err == nil || writes != 1 {
		t.Fatal("rename failure")
	}
	if _, err := os.Stat(serviceRecordPath(home) + ".tmp"); err != nil {
		t.Fatal("failed rename removed temp")
	}
	e = newBootstrapEffects(bootstrapServiceRequest{Op: "remove-record"})
	e.home = home
	if err := os.WriteFile(serviceRecordPath(home), []byte("malformed"), 0600); err != nil {
		t.Fatal(err)
	}
	if r := runBootstrapWithEffects(e.q, e); r.Err != nil {
		t.Fatal(r.Err)
	}
	if _, err := os.Stat(serviceRecordPath(home)); !os.IsNotExist(err) {
		t.Fatal("malformed not removed")
	}
}
func TestBootstrapServiceSafeLockLease(t *testing.T) {
	for _, fail := range []bool{false, true} {
		e := newBootstrapEffects(bootstrapServiceRequest{Op: "lock"})
		closed := 0
		attempts := 0
		e.clock = func() time.Time { return time.Unix(1, 0) }
		e.hooks = map[string]func(bootstrapServiceEffect) bootstrapServiceReply{
			"lock-open": func(bootstrapServiceEffect) bootstrapServiceReply {
				e.lockClose = func() { closed++ }
				return bootstrapServiceReply{}
			},
			"lock-try": func(bootstrapServiceEffect) bootstrapServiceReply {
				attempts++
				if fail {
					panic("try fault")
				}
				return bootstrapServiceReply{}
			},
			"lock-acquired": func(bootstrapServiceEffect) bootstrapServiceReply {
				e.lockRelease = func() { closed++ }
				return bootstrapServiceReply{}
			},
		}
		r := runBootstrapWithEffects(e.q, e)
		if fail {
			if r.Err == nil || closed != 1 {
				t.Fatal("failure leaked lock")
			}
		} else {
			if r.Err != nil || !r.Lock || closed != 0 {
				t.Fatal("successful lock not transferred")
			}
		}
		e.Release()
		if attempts != 1 {
			t.Fatal("replay")
		}
	}
}
func TestBootstrapServiceSafePortable(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "portable.txt"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	e := newBootstrapEffects(bootstrapServiceRequest{Op: "portable"})
	e.bundleDir = root
	r := runBootstrapWithEffects(e.q, e)
	if r.Err != nil || !r.Bool || e.home != filepath.Join(root, "SmartVPN-data") {
		t.Fatalf("%+v", r)
	}
	if _, err := os.Stat(filepath.Join(e.home, ".write-probe")); !os.IsNotExist(err) {
		t.Fatal("probe remains")
	}
}
func TestBootstrapServiceSafeOwnedMutex(t *testing.T) {
	a := &app{}
	e := newBootstrapEffects(bootstrapServiceRequest{Op: "tunnel"})
	e.a = a
	e.Call(bootstrapServiceEffect{Kind: "app-lock"})
	if a.mu.TryLock() {
		a.mu.Unlock()
		t.Fatal("lock not acquired")
	}
	closed := 0
	e.closeKey = func() { closed++; panic("key close") }
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("missing cleanup panic")
			}
		}()
		e.Release()
	}()
	if !a.mu.TryLock() {
		t.Fatal("cleanup panic leaked mutex")
	}
	a.mu.Unlock()
	e.Release()
	if closed != 1 {
		t.Fatal("key close repeated")
	}
	a.mu.Lock()
	borrowed := newBootstrapEffects(bootstrapServiceRequest{})
	borrowed.a = a
	borrowed.Release()
	if a.mu.TryLock() {
		a.mu.Unlock()
		t.Fatal("released caller mutex")
	}
	a.mu.Unlock()
}
func TestBootstrapServiceSafeJNIResponseLock(t *testing.T) {
	a := &app{}
	a.mu.Lock()
	e := newBootstrapEffects(bootstrapServiceRequest{Op: "jni-start"})
	e.serviceUnlock = a.mu.Unlock
	e.deferServiceUnlock = true
	e.Release()
	if a.mu.TryLock() {
		a.mu.Unlock()
		t.Fatal("JNI response lost service lock")
	}
	e.deferServiceUnlock = false
	e.Release()
	if !a.mu.TryLock() {
		t.Fatal("JNI final response leaked lock")
	}
	a.mu.Unlock()
	e.Release()
}
