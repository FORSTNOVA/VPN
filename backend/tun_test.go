package main

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsFakeAddress(t *testing.T) {
	fake := []string{"198.18.0.5", "198.18.255.254", "198.18.0.1"}
	for _, address := range fake {
		if !isFakeAddress(address) {
			t.Errorf("%q must be recognised as a fake-ip address", address)
		}
	}
	// Only the generated range counts as fake. An IPv6 answer is never a fake
	// address, because the DNS section resolves IPv4 names only.
	real := []string{
		"1.1.1.1", "198.19.0.1", "198.17.255.255", "2001:2::1",
		"2001:db8::1", "", "not-an-ip", "192.168.1.1",
	}
	for _, address := range real {
		if isFakeAddress(address) {
			t.Errorf("%q must not be treated as a fake-ip address", address)
		}
	}
}

func TestGeneratedConfigWithTUN(t *testing.T) {
	config := generatedConfig(configOptions{
		MixedPort: 1000, ControllerPort: 1001, ProviderPath: "p.yaml",
		ControllerSecret: "s", TUN: true,
	})
	required := []string{
		"tun:\n  enable: true\n  stack: gvisor\n  device: SmartVPN",
		"auto-route: true",
		"auto-detect-interface: true",
		"dns-hijack:\n    - any:53",
		"mtu: 1500",
		"dns:\n  enable: true",
		"enhanced-mode: fake-ip",
		"fake-ip-range: 198.18.0.1/16",
		"https://doh.pub/dns-query",
		"default-nameserver:",
	}
	for _, want := range required {
		if !strings.Contains(config, want) {
			t.Errorf("a TUN config must contain %q\n---\n%s", want, config)
		}
	}
	// The fake-ip range in the config must be the one the check compares with.
	if !strings.Contains(config, tunFakeIPv4Range) {
		t.Error("the generated fake-ip range drifted from the one the path check verifies")
	}
	// Handing out AAAA records while the IPv6 default route points at the adapter
	// starts every connection with a request the node cannot carry.
	for _, unwanted := range []string{"ipv6: true", "fake-ip-range6"} {
		if strings.Contains(config, unwanted) {
			t.Errorf("a TUN config must not contain %q\n---\n%s", unwanted, config)
		}
	}
	if !strings.Contains(config, "ipv6: false") {
		t.Errorf("the DNS section must resolve IPv4 names only:\n%s", config)
	}
	if !strings.HasSuffix(config, "  - MATCH,SmartVPN\n") {
		t.Errorf("the catch-all rule must stay last:\n%s", config)
	}
}

func TestGeneratedConfigWithoutTUNOmitsTunSections(t *testing.T) {
	config := generatedConfig(configOptions{
		MixedPort: 1000, ControllerPort: 1001, ProviderPath: "p.yaml", ControllerSecret: "s",
	})
	for _, unwanted := range []string{"tun:", "dns:", "fake-ip", "dns-hijack"} {
		if strings.Contains(config, unwanted) {
			t.Errorf("a system-proxy config must not contain %q\n---\n%s", unwanted, config)
		}
	}
}

func TestParseAdapterReport(t *testing.T) {
	raw := []byte(`{"AdapterPresent":true,"AdapterStatus":"Up","Routes":[` +
		`{"Prefix":"0.0.0.0/0","Alias":"SmartVPN","NextHop":"0.0.0.0"},` +
		`{"Prefix":"::/0","Alias":"SmartVPN","NextHop":"::"},` +
		`{"Prefix":"0.0.0.0/0","Alias":"Ethernet","NextHop":"192.168.1.1"}]}`)
	report, err := parseAdapterReport(raw, "SmartVPN")
	if err != nil {
		t.Fatalf("parseAdapterReport: %v", err)
	}
	if !report.Present || report.Status != "Up" {
		t.Fatalf("unexpected adapter state: %+v", report)
	}
	if !report.IPv4Route || !report.IPv6Route {
		t.Fatalf("both default routes should be recognised: %+v", report)
	}
	if len(report.RouteVia) != 2 {
		t.Fatalf("only the TUN adapter's own routes count, got %v", report.RouteVia)
	}

	// Routes through a different adapter must not count as tunnelled.
	other, err := parseAdapterReport([]byte(`{"AdapterPresent":true,"AdapterStatus":"Up","Routes":[{"Prefix":"0.0.0.0/0","Alias":"Ethernet","NextHop":"192.168.1.1"}]}`), "SmartVPN")
	if err != nil {
		t.Fatal(err)
	}
	if other.IPv4Route || other.IPv6Route {
		t.Fatalf("another adapter's default route must not count: %+v", other)
	}

	if _, err := parseAdapterReport([]byte("not json"), "SmartVPN"); err == nil {
		t.Fatal("a malformed report must surface an error")
	}
}

