//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"golang.org/x/sys/windows"
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
	path, err := os.Executable()
	if err != nil {
		return 0, 0
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0, 0
	}
	return info.Size(), info.ModTime().UnixNano()
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
	// A rebuilt binary has to take effect, otherwise the new build would be
	// silently served by the old code. A live session is never dropped for it.
	if existing.stale() && !existing.Connected {
		return true
	}
	// Otherwise keep the privilege the running service has and we lack: an
	// unnecessary restart would silently disable TUN.
	if existing.Elevated && !elevated {
		return false
	}
	return elevated && !existing.Elevated
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
	if record.Port <= 0 || record.Token == "" {
		return runningServiceInfo{}, false
	}
	response, err := serviceRequest(record, http.MethodGet, "/api/state")
	if err != nil {
		return runningServiceInfo{}, false
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 64*1024))
	if response.StatusCode != http.StatusOK {
		return runningServiceInfo{}, false
	}
	var state struct {
		Elevated  bool `json:"elevated"`
		Connected bool `json:"connected"`
	}
	_ = json.Unmarshal(body, &state)
	return runningServiceInfo{
		Record:    record,
		Elevated:  state.Elevated,
		Connected: state.Connected,
	}, true
}

// runningService reads the published record and checks that it is live.
func runningService(home string) (runningServiceInfo, bool) {
	body, err := os.ReadFile(serviceRecordPath(home))
	if err != nil {
		return runningServiceInfo{}, false
	}
	var record serviceRecord
	if json.Unmarshal(body, &record) != nil {
		return runningServiceInfo{}, false
	}
	return probeService(record)
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
	// The usual case needs no lock: a service that is already serving publishes
	// its address atomically, so it can simply be asked.
	if existing, alive := runningService(home); alive && !shouldTakeOver(existing, elevated) {
		return nil, existing.Record, nil
	}

	lock, err := lockService(home, lockWait)
	if err != nil {
		return nil, serviceRecord{}, err
	}
	// The launch that held the lock may have published a service while we
	// waited, which is the whole point of having taken it.
	existing, alive := runningService(home)
	if !alive {
		return lock, serviceRecord{}, nil
	}
	if !shouldTakeOver(existing, elevated) {
		lock.Release()
		return nil, existing.Record, nil
	}
	log.Printf("taking over from the SmartVPN service on port %d", existing.Record.Port)
	if !handOverTo(existing, shutdownWait) {
		lock.Release()
		return nil, serviceRecord{}, fmt.Errorf(
			"the SmartVPN service on port %d did not shut down; stop it and try again",
			existing.Record.Port)
	}
	return lock, serviceRecord{}, nil
}

// handOverTo asks the recorded service to shut down and waits for it to stop
// answering, which is what makes room for an instance that holds a higher
// privilege than the running one. It reports whether the service went away: a
// service that keeps answering is one this launch must not displace, and the
// caller turns that into a refusal rather than into a second service.
func handOverTo(info runningServiceInfo, wait time.Duration) bool {
	if response, err := serviceRequest(info.Record, http.MethodPost, "/api/shutdown"); err == nil {
		_ = response.Body.Close()
	}
	deadline := time.Now().Add(wait)
	for {
		if _, alive := probeService(info.Record); !alive {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
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
	path := serviceLockPath(home)
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(name,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil, windows.OPEN_ALWAYS, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, fmt.Errorf("could not open %s: %w", path, err)
	}
	deadline := time.Now().Add(wait)
	for {
		// The first byte stands for the whole lock, and asking not to wait is
		// what makes the deadline below possible.
		err := windows.LockFileEx(handle,
			lockfileExclusiveLock|lockfileFailImmediately, 0, 1, 0, &windows.Overlapped{})
		if err == nil {
			return &serviceLock{handle: handle}, nil
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			_ = windows.CloseHandle(handle)
			return nil, fmt.Errorf("could not lock %s: %w", path, err)
		}
		if !time.Now().Before(deadline) {
			_ = windows.CloseHandle(handle)
			return nil, fmt.Errorf("another SmartVPN service is starting up and holds %s", path)
		}
		time.Sleep(50 * time.Millisecond)
	}
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
