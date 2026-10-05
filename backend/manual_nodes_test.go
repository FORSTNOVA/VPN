package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// storeApp builds an app with a real (temporary) store, which is where the
// pasted nodes and the subscription list live.
func storeApp(t *testing.T) *app {
	t.Helper()
	home := t.TempDir()
	opened, err := openStore(filepath.Join(home, "smartvpn.db"))
	if err != nil {
		t.Fatal(err)
	}
	// A writing connection keeps the database file open, which would stop the
	// temporary directory from being cleaned up.
	t.Cleanup(func() { _ = opened.db.Close() })
	return &app{
		home: home, store: opened,
		health: map[string]nodeHealth{}, regions: map[string]regionRecord{},
		healthParams: defaultHealthParams(),
	}
}

func TestManualNodeRoundTrip(t *testing.T) {
	a := storeApp(t)
	node, err := a.addManualNodeLocked("trojan://secret@tj.example.com:443?sni=tj.example.com#手动TJ", "")
	if err != nil {
		t.Fatal(err)
	}
	if node.Name != "手动TJ" || node.Type != "trojan" || node.Port != 443 {
		t.Fatalf("unexpected node: %+v", node)
	}

	body, err := os.ReadFile(a.manualPath())
	if err != nil {
		t.Fatalf("the provider file must be written: %v", err)
	}
	for _, want := range []string{"proxies:", "type: trojan", "server: tj.example.com", "password: secret"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("provider file must contain %q:\n%s", want, body)
		}
	}

	// A restarted service must find them again.
	reloaded := &app{home: a.home, store: a.store}
	reloaded.loadManualNodesLocked()
	if len(reloaded.manualNodes) != 1 || reloaded.manualNodes[0].Name != "手动TJ" {
		t.Fatalf("reloaded: %+v", reloaded.manualNodes)
	}
	if reloaded.manualNodes[0].Proxy["password"] != "secret" {
		t.Fatalf("the proxy object must survive the round trip: %+v", reloaded.manualNodes[0].Proxy)
	}
}

func TestManualNodesNeedANameOfTheirOwn(t *testing.T) {
	a := storeApp(t)
	link := "trojan://secret@tj.example.com:443#同名"
	if _, err := a.addManualNodeLocked(link, ""); err != nil {
		t.Fatal(err)
	}
	// The same name twice would make the kernel's list ambiguous.
	if _, err := a.addManualNodeLocked(link, ""); err == nil {
		t.Fatal("a duplicate name must be refused")
	}
	// Renaming on the way in is allowed, and then it is no longer a duplicate.
	if _, err := a.addManualNodeLocked(link, "另一个"); err != nil {
		t.Fatalf("a different name must be accepted: %v", err)
	}

	// A name already used by a subscription node would be shadowed in the list.
	a.cachedNodes = []proxyNode{{Name: "订阅节点"}}
	if _, err := a.addManualNodeLocked(link, "订阅节点"); err == nil {
		t.Fatal("a name already taken by the subscription must be refused")
	}
}

func TestRemoveManualNode(t *testing.T) {
	a := storeApp(t)
	if _, err := a.addManualNodeLocked("trojan://secret@tj.example.com:443#第一个", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := a.addManualNodeLocked("trojan://secret@tj2.example.com:443#第二个", ""); err != nil {
		t.Fatal(err)
	}
	if err := a.removeManualNodeLocked("第一个"); err != nil {
		t.Fatal(err)
	}
	if len(a.manualNodes) != 1 || a.manualNodes[0].Name != "第二个" {
		t.Fatalf("unexpected list: %+v", a.manualNodes)
	}
	if _, err := os.Stat(a.manualPath()); err != nil {
		t.Fatalf("the provider file must still exist while a node is left: %v", err)
	}

	if err := a.removeManualNodeLocked("第二个"); err != nil {
		t.Fatal(err)
	}
	// Nothing left: the provider file goes, so the kernel is never handed an
	// empty provider.
	if _, err := os.Stat(a.manualPath()); !os.IsNotExist(err) {
		t.Fatal("the provider file should have been removed")
	}
	if err := a.removeManualNodeLocked("不存在"); err == nil {
		t.Fatal("removing an unknown node must be refused")
	}
}

func TestManualNodeDetails(t *testing.T) {
	link := "vless://22222222-3333-4444-5555-666666666666@a.example.com:8443" +
		"?security=tls&type=ws&path=%2Fvless&host=a.example.com#VLESS"
	node, err := parseNodeURI(link)
	if err != nil {
		t.Fatal(err)
	}
	details := manualNodeDetails([]manualNode{node})
	entry, ok := details["VLESS"]
	if !ok {
		t.Fatalf("unexpected map: %+v", details)
	}
	if !entry.Manual || entry.Type != "vless" || entry.Network != "ws" || !entry.TLS {
		t.Fatalf("unexpected details: %+v", entry)
	}
	if !entry.WsPath || !entry.WsHost {
		t.Fatalf("ws details: %+v", entry)
	}
}

func TestManualServerDomains(t *testing.T) {
	nodes := []manualNode{
		{Server: "node.example.com"},
		{Server: "1.2.3.4"},
		{Server: "a.b.example.net"},
	}
	entries := manualServerDomains(nodes)
	if len(entries) != 2 {
		t.Fatalf("an IP literal needs no filter entry: %v", entries)
	}
	merged := mergeProxyServerDomains([]string{"+.qos.onl", "+.example.com"}, entries)
	want := []string{"+.example.com", "+.example.net", "+.qos.onl"}
	if strings.Join(merged, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v, want %v", merged, want)
	}
}

func TestAddManualNodeRefusesGarbage(t *testing.T) {
	a := storeApp(t)
	if _, err := a.addManualNodeLocked("http://example.com/sub", ""); err == nil {
		t.Fatal("a subscription URL is not a node link")
	}
	if _, err := a.addManualNodeLocked("", ""); err == nil {
		t.Fatal("an empty link must be refused")
	}
	if len(a.manualNodes) != 0 {
		t.Fatalf("nothing may be stored: %+v", a.manualNodes)
	}
	if _, err := os.Stat(a.manualPath()); !os.IsNotExist(err) {
		t.Fatal("no provider file may be written for a refused node")
	}
}
