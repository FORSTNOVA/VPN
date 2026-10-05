//go:build android

package main

import (
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

// The Android tunnel is not something this service creates. The system gives it
// to the app as a file descriptor once the user has authorised the VpnService,
// and everything around it — the routes, the DNS address, the interface — is
// the system's to decide and to undo. What is left here is to hand the
// descriptor to the kernel and to report honestly what was granted: a checks
// page that claimed to have created the tunnel would be describing something
// this process cannot see.
type vpnTunnel struct {
	mu         sync.Mutex
	authorized bool
	fd         int
	iface      string
	routes     []string
	dns        []string
	// Whether the kernel took the descriptor over. From that moment the kernel
	// closes it, and this side must not: closing a descriptor twice is a
	// different descriptor's problem.
	handedOver bool
}

var tunnel vpnTunnel

// grant records what the VpnService established. It is called from the Java
// side as the tunnel comes up, and again whenever the system says more about
// it — the description arrives once when it exists and once when the network
// has finished registering it.
func (t *vpnTunnel) grant(authorized bool, fd int, iface string, routes, dns []string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	// A second description of the same tunnel must not reset what is known
	// about it: the flag below decides who closes the descriptor, and clearing
	// it here would have this side close one the kernel is still reading.
	if t.fd != fd {
		t.handedOver = false
	}
	t.authorized = authorized
	t.fd = fd
	t.iface = iface
	t.routes = routes
	t.dns = dns
	log.Printf("tunnel: descriptor %d, interface %q, %d route(s), %d resolver(s)",
		fd, iface, len(routes), len(dns))
}

// handOver marks the descriptor as the kernel's, which happens when its tunnel
// listener came up with it.
func (t *vpnTunnel) handOver() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.handedOver = true
	log.Printf("tunnel: descriptor %d is the kernel's from here", t.fd)
}

// release forgets the tunnel. A descriptor the kernel never took has no owner
// once the service gives it up, and Android would hold the tunnel's file open
// for the life of the process, so it is closed here — which is safe only
// because the Java side detached it from the object that made it.
func (t *vpnTunnel) release() {
	t.mu.Lock()
	orphan := t.fd
	if t.handedOver {
		orphan = 0
	}
	t.fd = 0
	t.iface = ""
	t.routes = nil
	t.dns = nil
	t.handedOver = false
	t.mu.Unlock()
	if orphan > 0 {
		log.Printf("tunnel: closing descriptor %d, which no kernel ever took", orphan)
		_ = syscall.Close(orphan)
	}
}

func (t *vpnTunnel) state() (bool, int, string, []string, []string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.authorized, t.fd, t.iface, append([]string(nil), t.routes...), append([]string(nil), t.dns...)
}

// descriptor is the file descriptor the kernel is told to write to, and zero
// while there is no tunnel.
func (t *vpnTunnel) descriptor() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.fd
}

// connectionModeSupported and defaultConnectionMode describe what Android can
// offer: one mode, in which the platform itself carries the tunnel.
func connectionModeSupported(mode string) bool { return mode == modeVPN }

func defaultConnectionMode() string { return modeVPN }

// tunUnavailableLocked reports why the tunnel cannot be used, or nil when it can.
func (a *app) tunUnavailableLocked() error {
	authorized, fd, _, _, _ := tunnel.state()
	if !authorized {
		return errors.New("VPN 未授权：请在系统的连接请求里允许 SmartVPN")
	}
	if fd <= 0 {
		return errors.New("隧道尚未建立：先打开连接开关")
	}
	return nil
}

// tunConfigSection describes the tunnel to the kernel. The difference from the
// Windows section is the whole platform: there is no adapter to create and no
// route to take over, because the VpnService has already done both and the
// kernel is handed the descriptor of the interface it made. auto-route and
// auto-detect-interface stay off for that reason — a kernel that tried to
// configure the interface would be fighting the system for it.
//
// dns-hijack is kept: the system resolves through the tunnel, so the queries
// arrive on this interface, and the kernel answers them from its own resolver.
func tunConfigSection() string {
	return fmt.Sprintf(`tun:
  enable: true
  stack: %s
  file-descriptor: %d
  auto-route: false
  auto-detect-interface: false
  dns-hijack:
    - any:53
  mtu: %d
`, tunStack, tunnel.descriptor(), tunMTU)
}

// dnsListenLine is empty on Android and has to be. The Windows section asks the
// kernel for a DNS server on port 53; an unprivileged app cannot bind that port,
// and it does not need to: the hijacked queries above are answered by the
// resolver directly, without a listening socket in between.
const dnsListenLine = ""

