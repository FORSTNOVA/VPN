//go:build windows

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

const (
	wintunVersion = "0.14.1"
	wintunURL     = "https://www.wintun.net/builds/wintun-0.14.1.zip"
	// Pinned digests of that exact build, so a tampered download cannot be
	// installed even if the signature check is unavailable.
	wintunZipSHA256 = "07c256185d6ee3652e09fa55c0b673e2624b565e02c4b9091c79ca7d2f24ef51"
	wintunEntry     = "wintun/bin/amd64/wintun.dll"
	wintunDLLSHA256 = "e5da8447dc2c320edc0fc52fa01885c103de8c118481f683643cacc3220dafce"
	wintunSigner    = "WireGuard LLC"

	tunInterfaceName = "SmartVPN"
)

var (
	errNotElevated   = errors.New("TUN 需要管理员权限：请以管理员身份重启 SmartVPN")
	errNoWintun      = errors.New("缺少 wintun.dll：请先下载，或在设置中指定它的路径")
	errBadWintun     = driverBadWintun
	errBadWintunArch = driverBadArch
)

// connectionModeSupported and defaultConnectionMode describe what Windows can
// offer: both of its own modes, with the system proxy as the default.
func connectionModeSupported(mode string) bool {
	return mode == modeSystemProxy || mode == modeTUN
}

func defaultConnectionMode() string { return modeSystemProxy }

// Check the Windows routing decision without sending a probe outside the VPN.
// The mixed proxy can start successfully even if TUN setup failed.
func (a *app) verifyPrivacyTunnel() error {
	return a.runWindowsKernelAdmission(nil, kernelAdmissionRequest{Op: "privacy"}).Err
}

// dnsListenLine asks the kernel for a DNS server of its own on port 53, which
// the TUN section's dns-hijack then answers from. It is a Windows-only line:
// the port is privileged, and the platform this was written for is the one
// where binding it is ordinary.
const dnsListenLine = "  listen: :53\n"

// findProcessMode is always on Windows: naming the process behind a connection
// works here, and a process rule needs it.
const findProcessMode = "always"

// isElevated reports whether this process holds an administrator token. Mihomo
// needs one to create the TUN adapter, and it inherits ours.
func isElevated() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

// wintunInstallPath is both where the library is looked for and where a
// downloaded one is placed, so a portable copy can use the one that came with
// the package without being told about it.
func (a *app) wintunInstallPath() string {
	return a.lookFor("wintun.dll")
}

// verifyPEArch reads the COFF header so a 32-bit or ARM library is rejected
// before Windows ever tries to load it.
func verifyPEArch(body []byte) error {
	q := driverInstallRequest{Op: "pe", Body: body}
	return runDriverInstallWithEffects(q, newAppDriverInstallEffects(nil, nil, q)).Err
}

// psQuote renders a value as a single-quoted PowerShell literal. Single-quoted
// strings do not interpolate, and ” is the escape for a quote inside them, so
// a path can never break out of the literal.
func psQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

// verifyDLLSignature asks WinVerifyTrust, through PowerShell, for a valid
// Authenticode signature and its signer. A library that ends up inside the TUN
// data path is worth this extra check.
func verifyDLLSignature(path string) (string, error) {
	q := driverInstallRequest{Op: "signature", Path: path}
	r := runDriverInstallWithEffects(q, newAppDriverInstallEffects(nil, nil, q))
	return r.Text, r.Err
}

func (a *app) wintunStatusLocked() wintunStatus {
	target := a.wintunInstallPath()
	status := wintunStatus{Path: target}
	info, err := os.Stat(target)
	if err != nil || info.IsDir() {
		status.Detail = "尚未安装"
		return status
	}
	status.Installed = true
	status.Version = wintunVersion
	status.Signer = wintunSigner
	if digest, err := fileSHA256(target); err == nil {
		status.SHA256 = digest
	}
	return status
}

// tunUnavailableLocked reports why TUN cannot be used, or nil when it can.
func (a *app) tunUnavailableLocked() error {
	if !a.elevated {
		return errNotElevated
	}
	if !a.wintunStatusLocked().Installed {
		return errNoWintun
	}
	return nil
}

func (a *app) tunStatus(w http.ResponseWriter, _ *http.Request) {
	a.runTunReportHTTP(w, tunReportRequest{Op: "windows-status"})
}

// installWintun places a verified wintun.dll in the kernel's working directory,
// which is where the loader looks for it. The download runs outside the lock so
// the UI can keep polling while a slow link is being fetched.
func (a *app) installWintun(w http.ResponseWriter, r *http.Request) {
	q := driverInstallRequest{Op: "install"}
	runDriverInstallHTTPWithEffects(w, q, newAppDriverInstallEffects(a, r, q))
}

func downloadWintun() ([]byte, error) {
	q := driverInstallRequest{Op: "download"}
	fx := newAppDriverInstallEffects(nil, nil, q)
	r := runDriverInstallWithEffects(q, fx)
	if r.Err != nil {
		return nil, r.Err
	}
	return fx.content, nil
}

