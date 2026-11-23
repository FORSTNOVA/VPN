//go:build windows

package main

import (
	"golang.org/x/sys/windows"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// window constants for the startup lock. x/sys exposes LockFileEx but not its
// flags.
const (
	lockfileFailImmediately = 0x00000001
	lockfileExclusiveLock   = 0x00000002
)

const (
	// serviceLockWait is how long a launch waits for another launch to finish
	// starting before it gives up. It is only ever spent when two windows are
	// opened at the same moment, because a service that is already serving is
	// found without the lock.
	serviceLockWait = 5 * time.Second
	// serviceShutdownWait is how long a relaunch waits for the service it
	// outranks to disconnect, stop the kernel and hand the proxy settings back.
	serviceShutdownWait = 10 * time.Second
)

type runningServiceInfo struct {
	Record    serviceRecord
	Elevated  bool
	Connected bool
}

// selfIdentity describes the executable on disk, which is the same path for
// every instance because they are launched from the app directory.
func selfIdentity() (int64, int64) {
	e := newBootstrapEffects(bootstrapServiceRequest{Op: "identity"})
	r := runBootstrapWithEffects(e.q, e)
	if r.Err != nil {
		return 0, 0
	}
	return e.size, e.mtime
}

// stale reports whether the service binary was replaced after the recorded
// instance started.
func (info runningServiceInfo) stale() bool {
	if info.Record.ExeSize == 0 {
		return false
	}
	size, mtime := selfIdentity()
	if size == 0 {
		return false
	}
	return size != info.Record.ExeSize || mtime != info.Record.ExeMtime
}

// shouldTakeOver decides whether a new instance asks the running service to
// stop instead of reusing it.
func shouldTakeOver(existing runningServiceInfo, elevated bool) bool {
	return serviceTakeoverDecision(existing.stale(), existing.Connected, existing.Elevated, elevated)
}

func serviceRequest(record serviceRecord, method, route string) (*http.Response, error) {
	request, err := http.NewRequest(method,
		"http://127.0.0.1:"+strconv.Itoa(record.Port)+route, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+record.Token)
	client := http.Client{Timeout: 3 * time.Second}
	return client.Do(request)
}

// probeService reports whether the recorded service is still answering. The
// token has to be accepted, so an unrelated process that happens to hold the
// port cannot be mistaken for ours, and a record left behind by a crash simply
// fails the probe.
func probeService(record serviceRecord) (runningServiceInfo, bool) {
	e := newBootstrapEffects(bootstrapServiceRequest{Op: "probe"})
	e.record = record
	r := runBootstrapWithEffects(e.q, e)
	if r.Err != nil || !r.Bool {
		return runningServiceInfo{}, false
	}
	return runningServiceInfo{Record: record, Elevated: e.existingElevated, Connected: e.existingConnected}, true
}

// runningService reads the published record and checks that it is live.
func runningService(home string) (runningServiceInfo, bool) {
	e := newBootstrapEffects(bootstrapServiceRequest{Op: "running"})
	e.home = home
	r := runBootstrapWithEffects(e.q, e)
	if r.Err != nil || !r.Bool {
		return runningServiceInfo{}, false
	}
	return runningServiceInfo{Record: e.record, Elevated: e.existingElevated, Connected: e.existingConnected}, true
}

// removeServiceRecord deletes the record only while it still describes this
// process, so a handing-over instance cannot delete the live service's address.

// adoptBundledFiles copies the kernel and the TUN library out of the folder the
// app was unpacked into. A portable package carries them beside the executable,
// but the service keeps everything it runs in one directory, so without this the
// recipient would have to move both files by hand before anything worked. A file
// that is already there is never replaced.
func (a *app) adoptBundledFiles(sourceDir string) {
	if sourceDir == "" {
		return
	}
	for _, name := range []string{"mihomo.exe", "wintun.dll"} {
		target := filepath.Join(a.home, name)
		if _, err := os.Stat(target); err == nil {
			continue
		}
		source := filepath.Join(sourceDir, name)
		if _, err := os.Stat(source); err != nil {
			continue
		}
		body, err := os.ReadFile(source)
		if err != nil {
			log.Printf("could not read the %s that came with the package: %v", name, err)
			continue
		}
		if err := os.WriteFile(target, body, 0700); err != nil {
			log.Printf("could not place the %s that came with the package: %v", name, err)
			continue
		}
		log.Printf("adopted the %s that came with the package", name)
	}
}

// joinService decides what this launch should do about an earlier service, and
// serialises that decision against every other launch.
//
// A record with a port means an earlier service is to be reused: the caller
// prints it and exits. The empty record means this launch is to start one, and
// the returned lock must then be held until the caller has published its own
// record — a second launch arriving in the meantime waits for the lock and
// then finds the record rather than starting a rival. Two services would share
// one database, and the newcomer's recovery of the proxy settings would
// disable the proxy of the one already connected.
func joinService(home string, elevated bool, lockWait, shutdownWait time.Duration) (*serviceLock, serviceRecord, error) {
	e := newBootstrapEffects(bootstrapServiceRequest{Op: "join", Elevated: elevated, Wait: lockWait, ShutdownWait: shutdownWait})
	e.home = home
	r := runBootstrapWithEffects(e.q, e)
	if r.Err != nil {
		return nil, serviceRecord{}, r.Err
	}
	if r.Reuse {
		return nil, e.record, nil
	}
	return e.win().lock, serviceRecord{}, nil
}

// handOverTo asks the recorded service to shut down and waits for it to stop
// answering, which is what makes room for an instance that holds a higher
// privilege than the running one. It reports whether the service went away: a
// service that keeps answering is one this launch must not displace, and the
// caller turns that into a refusal rather than into a second service.
func handOverTo(info runningServiceInfo, wait time.Duration) bool {
	e := newBootstrapEffects(bootstrapServiceRequest{Op: "handover", ShutdownWait: wait})
	e.record = info.Record
	r := runBootstrapWithEffects(e.q, e)
	return r.Err == nil && r.Bool
}

// serviceLock is the exclusive hold a launch keeps on the window in which it
// looks for an earlier service and publishes its own.
type serviceLock struct {
	handle windows.Handle
	once   sync.Once
}

// serviceLockPath names the lock file, which is deliberately not the record:
// the record is rewritten by every launch and read by anyone, while this file
// exists only to be held. It is left on disk after the lock is given back,
// because removing it would let two launches lock two different files.
func serviceLockPath(home string) string {
	return filepath.Join(home, "service.lock")
}

// lockService takes the lock, waiting up to wait for whoever holds it. The lock
// is a file lock rather than a marker file so that Windows gives it back when
// the handle is closed or the process dies: a launch that crashes or is killed
// cannot leave the next one waiting forever.
func lockService(home string, wait time.Duration) (*serviceLock, error) {
	e := newBootstrapEffects(bootstrapServiceRequest{Op: "lock", Wait: wait})
	e.home = home
	r := runBootstrapWithEffects(e.q, e)
	return e.win().lock, r.Err
}

// Release gives the lock back. It may be called more than once, so a caller can
// both defer it as a safety net and release it as soon as it has published.
func (lock *serviceLock) Release() {
	if lock == nil {
		return
	}
	lock.once.Do(func() {
		_ = windows.UnlockFileEx(lock.handle, 0, 1, 0, &windows.Overlapped{})
		_ = windows.CloseHandle(lock.handle)
	})
}