// findProcessMode is off, because the answer is not there to be had: since
// Android 10 an app cannot read another app's command line, so every lookup
// would fail and cost a table read per connection for nothing. The connection
// list on this platform shows addresses and rules instead of process names.
const findProcessMode = "off"

func (a *app) tunStatus(w http.ResponseWriter, _ *http.Request) {
	tunnel.refresh()
	a.mu.Lock()
	defer a.mu.Unlock()
	authorized, fd, iface, routes, dns := tunnel.state()
	reason := a.tunUnavailableLocked()
	writeJSON(w, map[string]any{
		"mode":       a.connectionModeLocked(),
		"elevated":   authorized,
		"interface":  iface,
		"stack":      tunStack,
		"mtu":        tunMTU,
		"routes":     routes,
		"dns":        dns,
		"available":  reason == nil,
		"reason":     tunReason(reason),
		"active":     a.kernelRunningLocked() && fd > 0,
		"descriptor": fd,
	})
}

// runTunChecks answers the same question the Windows page does — is traffic
// really inside the tunnel — with the evidence this platform can produce. The
// last check is the one that matters most and the one Windows cannot do from
// the app's side: the address the tunnel exits at is compared with the address
// the locked region was verified at, so a tunnel that is up but leaving from
// somewhere else is reported as a failure rather than as a working connection.
func (a *app) runTunChecks(w http.ResponseWriter, _ *http.Request) {
	tunnel.refresh()
	a.mu.Lock()
	defer a.mu.Unlock()

	authorized, fd, iface, routes, dns := tunnel.state()
	checks := make([]tunCheck, 0, 8)
	if authorized {
		checks = append(checks, tunCheck{Name: "VPN 授权", State: "ok", Detail: "系统已允许 SmartVPN 建立 VPN 连接"})
	} else {
		checks = append(checks, tunCheck{Name: "VPN 授权", State: "fail", Detail: "尚未授权，连接会由系统弹窗询问"})
	}
	if fd > 0 {
		state, detail := "ok", fmt.Sprintf("VpnService 已建立隧道（fd %d）", fd)
		if !tunnel.descriptorAlive() {
			// Worth failing loudly: in this state the device has no network at
			// all, and everything else on this page would look healthy.
			state = "fail"
			detail = fmt.Sprintf("隧道描述符 %d 已被提前关闭：隧道还在接管路由，但没人再读它，"+
				"此时全部应用的流量都会被丢弃", fd)
		}
		checks = append(checks, tunCheck{Name: "隧道", State: state, Value: iface, Detail: detail})
	} else {
		checks = append(checks, tunCheck{Name: "隧道", State: "fail", Detail: "VpnService 尚未建立隧道"})
	}
	switch {
	case len(routes) == 0:
		checks = append(checks, tunCheck{Name: "默认路由", State: "warn",
			Detail: "读不到隧道的路由表，无法确认默认流量是否进入隧道"})
	case hasDefaultRoute(routes):
		checks = append(checks, tunCheck{Name: "默认路由", State: "ok", Value: strings.Join(routes, " "),
			Detail: "VpnService 把默认路由指向了隧道，全部应用的流量都经过这里"})
	default:
		checks = append(checks, tunCheck{Name: "默认路由", State: "fail", Value: strings.Join(routes, " "),
			Detail: "隧道没有默认路由，受保护流量会走系统网络"})
	}
	if len(dns) > 0 {
		checks = append(checks, tunCheck{Name: "隧道 DNS", State: "ok", Value: strings.Join(dns, ", "),
			Detail: "系统解析地址落在隧道内，查询会由内核接管"})
	}

	active := a.kernelRunningLocked() && fd > 0
	if !active {
		checks = append(checks, tunCheck{Name: "连接", State: "warn",
			Detail: "尚未连接，路径检查只在连接后才有意义"})
		writeJSON(w, map[string]any{"active": false, "checks": checks})
		return
	}

	checks = append(checks, dnsHijackNote())
	checks = append(checks, a.nodeDomainCheck(net.LookupHost))
	checks = append(checks, a.exitRegionCheckLocked())
	checks = append(checks, tunCheck{Name: "IPv6 出口", State: "warn", Detail: tunIPv6Disabled})

	writeJSON(w, map[string]any{"active": true, "checks": checks, "interface": iface})
}

