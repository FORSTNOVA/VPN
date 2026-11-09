package main

import (
	"fmt"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestWindowsSocksPortSettings(t *testing.T) {
	a := storeApp(t)
	update := func(body string, want int) {
		t.Helper()
		w := httptest.NewRecorder()
		a.updateSettings(w, httptest.NewRequest("PUT", "/api/settings", strings.NewReader(body)))
		if w.Code != want {
			t.Fatalf("settings %s: %d %s", body, w.Code, w.Body.String())
		}
	}
	update(`{"socksPort":6258}`, 200)
	update(`{"dnsProtection":true}`, 200)
	for _, v := range []int{-1, 65536} {
		update(fmt.Sprintf(`{"socksPort":%d}`, v), 400)
	}
	a.settings = settings{}
	a.loadSettings()
	if a.settings.SocksPort != 6258 {
		t.Fatal("lost saved port")
	}
	update(`{"socksPort":0}`, 200)
	a.loadSettings()
	if a.settings.SocksPort != 0 {
		t.Fatal("automatic selection not restored")
	}
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if _, err := proxyListenPort(l.Addr().(*net.TCPAddr).Port); err == nil {
		t.Fatal("occupied port silently accepted")
	}
}

func TestWindowsFixedPortKernelReconnect(t *testing.T) {
	exe := os.Getenv("SMARTVPN_TEST_MIHOMO")
	if exe == "" {
		t.Skip("set SMARTVPN_TEST_MIHOMO to test the bundled Windows kernel")
	}
	a := storeApp(t)
	p, err := freeLoopbackPort()
	if err != nil {
		t.Fatal(err)
	}
	a.settings.MihomoPath = exe
	a.settings.SocksPort = p
	if err := a.saveSettings(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.addManualNodeLocked("trojan://test@127.0.0.1:9#local-test", ""); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		a.settings = settings{}
		a.loadSettings()
		a.mu.Lock()
		err = a.startMihomoLocked()
		a.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		func() {
			defer func() {
				a.mu.Lock()
				defer a.mu.Unlock()
				if err := a.stopMihomoLocked(); err != nil {
					t.Error(err)
				}
			}()
			c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(p), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			c.SetDeadline(time.Now().Add(time.Second))
			if _, err = c.Write([]byte{5, 1, 0}); err != nil {
				t.Fatal(err)
			}
			buf := make([]byte, 2)
			if _, err = io.ReadFull(c, buf); err != nil || buf[0] != 5 || buf[1] != 0 {
				t.Fatalf("SOCKS5 reply %v: %v", buf, err)
			}
		}()
	}
}
