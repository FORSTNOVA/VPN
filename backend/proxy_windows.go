//go:build windows

package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// setSystemProxy turns the machine's own proxy setting on or off, through the
// helper script that owns the backup and restore of what was there before.
func (a *app) setSystemProxy(enable bool) error {
	action := "restore"
	if enable {
		action = "enable"
	}
	script := filepath.Join(filepath.Dir(os.Args[0]), "proxy.ps1")
	if _, err := os.Stat(script); err != nil {
		script = filepath.Join(a.home, "proxy.ps1")
	}
	proxyPort := a.mixedPort
	if proxyPort == 0 {
		proxyPort = 7890 // Restore backups made by earlier releases.
	}
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", script, "-Action", action, "-BackupPath", filepath.Join(a.home, "proxy-backup.json"), "-ProxyAddress", net.JoinHostPort("127.0.0.1", strconv.Itoa(proxyPort)))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("system proxy update failed: %s", strings.TrimSpace(string(out)))
	}
	// Which copy serves the machine's one proxy setting is recorded beside the
	// setting itself, so another copy can tell that this one is using it. The
	// record names the port the setting actually points at, which is the kernel's.
	if enable {
		if err := writeProxyOwner(proxyOwner{
			Home: a.home, APIPort: a.apiPort, MixedPort: proxyPort,
		}); err != nil {
			log.Printf("could not publish which copy serves the Windows proxy: %v", err)
		}
	} else {
		clearProxyOwner(a.home)
	}
	return nil
}

// restoreProxy puts the setting back, and only bothers when this run is what
// turned it on: a backup left behind by a run that crashed is recovered at
// startup instead, and touching it twice would restore the wrong thing.
func (a *app) restoreProxy() {
	if a.proxyOn {
		_ = a.setSystemProxy(false)
		a.proxyOn = false
	}
}
