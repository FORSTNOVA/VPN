//go:build android

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/metacubex/mihomo/common/observable"
	"github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/hub"
	"github.com/metacubex/mihomo/hub/executor"
	mihomolog "github.com/metacubex/mihomo/log"
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
	app *app

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
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.running
}

func (k *embeddedKernel) Start() error {
	home := k.app.home
	// The kernel resolves the paths in the configuration — the two provider
	// files and the domestic address list — relative to its working directory,
	// which is how the Windows copy starts it in the profile. Here the process
	// is the app's, so the working directory has to be moved instead.
	if err := os.Chdir(home); err != nil {
		return fmt.Errorf("无法切换到配置目录：%w", err)
	}
	// The kernel keeps its own files — the selected-node cache, any geodata —
	// under this directory, and reads its own configuration from it.
	constant.SetHomeDir(home)
	if err := os.MkdirAll(home, 0700); err != nil {
		return fmt.Errorf("无法创建配置目录：%w", err)
	}
	body, err := os.ReadFile(k.app.configPath())
	if err != nil {
		return errors.New("could not read Mihomo configuration")
	}
	k.startLogging()
	// hub.Parse is what the kernel's own command line does with a configuration:
	// it parses it, applies it, and brings up the control interface the
	// configuration names. A failure here is the configuration being refused,
	// which is why the reason is passed on rather than replaced.
	if err := hub.Parse(body); err != nil {
		k.stopLogging()
		return fmt.Errorf("Mihomo 拒绝了这份配置：%w", err)
	}
	if err := k.tunnelRefused(); err != nil {
		// The kernel is up but cannot use the tunnel. It is taken down here
		// rather than left running: a kernel with a control port and no tunnel
		// is a connection the window would eventually report as working.
		executor.Shutdown()
		k.stopLogging()
		return err
	}
	// The kernel has the tunnel now, and with it the descriptor: it closes it
	// when it stops, and this side must not.
	tunnel.handOver()
	k.mu.Lock()
	k.running = true
	k.mu.Unlock()
	return nil
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
	time.Sleep(200 * time.Millisecond)
	if k.tunError == "" {
		return nil
	}
	return fmt.Errorf("内核没能接管隧道：%s。"+
		"为避免全部应用的流量被送进一条没人处理的隧道，本次连接已终止", k.tunError)
}

func (k *embeddedKernel) Stop() error {
	executor.Shutdown()
	k.stopLogging()
	k.mu.Lock()
	k.running = false
	k.mu.Unlock()
	return nil
}

// checkKernelConfig parses a configuration with the kernel that is linked in
// and reports whether it is accepted. It is the same question the Windows copy
// asks by running the kernel's own preflight, answered without a process: the
// parser is the kernel's.
func (a *app) checkKernelConfig(configPath string) error {
	body, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	if _, err := executor.ParseWithBytes(body); err != nil {
		return err
	}
	return nil
}

// startLogging puts the kernel's lines into the profile's kernel log, through
// the same redactor the service's own log uses: a subscription URL that reaches
// a kernel message is stripped there too.
func (k *embeddedKernel) startLogging() {
	path := filepath.Join(k.app.home, "mihomo.log")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		// Without a file the kernel still logs to the system log, which is where
		// an Android app's output goes; this is the copy that stays with the
		// profile, and not having it is not a reason to refuse to connect.
		return
	}
	redactor := newLogRedactor(file)
	redactor.Set(k.app.subscriptionSecrets())
	sub := mihomolog.Subscribe()
	k.mu.Lock()
	k.logFile, k.logSub = file, sub
	// A failure from an earlier start is not this one's.
	k.tunError = ""
	k.mu.Unlock()
	go func() {
		for event := range sub {
			if strings.Contains(event.Payload, "TUN listening error") {
				k.mu.Lock()
				if k.tunError == "" {
					k.tunError = event.Payload
				}
				k.mu.Unlock()
			}
			_, _ = redactor.Write([]byte(fmt.Sprintf("time=\"%s\" level=%s msg=%q\n",
				time.Now().Format(time.RFC3339), event.LogLevel.String(), event.Payload)))
		}
	}()
}

func (k *embeddedKernel) stopLogging() {
	k.mu.Lock()
	file, sub := k.logFile, k.logSub
	k.logFile, k.logSub = nil, nil
	k.mu.Unlock()
	if sub != nil {
		mihomolog.UnSubscribe(sub)
	}
	if file != nil {
		_ = file.Close()
	}
}
