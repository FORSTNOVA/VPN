//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"golang.org/x/sys/windows/registry"
)

// A machine has exactly one system proxy setting, and the copy of this app that
// set it is the copy that may take it away. The address in that setting is the
// kernel's proxy port, which this service does not serve — it is mihomo's — so
// the setting alone cannot say which copy is behind it.
//
// The answer is therefore published beside the setting, in the registry the
// setting already lives in, which is the one place every copy of this app can
// read no matter where it was unpacked. The record names the profile directory
// that owns the setting and the ports that copy is listening on. It is written
// when the proxy is turned on and removed when the setting is handed back, so a
// record that is there describes a setting that is in use.
const (
	// internetSettingsKey is where Windows keeps the system proxy, and where the
	// helper script writes it.
	internetSettingsKey = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`
	proxyOwnerKeyName   = `Software\SmartVPN`
	proxyOwnerValueName = "ProxyOwner"
)

// proxyOwnerKeyPath is a variable so a test can round-trip the record somewhere
// other than where a copy that is running would read it.
var proxyOwnerKeyPath = proxyOwnerKeyName

func encodeProxyOwner(owner proxyOwner) (string, error) {
	body, err := json.Marshal(owner)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func decodeProxyOwner(text string) (proxyOwner, bool) {
	var owner proxyOwner
	if json.Unmarshal([]byte(text), &owner) != nil {
		return proxyOwner{}, false
	}
	if owner.Home == "" || owner.APIPort == 0 || owner.MixedPort == 0 {
		return proxyOwner{}, false
	}
	return owner, true
}

// writeProxyOwner publishes which copy is serving the machine's proxy setting.
func writeProxyOwner(owner proxyOwner) error {
	text, err := encodeProxyOwner(owner)
	if err != nil {
		return err
	}
	key, _, err := registry.CreateKey(registry.CURRENT_USER, proxyOwnerKeyPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	return key.SetStringValue(proxyOwnerValueName, text)
}

// readProxyOwner reports the copy that last claimed the setting.
func readProxyOwner() (proxyOwner, bool) {
	key, err := registry.OpenKey(registry.CURRENT_USER, proxyOwnerKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return proxyOwner{}, false
	}
	defer key.Close()
	text, _, err := key.GetStringValue(proxyOwnerValueName)
	if err != nil {
		return proxyOwner{}, false
	}
	return decodeProxyOwner(text)
}

// clearProxyOwner removes the record only while it still names this profile
// directory, so a copy that is handing its own setting back cannot erase another
// copy's claim to the setting it is still serving.
func clearProxyOwner(home string) {
	owner, ok := readProxyOwner()
	if ok && owner.Home != home {
		return
	}
	key, err := registry.OpenKey(registry.CURRENT_USER, proxyOwnerKeyPath, registry.SET_VALUE)
	if err != nil {
		return
	}
	defer key.Close()
	_ = key.DeleteValue(proxyOwnerValueName)
}

// readWindowsProxyState reads the system proxy setting as Windows holds it:
// whether it is on, and the address it points at. It is read here rather than
// through the helper script because the guard needs the raw values — the script
// answers "on *and* pointing at the address you gave me", and a copy that has
// not chosen a port yet has no address to give. Reading the registry directly
// also removes a process spawn from a value the interface polls every fifteen
// seconds.
func readWindowsProxyState() (bool, string, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, internetSettingsKey, registry.QUERY_VALUE)
	if err != nil {
		return false, "", err
	}
	defer key.Close()
	enabled := false
	if value, _, err := key.GetIntegerValue("ProxyEnable"); err == nil {
		enabled = value == 1
	} else if !errors.Is(err, registry.ErrNotExist) {
		return false, "", err
	}
	server, _, err := key.GetStringValue("ProxyServer")
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		return false, "", err
	}
	return enabled, server, nil
}

// identifyTimeout bounds the ownership probe. It is a request to a port on this
// machine, so a second is already generous.
const identifyTimeout = 2 * time.Second

// ownerRecord is the published record, or the fixture the tests put in its
// place: reading the real one would consult this machine's own registry.
func (a *app) ownerRecord() (proxyOwner, bool) {
	if a.proxyOwnerHook != nil {
		return a.proxyOwnerHook()
	}
	return readProxyOwner()
}

// proxyServedByAnotherInstance reports whether the machine's proxy setting is
// being served right now by a SmartVPN from another profile directory.
//
// A second copy — another profile directory, a portable copy beside an installed
// one — that took the setting over would leave the copy already using it
// reporting that something else had changed its proxy. Refusing is the honest
// answer: there is one setting, and the copy serving it already owns it.
//
// Nothing answering at the recorded port is not another instance. That is a
// setting left behind by a run that is gone, including this app's own previous
// run, which is exactly what the startup restore exists to clean up.
func (a *app) proxyServedByAnotherInstance() bool {
	enabled, server, err := a.proxyState()
	if err != nil || !enabled || server == "" {
		return false
	}
	owner, ok := a.ownerRecord()
	if !ok || owner.Home == a.home {
		return false
	}
	// The setting has to still point where the record says it was pointed, or it
	// has been changed since and the record describes nothing.
	if server != net.JoinHostPort("127.0.0.1", strconv.Itoa(owner.MixedPort)) {
		return false
	}
	return smartVPNIsListeningAt(net.JoinHostPort("127.0.0.1", strconv.Itoa(owner.APIPort)))
}

// smartVPNIsListeningAt asks an address on this machine to name itself. A port
// nothing listens on, and a server that answers something else, both mean the
// setting is not another copy's and may be taken over — which is what happens
// with another proxy client's setting, restored afterwards as always.
func smartVPNIsListeningAt(address string) bool {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// The probe is to a loopback port; an application-level proxy would send it
	// somewhere else entirely.
	transport.Proxy = nil
	transport.DisableKeepAlives = true
	defer transport.CloseIdleConnections()
	client := http.Client{Timeout: identifyTimeout, Transport: transport}
	response, err := client.Get("http://" + address + "/api/identify")
	if err != nil {
		return false
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return false
	}
	var identity struct {
		App string `json:"app"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 512)).Decode(&identity) != nil {
		return false
	}
	return identity.App == appName
}
