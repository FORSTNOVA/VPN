//go:build windows

package main

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRoutingConflictConfigRules(t *testing.T) {
	subRules := []string{
		"DOMAIN-SUFFIX,ad-cn.example.com,REJECT",
		"DOMAIN-SUFFIX,steamcontent.com,DIRECT",
		"IP-CIDR,198.51.100.1/32,DIRECT,no-resolve",
		"DOMAIN-SUFFIX,proxy-cn.example.com,SmartVPN",
	}

	// Fix 1: SourceRuleOrder (non-privateTUN) puts subscription rules before SmartVPNChina.
	cfg1 := generatedConfig(configOptions{
		MixedPort: 1000, ControllerPort: 1001, ControllerSecret: "s",
		ProviderPath: "p.yaml", TUN: true, ChinaDirect: true,
		SourceRuleOrder: true, SubscriptionProvider: true, Rules: subRules,
	})
	idxReject := strings.Index(cfg1, `"DOMAIN-SUFFIX,ad-cn.example.com,REJECT"`)
	idxProxy := strings.Index(cfg1, `"DOMAIN-SUFFIX,proxy-cn.example.com,SmartVPN"`)
	idxChina := strings.Index(cfg1, `"RULE-SET,SmartVPNChina,DIRECT"`)
	idxQuic := strings.Index(cfg1, `"`+quicRejectRule+`"`)
	if idxReject < 0 || idxProxy < 0 || idxChina < 0 || idxQuic < 0 {
		t.Fatalf("missing expected rules in SourceRuleOrder config:\n%s", cfg1)
	}
	if !(idxReject < idxProxy && idxProxy < idxChina && idxChina < idxQuic) {
		t.Fatalf("unexpected rule order in SourceRuleOrder config: reject=%d proxy=%d china=%d quic=%d", idxReject, idxProxy, idxChina, idxQuic)
	}

	// Fix 2 & Fix 5: privateTUN + DomesticWeb + SourceRuleOrder puts proxied before domestic,
	// preserves safe subscription DIRECT rules as TCP-only after DNS/STUN rules, and covers expanded browsers.
	cfg2 := generatedConfig(configOptions{
		MixedPort: 1000, ControllerPort: 1001, ControllerSecret: "s",
		ProviderPath: "p.yaml", TUN: true, ChinaDirect: true,
		SourceRuleOrder: true, SubscriptionProvider: true, Rules: subRules,
		trafficPolicy: trafficPolicy{DomesticWeb: true, WebRTCProtection: true, DNSProtection: true},
	})
	idxProxy2 := strings.Index(cfg2, `"DOMAIN-SUFFIX,proxy-cn.example.com,SmartVPN"`)
	idxDNSGuard := strings.Index(cfg2, `"AND,((NETWORK,tcp),(DST-PORT,53/853)),SmartVPN"`)
	idxSTUNGuard := strings.Index(cfg2, `"AND,((NETWORK,tcp),(DST-PORT,3478/5349/19302)),SmartVPN"`)
	idxSubDirectTCP := strings.Index(cfg2, `"AND,((NETWORK,tcp),(DOMAIN-SUFFIX,steamcontent.com)),DIRECT"`)
	idxSubIPDirectTCP := strings.Index(cfg2, `"AND,((NETWORK,tcp),(IP-CIDR,198.51.100.1/32,no-resolve)),DIRECT"`)
	idxBrowserQUIC := strings.Index(cfg2, `(PROCESS-NAME,brave.exe)`)
	idxChinaTCP := strings.Index(cfg2, `"AND,((NETWORK,tcp),(RULE-SET,SmartVPNChina)),DIRECT"`)
	if idxProxy2 < 0 || idxDNSGuard < 0 || idxSTUNGuard < 0 || idxSubDirectTCP < 0 || idxSubIPDirectTCP < 0 || idxBrowserQUIC < 0 || idxChinaTCP < 0 {
		t.Fatalf("missing expected rules in privateTUN config:\n%s", cfg2)
	}
	if !(idxProxy2 < idxDNSGuard && idxDNSGuard < idxSTUNGuard && idxSTUNGuard < idxSubDirectTCP && idxSubDirectTCP < idxSubIPDirectTCP && idxSubIPDirectTCP < idxBrowserQUIC && idxBrowserQUIC < idxChinaTCP) {
		t.Fatalf("unexpected rule order in privateTUN config")
	}

	// Fix 3 & Fix 4: DNSProtection without DomesticWeb omits domestic nameserver-policy;
	// DNSProtection with DomesticWeb includes expanded domestic suffixes.
	cfgDNSOnly := generatedConfig(configOptions{
		MixedPort: 1000, ControllerPort: 1001, ControllerSecret: "s",
		ProviderPath: "p.yaml", TUN: true, ChinaDirect: true,
		SubscriptionProvider: true, Rules: subRules,
		trafficPolicy: trafficPolicy{DNSProtection: true},
	})
	if strings.Contains(cfgDNSOnly, "nameserver-policy:") {
		t.Fatalf("pure DNSProtection without DomesticWeb must not emit domestic nameserver-policy:\n%s", cfgDNSOnly)
	}
	for _, want := range []string{`"+.douyin.com":`, `"+.xiaohongshu.com":`, `"+.steamcontent.com":`} {
		if !strings.Contains(cfg2, want) {
			t.Fatalf("DomesticWeb + DNSProtection missing expected domestic DNS suffix %s", want)
		}
	}
	if strings.Contains(cfg2, `"+.steampowered.com":`) {
		t.Fatal("steampowered.com must not be routed to domestic DNS")
	}
}

