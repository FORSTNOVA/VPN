package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func TestLiveChainSwitchThroughMihomo(t *testing.T) {
	bin := os.Getenv("SMARTVPN_TEST_MIHOMO")
	if bin == "" {
		t.Skip("requires installed Mihomo")
	}
	const target = "198.51.100.1:80"
	exit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "CONNECT" || r.Host != target {
			http.Error(w, "fixture only", 403)
			return
		}
		c, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(5 * time.Second))
		fmt.Fprint(c, "HTTP/1.1 200 OK\r\n\r\n")
		req, err := http.ReadRequest(rw.Reader)
		if err == nil && req.URL.Path == "/phase98" {
			time.Sleep(8 * time.Millisecond)
			fmt.Fprint(c, "HTTP/1.1 204 No Content\r\nConnection: close\r\n\r\n")
		}
	}))
	defer exit.Close()
	a := storeApp(t)
	add := func(name string, s *httptest.Server) {
		host, port, _ := net.SplitHostPort(s.Listener.Addr().String())
		p, _ := strconv.Atoi(port)
		a.appendManualLocked(manualNode{Name: name, Type: "http", Server: host, Port: p, Proxy: map[string]any{"name": name, "type": "http", "server": host, "port": p}})
	}
	add("last", exit)
	var seen [2]atomic.Int32
	for i, name := range []string{"A", "B"} {
		first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "CONNECT" || r.Host != exit.Listener.Addr().String() {
				http.Error(w, "fixture only", 403)
				return
			}
			upstream, err := net.DialTimeout("tcp", exit.Listener.Addr().String(), time.Second)
			if err != nil {
				return
			}
			defer upstream.Close()
			c, rw, err := w.(http.Hijacker).Hijack()
			if err != nil {
				return
			}
			defer c.Close()
			c.SetDeadline(time.Now().Add(6 * time.Second))
			fmt.Fprint(c, "HTTP/1.1 200 OK\r\n\r\n")
			seen[i].Add(1)
			go func(r *bufio.Reader) { io.Copy(upstream, r); upstream.(*net.TCPConn).CloseWrite() }(rw.Reader)
			io.Copy(c, upstream)
		}))
		defer first.Close()
		add(name, first)
	}
	testEditSettings(a, func(s *settings) { s.ConnectionMode = modeSystemProxy; s.MihomoPath = bin })
	for _, name := range []string{"A", "B"} {
		if r := a.runChainService(chainServiceRequest{Op: "save-profile", Name: name, Hops: []string{name, "last"}}); r.Status != 200 {
			t.Fatal(r)
		}
	}
	switchTo := func(name string) chainServiceResult {
		return a.runChainService(chainServiceRequest{Op: "update", Next: proxyChain{Name: name, Enabled: true, Hops: []string{name, "last"}}})
	}
	for i, name := range []string{"A", "B"} {
		func() {
			if r := switchTo(name); r.Status != 200 {
				t.Fatal(r)
			}
			a.mu.Lock()
			err := a.startMihomoLocked()
			a.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				a.mu.Lock()
				defer a.mu.Unlock()
				if err := a.stopMihomoLocked(); err != nil {
					t.Error(err)
				}
			}()
			if r := switchTo("B"); r.Status != 409 {
				t.Fatal("running switch", r)
			}
			if err := a.switchGroupLocked(a.selectedChoice()); err != nil {
				t.Fatal(err)
			}
			if a.activeNodeLocked() != chainExit {
				t.Fatal("unexpected controller selection")
			}
			before, other := seen[i].Load(), seen[1-i].Load()
			reply, err := a.mihomoRequest("GET", "/proxies/"+chainExit+"/delay?timeout=4000&url="+url.QueryEscape("http://198.51.100.1/phase98"), nil)
			if err != nil || reply["delay"] == nil || seen[i].Load() <= before || seen[1-i].Load() != other {
				t.Fatalf("chain %s: %v %v hops=%d/%d", name, reply, err, seen[0].Load(), seen[1].Load())
			}
		}()
	}
}