// fakePE builds the smallest buffer that satisfies the COFF header checks.
func fakePE(machine uint16) []byte {
	body := make([]byte, 0x100)
	copy(body, "MZ")
	offset := uint32(0x80)
	binary.LittleEndian.PutUint32(body[0x3C:], offset)
	copy(body[offset:], "PE\x00\x00")
	binary.LittleEndian.PutUint16(body[offset+4:], machine)
	return body
}

func TestVerifyPEArch(t *testing.T) {
	if err := verifyPEArch(fakePE(0x8664)); err != nil {
		t.Fatalf("an amd64 image must be accepted: %v", err)
	}
	if err := verifyPEArch(fakePE(0x14c)); err == nil {
		t.Fatal("a 32-bit image must be rejected")
	}
	if err := verifyPEArch(fakePE(0xaa64)); err == nil {
		t.Fatal("an ARM64 image must be rejected")
	}
	if err := verifyPEArch([]byte("nope")); err == nil {
		t.Fatal("a truncated file must be rejected")
	}
	noPE := fakePE(0x8664)
	copy(noPE[0x80:], "XX\x00\x00")
	if err := verifyPEArch(noPE); err == nil {
		t.Fatal("a missing PE signature must be rejected")
	}
}

func TestTunUnavailableReasons(t *testing.T) {
	home := t.TempDir()
	plain := &app{home: home}
	if err := plain.tunUnavailableLocked(); err != errNotElevated {
		t.Fatalf("err = %v, want the elevation error", err)
	}

	elevated := &app{home: home, elevated: true}
	if err := elevated.tunUnavailableLocked(); err != errNoWintun {
		t.Fatalf("err = %v, want the missing wintun error", err)
	}

	if err := os.WriteFile(elevated.wintunInstallPath(), []byte("dll"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := elevated.tunUnavailableLocked(); err != nil {
		t.Fatalf("TUN should be available once the dll is present, got %v", err)
	}
}

func TestWintunStatus(t *testing.T) {
	home := t.TempDir()
	a := &app{home: home}
	missing := a.wintunStatusLocked()
	if missing.Installed {
		t.Fatal("a missing dll must not read as installed")
	}
	if missing.Path != filepath.Join(home, "wintun.dll") {
		t.Fatalf("path = %q, want the kernel working directory", missing.Path)
	}

	if err := os.WriteFile(a.wintunInstallPath(), []byte("contents"), 0600); err != nil {
		t.Fatal(err)
	}
	installed := a.wintunStatusLocked()
	if !installed.Installed || installed.Version != wintunVersion || len(installed.SHA256) != 64 {
		t.Fatalf("unexpected status: %+v", installed)
	}
}

func TestConnectionModeDefaultsToSystemProxy(t *testing.T) {
	a := &app{}
	if got := a.connectionModeLocked(); got != modeSystemProxy {
		t.Fatalf("got %q, want %q", got, modeSystemProxy)
	}
	a.settings.ConnectionMode = modeTUN
	if got := a.connectionModeLocked(); got != modeTUN {
		t.Fatalf("got %q, want %q", got, modeTUN)
	}
	a.settings.ConnectionMode = "nonsense"
	if got := a.connectionModeLocked(); got != modeSystemProxy {
		t.Fatalf("an unknown mode must fall back to the system proxy, got %q", got)
	}
}

func TestProbeDomainIsFreshEveryTime(t *testing.T) {
	first := probeDomain()
	second := probeDomain()
	if first == second {
		t.Fatal("the DNS probe domain must differ between runs or a cached answer could fool it")
	}
	if !strings.HasSuffix(first, ".smartvpn-probe.example.com") {
		t.Fatalf("probe domain = %q, want a name under the probe zone", first)
	}
	// The probe zone must not be inside the fake-ip bypass list, otherwise the
	// check would never see a fake address.
	filters := proxyServerDomains([]byte("proxies:\n  - name: node\n    server: abc123.qos.onl\n"))
	section := dnsConfigSection(filters)
	if strings.Contains(section, "smartvpn-probe") {
		t.Fatal("the probe zone must not be excluded from fake-ip")
	}
	// A node hostname inside the fake-ip pool is what breaks TUN: the kernel
	// dials its own fake address instead of the node.
	if !strings.Contains(section, `"+.qos.onl"`) {
		t.Fatalf("the node domain must be excluded from fake-ip:\n%s", section)
	}
	if !strings.Contains(section, "proxy-server-nameserver") {
		t.Fatal("node hostnames must be resolvable outside fake-ip")
	}
}

func TestExitIPProbeURLSelection(t *testing.T) {
	if got := exitIPProbeURL(false); !strings.Contains(got, "api.ipify.org") || strings.Contains(got, "api6") {
		t.Fatalf("ipv4 probe = %q", got)
	}
	if got := exitIPProbeURL(true); !strings.Contains(got, "api6.ipify.org") {
		t.Fatalf("ipv6 probe = %q", got)
	}
}

func writeProfile(t *testing.T, a *app, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(a.profilePath()), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.profilePath(), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestNodeDomainCheck(t *testing.T) {
	a := &app{home: t.TempDir()}
	writeProfile(t, a, "proxies:\n  - name: 日本 01\n    server: abc123.qos.onl\n    port: 10008\n")

	fake := func(string) ([]string, error) { return []string{"198.18.0.7"}, nil }
	real := func(string) ([]string, error) { return []string{"87.192.56.121", "13.208.186.142"}, nil }
	broken := func(string) ([]string, error) { return nil, errors.New("no such host") }

	// A fake answer is the failure this check exists for: the kernel would dial
	// its own address instead of the node.
	check := a.nodeDomainCheck(fake)
	if check.State != "fail" || !strings.Contains(check.Detail, "fake-ip") {
		t.Fatalf("a fake answer must fail the check: %+v", check)
	}
	check = a.nodeDomainCheck(real)
	if check.State != "ok" || !strings.Contains(check.Value, "87.192.56.121") {
		t.Fatalf("a real answer must pass: %+v", check)
	}
	if !strings.Contains(check.Value, "qos.onl") {
		t.Fatalf("the checked hostname should be visible: %+v", check)
	}
	check = a.nodeDomainCheck(broken)
	if check.State != "fail" || !strings.Contains(check.Detail, "无法解析") {
		t.Fatalf("an unresolvable node domain must fail: %+v", check)
	}
}

func TestNodeDomainCheckWithoutDomainsOrProfile(t *testing.T) {
	a := &app{home: t.TempDir()}
	// No profile at all: the check cannot claim anything about the node.
	if check := a.nodeDomainCheck(nil); check.State != "warn" {
		t.Fatalf("a missing profile must be reported as unknown: %+v", check)
	}
	writeProfile(t, a, "proxies:\n  - name: direct\n    server: 1.2.3.4\n    port: 443\n")
	check := a.nodeDomainCheck(func(string) ([]string, error) {
		t.Fatal("an IP-only subscription must not be looked up")
		return nil, nil
	})
	if check.State != "ok" || !strings.Contains(check.Detail, "IP 直连") {
		t.Fatalf("an IP-only subscription has nothing to exclude: %+v", check)
	}
}
