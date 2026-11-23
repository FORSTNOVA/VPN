//go:build android

package main

import (
	"github.com/metacubex/mihomo/common/observable"
	mihomolog "github.com/metacubex/mihomo/log"
	"os"
	"sync"
)

// embeddedKernel is the kernel running inside this process.
//
// An Android app cannot start a process of its own: since Android 10 an app's
// private directory is not executable, and the only executable files it has are
// the native libraries the installer unpacked for it. The kernel is therefore
// linked into this one, and the tunnel it writes to is the descriptor the
// VpnService established rather than an adapter the kernel created. From the
// outside nothing changes: the kernel still answers on its control port with
// its own secret, and everything above this type talks to it over HTTP exactly
// as it does on Windows.
type embeddedKernel struct {
	app        *app
	monitor    *androidMonitor
	generation uint64

	mu      sync.Mutex
	running bool
	// The kernel's reason, if it gave one, for not being able to use the tunnel
	// it was handed. Set from the log and read once, right after the
	// configuration is applied.
	tunError string
	// The kernel's own log, and the subscription that feeds it. The kernel
	// writes to the process's standard output, which on Android goes to the
	// system log; this is what puts its lines in the profile instead, where the
	// rest of the diagnostics are and where a bug report can pick them up.
	logFile *os.File
	logSub  observable.Subscription[mihomolog.Event]
}

func (a *app) newKernel() (kernelRunner, error) {
	return &embeddedKernel{app: a}, nil
}

func (k *embeddedKernel) Running() bool {
	q := embeddedKernelRequest{Op: "running"}
	r := runEmbeddedKernelOperation(q, &appEmbeddedKernelEffects{k: k, a: k.app, q: q})
	return r.Status == 1
}

func (k *embeddedKernel) Start() error {
	q := embeddedKernelRequest{Op: "session-start"}
	r := runEmbeddedKernelOperation(q, &appEmbeddedKernelEffects{k: k, a: k.app, q: q, home: k.app.home})
	return r.Err
}

// tunnelRefused reports whether the kernel's TUN listener failed to start.
//
// This is worth a check of its own because of what the failure looks like from
// outside: the kernel comes up, answers on its control port and reports a
// working connection, while the interface it was given has nobody reading it.
// Every application's traffic is routed into it and disappears — the device
// loses its network and the window says connected. The kernel only says so in
// its log, and its log is ours to read.
//
// The wait is for delivery, not for the kernel: the line is written while the
// configuration is being applied, and this side reads it from a channel. Two
// hundred milliseconds is far longer than that takes.
func (k *embeddedKernel) tunnelRefused() error {
	q := embeddedKernelRequest{Op: "refusal"}
	r := runEmbeddedKernelOperation(q, &appEmbeddedKernelEffects{k: k, a: k.app, q: q})
	return r.Err
}

func (k *embeddedKernel) Stop() error {
	q := embeddedKernelRequest{Op: "session-stop"}
	r := runEmbeddedKernelOperation(q, &appEmbeddedKernelEffects{k: k, a: k.app, q: q})
	return r.Err
}

// checkKernelConfig parses a configuration with the kernel that is linked in
// and reports whether it is accepted. It is the same question the Windows copy
// asks by running the kernel's own preflight, answered without a process: the
// parser is the kernel's.
func (a *app) checkKernelConfig(configPath string) error {
	q := embeddedKernelRequest{Op: "check"}
	return runEmbeddedKernelOperation(q, &appEmbeddedKernelEffects{a: a, q: q, path: configPath}).Err
}

// startLogging puts the kernel's lines into the profile's kernel log, through
// the same redactor the service's own log uses: a subscription URL that reaches
// a kernel message is stripped there too.
func (k *embeddedKernel) startLogging() {
	q := embeddedKernelRequest{Op: "start-logging"}
	r := runEmbeddedKernelOperation(q, &appEmbeddedKernelEffects{k: k, a: k.app, q: q})
	_ = r
}

func (k *embeddedKernel) stopLogging() {
	q := embeddedKernelRequest{Op: "stop-logging"}
	r := runEmbeddedKernelOperation(q, &appEmbeddedKernelEffects{k: k, a: k.app, q: q})
	_ = r
}

// The CMFA build disables PUT /configs. Keep the VpnService descriptor
// and listeners while applying the parsed configuration in-process.
func (a *app) applyLiveConfig(payload string) error {
	q := embeddedKernelRequest{Op: "session-apply"}
	k, _ := a.kernel.(*embeddedKernel)
	return runEmbeddedKernelOperation(q, &appEmbeddedKernelEffects{k: k, a: a, q: q, payload: payload}).Err
}
