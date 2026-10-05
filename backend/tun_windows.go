//go:build windows

package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
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
	errBadWintun     = errors.New("wintun.dll 未通过校验")
	errBadWintunArch = errors.New("wintun.dll 不是 64 位 Windows 动态库")
)

type wintunStatus struct {
	Installed bool   `json:"installed"`
	Path      string `json:"path"`
	Version   string `json:"version,omitempty"`
	SHA256    string `json:"sha256,omitempty"`
	Signer    string `json:"signer,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

// connectionModeSupported and defaultConnectionMode describe what Windows can
// offer: both of its own modes, with the system proxy as the default.
func connectionModeSupported(mode string) bool {
	return mode == modeSystemProxy || mode == modeTUN
}

func defaultConnectionMode() string { return modeSystemProxy }

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
	if len(body) < 0x40 || !bytes.HasPrefix(body, []byte("MZ")) {
		return errBadWintunArch
	}
	offset := int(uint32(body[0x3C]) | uint32(body[0x3D])<<8 | uint32(body[0x3E])<<16 | uint32(body[0x3F])<<24)
	if offset <= 0 || offset+6 > len(body) || !bytes.Equal(body[offset:offset+4], []byte("PE\x00\x00")) {
		return errBadWintunArch
	}
	machine := uint16(body[offset+4]) | uint16(body[offset+5])<<8
	if machine != 0x8664 {
		return fmt.Errorf("%w（machine 0x%04x）", errBadWintunArch, machine)
	}
	return nil
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
	// The path is interpolated rather than passed through $args: an argument
	// after -Command does not reliably reach the script. No execution-policy
	// flag is needed either — that policy governs script files, not an inline
	// command, so asking for a bypass would request more than this uses.
	script := `$s = Get-AuthenticodeSignature -LiteralPath ` + psQuote(path) + `
if ($null -eq $s) { Write-Output 'none'; exit 0 }
if ($s.Status -ne 'Valid') { Write-Output ('invalid:' + $s.Status); exit 0 }
Write-Output ('valid:' + $s.SignerCertificate.Subject)`
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("无法校验 wintun.dll 的签名：%s", strings.TrimSpace(string(out)))
	}
	text := strings.TrimSpace(string(out))
	if !strings.HasPrefix(text, "valid:") {
		return "", fmt.Errorf("%w（签名状态：%s）", errBadWintun, strings.TrimPrefix(text, "invalid:"))
	}
	subject := strings.TrimPrefix(text, "valid:")
	if !strings.Contains(subject, wintunSigner) {
		return subject, fmt.Errorf("%w（签发者：%s）", errBadWintun, subject)
	}
	return wintunSigner, nil
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
	a.mu.Lock()
	defer a.mu.Unlock()
	reason := a.tunUnavailableLocked()
	writeJSON(w, map[string]any{
		"mode":      a.connectionModeLocked(),
		"elevated":  a.elevated,
		"wintun":    a.wintunStatusLocked(),
		"interface": tunInterfaceName,
		"stack":     tunStack,
		"mtu":       tunMTU,
		"available": reason == nil,
		"reason":    tunReason(reason),
		"active":    a.kernelRunningLocked() && a.connectionModeLocked() == modeTUN,
	})
}

// installWintun places a verified wintun.dll in the kernel's working directory,
// which is where the loader looks for it. The download runs outside the lock so
// the UI can keep polling while a slow link is being fetched.
func (a *app) installWintun(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if r.Body != nil {
		if err := decodeJSON(r.Body, &body); err != nil && !errors.Is(err, io.EOF) {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
	}

	source := strings.TrimSpace(body.Path)
	var content []byte
	detail := ""
	if source != "" {
		read, err := os.ReadFile(source)
		if err != nil {
			http.Error(w, "无法读取指定的 wintun.dll", http.StatusBadRequest)
			return
		}
		if err := verifyPEArch(read); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		content = read
		detail = "已使用你指定的文件"
	} else {
		downloaded, err := downloadWintun()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		content = downloaded
		detail = fmt.Sprintf("已从官方源安装 wintun %s", wintunVersion)
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	target := a.wintunInstallPath()
	tempPath := target + ".tmp"
	if err := os.WriteFile(tempPath, content, 0600); err != nil {
		http.Error(w, "无法写入 wintun.dll", http.StatusInternalServerError)
		return
	}
	signer, err := verifyDLLSignature(tempPath)
	if err != nil {
		_ = os.Remove(tempPath)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if err := os.Rename(tempPath, target); err != nil {
		_ = os.Remove(tempPath)
		http.Error(w, "无法安装 wintun.dll", http.StatusInternalServerError)
		return
	}
	log.Printf("installed wintun.dll at %s (signer %s)", target, signer)
	status := a.wintunStatusLocked()
	status.Detail = detail
	writeJSON(w, map[string]any{"ok": true, "wintun": status, "available": a.tunUnavailableLocked() == nil})
}

func downloadWintun() ([]byte, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	client := http.Client{Timeout: 150 * time.Second, Transport: transport}
	response, err := client.Get(wintunURL)
	if err != nil {
		return nil, errors.New("无法下载 wintun.dll，请检查网络，或在设置中指定本地文件")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("wintun 下载返回 HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 8*1024*1024))
	if err != nil {
		return nil, errors.New("wintun 下载中断")
	}
	if digest := sha256.Sum256(body); hex.EncodeToString(digest[:]) != wintunZipSHA256 {
		return nil, fmt.Errorf("%w（压缩包哈希不匹配）", errBadWintun)
	}
	reader, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return nil, fmt.Errorf("%w（压缩包无法解析）", errBadWintun)
	}
	for _, entry := range reader.File {
		if entry.Name != wintunEntry {
			continue
		}
		file, err := entry.Open()
		if err != nil {
			return nil, fmt.Errorf("%w（压缩包读取失败）", errBadWintun)
		}
		defer file.Close()
		content, err := io.ReadAll(io.LimitReader(file, 2*1024*1024))
		if err != nil {
			return nil, fmt.Errorf("%w（压缩包读取失败）", errBadWintun)
		}
		if digest := sha256.Sum256(content); hex.EncodeToString(digest[:]) != wintunDLLSHA256 {
			return nil, fmt.Errorf("%w（dll 哈希不匹配）", errBadWintun)
		}
		if err := verifyPEArch(content); err != nil {
			return nil, err
		}
		return content, nil
	}
	return nil, fmt.Errorf("%w（压缩包内没有 amd64 库）", errBadWintun)
}

type adapterReport struct {
	Name      string
	Present   bool
	Status    string
	IPv4Route bool
	IPv6Route bool
	RouteVia  []string
}

// inspectAdapterAndRoutes asks Windows for the TUN adapter and for where the
// default routes currently point.
func inspectAdapterAndRoutes(name string) (adapterReport, error) {
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
		return adapterReport{}, fmt.Errorf("无法读取网卡与路由：%s", strings.TrimSpace(string(out)))
	}
	return parseAdapterReport(out, name)
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
	a.mu.Lock()
	defer a.mu.Unlock()

	checks := make([]tunCheck, 0, 8)
	if a.elevated {
		checks = append(checks, tunCheck{Name: "管理员权限", State: "ok", Detail: "进程已具备管理员权限"})
	} else {
		checks = append(checks, tunCheck{Name: "管理员权限", State: "fail", Detail: errNotElevated.Error()})
	}
	status := a.wintunStatusLocked()
	if status.Installed {
		checks = append(checks, tunCheck{
			Name: "wintun.dll", State: "ok", Value: status.Path,
			Detail: fmt.Sprintf("%s · sha256 %s…", wintunVersion, shortDigest(status.SHA256)),
		})
	} else {
		checks = append(checks, tunCheck{Name: "wintun.dll", State: "fail", Value: status.Path, Detail: errNoWintun.Error()})
	}

	active := a.kernelRunningLocked() && a.connectionModeLocked() == modeTUN
	if !active {
		checks = append(checks, tunCheck{Name: "TUN 连接", State: "warn",
			Detail: "当前不是 TUN 模式或尚未连接，路径检查只在 TUN 连接后才有意义"})
		writeJSON(w, map[string]any{"active": false, "checks": checks})
		return
	}

	report, err := inspectAdapterAndRoutes(tunInterfaceName)
	if err != nil {
		checks = append(checks, tunCheck{Name: "TUN 网卡", State: "fail", Detail: err.Error()})
	} else {
		if report.Present && report.Status == "Up" {
			checks = append(checks, tunCheck{Name: "TUN 网卡", State: "ok", Value: report.Name, Detail: "虚拟网卡存在且已启用"})
		} else {
			checks = append(checks, tunCheck{Name: "TUN 网卡", State: "fail", Value: report.Name,
				Detail: fmt.Sprintf("状态为 %q，虚拟网卡未正常工作", report.Status)})
		}
		switch {
		case report.IPv4Route && report.IPv6Route:
			checks = append(checks, tunCheck{Name: "默认路由", State: "ok", Value: strings.Join(report.RouteVia, " "),
				Detail: "IPv4 与 IPv6 默认路由都指向虚拟网卡"})
		case report.IPv4Route || report.IPv6Route:
			checks = append(checks, tunCheck{Name: "默认路由", State: "warn", Value: strings.Join(report.RouteVia, " "),
				Detail: "只有一种协议的默认路由经过虚拟网卡，另一端可能绕过隧道"})
		default:
			checks = append(checks, tunCheck{Name: "默认路由", State: "fail",
				Detail: "默认路由没有指向虚拟网卡，流量不会进入隧道"})
		}
	}

	checks = append(checks, dnsHijackCheck())
	checks = append(checks, a.nodeDomainCheck(net.LookupHost))

	proxyURL := &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(a.mixedPort))}
	checks = append(checks, exitPathCheck("IPv4 出口", proxyURL, false))
	// There is no IPv6 exit to compare against because the resolver hands out no
	// AAAA records on purpose: a node with an IPv4-only exit would otherwise be
	// asked to carry every connection, which is what stalls the whole system.
	checks = append(checks, tunCheck{Name: "IPv6 出口", State: "warn", Detail: tunIPv6Disabled})

	writeJSON(w, map[string]any{"active": true, "checks": checks, "interface": tunInterfaceName})
}

// exitPathCheck compares the address an ordinary program exits at with the one
// seen through the tunnel. They have to match, otherwise protected traffic is
// escaping the kernel.
func exitPathCheck(name string, proxyURL *url.URL, wantIPv6 bool) tunCheck {
	check := tunCheck{Name: name}
	tunnelled := checkExitIP(proxyURL, exitIPProbeURL(wantIPv6), wantIPv6)
	system := noProxyExitIP(exitIPProbeURL(wantIPv6), wantIPv6)
	switch {
	case tunnelled.Address == "" && system.Address == "":
		check.State = "warn"
		check.Detail = fmt.Sprintf("两条路径都未能取得地址（%s / %s）", tunnelled.Error, system.Error)
	case system.Address == "":
		check.State = "warn"
		check.Value = tunnelled.Address
		check.Detail = "系统路径未响应：" + system.Error
	case tunnelled.Address == "":
		check.State = "fail"
		check.Value = system.Address
		check.Detail = "本机代理路径不可达，无法与系统路径比较"
	case system.Address == tunnelled.Address:
		check.State = "ok"
		check.Value = system.Address
		check.Detail = "系统路径与隧道出口一致，流量确实经过内核"
	default:
		check.State = "fail"
		check.Value = fmt.Sprintf("系统 %s / 隧道 %s", system.Address, tunnelled.Address)
		check.Detail = "系统路径的出口与隧道不一致，受保护流量可能绕过隧道"
	}
	return check
}

// dropTunnel gives up whatever this platform made for the connection. On
// Windows that is the machine's proxy setting, and the caller has already put
// it back; there is no tunnel of this app's to take away, because the kernel
// makes its own adapter and that adapter goes when the kernel does.
func (a *app) dropTunnel() {}

// tunConfigSection enables Mihomo's own TUN inbound. auto-route takes over the
// default routes and dns-hijack sends every :53 query on the adapter to the
// resolver configured below.
func tunConfigSection() string {
	return fmt.Sprintf(`tun:
  enable: true
  stack: %s
  device: %s
  auto-route: true
  auto-detect-interface: true
  dns-hijack:
    - any:53
  mtu: %d
`, tunStack, tunInterfaceName, tunMTU)
}
