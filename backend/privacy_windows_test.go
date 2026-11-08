package main

import (
	"bytes"
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
)

// Opt-in integration check against the actual packaged kernel. Validate the
// full TUN config with -t, then exercise its routing on loopback without
// creating a system adapter or changing the host's DNS.
func TestPrivacyWithPackagedKernel(t *testing.T) {
	kernel := os.Getenv("SMARTVPN_TEST_MIHOMO")
	if kernel == "" {
		t.Skip("set SMARTVPN_TEST_MIHOMO to run the packaged kernel")
	}
	dir := t.TempDir()
	providerDir := filepath.Join(dir, "providers")
	if err := os.MkdirAll(providerDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(providerDir, chinaIPListFile), []byte("127.0.0.1/32\n"), 0600); err != nil {
		t.Fatal(err)
	}
	node, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()
	dohViaNode := make(chan string, 1)
	go func() {
		for {
			c, err := node.Accept()
			if err != nil {
				return
			}
			go observeDNSConnect(c, dohViaNode)
		}
	}()
	portText := strconv.Itoa(node.Addr().(*net.TCPAddr).Port)
	if err := os.WriteFile(filepath.Join(dir, "nodes.yaml"), []byte("proxies:\n  - {name: tcp-only, type: socks5, server: 127.0.0.1, port: "+portText+", udp: false}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	echo, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	received := make(chan string, 4)
	go func() {
		buf := make([]byte, 128)
		for {
			n, peer, err := echo.ReadFrom(buf)
			if err != nil {
				return
			}
			received <- string(buf[:n])
			_, _ = echo.WriteTo(buf[:n], peer)
		}
	}()
	for _, protect := range []bool{false, true} {
		port, err := freeLoopbackPort()
		if err != nil {
			t.Fatal(err)
		}
		control, err := freeLoopbackPort()
		if err != nil || control == port {
			t.Fatal("could not allocate separate local ports")
		}
		opts := configOptions{MixedPort: port, ControllerPort: control, ProviderPath: "nodes.yaml",
			TUN: true, ChinaDirect: true, trafficPolicy: trafficPolicy{DomesticWeb: protect, WebRTCProtection: protect, DNSProtection: protect}, SubscriptionProvider: true, Rules: []string{"IP-CIDR,127.0.0.1/32,DIRECT,no-resolve"}}
		conf := generatedConfig(opts)
		path := filepath.Join(dir, "config.yaml")
		if err := os.WriteFile(path, []byte(conf), 0600); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(kernel, "-t", "-d", dir, "-f", path).CombinedOutput(); err != nil {
			t.Fatalf("kernel config validation: %v %s", err, out)
		}
		// Keep both modes' actual DNS, proxy groups and rules. Bind DNS on a
		// private loopback port instead of the machine's DNS port.
		start, end := strings.Index(conf, "\ntun:\n"), strings.Index(conf, "\ndns:\n")
		if start < 0 || end <= start {
			t.Fatal("TUN and DNS sections missing")
		}
		dnsPort, err := freeLoopbackPort()
		if err != nil {
			t.Fatal(err)
		}
		dnsAddress := net.JoinHostPort("127.0.0.1", strconv.Itoa(dnsPort))
		local := strings.Replace(conf[:start]+conf[end:], "listen: :53", "listen: "+dnsAddress, 1)
		// Put the named fixtures outside the test's domestic IP range. Only
		// a domain rule can deliver their requests to the local gateway.
		domains := []string{"sr.mihoyo.com", "bh3.mihoyo.com", "www.bilibili.com", "portal.example.gov.cn"}
		local += "\nhosts:\n"
		for _, domain := range domains {
			local += "  " + domain + ": 127.0.0.2\n"
		}
		if err := os.WriteFile(path, []byte(local), 0600); err != nil {
			t.Fatal(err)
		}
		func() {
			cmd := exec.Command(kernel, "-d", dir, "-f", path)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
			address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
			for deadline := time.Now().Add(8 * time.Second); ; {
				c, err := net.DialTimeout("tcp", address, time.Second/4)
				if err == nil {
					_ = c.Close()
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("test kernel did not open its mixed port")
				}
				time.Sleep(40 * time.Millisecond)
			}
			payload := []byte("udp-test-" + strconv.FormatBool(protect))
			_, _, err := socksUDPExchange(address, echo.LocalAddr().String(), payload,
				func(p []byte) bool { return bytes.Equal(p, payload) }, 2*time.Second)
			if protect != (err != nil) {
				t.Fatalf("strict=%t: unexpected UDP result: %v", protect, err)
			}
			select {
			case data := <-received:
				if protect || data != string(payload) {
					t.Fatalf("unexpected direct datagram %q", data)
				}
			default:
				if !protect {
					t.Fatal("control datagram did not reach direct receiver")
				}
			}
			if protect {
				game := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("game gateway")) }))
				defer game.Close()
				proxyURL, _ := url.Parse("http://" + address)
				transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
				defer transport.CloseIdleConnections()
				client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
				res, err := client.Get(game.URL)
				if err != nil {
					t.Fatalf("domestic nonstandard TCP port did not connect: %v", err)
				}
				body, err := io.ReadAll(res.Body)
				res.Body.Close()
				if err != nil || string(body) != "game gateway" {
					t.Fatalf("game gateway unreachable: %d", res.StatusCode)
				}
				listener, err := net.Listen("tcp4", "127.0.0.2:0")
				if err != nil {
					t.Fatal(err)
				}
				server := &http.Server{Handler: game.Config.Handler}
				go server.Serve(listener)
				defer server.Close()
				for _, domain := range domains {
					res, err := client.Get("http://" + net.JoinHostPort(domain, strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)))
					if err != nil {
						t.Fatalf("domain routing failed for %s: %v", domain, err)
					}
					body, err := io.ReadAll(res.Body)
					res.Body.Close()
					if err != nil || string(body) != "game gateway" {
						t.Fatalf("domain missed direct gateway: %s", domain)
					}
				}
				c, err := net.Dial("udp4", dnsAddress)
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				// *.lan is outside the fake-IP pool, so this needs upstream DNS.
				query, _, err := buildDNSQuery("privacy-probe.lan")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := c.Write(query); err != nil {
					t.Fatal(err)
				}
				select {
				case resolver := <-dohViaNode:
					t.Logf("node received a CONNECT for DoH at %s", resolver)
				case <-time.After(5 * time.Second):
					t.Fatal("no upstream DNS connection reached the selected node")
				}
			}
		}()
	}
}

// Minimal SOCKS node: observe the destination, then close without forwarding.
func observeDNSConnect(c net.Conn, seen chan<- string) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	var hello [2]byte
	if _, err := io.ReadFull(c, hello[:]); err != nil || hello[0] != 5 {
		return
	}
	if _, err := io.CopyN(io.Discard, c, int64(hello[1])); err != nil {
		return
	}
	if _, err := c.Write([]byte{5, 0}); err != nil {
		return
	}
	var request [4]byte
	if _, err := io.ReadFull(c, request[:]); err != nil || request[1] != 1 {
		return
	}
	host, port, err := readSocksAddress(c, request[3])
	if err == nil && port == 443 && (host == "1.1.1.1" || host == "8.8.8.8") {
		select {
		case seen <- host:
		default:
		}
	}
}
