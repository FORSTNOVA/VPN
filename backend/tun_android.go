//go:build android

package main

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/netip"
	"sync"
	"syscall"

	"github.com/metacubex/mihomo/component/process"
	"github.com/metacubex/mihomo/constant"
)

// The Android tunnel is not something this service creates. The system gives it
// to the app as a file descriptor once the user has authorised the VpnService,
// and everything around it — the routes, the DNS address, the interface — is
// the system's to decide and to undo. What is left here is to hand the
// descriptor to the kernel and to report honestly what was granted: a checks
// page that claimed to have created the tunnel would be describing something
// this process cannot see.
type vpnTunnel struct {
	mu     sync.Mutex
	lease  androidTunLease
	iface  string
	routes []string
	dns    []string
}

var tunnel vpnTunnel

// grant records what the VpnService established. It is called from the Java
// side as the tunnel comes up, and again whenever the system says more about
// it — the description arrives once when it exists and once when the network
// has finished registering it.
func (t *vpnTunnel) grant(authorized bool, fd int, iface string, routes, dns []string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lease.apply(leaseGrant, int32(fd), authorized)
	t.iface = iface
	t.routes = routes
	t.dns = dns
	log.Printf("tunnel: descriptor %d, interface %q, %d route(s), %d resolver(s)",
		fd, iface, len(routes), len(dns))
}

// handOver marks the descriptor as the kernel's, which happens when its tunnel
// listener came up with it.
func (t *vpnTunnel) handOver() uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.handOverLocked()
}

func (t *vpnTunnel) handOverLocked() uint64 {
	_, rust := t.lease.apply(leaseHandOver, 0, false)
	log.Printf("tunnel: descriptor %d handed to kernel (Rust lease=%t)", t.lease.fd, rust)
	return t.lease.generation
}

func (t *vpnTunnel) ownedBy(generation uint64) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lease.generation == generation && t.lease.fd > 0 && t.lease.flags == (leaseAuthorized|leaseKernelOwns)
}

// release forgets the tunnel. A descriptor the kernel never took has no owner
// once the service gives it up, and Android would hold the tunnel's file open
// for the life of the process, so it is closed here — which is safe only
// because the Java side detached it from the object that made it.
func (t *vpnTunnel) release() {
	t.mu.Lock()
	orphan, _ := t.lease.apply(leaseRelease, 0, false)
	t.iface = ""
	t.routes = nil
	t.dns = nil
	t.mu.Unlock()
	if orphan > 0 {
		log.Printf("tunnel: closing descriptor %d, which no kernel ever took", orphan)
		_ = syscall.Close(orphan)
	}
}

func (t *vpnTunnel) state() (bool, int, string, []string, []string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lease.flags&leaseAuthorized != 0, int(t.lease.fd), t.iface, append([]string(nil), t.routes...), append([]string(nil), t.dns...)
}

// descriptor is the file descriptor the kernel is told to write to, and zero
// while there is no tunnel.
func (t *vpnTunnel) descriptor() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return int(t.lease.fd)
}

// connectionModeSupported and defaultConnectionMode describe what Android can
// offer: one mode, in which the platform itself carries the tunnel.
func connectionModeSupported(mode string) bool { return mode == modeVPN }

func defaultConnectionMode() string { return modeVPN }

func (a *app) verifyPrivacyTunnel() error {
	return a.tunUnavailableLocked()
}

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
func tunConfigSectionGo(_ bool) string {
	return fmt.Sprintf(`tun:
  enable: true
  stack: %s
  file-descriptor: %d
  auto-route: false
  auto-detect-interface: false
  dns-hijack:
    - any:53
    - tcp://any:53
  mtu: %d
`, tunStack, tunnel.descriptor(), tunMTU)
}

// dnsListenLine is empty on Android and has to be. The Windows section asks the
// kernel for a DNS server on port 53; an unprivileged app cannot bind that port,
// and it does not need to: the hijacked queries above are answered by the
// resolver directly, without a listening socket in between.
const dnsListenLine = ""

const findProcessMode = "always"

func init() {
	process.DefaultPackageNameResolver = resolveAndroidPackage
}

func resolveAndroidPackage(metadata *constant.Metadata) (string, error) {
	var protocol int
	switch metadata.NetWork {
	case constant.TCP:
		protocol = syscall.IPPROTO_TCP
	case constant.UDP:
		protocol = syscall.IPPROTO_UDP
	default:
		return "", fmt.Errorf("unsupported Android owner lookup network %d", metadata.NetWork)
	}
	src, dst := metadata.SourceAddrPort(), metadata.AddrPort()
	// The TUN listener retains the socket destination before DNS and sniffing
	// can change the routing destination.
	if metadata.Type == constant.TUN {
		if raw, ok := metadata.RawDstAddr.(interface{ AddrPort() netip.AddrPort }); ok {
			dst = raw.AddrPort()
		}
	}
	if !src.IsValid() || !dst.IsValid() || src.Port() == 0 || dst.Port() == 0 {
		return "", fmt.Errorf("incomplete Android owner lookup endpoints %s -> %s", src, dst)
	}
	return jniResolveOwnerPackage(protocol, src, dst)
}

func (a *app) tunStatus(w http.ResponseWriter, _ *http.Request) {
	a.runTunReportHTTP(w, tunReportRequest{Op: "android-status"})
}

// runTunChecks answers the same question the Windows page does — is traffic
// really inside the tunnel — with the evidence this platform can produce. The
// last check is the one that matters most and the one Windows cannot do from
// the app's side: the address the tunnel exits at is compared with the address
// the locked region was verified at, so a tunnel that is up but leaving from
// somewhere else is reported as a failure rather than as a working connection.
func (a *app) runTunChecks(w http.ResponseWriter, _ *http.Request) {
	a.runTunReportHTTP(w, tunReportRequest{Op: "android-checks"})
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

// exitRegionCheckLocked measures the address the tunnel exits at and holds it
// against the region that is locked and the address that region was verified
// at. Everything else on this page proves the tunnel exists; this proves the
// traffic in it leaves where the user asked it to.
func (a *app) exitRegionCheckLocked() tunCheck {
	q := tunReportRequest{Op: "region"}
	return tunReportCheck(runTunReportWithEffects(q, newAppTunReportEffects(a, q)))
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
	lease := t.lease
	t.mu.Unlock()
	if lease.fd <= 0 {
		return
	}
	report, ok := jniDescribeTunnel()
	if !ok {
		return
	}
	t.refreshReport(lease.generation, report)
}

func (t *vpnTunnel) refreshReport(generation uint64, report tunnelReport) {
	t.mu.Lock()
	if t.lease.fd > 0 && t.lease.generation == generation {
		t.iface = report.Interface
		t.routes = report.Routes
		t.dns = report.DNS
	}
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
	defer t.mu.Unlock()
	fd := int(t.lease.fd)
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

func configTUNFacts() (string, string, int, int) {
	return tunStack, "", tunMTU, tunnel.descriptor()
}
