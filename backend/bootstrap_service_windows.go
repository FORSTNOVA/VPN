//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
)

type bootstrapWindowsResources struct {
	handle windows.Handle
	lock   *serviceLock
	key    registry.Key
}

func (e *appBootstrapEffects) win() *bootstrapWindowsResources {
	if e.platformData == nil {
		e.platformData = &bootstrapWindowsResources{}
	}
	return e.platformData.(*bootstrapWindowsResources)
}
func (e *appBootstrapEffects) platformCall(q bootstrapServiceEffect) (r bootstrapServiceReply) {
	p := e.win()
	switch q.Kind {
	case "lock-open":
		var name *uint16
		name, r.Err = windows.UTF16PtrFromString(serviceLockPath(e.home))
		if r.Err != nil {
			return
		}
		p.handle, r.Err = windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_ALWAYS, windows.FILE_ATTRIBUTE_NORMAL, 0)
		if r.Err != nil {
			r.Err = fmt.Errorf("could not open %s: %w", serviceLockPath(e.home), r.Err)
		} else {
			e.lockClose = func() { _ = windows.CloseHandle(p.handle) }
		}
	case "lock-try":
		r.Err = windows.LockFileEx(p.handle, lockfileExclusiveLock|lockfileFailImmediately, 0, 1, 0, &windows.Overlapped{})
		r.Flag = errors.Is(r.Err, windows.ERROR_LOCK_VIOLATION)
	case "lock-acquired":
		p.lock = &serviceLock{handle: p.handle}
		e.lockRelease = p.lock.Release
	case "proxy-state":
		var enabled bool
		enabled, e.serverText, r.Err = e.a.proxyState()
		r.Flag = enabled
		r.Other = e.serverText != ""
	case "owner-read":
		var ok bool
		if e.a != nil {
			e.owner, ok = e.a.ownerRecord()
		} else {
			e.owner, ok = readProxyOwner()
		}
		r.Flag = ok
		r.Other = e.owner.Home == e.home
	case "owner-address":
		r.Flag = e.serverText == net.JoinHostPort("127.0.0.1", strconv.Itoa(e.owner.MixedPort))
		e.address = net.JoinHostPort("127.0.0.1", strconv.Itoa(e.owner.APIPort))
	case "owner-key":
		p.key, r.Err = registry.OpenKey(registry.CURRENT_USER, proxyOwnerKeyPath, registry.SET_VALUE)
		if r.Err == nil {
			e.closeKey = func() { _ = p.key.Close() }
		}
	case "owner-key-close":
		if f := e.closeKey; f != nil {
			e.closeKey = nil
			f()
		}
	case "owner-delete":
		r.Err = p.key.DeleteValue(proxyOwnerValueName)
	case "proxy-script":
		e.script = filepath.Join(filepath.Dir(os.Args[0]), "proxy.ps1")
		_, r.Err = os.Stat(e.script)
	case "proxy-script-fallback":
		e.script = filepath.Join(e.home, "proxy.ps1")
	case "proxy-port":
		r.Count = e.a.mixedPort
	case "proxy-execute":
		action := "restore"
		if q.Flag {
			action = "enable"
		}
		e.port = q.Count
		cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", e.script, "-Action", action, "-BackupPath", filepath.Join(e.home, "proxy-backup.json"), "-ProxyAddress", net.JoinHostPort("127.0.0.1", strconv.Itoa(q.Count)))
		planner := ""
		if !q.Flag && windowsKernelHostEnabled() {
			planner, _ = managedHostPath()
		}
		cmd.Env = append(os.Environ(), "SMARTVPN_PROXY_PLANNER="+planner)
		e.raw, r.Err = cmd.CombinedOutput()
	case "owner-write":
		r.Err = writeProxyOwner(proxyOwner{Home: e.home, APIPort: e.a.apiPort, MixedPort: e.port})
	case "elevated":
		e.elevated = isElevated()
		r.Flag = e.elevated
	case "announce":
		v := bootstrap{Port: e.port, Token: e.token}
		if q.Count == 1 {
			v = bootstrap{Error: e.lastErr.Error()}
		}
		if q.Count == 2 {
			v = bootstrap{Port: e.record.Port, Token: e.record.Token}
		}
		r.Err = json.NewEncoder(os.Stdout).Encode(v)
	case "signal-wait":
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, os.Interrupt)
		select {
		case <-signals:
		case <-e.a.shutdown:
		}
	case "failure":
		switch q.Count {
		case 1:
			r.Err = fmt.Errorf("could not lock %s: %w", serviceLockPath(e.home), e.lastErr)
		case 2:
			r.Err = fmt.Errorf("another SmartVPN service is starting up and holds %s", serviceLockPath(e.home))
		case 3:
			r.Err = fmt.Errorf("the SmartVPN service on port %d did not shut down; stop it and try again", e.record.Port)
		case 4:
			r.Err = fmt.Errorf("system proxy update failed: %s", strings.TrimSpace(string(e.raw)))
		default:
			r.Err = errors.New("unsupported bootstrap failure")
		}
	default:
		panic("unknown Windows bootstrap effect " + q.Kind)
	}
	if r.Err != nil {
		e.lastErr = r.Err
	}
	return
}
