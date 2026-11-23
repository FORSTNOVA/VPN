package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

type appBootstrapEffects struct {
	deferServiceUnlock                                             bool
	closeKey                                                       func()
	q                                                              bootstrapServiceRequest
	a                                                              *app
	home, bundleDir, base, executable, address, script, serverText string
	portable, elevated                                             bool
	record                                                         serviceRecord
	owner                                                          proxyOwner
	existingElevated, existingConnected                            bool
	size, mtime                                                    int64
	raw                                                            []byte
	lastErr                                                        error
	body                                                           io.ReadCloser
	response                                                       *http.Response
	client                                                         *http.Client
	idleClose                                                      func()
	deadline                                                       time.Time
	lockClose, lockRelease                                         func()
	appUnlock, serviceUnlock                                       func()
	server                                                         *http.Server
	serverClose                                                    func() error
	token                                                          string
	port                                                           int
	redactor                                                       *logRedactor
	hooks                                                          map[string]func(bootstrapServiceEffect) bootstrapServiceReply
	readFile                                                       func(string) ([]byte, error)
	writeFile                                                      func(string, []byte, os.FileMode) error
	remove                                                         func(string) error
	rename                                                         func(string, string) error
	clock                                                          func() time.Time
	sleep                                                          func(time.Duration)
	request                                                        func(serviceRecord, string, string) (*http.Response, error)
	identifyDo                                                     func(string) (*http.Response, error)
	platform                                                       func(bootstrapServiceEffect) bootstrapServiceReply
	platformData                                                   any
}

