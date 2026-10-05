package main

// The parts of the TUN checks that are the same wherever the tunnel comes from:
// what a fake answer looks like, how a node's own hostname is checked against
// it, and how to measure an exit address. The adapter, the routes and the
// permission each platform asks for are not here.
import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"strings"
)

const (
	tunStack = "gvisor"
	tunMTU   = 1500

	// Must stay in step with the generated DNS section, which resolves IPv4
	// names only, so the fake addresses a hijacked lookup can return are IPv4.
	tunFakeIPv4Range = "198.18.0.1/16"
)

// exitIPProbeURL is the address the exit is measured against. The two are
// separate hosts on purpose: a service that answered for both protocols would
// hide an exit that carries only one of them.
func exitIPProbeURL(wantIPv6 bool) string {
	if wantIPv6 {
		return "https://api6.ipify.org?format=json"
	}
	return "https://api.ipify.org?format=json"
}

// tunIPv6Disabled is what the path check reports in place of an IPv6 exit.
const tunIPv6Disabled = "IPv6 未启用：订阅节点通常只有 IPv4 出口，而隧道会接管 IPv6 默认路由，" +
	"若下发 AAAA 记录，每个连接都会先尝试一条走不通的路径"

type tunCheck struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Detail string `json:"detail"`
	Value  string `json:"value,omitempty"`
}

// fakeIPRanges returns the networks the generated DNS section hands out, so a
// resolver answer can be checked against them.
func fakeIPRanges() ([]*net.IPNet, error) {
	_, network, err := net.ParseCIDR(tunFakeIPv4Range)
	if err != nil {
		return nil, err
	}
	return []*net.IPNet{network}, nil
}

// isFakeAddress reports whether an address came from the fake-ip pool, which is
// how a hijacked resolution is told apart from a leaked one.
func isFakeAddress(address string) bool {
	ip := net.ParseIP(strings.TrimSpace(address))
	if ip == nil {
		return false
	}
	networks, err := fakeIPRanges()
	if err != nil {
		return false
	}
	for _, network := range networks {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

func probeDomain() string {
	var raw [4]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "smartvpn-check.example.com"
	}
	return fmt.Sprintf("check-%s.smartvpn-probe.example.com", hex.EncodeToString(raw[:]))
}

// dnsHijackCheck resolves a fresh name and reports whether the answer came from
// the fake-ip pool rather than a real recursive resolver. A fresh name cannot be
// in any resolver cache, so a fake answer really proves interception.
func dnsHijackCheck() tunCheck {
	check := tunCheck{Name: "DNS 接管（fake-ip）", State: "fail"}
	addresses, err := net.LookupHost(probeDomain())
	if err != nil || len(addresses) == 0 {
		check.Detail = "域名解析失败，无法确认 DNS 是否经过内核"
		return check
	}
	check.Value = strings.Join(addresses, ", ")
	for _, address := range addresses {
		if isFakeAddress(address) {
			check.State = "ok"
			check.Detail = "解析结果落在 fake-ip 网段，DNS 查询已由内核接管"
			return check
		}
	}
	check.Detail = "解析结果不是 fake-ip，DNS 可能绕过内核（存在 DNS 泄漏风险）"
	return check
}

// nodeDomainCheck proves the counterpart of the hijack above: the tunnel must
// not swallow its own outbound connections. The node's own hostnames have to
// resolve to real addresses — a fake answer sends the kernel's connection back
// into the tunnel, and no node can ever be reached that way.
func (a *app) nodeDomainCheck(lookup func(string) ([]string, error)) tunCheck {
	check := tunCheck{Name: "节点域名解析", State: "warn"}
	body, err := os.ReadFile(a.profilePath())
	if err != nil {
		check.Detail = "读不到订阅文件，无法确认节点域名"
		return check
	}
	// A node's own hostname rather than the suffix its hostnames share: what the
	// kernel has to resolve is the host in the profile, and a suffix like
	// `+.qos.onl` is not a name that answers anywhere.
	host := proxyServerHostname(body)
	if host == "" {
		check.State = "ok"
		check.Detail = "订阅里的节点使用 IP 直连，没有需要排除的域名"
		return check
	}
	check.Value = host
	addresses, err := lookup(host)
	if err != nil || len(addresses) == 0 {
		check.State = "fail"
		check.Detail = "节点域名无法解析，节点会因此不可达"
		return check
	}
	check.Value = host + " → " + strings.Join(addresses, ", ")
	for _, address := range addresses {
		if isFakeAddress(address) {
			check.State = "fail"
			check.Detail = "节点域名被 fake-ip 接管：代理自身到节点的连接会被吸回隧道"
			return check
		}
	}
	check.State = "ok"
	check.Detail = "节点域名解析为真实地址，代理自身的连接不会进入隧道"
	return check
}

func shortDigest(digest string) string {
	if len(digest) <= 16 {
		return digest
	}
	return digest[:16]
}