// inspectAdapterAndRoutes asks Windows for the TUN adapter and for where the
// default routes currently point.
func inspectAdapterAndRoutes(name string) (adapterReport, error) {
	b, e := readTunAdapterRaw(name)
	if e != nil {
		return adapterReport{}, e
	}
	return parseAdapterReport(b, name)
}

func readTunAdapterRaw(name string) ([]byte, error) {
	script := `$ErrorActionPreference = 'SilentlyContinue'
$name = ` + psQuote(name) + `
$adapter = Get-NetAdapter -Name $name | Select-Object -First 1
$routes = Get-NetRoute -DestinationPrefix '0.0.0.0/0','::/0' | Select-Object DestinationPrefix, InterfaceAlias, NextHop
[pscustomobject]@{
  AdapterPresent = [bool]$adapter
  AdapterStatus  = if ($adapter) { [string]$adapter.Status } else { '' }
  Routes         = @($routes | ForEach-Object { [pscustomobject]@{ Prefix = [string]$_.DestinationPrefix; Alias = [string]$_.InterfaceAlias; NextHop = [string]$_.NextHop } })
} | ConvertTo-Json -Depth 5 -Compress`
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("无法读取网卡与路由：%s", strings.TrimSpace(string(out)))
	}
	return out, nil
}

func parseAdapterReport(raw []byte, name string) (adapterReport, error) {
	var payload struct {
		AdapterPresent bool   `json:"AdapterPresent"`
		AdapterStatus  string `json:"AdapterStatus"`
		Routes         []struct {
			Prefix  string `json:"Prefix"`
			Alias   string `json:"Alias"`
			NextHop string `json:"NextHop"`
		} `json:"Routes"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(raw), &payload); err != nil {
		return adapterReport{}, fmt.Errorf("网卡状态无法解析: %w", err)
	}
	report := adapterReport{Name: name, Present: payload.AdapterPresent, Status: payload.AdapterStatus}
	for _, route := range payload.Routes {
		if !strings.EqualFold(route.Alias, name) {
			continue
		}
		report.RouteVia = append(report.RouteVia, route.Prefix)
		switch route.Prefix {
		case "0.0.0.0/0":
			report.IPv4Route = true
		case "::/0":
			report.IPv6Route = true
		}
	}
	return report, nil
}

// noProxyExitIP measures the exit address the operating system itself would
// use, with no proxy configured. With TUN in place this must match the address
// seen through the local mixed port, otherwise traffic is escaping the tunnel.
func noProxyExitIP(address string, wantIPv6 bool) exitIPResult {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DisableKeepAlives = true
	defer transport.CloseIdleConnections()
	client := http.Client{Timeout: 12 * time.Second, Transport: transport}
	response, err := client.Get(address)
	if err != nil {
		return exitIPResult{Error: "系统路径查询失败"}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return exitIPResult{Error: "系统路径查询未返回地址"}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 512))
	if err != nil {
		return exitIPResult{Error: "系统路径响应无效"}
	}
	var answer struct {
		IP string `json:"ip"`
	}
	if json.Unmarshal(body, &answer) != nil {
		return exitIPResult{Error: "系统路径响应无效"}
	}
	ip := net.ParseIP(strings.TrimSpace(answer.IP))
	if ip == nil || (ip.To4() == nil) != wantIPv6 {
		return exitIPResult{Error: "系统路径地址类型不符"}
	}
	return exitIPResult{Address: ip.String()}
}

// runTunChecks is the whole page for one platform: what this tunnel needs in
// order to exist, and whether traffic really goes through it.
func (a *app) runTunChecks(w http.ResponseWriter, _ *http.Request) {
	a.runTunReportHTTP(w, tunReportRequest{Op: "windows-checks"})
}

// exitPathCheck compares the address an ordinary program exits at with the one
// seen through the tunnel. They have to match, otherwise protected traffic is
// escaping the kernel.
func exitPathCheck(name string, proxyURL *url.URL, wantIPv6 bool) tunCheck {
	q := tunReportRequest{Op: "path", Name: name, IPv6: wantIPv6}
	fx := newAppTunReportEffects(nil, q)
	fx.proxy = proxyURL
	return tunReportCheck(runTunReportWithEffects(q, fx))
}

// dropTunnel gives up whatever this platform made for the connection. On
// Windows that is the machine's proxy setting, and the caller has already put
// it back; there is no tunnel of this app's to take away, because the kernel
// makes its own adapter and that adapter goes when the kernel does.
func (a *app) dropTunnel() {}

// tunConfigSection enables Mihomo's own TUN inbound. auto-route takes over the
// default routes and dns-hijack sends every :53 query on the adapter to the
// resolver configured below.
func tunConfigSectionGo(strict bool) string {
	strictRoute := ""
	if strict {
		strictRoute = "  strict-route: true\n"
	}
	return fmt.Sprintf(`tun:
  enable: true
  stack: %s
  device: %s
  auto-route: true
  auto-detect-interface: true
%s  dns-hijack:
    - any:53
    - tcp://any:53
  mtu: %d
`, tunStack, tunInterfaceName, strictRoute, tunMTU)
}

func configTUNFacts() (string, string, int, int) {
	return tunStack, tunInterfaceName, tunMTU, 0
}