func newBootstrapEffects(q bootstrapServiceRequest) *appBootstrapEffects {
	e := &appBootstrapEffects{q: q, elevated: q.Elevated, readFile: os.ReadFile, writeFile: os.WriteFile, remove: os.Remove, rename: os.Rename, clock: time.Now, sleep: time.Sleep}
	e.platform = e.platformCall
	e.request = func(r serviceRecord, method, route string) (*http.Response, error) {
		req, err := http.NewRequest(method, "http://127.0.0.1:"+strconv.Itoa(r.Port)+route, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+r.Token)
		return (&http.Client{Timeout: 3 * time.Second}).Do(req)
	}
	return e
}
func (e *appBootstrapEffects) closeBody() {
	if b := e.body; b != nil {
		e.body = nil
		_ = b.Close()
	}
}
func (e *appBootstrapEffects) closeIdle() {
	if f := e.idleClose; f != nil {
		e.idleClose = nil
		f()
	}
}
func (e *appBootstrapEffects) releaseLock() {
	if f := e.lockRelease; f != nil {
		e.lockRelease = nil
		e.lockClose = nil
		f()
	} else if f := e.lockClose; f != nil {
		e.lockClose = nil
		f()
	}
}
func (e *appBootstrapEffects) closeServer() {
	if f := e.serverClose; f != nil {
		e.serverClose = nil
		_ = f()
	}
}
func (e *appBootstrapEffects) Release() {
	defer func() {
		if f := e.closeKey; f != nil {
			e.closeKey = nil
			f()
		}
	}()
	defer func() {
		if f := e.serviceUnlock; f != nil && !e.deferServiceUnlock {
			e.serviceUnlock = nil
			f()
		}
	}()
	defer func() {
		if f := e.appUnlock; f != nil {
			e.appUnlock = nil
			f()
		}
	}()
	defer e.releaseLock()
	defer e.closeIdle()
	defer e.closeBody()
	e.closeServer()
}
func (e *appBootstrapEffects) FinalizeBootstrapResult(q bootstrapServiceRequest, r bootstrapServiceResult) bootstrapServiceResult {
	if r.Err == nil {
		if (q.Op == "join" || q.Op == "lock") && r.Lock {
			e.lockClose = nil
			e.lockRelease = nil
		}
		if q.Op == "android-start" || q.Op == "jni-start" {
			e.serverClose = nil
		}
	}
	return r
}
func (e *appBootstrapEffects) Call(q bootstrapServiceEffect) (r bootstrapServiceReply) {
	if h := e.hooks[q.Kind]; h != nil {
		return h(q)
	}
	switch q.Kind {
	case "record-read":
		e.raw, r.Err = e.readFile(serviceRecordPath(e.home))
	case "record-decode":
		e.record = serviceRecord{}
		r.Err = json.Unmarshal(e.raw, &e.record)
	case "record-valid":
		r.Flag = e.record.Port > 0 && e.record.Token != ""
	case "service-request":
		method, route := "GET", "/api/state"
		if q.Count == 1 {
			method, route = "POST", "/api/shutdown"
		}
		e.response, r.Err = e.request(e.record, method, route)
		if r.Err == nil {
			e.body = e.response.Body
		}
	case "service-read":
		e.raw, _ = io.ReadAll(io.LimitReader(e.body, 64*1024))
		r.Count = e.response.StatusCode
	case "service-decode":
		var s struct {
			Elevated  bool `json:"elevated"`
			Connected bool `json:"connected"`
		}
		_ = json.Unmarshal(e.raw, &s)
		e.existingElevated, e.existingConnected = s.Elevated, s.Connected
	case "service-facts":
		r.Elevated = e.existingElevated
		r.Connected = e.existingConnected
		r.Value = e.record.ExeSize
		r.OtherValue = e.record.ExeMtime
	case "body-close":
		e.closeBody()
	case "executable":
		e.executable, r.Err = os.Executable()
		e.size, e.mtime = 0, 0
	case "executable-stat":
		var info os.FileInfo
		info, r.Err = os.Stat(e.executable)
		if r.Err == nil {
			e.size, e.mtime = info.Size(), info.ModTime().UnixNano()
			r.Value, r.OtherValue = e.size, e.mtime
		}
	case "deadline":
		e.deadline = e.clock().Add(q.Duration)
	case "before":
		r.Flag = e.clock().Before(e.deadline)
	case "sleep":
		e.sleep(q.Duration)
	case "lock-close":
		if f := e.lockClose; f != nil {
			e.lockClose = nil
			f()
		}
	case "lock-release":
		e.releaseLock()
	case "identify-client":
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.Proxy = nil
		tr.DisableKeepAlives = true
		e.idleClose = tr.CloseIdleConnections
		e.client = &http.Client{Timeout: 2 * time.Second, Transport: tr}
	case "identify-request":
		if e.identifyDo != nil {
			e.response, r.Err = e.identifyDo(e.address)
		} else {
			e.response, r.Err = e.client.Get("http://" + e.address + "/api/identify")
		}
		if r.Err == nil {
			e.body = e.response.Body
		}
	case "response-status":
		r.Count = e.response.StatusCode
	case "identify-decode":
		var v struct {
			App string `json:"app"`
		}
		r.Err = json.NewDecoder(io.LimitReader(e.body, 512)).Decode(&v)
		r.Flag = v.App == appName
	case "idle-close":
		e.closeIdle()
	case "bundle-present":
		r.Flag = e.bundleDir != ""
	case "portable-stat":
		_, r.Err = os.Stat(filepath.Join(e.bundleDir, "portable.txt"))
	case "portable-mkdir":
		r.Err = os.MkdirAll(filepath.Join(e.bundleDir, "SmartVPN-data"), 0700)
	case "portable-write":
		r.Err = e.writeFile(filepath.Join(e.bundleDir, "SmartVPN-data", ".write-probe"), []byte("ok"), 0600)
	case "portable-remove":
		r.Err = e.remove(filepath.Join(e.bundleDir, "SmartVPN-data", ".write-probe"))
	case "portable-select":
		e.home = filepath.Join(e.bundleDir, "SmartVPN-data")
		e.portable = true
	case "record-encode":
		e.raw, r.Err = json.Marshal(e.record)
	case "record-temp":
		r.Err = e.writeFile(serviceRecordPath(e.home)+".tmp", e.raw, 0600)
	case "record-rename":
		r.Err = e.rename(serviceRecordPath(e.home)+".tmp", serviceRecordPath(e.home))
	case "record-own":
		r.Flag = e.record.PID == os.Getpid()
	case "record-remove":
		r.Err = e.remove(serviceRecordPath(e.home))
	case "config-home":
		e.base, r.Err = os.UserConfigDir()
	case "bundle-dir":
		if x, err := os.Executable(); err == nil {
			e.bundleDir = filepath.Dir(x)
		}
	case "default-home":
		e.home = filepath.Join(e.base, "SmartVPN")
	case "android-home":
		e.home = filepath.Join(e.base, "SmartVPN")
	case "home-mkdir":
		r.Err = os.MkdirAll(e.home, 0700)
	case "open-log":
		e.redactor = openServiceLog(e.home)
	case "new-service":
		e.a = newService(e.home, e.bundleDir, e.portable, e.elevated)
	case "prepare":
		e.a.prepare(e.redactor)
	case "token":
		e.token = e.a.newToken()
	case "serve":
		e.port, e.server, r.Err = e.a.serveLocalAPI(e.token)
		if r.Err == nil {
			srv := e.server
			e.serverClose = func() error { return drainLocalServer(srv) }
		}
	case "record-build":
		e.record = serviceRecord{PID: os.Getpid(), Port: e.port, Token: e.token, ExeSize: e.size, ExeMtime: e.mtime}
	case "server-close":
		e.closeServer()
	case "cleanup":
		e.a.shutdownAndCleanup()
	case "app-lock":
		e.a.mu.Lock()
		e.appUnlock = e.a.mu.Unlock
	case "app-unlock":
		if f := e.appUnlock; f != nil {
			e.appUnlock = nil
			f()
		}
	case "drop":
		r.Err = e.a.dropConnectionLocked()
	case "permission":
		e.a.elevated = q.Flag
	case "kernel-running":
		r.Flag = e.a.kernelRunningLocked()
	case "log":
		e.log(q.Count)
	default:
		return e.platform(q)
	}
	if r.Err != nil {
		e.lastErr = r.Err
	}
	return
}
func (e *appBootstrapEffects) log(n int) {
	switch n {
	case 0:
		log.Printf("taking over from the SmartVPN service on port %d", e.record.Port)
	case 1:
		log.Printf("could not publish which copy serves the Windows proxy: %v", e.lastErr)
	case 2:
		log.Printf("this copy carries portable.txt but %s cannot be created (%v); using the profile instead", filepath.Join(e.bundleDir, "SmartVPN-data"), e.lastErr)
	case 3:
		log.Printf("this copy carries portable.txt but %s is not writable (%v); using the profile instead", filepath.Join(e.bundleDir, "SmartVPN-data"), e.lastErr)
	case 4:
		log.Printf("service starting (pid %d, elevated %v, portable %v, profile %s)", os.Getpid(), e.elevated, e.portable, e.home)
	case 5:
		log.Printf("reusing the running SmartVPN service on port %d", e.record.Port)
	case 6:
		log.Printf("could not publish the service address: %v", e.lastErr)
	case 7:
		log.Printf("service listening on 127.0.0.1:%d (pid %d, profile %s)", e.port, os.Getpid(), e.home)
	case 8:
		log.Printf("could not start the service: %v", e.lastErr)
	case 9:
		log.Printf("could not read the tunnel description: %v", e.lastErr)
	case 10:
		log.Printf("could not end the connection after the tunnel went away: %v", e.lastErr)
	}
}
func runBootstrapWithEffects(q bootstrapServiceRequest, e *appBootstrapEffects) (out bootstrapServiceResult) {
	defer func() {
		if p := recover(); p != nil {
			out.Err = fmt.Errorf("bootstrap interrupted: %v", p)
		}
	}()
	defer e.Release()
	return runSelectedBootstrapService(q, e)
}

// A shutdown request itself may still be returning through the local Rust host.
func drainLocalServer(s *http.Server) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		return s.Close()
	}
	return nil
}
