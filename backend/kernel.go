package main

import (
	"fmt"
	"log"
	"net"
	"runtime"
	"strconv"
)

// kernelRunner is a kernel this service drives through its control API.
//
// Everything above it is the same on every platform: the same configuration,
// the same readiness poll, the same health state machine. What differs is only
// where the kernel comes from — Windows runs one as a separate process, because
// the kernel needs an administrator token and a TUN adapter that the window
// must not have to hold; Android links it in, because an app cannot start a
// process of its own outside its native library directory and has no need to:
// the platform's VpnService already owns the tunnel the kernel writes to.
type kernelRunner interface {
	// Running reports whether the kernel is up now. It is consulted before
	// every start, so a second connect does not start a second kernel.
	Running() bool
	// Start brings the kernel up with the configuration already written to
	// disk. It returns once the process or the engine exists, not once it is
	// ready: readiness is the same poll on every platform.
	Start() error
	// Stop ends it. It is safe to call when it is not running.
	Stop() error
}

// kernelRunningLocked reports whether the kernel is up. The caller holds the lock.
func (a *app) kernelRunningLocked() bool {
	return a.kernel != nil && a.kernel.Running()
}

// startMihomoLocked brings the kernel up with the configuration the current
// settings describe.
func (a *app) startMihomoLocked() error {
	return a.runKernelService(kernelServiceRequest{Op: "start"}).Err
}

func (a *app) kernelConfigOptions(profile []byte, hasSubscription bool) (configOptions, error) {
	return a.runChinaOptions(profile, hasSubscription)
}

func proxyListenPort(saved int) (int, error) {
	if runtime.GOOS != "windows" || saved == 0 {
		return freeLoopbackPort()
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(saved))
	listener, err := net.Listen("tcp4", addr)
	if err != nil {
		return 0, fmt.Errorf("无法监听 SOCKS5 端口 %s，请检查端口是否被占用或更换端口：%w", addr, err)
	}
	return saved, listener.Close()
}

// stopMihomoLocked ends the kernel and leaves nothing of the configuration in
// place, so a later run cannot mistake a stale file for a live one.
func (a *app) stopMihomoLocked() error {
	return a.runKernelService(kernelServiceRequest{Op: "stop"}).Err
}

// kernelExited is the kernel reporting that it is gone by itself: it crashed,
// or it refused the configuration. It is not the same as a disconnect — the
// user did not ask for this — so the proxy setting this run turned on has to be
// put back, or the machine is left pointing at a port nothing listens on.
//
// which names the kernel that reported it, because a kernel replaced in the
// meantime has already been accounted for and must not be cleaned up twice.
func (a *app) kernelExited(which kernelRunner) {
	if result := a.runExitService(which); result.Err != nil {
		log.Printf("kernel exit cleanup incomplete: %v", result.Err)
	}
}
