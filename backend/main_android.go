//go:build android

package main

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
)

// An Android process is started by the system, not by this service, and the
// window that talks to it runs in the same process: there is no second program
// to start and no standard output to announce a port on. The entry points are
// the exported functions in android_bridge.go, which the Java side calls.
func main() {}

// registerPlatformRoutes adds nothing: every route the local API serves is
// shared, and the one Windows adds installs a Windows driver library.
func registerPlatformRoutes(*http.ServeMux, string, *app) {}

// mihomoPath is empty, and says so rather than guessing: the kernel is not a
// file this app runs but a library inside it. The state route reports this
// field, and an empty value is the truth — there is no path to check.
func (a *app) mihomoPath() string { return "" }

// adoptBundledFiles has nothing to adopt: the kernel and the tunnel come with
// the application package, and there is no folder beside an executable to look
// in.
func (a *app) adoptBundledFiles(string) {}

// The one service this process serves. An Android app is one process under one
// user id, so there is no second copy of it to arbitrate with — the system
// refuses a second VpnService from the same app outright.
var (
	serviceMu    sync.Mutex
	service      *app
	serviceToken string
	serviceAPI   *http.Server
)

// startAndroidService prepares a profile and starts the local API on it. It is
// called once, from Java, as the application comes up.
func startAndroidService(dataDir string) (bootstrap, error) {
	home := filepath.Join(dataDir, "SmartVPN")
	if err := os.MkdirAll(home, 0700); err != nil {
		return bootstrap{}, err
	}
	redactor := openServiceLog(home)
	// The tunnel's permission is false to begin with, because on this platform
	// what the tunnel needs is the user's authorisation of the VpnService, and
	// that is granted after the service is up.
	a := newService(home, "", false, false)
	a.prepare(redactor)
	token := a.newToken()
	port, server, err := a.serveLocalAPI(token)
	if err != nil {
		return bootstrap{}, err
	}
	service, serviceToken, serviceAPI = a, token, server
	// The same record the Windows service publishes, for the same reason: the
	// address of a running service belongs somewhere a later look can find it.
	// Here the reader is a person with adb — the profile is private to the app,
	// so the token in it is no more exposed than the one the window holds.
	if err := writeServiceRecord(home, serviceRecord{PID: os.Getpid(), Port: port, Token: token}); err != nil {
		log.Printf("could not publish the service address: %v", err)
	}
	log.Printf("service listening on 127.0.0.1:%d (pid %d, profile %s)", port, os.Getpid(), home)
	return bootstrap{Port: port, Token: token}, nil
}

// stopAndroidService ends the connection as well as the service. The
// connection is dropped first and explicitly: the process may outlive the
// service — the window keeps running — and a patrol left behind would keep
// probing a kernel that is no longer there.
func stopAndroidService() {
	serviceMu.Lock()
	a, server := service, serviceAPI
	service, serviceToken, serviceAPI = nil, "", nil
	serviceMu.Unlock()
	if a == nil {
		return
	}
	a.mu.Lock()
	_ = a.dropConnectionLocked()
	a.mu.Unlock()
	a.shutdownAndCleanup()
	if server != nil {
		_ = server.Close()
	}
	removeServiceRecord(a.home, os.Getpid())
}
