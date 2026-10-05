package main

import (
	"errors"
	"log"
	"os"
	"path/filepath"
	"time"
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
	if a.kernelRunningLocked() {
		return nil
	}
	// Every mode except the system proxy needs a tunnel of its own, and a
	// platform that cannot give it one says so before anything is started.
	if a.connectionModeLocked() != modeSystemProxy {
		if err := a.tunUnavailableLocked(); err != nil {
			return err
		}
	}
	// Nodes come from the subscription, from pasted links, or from both.
	if a.settings.SubscriptionURL == "" && len(a.manualNodes) == 0 {
		return errors.New("请先添加订阅地址，或手动添加至少一个节点")
	}
	if a.settings.SubscriptionURL != "" && a.settings.SubscriptionURL != mergedSubscriptionURL && !a.profileMatchesSubscription() {
		if _, err := a.fetchProfileLocked(); err != nil {
			return err
		}
	}
	if err := a.writeManualProviderLocked(); err != nil {
		return errors.New("could not write the file for the nodes added by hand")
	}
	var err error
	a.mixedPort, err = freeLoopbackPort()
	if err != nil {
		return errors.New("could not allocate a local proxy port")
	}
	a.ctrlPort, err = freeLoopbackPort()
	if err != nil {
		return errors.New("could not allocate a local control port")
	}
	for a.ctrlPort == a.mixedPort {
		a.ctrlPort, err = freeLoopbackPort()
		if err != nil {
			return errors.New("could not allocate a local control port")
		}
	}
	configPath := a.configPath()
	profile, err := os.ReadFile(a.profilePath())
	if err != nil && a.settings.SubscriptionURL != "" {
		return errors.New("could not read the downloaded subscription")
	}
	// Only the cached list is consulted here: a rule-provider file that is
	// missing is a configuration the kernel refuses to start with, and a
	// download has no place in the path the user is waiting on.
	a.chinaDirect = a.chinaIPListStatus(chinaListCached)
	options := configOptions{
		MixedPort:            a.mixedPort,
		ControllerPort:       a.ctrlPort,
		ProviderPath:         filepath.Join("providers", "subscription.yaml"),
		ControllerSecret:     a.ctrlSecret,
		Rules:                subscriptionRules(profile),
		TUN:                  a.connectionModeLocked() != modeSystemProxy,
		SubscriptionProvider: a.settings.SubscriptionURL != "",
		ManualProvider:       len(a.manualNodes) > 0,
		ChinaDirect:          a.chinaDirect.Available && a.chinaRuleSupported(),
	}
	a.chinaDirectActive = options.ChinaDirect
	if options.TUN {
		options.ProxyServerDomains = mergeProxyServerDomains(
			proxyServerDomains(profile), manualServerDomains(a.manualNodes))
	}
	if err := os.WriteFile(configPath, []byte(generatedConfig(options)), 0600); err != nil {
		return errors.New("could not write Mihomo configuration")
	}
	runner, err := a.newKernel()
	if err != nil {
		return err
	}
	if err := runner.Start(); err != nil {
		return err
	}
	a.kernel = runner
	a.started = time.Now()
	if err := a.waitForMihomo(10 * time.Second); err != nil {
		_ = a.stopMihomoLocked()
		return errors.New("Mihomo did not become ready; see the local log")
	}
	if err := a.waitForNodes(20 * time.Second); err != nil {
		_ = a.stopMihomoLocked()
		return errors.New("subscription returned no selectable nodes; check the subscription and network")
	}
	return nil
}

// stopMihomoLocked ends the kernel and leaves nothing of the configuration in
// place, so a later run cannot mistake a stale file for a live one.
func (a *app) stopMihomoLocked() error {
	if a.kernel == nil {
		return nil
	}
	err := a.kernel.Stop()
	a.kernel = nil
	// Nothing is being routed any more, so no configuration is in effect.
	a.chinaDirectActive = false
	a.removeConfigLocked()
	return err
}

// kernelExited is the kernel reporting that it is gone by itself: it crashed,
// or it refused the configuration. It is not the same as a disconnect — the
// user did not ask for this — so the proxy setting this run turned on has to be
// put back, or the machine is left pointing at a port nothing listens on.
//
// which names the kernel that reported it, because a kernel replaced in the
// meantime has already been accounted for and must not be cleaned up twice.
func (a *app) kernelExited(which kernelRunner) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.kernel != which {
		return
	}
	a.kernel = nil
	a.chinaDirectActive = false
	a.removeConfigLocked()
	if a.patrolCancel != nil {
		close(a.patrolCancel)
		a.patrolCancel = nil
	}
	a.blocked = false
	a.blockReason = ""
	if a.proxyOn {
		if err := a.setSystemProxy(false); err != nil {
			log.Printf("could not restore system proxy after Mihomo exit: %v", err)
		}
		a.proxyOn = false
	}
	// Whatever this platform made for the connection has to go with it. On
	// Android that is the tunnel itself, and leaving it up is not a detail: a
	// tunnel nobody reads is where every application's traffic goes to be
	// dropped.
	a.dropTunnel()
}
