//go:build windows

package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
)

// registerPlatformRoutes adds the routes only this platform can serve: the one
// that installs wintun, which is a Windows driver library and has no
// counterpart anywhere else.
func registerPlatformRoutes(mux *http.ServeMux, token string, a *app) {
	mux.HandleFunc("POST /api/tun/wintun", a.authorize(token, a.installWintun))
}

func main() {
	base, err := os.UserConfigDir()
	if err != nil {
		log.Fatal(err)
	}
	// A copy that carries the portable marker keeps everything it writes beside
	// the executable instead of in the user's profile.
	bundleDir := ""
	if executable, err := os.Executable(); err == nil {
		bundleDir = filepath.Dir(executable)
	}
	home := filepath.Join(base, "SmartVPN")
	portable := false
	if resolved, ok := portableHome(bundleDir); ok {
		home, portable = resolved, true
	}
	if err := os.MkdirAll(home, 0700); err != nil {
		log.Fatal(err)
	}
	// The service's own account of what it did is the only place a refused
	// failover, an unmeasurable health sweep or a failed proxy restore shows up:
	// the app pipes this process's output away, and mihomo.log holds the kernel's
	// log, not ours.
	redactor := openServiceLog(home)
	elevated := isElevated()
	log.Printf("service starting (pid %d, elevated %v, portable %v, profile %s)",
		os.Getpid(), elevated, portable, home)

	// An earlier launch may still be serving, because the kernel and the proxy
	// are meant to outlive the window. Deciding that is serialised against other
	// launches, so two windows opened at the same moment cannot both start one.
	lock, existing, err := joinService(home, elevated, serviceLockWait, serviceShutdownWait)
	if err != nil {
		_ = json.NewEncoder(os.Stdout).Encode(bootstrap{Error: err.Error()})
		log.Fatalf("%v", err)
	}
	if existing.Port > 0 {
		_ = json.NewEncoder(os.Stdout).Encode(bootstrap{Port: existing.Port, Token: existing.Token})
		log.Printf("reusing the running SmartVPN service on port %d", existing.Port)
		return
	}
	// Held until the address below is published, and released either way if this
	// launch gives up first: a lock left behind would outlive the process and
	// keep every later launch waiting.
	defer lock.Release()

	a := newService(home, bundleDir, portable, elevated)
	a.prepare(redactor)
	token := a.newToken()
	port, server, err := a.serveLocalAPI(token)
	if err != nil {
		log.Fatalf("%v", err)
	}
	// Publish the address so a later launch can reuse this service, and the
	// binary's identity so a relaunch after a rebuild can tell it is out of date.
	// This comes before the window is told, and the startup lock is given back
	// only once it is on disk, so a launch starting in the meantime waits and
	// then finds a service rather than starting one of its own.
	size, mtime := selfIdentity()
	if err := writeServiceRecord(home, serviceRecord{
		PID: os.Getpid(), Port: port, Token: token,
		ExeSize: size, ExeMtime: mtime,
	}); err != nil {
		log.Printf("could not publish the service address: %v", err)
	}
	lock.Release()
	_ = json.NewEncoder(os.Stdout).Encode(bootstrap{Port: port, Token: token})

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt)
	select {
	case <-signals:
	case <-a.shutdown:
	}
	_ = server.Close()
	a.shutdownAndCleanup()
	removeServiceRecord(home, os.Getpid())
}