// Opt-in isolated loopback regression matrix against the real packaged mihomo.exe.
// Does NOT create a TUN adapter, does NOT bind host :53, does NOT touch
// Windows system proxy, and does NOT interact with any running VPN instance.
func TestRoutingConflictsWithPackagedKernel(t *testing.T) {
	kernel := os.Getenv("SMARTVPN_TEST_MIHOMO")
	if kernel == "" {
		t.Skip("set SMARTVPN_TEST_MIHOMO to run the packaged kernel")
	}

	runRoutingConflicts(t, kernel, false)
}

func runRoutingConflicts(t *testing.T, kernel string, clashRS bool) {
	// Direct server 1: 127.0.0.1 (inside SmartVPNChina)
	lnDirectCN, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer lnDirectCN.Close()
	srvCN := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "VIA_DIRECT_CN:%s", r.Host)
	})}
	go srvCN.Serve(lnDirectCN)
	defer srvCN.Close()
	portCN := lnDirectCN.Addr().(*net.TCPAddr).Port

	// Direct server 2: 127.0.0.2 (outside SmartVPNChina)
	lnDirectOS, err := net.Listen("tcp4", "127.0.0.2:0")
	if err != nil {
		t.Fatal(err)
	}
	defer lnDirectOS.Close()
	srvOS := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "VIA_DIRECT_OS:%s", r.Host)
	})}
	go srvOS.Serve(lnDirectOS)
	defer srvOS.Close()
	portOS := lnDirectOS.Addr().(*net.TCPAddr).Port

	// Mock SOCKS5 proxy node: accepts TCP CONNECT and replies VIA_PROXY:<host>:<port>
	lnProxy, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer lnProxy.Close()
	var udpAssociates atomic.Int64
	var proxyMu sync.Mutex
	var proxyHits []string
	recordProxy := func(dst string) {
		proxyMu.Lock()
		proxyHits = append(proxyHits, dst)
		proxyMu.Unlock()
	}
	clearProxyHits := func() {
		proxyMu.Lock()
		proxyHits = nil
		proxyMu.Unlock()
	}
	getProxyHits := func() []string {
		proxyMu.Lock()
		defer proxyMu.Unlock()
		out := make([]string, len(proxyHits))
		copy(out, proxyHits)
		return out
	}

	go func() {
		for {
			c, err := lnProxy.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
				var hdr [2]byte
				if _, err := io.ReadFull(conn, hdr[:]); err != nil || hdr[0] != 5 {
					return
				}
				if _, err := io.CopyN(io.Discard, conn, int64(hdr[1])); err != nil {
					return
				}
				_, _ = conn.Write([]byte{5, 0})
				var req [4]byte
				if _, err := io.ReadFull(conn, req[:]); err != nil {
					return
				}
				host, port, err := readSocksAddress(conn, req[3])
				if err != nil {
					return
				}
				dst := net.JoinHostPort(host, strconv.Itoa(port))
				recordProxy(dst)
				if req[1] == 3 {
					udpAssociates.Add(1)
				}
				if req[1] == 1 {
					_, _ = conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0})
					buf := make([]byte, 1024)
					n, _ := conn.Read(buf)
					if n > 0 && (bytes.HasPrefix(buf[:n], []byte("GET ")) || bytes.HasPrefix(buf[:n], []byte("HEAD "))) {
						if clashRS {
							time.Sleep(5 * time.Millisecond)
						} // Ensure a measurable member delay on loopback.
						body := "VIA_PROXY:" + dst
						fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
					}
				}
			}(c)
		}
	}()
	proxyPort := lnProxy.Addr().(*net.TCPAddr).Port

	var directPackets atomic.Int64
	udpEchoCN, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer udpEchoCN.Close()
	go func() {
		buf := make([]byte, 256)
		for {
			n, peer, err := udpEchoCN.ReadFrom(buf)
			if err != nil {
				return
			}
			directPackets.Add(1)
			_, _ = udpEchoCN.WriteTo(buf[:n], peer)
		}
	}()

	subRules := []string{
		"DOMAIN-SUFFIX,ad-cn.example.com,REJECT",
		"DOMAIN-SUFFIX,steamcontent.com,DIRECT",
		"IP-CIDR,127.0.0.1/32,DIRECT,no-resolve",
		"DOMAIN-SUFFIX,proxy-cn.example.com,SmartVPN",
	}

	type scenario struct {
		name            string
		domesticWeb     bool
		webrtcProtect   bool
		dnsProtect      bool
		sourceRuleOrder bool
		wantAdCN        string
		wantSteam       string
		wantProxyCN     string
		wantUnlistedCN  string
		wantUDPBlocked  bool
		dnsQueryDomain  string
		wantDNSViaProxy bool
	}

	scenarios := []scenario{
		{
			name:            "S1_AllOn_DefaultRules",
			domesticWeb:     true,
			webrtcProtect:   true,
			dnsProtect:      true,
			sourceRuleOrder: false,
			wantAdCN:        "REJECT",
			wantSteam:       "VIA_DIRECT_OS:",
			wantProxyCN:     "VIA_DIRECT_CN:", // default rules don't run proxied before domestic
			wantUnlistedCN:  "VIA_DIRECT_CN:",
			wantUDPBlocked:  true,
			dnsQueryDomain:  "unlisted-overseas.example",
			wantDNSViaProxy: true,
		},
		{
			name:            "S2_DNSOnly_NoDomesticWeb",
			domesticWeb:     false,
			webrtcProtect:   false,
			dnsProtect:      true,
			sourceRuleOrder: false,
			wantAdCN:        "REJECT",
			wantSteam:       "VIA_PROXY:",
			wantProxyCN:     "VIA_PROXY:",
			wantUnlistedCN:  "VIA_PROXY:",
			wantUDPBlocked:  false,
			dnsQueryDomain:  "www.baidu.com",
			wantDNSViaProxy: true, // Fix 3: pure DNSProtection sends even baidu.com through proxy DoH
		},
		{
			name:            "S3_ImportPolicy_NoPrivacy",
			domesticWeb:     false,
			webrtcProtect:   false,
			dnsProtect:      false,
			sourceRuleOrder: true,
			wantAdCN:        "REJECT", // Fix 1: REJECT no longer bypassed by SmartVPNChina
			wantSteam:       "VIA_DIRECT_OS:",
			wantProxyCN:     "VIA_PROXY:", // Fix 1: proxy rule no longer bypassed by SmartVPNChina
			wantUnlistedCN:  "VIA_DIRECT_CN:",
			wantUDPBlocked:  false,
			dnsQueryDomain:  "unlisted-overseas.example",
			wantDNSViaProxy: false,
		},
		{
			name:            "S4_ImportPolicy_AllOn",
			domesticWeb:     true,
			webrtcProtect:   true,
			dnsProtect:      true,
			sourceRuleOrder: true,
			wantAdCN:        "REJECT",
			wantSteam:       "VIA_DIRECT_OS:", // Fix 2: subscription DIRECT preserved as TCP-only
			wantProxyCN:     "VIA_PROXY:",     // Fix 2: subscription proxy overrides domestic accel
			wantUnlistedCN:  "VIA_DIRECT_CN:",
			wantUDPBlocked:  true, // Fix 2: even with IP-CIDR,127.0.0.1/32,DIRECT in sub, UDP is still blocked!
			dnsQueryDomain:  "unlisted-overseas.example",
			wantDNSViaProxy: true,
		},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			dir := t.TempDir()
			providerDir := filepath.Join(dir, "providers")
			if err := os.MkdirAll(providerDir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(providerDir, chinaIPListFile), []byte("127.0.0.1/32\n"), 0600); err != nil {
				t.Fatal(err)
			}
			nodesYAML := fmt.Sprintf("proxies:\n  - {name: mock-node, type: socks5, server: 127.0.0.1, port: %d, udp: false}\n", proxyPort)
			if clashRS {
				nodesYAML += fmt.Sprintf("  - {name: '节点 / B', type: socks5, server: 127.0.0.1, port: %d, udp: false}\n", proxyPort)
			}
			if err := os.WriteFile(filepath.Join(dir, "nodes.yaml"), []byte(nodesYAML), 0600); err != nil {
				t.Fatal(err)
			}
			mixedPort, err := freeLoopbackPort()
			if err != nil {
				t.Fatal(err)
			}
			ctrlPort, err := freeLoopbackPort()
			if err != nil {
				t.Fatal(err)
			}
			dnsPort, err := freeLoopbackPort()
			if err != nil {
				t.Fatal(err)
			}
			// Keep 127.0.0.1/32 out of subRules in S3 so proxy-cn.example.com (which is 127.0.0.1) tests domain rule order
			rules := subRules
			if sc.name == "S3_ImportPolicy_NoPrivacy" {
				rules = []string{
					"DOMAIN-SUFFIX,ad-cn.example.com,REJECT",
					"DOMAIN-SUFFIX,steamcontent.com,DIRECT",
					"DOMAIN-SUFFIX,proxy-cn.example.com,SmartVPN",
				}
			}
			opts := configOptions{
				ClashRS: clashRS, MixedPort: mixedPort, ControllerPort: ctrlPort, ControllerSecret: "audit-secret",
				ProviderPath: "nodes.yaml", TUN: true, ChinaDirect: true,
				SourceRuleOrder: sc.sourceRuleOrder, SubscriptionProvider: true, Rules: rules,
				trafficPolicy: trafficPolicy{
					DomesticWeb:      sc.domesticWeb,
					WebRTCProtection: sc.webrtcProtect,
					DNSProtection:    sc.dnsProtect,
				},
			}
			conf := generatedConfig(opts)
			cfgPath := filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(cfgPath, []byte(conf), 0600); err != nil {
				t.Fatal(err)
			}
			validation := kernelArgs(clashRS, "-t", "-d", dir, "-f", cfgPath)
			if clashRS {
				validation = append(validation, "--strict-config")
			}
			if out, err := exec.Command(kernel, validation...).CombinedOutput(); err != nil {
				t.Fatalf("kernel -t failed: %v\n%s", err, out)
			}
			start, end := strings.Index(conf, "\ntun:\n"), strings.Index(conf, "\ndns:\n")
			if start < 0 || end <= start {
				t.Fatal("missing tun/dns sections")
			}
			dnsAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(dnsPort))
			localConf := strings.Replace(conf[:start]+conf[end:], "listen: :53", "listen: "+dnsAddr, 1)
			localConf += "\nhosts:\n" +
				"  ad-cn.example.com: 127.0.0.1\n" +
				"  steamcontent.com: 127.0.0.2\n" +
				"  proxy-cn.example.com: 127.0.0.1\n" +
				"  unlisted-cn.example.com: 127.0.0.1\n"
			if clashRS {
				localConf = clashRSLoopbackConfig(t, dir, localConf, sc.dnsQueryDomain, sc.dnsProtect, portCN)
			}
			if err := os.WriteFile(cfgPath, []byte(localConf), 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(kernel, kernelArgs(clashRS, "-d", dir, "-f", cfgPath)...)
			if clashRS {
				cmd.Stdout = os.Stderr
				cmd.Stderr = os.Stderr
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Logf("isolated kernel pid=%d", cmd.Process.Pid)
			defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

			mixedAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(mixedPort))
			for deadline := time.Now().Add(8 * time.Second); ; {
				c, err := net.DialTimeout("tcp", mixedAddr, 200*time.Millisecond)
				if err == nil {
					_ = c.Close()
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("mihomo mixed-port did not open")
				}
				time.Sleep(40 * time.Millisecond)
			}
			proxyURL, _ := url.Parse("http://" + mixedAddr)
			tr := &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: true}
			defer tr.CloseIdleConnections()
			client := &http.Client{Transport: tr, Timeout: 3 * time.Second}

			checkHTTP := func(label, targetURL, want string) {
				resp, err := client.Get(targetURL)
				if want == "REJECT" {
					if err == nil {
						body, _ := io.ReadAll(resp.Body)
						resp.Body.Close()
						if len(body) > 0 {
							t.Fatalf("%s: expected REJECT, got body %q", label, string(body))
						}
					}
					return
				}
				if err != nil {
					t.Fatalf("%s: unexpected error: %v", label, err)
				}
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if !strings.HasPrefix(string(body), want) {
					t.Fatalf("%s: got %q, want prefix %q", label, string(body), want)
				}
			}

			if clashRS {
				a := &app{ctrlPort: ctrlPort, ctrlSecret: "audit-secret"}
				checkClashRSController(t, a, mixedAddr, portCN)
			}
			checkHTTP("ad-cn", fmt.Sprintf("http://ad-cn.example.com:%d/", portCN), sc.wantAdCN)
			checkHTTP("steamcontent", fmt.Sprintf("http://steamcontent.com:%d/", portOS), sc.wantSteam)
			checkHTTP("proxy-cn", fmt.Sprintf("http://proxy-cn.example.com:%d/", portCN), sc.wantProxyCN)
			checkHTTP("unlisted-cn", fmt.Sprintf("http://unlisted-cn.example.com:%d/", portCN), sc.wantUnlistedCN)

			beforeAssociates := udpAssociates.Load()
			beforePackets := directPackets.Load()
			payload := []byte("udp-conflict-check")
			_, _, udpErr := socksUDPExchange(mixedAddr, udpEchoCN.LocalAddr().String(), payload, func(p []byte) bool { return bytes.Equal(p, payload) }, 1200*time.Millisecond)
			if sc.wantUDPBlocked != (udpErr != nil) {
				t.Fatalf("UDP to CN IP: blocked=%t, err=%v", sc.wantUDPBlocked, udpErr)
			}

			if clashRS && sc.wantUDPBlocked && udpAssociates.Load() != beforeAssociates {
				t.Fatal("udp:false node received UDP ASSOCIATE in protected mode")
			}
			if clashRS && sc.wantUDPBlocked && directPackets.Load() != beforePackets {
				t.Fatal("protected UDP reached the direct echo socket")
			}
			clearProxyHits()
			dc, err := net.Dial("udp4", dnsAddr)
			if err != nil {
				t.Fatal(err)
			}
			q, _, err := buildDNSQuery(sc.dnsQueryDomain)
			if err != nil {
				dc.Close()
				t.Fatal(err)
			}
			// Mihomo forwards TXT in fake-IP mode. The clash-rs fixture excludes the query name from synthesis.
			if !clashRS {
				q[len(q)-3] = 16
			}
			_, _ = dc.Write(q)
			time.Sleep(500 * time.Millisecond)
			dc.Close()
			hits := getProxyHits()
			sawDoH := false
			for _, h := range hits {
				if h == "1.1.1.1:443" || h == "8.8.8.8:443" {
					sawDoH = true
					break
				}
			}
			if sawDoH != sc.wantDNSViaProxy {
				t.Fatalf("DNS query %s via proxy DoH=%t, want %t (hits=%v)", sc.dnsQueryDomain, sawDoH, sc.wantDNSViaProxy, hits)
			}
		})
	}
}
