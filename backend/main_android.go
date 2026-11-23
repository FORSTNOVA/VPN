//go:build android

package main

import (
	"log"
	"net/http"
	"sync"
)

// An Android process is started by the system, not by this service, and the
// window that talks to it runs in the same process: there is no second program
// to start and no standard output to announce a port on. The entry points are
// the exported functions in android_bridge.go, which the Java side calls.
func main() {}

func (a *app) platformAPIRoutes(routes []apiRoute) []apiRoute { return routes }

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
	e := newBootstrapEffects(bootstrapServiceRequest{Op: "android-start"})
	e.base = dataDir
	r := runBootstrapWithEffects(e.q, e)
	return bootstrap{Port: e.port, Token: e.token}, r.Err
}

// stopAndroidService ends the connection as well as the service. The
// connection is dropped first and explicitly: the process may outlive the
// service — the window keeps running — and a patrol left behind would keep
// probing a kernel that is no longer there.
func stopAndroidService() {
	e := newBootstrapEffects(bootstrapServiceRequest{Op: "android-stop"})
	if r := runBootstrapWithEffects(e.q, e); r.Err != nil {
		log.Printf("service stop incomplete: %v", r.Err)
	}
}
