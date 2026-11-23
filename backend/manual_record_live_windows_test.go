//go:build windows

package main

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// Only replies to this fixture's VLESS TCP request, without opening an outbound socket.
func TestLiveManualRecordThroughMihomo(t *testing.T) {
	bin := os.Getenv("SMARTVPN_TEST_MIHOMO")
	if bin == "" {
		t.Skip("requires installed Mihomo")
	}
	seen := make(chan bool, 1)
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fixture-ws" || r.Host != "manual.invalid" {
			http.NotFound(w, r)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		conn := websocket.NetConn(ctx, c, websocket.MessageBinary)
		in := bufio.NewReader(conn)
		// version, UUID, empty addons, TCP, port 80, IPv4, test address
		want := append([]byte{0}, bytes.Repeat([]byte{0x11}, 16)...)
		want = append(want, 0, 1, 0, 80, 1, 198, 51, 100, 1)
		header := make([]byte, len(want))
		if _, err := io.ReadFull(in, header); err != nil || !bytes.Equal(want, header) {
			return
		}
		req, err := http.ReadRequest(in)
		if err != nil || req.URL.Path != "/phase97" {
			return
		}
		select {
		case seen <- true:
		default:
		}
		// Mihomo reports a sub-millisecond delay as a failed probe.
		time.Sleep(8 * time.Millisecond)
		_, _ = conn.Write([]byte("\x00\x00HTTP/1.1 204 No Content\r\nConnection: close\r\n\r\n"))
	}))
	defer fixture.Close()
	uri := "vless://11111111-1111-1111-1111-111111111111@" + strings.TrimPrefix(fixture.URL, "http://") + "?type=ws&path=%2Ffixture-ws&host=manual.invalid#phase97"
	a := storeApp(t)
	if _, err := a.addManualNodeLocked(uri, ""); err != nil {
		t.Fatal(err)
	}
	a.loadManualNodesLocked()
	a.mu.Lock()
	defer a.mu.Unlock()
	testEditSettings(a, func(s *settings) { s.ConnectionMode = modeSystemProxy; s.MihomoPath = bin })
	if err := a.startMihomoLocked(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := a.stopMihomoLocked(); err != nil {
			t.Error(err)
		}
	}()
	if err := a.switchGroupLocked("phase97"); err != nil {
		t.Fatal(err)
	}
	reply, err := a.mihomoRequest("GET", "/providers/proxies/"+manualProviderName+"/phase97/healthcheck?timeout=4000&url="+url.QueryEscape("http://198.51.100.1/phase97"), nil)
	if err != nil || reply["delay"] == nil {
		t.Fatalf("Mihomo manual provider probe: %v", err)
	}
	select {
	case <-seen:
	default:
		t.Fatal("manual transport did not reach fixture")
	}
}