// dnsHijackNote is what this platform can say about DNS.
//
// On Windows the equivalent check is a measurement: a fresh name is resolved
// and the answer is expected to come from the fake-ip pool, which proves the
// system's queries reach the kernel. Here that cannot be measured from inside.
// This process is deliberately outside the tunnel — see dns_android.go for why
// it has to be — so its own lookups are not the system's lookups, and resolving
// a name here says nothing about what other applications are handed.
//
// What is worth saying is that, plainly, so that nobody reads the rest of the
// page as though this check had passed.
func dnsHijackNote() tunCheck {
	return tunCheck{
		Name:  "DNS 接管",
		State: "warn",
		Detail: "本机进程有意留在隧道之外（订阅与节点连接必须直连），因此无法从进程内验证" +
			"系统 DNS 是否被内核接管；默认路由与出口地区两项才是这台设备生效的证据",
	}
}

// exitRegionCheckLocked measures the address the tunnel exits at and holds it
// against the region that is locked and the address that region was verified
// at. Everything else on this page proves the tunnel exists; this proves the
// traffic in it leaves where the user asked it to.
func (a *app) exitRegionCheckLocked() tunCheck {
	check := tunCheck{Name: "出口地区", State: "warn"}
	node := a.activeNodeLocked()
	record, known := a.regions[node]
	if !known || record.ExitIP == "" {
		check.Detail = "当前节点还没有验证过的出口地址，先在地区页做一次实测"
		return check
	}
	proxyURL := &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(a.mixedPort))}
	measured := checkExitIP(proxyURL, exitIPProbeURL(false), false)
	if measured.Address == "" {
		check.State = "fail"
		check.Detail = "经隧道查询出口地址失败：" + measured.Error
		return check
	}
	check.Value = measured.Address + " · " + record.Country
	if measured.Address != record.ExitIP {
		check.State = "fail"
		check.Detail = fmt.Sprintf("出口地址与验证过的 %s 不一致（当时为 %s），节点可能已经换了出口",
			record.ExitIP, record.Country)
		return check
	}
	if a.lockedRegion != "" && !strings.EqualFold(a.lockedRegion, record.Country) {
		check.State = "fail"
		check.Detail = fmt.Sprintf("锁定地区是 %s，当前出口却是 %s", a.lockedRegion, record.Country)
		return check
	}
	check.State = "ok"
	if a.lockedRegion == "" {
		check.Detail = "出口地址与验证结果一致；没有锁定地区，因此只是报告"
		return check
	}
	check.Detail = "出口地址与锁定地区的验证结果一致，锁定生效"
	return check
}

// refresh asks the system for the tunnel's description as it stands now and
// records it.
//
// The description arrives in pieces: the interface exists before the routes are
// on it, and the resolver is set after that. The copy taken when the tunnel was
// granted is therefore not the one to report — a checks page that read it would
// say the tunnel has no routes while it is carrying everything.
func (t *vpnTunnel) refresh() {
	t.mu.Lock()
	ask := t.fd > 0
	t.mu.Unlock()
	if !ask {
		return
	}
	report, ok := jniDescribeTunnel()
	if !ok {
		return
	}
	t.mu.Lock()
	t.iface = report.Interface
	t.routes = report.Routes
	t.dns = report.DNS
	t.mu.Unlock()
}

// descriptorAlive reports whether the descriptor is still open.
//
// It is here because of what its failure looks like: the tunnel still exists on
// the system's side, routing still points into it, and nothing reports an
// error — while every application's traffic goes to be dropped. The one thing
// that can be said about the descriptor is whether it is still a descriptor,
// and that is worth saying on the checks page before anything else notices.
func (t *vpnTunnel) descriptorAlive() bool {
	t.mu.Lock()
	fd := t.fd
	t.mu.Unlock()
	if fd <= 0 {
		return false
	}
	var stat syscall.Stat_t
	return syscall.Fstat(fd, &stat) == nil
}

// dropTunnel takes the tunnel away, and is called when the kernel is gone.
//
// The interface still exists at that moment, and every application's traffic is
// still being routed into it: a tunnel with nobody reading it is what a device
// with no network looks like, and it is the worst state this app can leave
// behind. Stopping the service is what removes a tunnel on Android.
func (a *app) dropTunnel() {
	if tunnel.descriptor() > 0 {
		jniDropTunnel()
	}
}

// hasDefaultRoute reports whether the tunnel owns the default route of either
// protocol. The VpnService sets these; this only reads them back.
func hasDefaultRoute(routes []string) bool {
	for _, route := range routes {
		if route == "0.0.0.0/0" || route == "::/0" {
			return true
		}
	}
	return false
}
