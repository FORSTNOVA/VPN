//go:build windows

package main

import (
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
	"golang.org/x/net/proxy"
	"gopkg.in/yaml.v3"
)

func TestClashRSRouting(t *testing.T) {
	kernel := os.Getenv("SMARTVPN_TEST_CLASH_RS")
	if kernel == "" {
		t.Skip("set SMARTVPN_TEST_CLASH_RS to run clash-rs")
	}
	runRoutingConflicts(t, kernel, true)
}

// An empty one-node IPv4 MMDB and empty protobuf GeoSiteList keep compatibility
// mode from downloading its default databases. These tests use only file rules.
func clashRSGeoFixtures(t *testing.T, dir string) {
	t.Helper()
	db, err := base64.StdEncoding.DecodeString("AAABAAABAAAAAAAAAAAAAAAAAAAAAKvN701heE1pbmQuY29t6VtiaW5hcnlfZm9ybWF0X21ham9yX3ZlcnNpb26hAltiaW5hcnlfZm9ybWF0X21pbm9yX3ZlcnNpb26hAEtidWlsZF9lcG9jaAECAU1kYXRhYmFzZV90eXBlTkdlb0lQMi1Db3VudHJ5S2Rlc2NyaXB0aW9u4EppcF92ZXJzaW9uoQRJbGFuZ3VhZ2VzAARKbm9kZV9jb3VudMEBS3JlY29yZF9zaXploRg=")
	if err != nil {
		t.Fatal(err)
	}
	for file, data := range map[string][]byte{"Country.mmdb": db, "geosite.dat": {}} {
		if err := os.WriteFile(filepath.Join(dir, file), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

// DIRECT resolves with enhanced=false in v0.10.12, bypassing the hosts table.
// Answer the same fixture domains over real loopback TCP DNS.
func clashRSLoopbackConfig(t *testing.T, dir, config, query string, strict bool, httpPort int) string {
	t.Helper()
	clashRSGeoFixtures(t, dir)
	var c map[string]any
	if err := yaml.Unmarshal([]byte(config), &c); err != nil {
		t.Fatal(err)
	}
	hosts := c["hosts"].(map[string]any)
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &dns.Server{Listener: ln, Net: "tcp", Handler: dns.HandlerFunc(func(w dns.ResponseWriter, q *dns.Msg) {
		reply := new(dns.Msg)
		reply.SetReply(q)
		for _, question := range q.Question {
			ip, ok := hosts[strings.TrimSuffix(question.Name, ".")]
			if ok && question.Qtype == dns.TypeA {
				reply.Answer = append(reply.Answer, &dns.A{Hdr: dns.RR_Header{Name: question.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: net.ParseIP(ip.(string)).To4()})
			}
		}
		if err := w.WriteMsg(reply); err != nil {
			t.Errorf("DNS fixture: %v", err)
		}
	})}
	ready := make(chan struct{})
	srv.NotifyStartedFunc = func() { close(ready) }
	go func() {
		if err := srv.ActivateAndServe(); err != nil {
			t.Errorf("DNS listener: %v", err)
		}
	}()
	<-ready
	t.Cleanup(func() {
		if err := srv.Shutdown(); err != nil {
			t.Error(err)
		}
	})
	d := c["dns"].(map[string]any)
	// This kernel synthesizes an A answer even for TXT in fake-IP mode.
	d["fake-ip-filter"] = append(d["fake-ip-filter"].([]any), query)
	policy, ok := d["nameserver-policy"].(map[string]any)
	if !ok {
		policy = map[string]any{}
		d["nameserver-policy"] = policy
	}
	for host := range hosts {
		policy[host] = "tcp://" + ln.Addr().String()
		delete(policy, "+."+host)
	}
	if !strict {
		d["nameserver"] = []string{"tcp://" + ln.Addr().String()}
	}
	for _, g := range c["proxy-groups"].([]any) {
		group := g.(map[string]any)
		if _, ok := group["url"]; ok {
			group["url"] = fmt.Sprintf("http://127.0.0.1:%d/health", httpPort)
		}
	}
	b, err := yaml.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func checkClashRSController(t *testing.T, a *app, mixed string, portCN int) {
	t.Helper()
	for _, route := range []string{"/version", "/proxies"} {
		v, err := a.mihomoRequest(http.MethodGet, route, nil)
		if err != nil || len(v) == 0 {
			t.Fatalf("%s: %v %v", route, v, err)
		}
	}
	if err := a.switchGroupLocked("mock-node"); err != nil {
		t.Fatal(err)
	}
	group, err := a.mihomoRequest("GET", "/proxies/SmartVPN", nil)
	if err != nil || group["now"] != "mock-node" {
		t.Fatalf("selected group: %v %v", group, err)
	}
	delays, err := a.groupDelaysForURLLocked(fmt.Sprintf("http://127.0.0.1:%d/", portCN), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if delays["节点 / B"] <= 0 || len(delays) != 2 {
		t.Fatalf("member delays: %v", delays)
	}
	if delays["mock-node"] <= 0 {
		t.Fatal(delays)
	}
	dialer, err := proxy.SOCKS5("tcp", mixed, nil, proxy.Direct)
	if err != nil {
		t.Fatal(err)
	}
	held, err := dialer.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", portCN))
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	deadline := time.Now().Add(time.Second)
	for {
		s, err := kernelTrafficAt(a.ctrlPort, a.ctrlSecret)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range s.Connections {
			if c.Metadata.Network == "tcp" && string(c.Metadata.DestinationPort) == strconv.Itoa(portCN) {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("held connection missing: %+v", s)
		}
		time.Sleep(time.Millisecond * 10)
	}
}

// A hop accepts exactly one allowed destination. A successful response and both
// recorded CONNECTs prove that the kernel followed dialer-proxy.
func clashRSSocksHop(t *testing.T, allowed string) (int, <-chan string) {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hits := make(chan string, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(5 * time.Second))
		var h [4]byte
		if _, err = io.ReadFull(c, h[:2]); err != nil {
			t.Error(err)
			return
		}
		if _, err = io.CopyN(io.Discard, c, int64(h[1])); err != nil {
			t.Error(err)
			return
		}
		if _, err = c.Write([]byte{5, 0}); err != nil {
			t.Error(err)
			return
		}
		if _, err = io.ReadFull(c, h[:]); err != nil {
			t.Error(err)
			return
		}
		host, port, err := readSocksAddress(c, h[3])
		if err != nil {
			t.Error(err)
			return
		}
		dst := net.JoinHostPort(host, strconv.Itoa(port))
		if h[0] != 5 || h[1] != 1 || dst != allowed {
			t.Errorf("unexpected SOCKS CONNECT %x %s", h, dst)
			return
		}
		upstream, err := net.DialTimeout("tcp", allowed, time.Second)
		if err != nil {
			t.Error(err)
			return
		}
		defer upstream.Close()
		upstream.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err = c.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); err != nil {
			t.Error(err)
			return
		}
		hits <- dst
		go func() { io.Copy(upstream, c); upstream.(*net.TCPConn).CloseWrite() }()
		io.Copy(c, upstream)
	}()
	t.Cleanup(func() { ln.Close(); <-done })
	return ln.Addr().(*net.TCPAddr).Port, hits
}
func TestClashRSTwoHop(t *testing.T) {
	kernel := os.Getenv("SMARTVPN_TEST_CLASH_RS")
	if kernel == "" {
		t.Skip("set SMARTVPN_TEST_CLASH_RS to run clash-rs")
	}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "chain-ok") }))
	defer target.Close()
	last, lastHits := clashRSSocksHop(t, target.Listener.Addr().String())
	first, firstHits := clashRSSocksHop(t, fmt.Sprintf("127.0.0.1:%d", last))
	profile := fmt.Sprintf("proxies:\n  - {name: first, type: socks5, server: 127.0.0.1, port: %d}\n  - {name: last, type: socks5, server: 127.0.0.1, port: %d}\n", first, last)
	hops, err := chainProxies([]string{"first", "last"}, []byte(profile), nil)
	if err != nil {
		t.Fatal(err)
	}
	chain, err := yaml.Marshal(map[string]any{"proxies": hops})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	clashRSGeoFixtures(t, dir)
	mixed, err := freeLoopbackPort()
	if err != nil {
		t.Fatal(err)
	}
	ctrl, err := freeLoopbackPort()
	if err != nil {
		t.Fatal(err)
	}
	cfg := generatedConfig(configOptions{ClashRS: true, MixedPort: mixed, ControllerPort: ctrl, ControllerSecret: "chain-test", ChainProxies: chain})
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(kernel, kernelArgs(true, "-d", dir, "-f", path)...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Logf("isolated kernel pid=%d", cmd.Process.Pid)
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	a := &app{ctrlPort: ctrl, ctrlSecret: "chain-test"}
	for deadline := time.Now().Add(5 * time.Second); ; {
		_, err := a.mihomoRequest("GET", "/version", nil)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	transport := &http.Transport{Proxy: http.ProxyURL(&url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", mixed)})}
	defer transport.CloseIdleConnections()
	c := http.Client{Transport: transport, Timeout: 3 * time.Second}
	resp, err := c.Get(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || string(body) != "chain-ok" {
		t.Fatalf("chain response %q: %v", body, err)
	}
	for _, hit := range []<-chan string{firstHits, lastHits} {
		select {
		case dst := <-hit:
			t.Log("CONNECT", dst)
		default:
			t.Fatal("missing hop")
		}
	}
}
