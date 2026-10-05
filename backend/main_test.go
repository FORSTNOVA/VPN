package main

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestChooseReachableNode(t *testing.T) {
	nodes := []proxyNode{{Name: "a"}, {Name: "b"}, {Name: "c"}}
	cases := []struct {
		name      string
		delays    map[string]int
		preferred string
		wantName  string
		wantDelay int
	}{
		{
			name:     "picks the lowest reachable delay",
			delays:   map[string]int{"a": 300, "b": 120, "c": 0},
			wantName: "b", wantDelay: 120,
		},
		{
			name:      "preferred node wins even when slower",
			delays:    map[string]int{"a": 300, "b": 120},
			preferred: "a",
			wantName:  "a", wantDelay: 300,
		},
		{
			name:      "unreachable preferred falls back to the best node",
			delays:    map[string]int{"a": 0, "b": 120},
			preferred: "a",
			wantName:  "b", wantDelay: 120,
		},
		{
			name:     "nothing reachable",
			delays:   map[string]int{"a": 0, "b": -1},
			wantName: "", wantDelay: 0,
		},
		{
			name:     "no delays at all",
			delays:   map[string]int{},
			wantName: "", wantDelay: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name, delay := chooseReachableNode(nodes, tc.delays, tc.preferred)
			if name != tc.wantName || delay != tc.wantDelay {
				t.Fatalf("got (%q, %d), want (%q, %d)", name, delay, tc.wantName, tc.wantDelay)
			}
		})
	}
}

func TestSelectedChoice(t *testing.T) {
	cases := []struct {
		mode     string
		selected string
		want     string
	}{
		{"auto", "", autoGroup},
		{"fallback", "", fallbackGroup},
		{"manual", "香港 01", "香港 01"},
		{"", "", autoGroup},
		{"unexpected", "x", autoGroup},
	}
	for _, tc := range cases {
		a := &app{settings: settings{SelectionMode: tc.mode, SelectedNode: tc.selected}}
		if got := a.selectedChoice(); got != tc.want {
			t.Errorf("mode %q selected %q = %q, want %q", tc.mode, tc.selected, got, tc.want)
		}
	}
}

func TestGeneratedConfig(t *testing.T) {
	config := generatedConfig(configOptions{
		MixedPort:            12345,
		ControllerPort:       12346,
		ProviderPath:         `providers\subscription.yaml`,
		ControllerSecret:     "controller-secret",
		SubscriptionProvider: true,
		Rules: []string{
			"DOMAIN,a.example,SmartVPN",
			"IP-CIDR,10.0.0.0/8,DIRECT,no-resolve",
		},
	})
	required := []string{
		"mixed-port: 12345",
		"allow-lan: false",
		"bind-address: 127.0.0.1",
		"external-controller: 127.0.0.1:12346",
		// The connection list names the process behind each connection.
		"find-process-mode: always",
		`secret: "controller-secret"`,
		`path: "providers\\subscription.yaml"`,
		"name: SmartVPNAuto",
		"type: url-test",
		"name: SmartVPNFallback",
		"type: fallback",
		"- name: SmartVPN\n    type: select",
		`  - "DOMAIN,a.example,SmartVPN"`,
		`  - "IP-CIDR,10.0.0.0/8,DIRECT,no-resolve"`,
	}
	for _, want := range required {
		if !strings.Contains(config, want) {
			t.Errorf("generated config must contain %q\n---\n%s", want, config)
		}
	}
	if !strings.HasSuffix(config, "  - MATCH,SmartVPN\n") {
		t.Errorf("generated config must end with the catch-all rule: %q", config[len(config)-40:])
	}
	if strings.Index(config, "MATCH,SmartVPN") < strings.Index(config, "IP-CIDR,10.0.0.0/8") {
		t.Error("the catch-all rule must come after the subscription rules")
	}
	// QUIC has to be refused before any subscription rule can claim the packet,
	// otherwise a client that tries HTTP/3 hangs on a node that cannot relay UDP.
	if quicAt, ruleAt := strings.Index(config, quicRejectRule), strings.Index(config, "DOMAIN,a.example"); quicAt == -1 {
		t.Error("the generated config must refuse UDP:443 so clients fall back to TCP")
	} else if quicAt > ruleAt {
		t.Error("the QUIC reject rule must come before the subscription rules")
	}
}

// The order is the whole point of the domestic rules: a country's own addresses
// have to be decided before anything is handed to the proxy, and the refusal of
// QUIC has to sit between the two — after the rules that serve it directly, and
// before the rules that would otherwise hand the datagram to a node that may not
// relay it.
func TestGeneratedConfigPutsDomesticRulesBeforeTheProxy(t *testing.T) {
	config := generatedConfig(configOptions{
		MixedPort: 1, ControllerPort: 2, ProviderPath: "p.yaml", ControllerSecret: "s",
		ChinaDirect: true,
		Rules: []string{
			"DOMAIN-SUFFIX,proxy.example,SmartVPN",
			"DOMAIN-SUFFIX,local,DIRECT",
			"DOMAIN-SUFFIX,ads.example,REJECT",
		},
	})
	order := []string{
		`  - "DOMAIN-SUFFIX,local,DIRECT"`,
		`  - "DOMAIN-SUFFIX,ads.example,REJECT"`,
		"  - " + strconv.Quote(chinaDirectRule),
		"  - " + strconv.Quote(quicRejectRule),
		`  - "DOMAIN-SUFFIX,proxy.example,SmartVPN"`,
		"  - MATCH,SmartVPN",
	}
	at := 0
	for _, want := range order {
		found := strings.Index(config, want)
		if found < 0 {
			t.Fatalf("generated config must contain %q\n---\n%s", want, config)
		}
		if found < at {
			t.Fatalf("%q is out of order\n---\n%s", want, config)
		}
		at = found
	}
	for _, want := range []string{
		"rule-providers:",
		chinaProviderName + ":",
		"behavior: ipcidr",
		"format: text",
		`path: "providers\\` + chinaIPListFile + `"`,
	} {
		if !strings.Contains(config, want) {
			t.Errorf("generated config must contain %q\n---\n%s", want, config)
		}
	}
}

func TestGeneratedConfigReferencesNoListWhenThereIsNone(t *testing.T) {
	config := generatedConfig(configOptions{
		MixedPort: 1, ControllerPort: 2, ProviderPath: "p.yaml", ControllerSecret: "s",
		Rules: []string{"DOMAIN-SUFFIX,proxy.example,SmartVPN"},
	})
	// A rule-provider whose file is missing is a configuration the kernel refuses
	// to start with, so it must not be mentioned at all.
	for _, unwanted := range []string{"rule-providers:", chinaProviderName, chinaDirectRule} {
		if strings.Contains(config, unwanted) {
			t.Errorf("a config with no list must not mention %q\n---\n%s", unwanted, config)
		}
	}
	if quicAt, ruleAt := strings.Index(config, quicRejectRule), strings.Index(config, "proxy.example"); quicAt < 0 || quicAt > ruleAt {
		t.Errorf("the QUIC reject rule must still come before the proxy rules\n---\n%s", config)
	}
}

func TestGeneratedConfigWithoutRules(t *testing.T) {
	config := generatedConfig(configOptions{MixedPort: 1, ControllerPort: 2, ProviderPath: "p.yaml", ControllerSecret: "s", SubscriptionProvider: true})
	if !strings.HasSuffix(config, "  - MATCH,SmartVPN\n") {
		t.Fatalf("expected the catch-all last: %q", config)
	}
	if !strings.Contains(config, quicRejectRule) {
		t.Fatal("even with no subscription rules the config must refuse QUIC")
	}
}

func TestGeneratedTUNConfigExcludesNodeDomains(t *testing.T) {
	options := configOptions{
		MixedPort: 1, ControllerPort: 2, ProviderPath: "p.yaml", ControllerSecret: "s",
		TUN: true, ProxyServerDomains: []string{"+.qos.onl"},
	}
	config := generatedConfig(options)
	for _, want := range []string{`    - "+.qos.onl"`, "proxy-server-nameserver"} {
		if !strings.Contains(config, want) {
			t.Errorf("the TUN configuration must contain %q\n---\n%s", want, config)
		}
	}
	// The system-proxy configuration has no fake-ip resolver, so it must not
	// carry a filter that would never be used.
	plain := generatedConfig(configOptions{MixedPort: 1, ControllerPort: 2, ProviderPath: "p.yaml", ControllerSecret: "s"})
	if strings.Contains(plain, "fake-ip") {
		t.Fatalf("a system-proxy configuration must not configure fake-ip:\n%s", plain)
	}
}

func TestGeneratedConfigProviders(t *testing.T) {
	options := configOptions{MixedPort: 1, ControllerPort: 2, ProviderPath: "p.yaml", ControllerSecret: "s"}

	// Only pasted nodes: the groups must not be pointed at a subscription file
	// that does not exist, because the kernel refuses to start without it.
	manualOnly := options
	manualOnly.ManualProvider = true
	config := generatedConfig(manualOnly)
	if !strings.Contains(config, manualProviderName+":") {
		t.Fatalf("the manual provider must be defined:\n%s", config)
	}
	if strings.Contains(config, "SmartVPNSubscription") {
		t.Fatalf("a machine without a subscription must not reference one:\n%s", config)
	}
	if got := strings.Count(config, "      - "+manualProviderName); got != 3 {
		t.Fatalf("each of the three groups must draw from the manual provider, got %d:\n%s", got, config)
	}

	// Both: a pasted node is measured, health checked and failed over exactly
	// like a subscription node, because it is in the same groups.
	both := options
	both.SubscriptionProvider = true
	both.ManualProvider = true
	config = generatedConfig(both)
	for _, provider := range []string{"SmartVPNSubscription", manualProviderName} {
		if got := strings.Count(config, "      - "+provider); got != 3 {
			t.Errorf("each group must list %s once, got %d:\n%s", provider, got, config)
		}
	}
}

// Every probe a provider or a group runs is a connection to the subscription's
// own servers, from the address the user is paying with. A subscription that
// limits how many connections one user may open sees the difference between one
// measuring loop and three, so the generated configuration has to carry exactly
// one — the app's own sweep — at a cadence that does not hammer it.
func TestGeneratedConfigCarriesOneMeasuringLoop(t *testing.T) {
	config := generatedConfig(configOptions{
		MixedPort: 1, ControllerPort: 2, ProviderPath: "p.yaml", ControllerSecret: "s",
		SubscriptionProvider: true, ManualProvider: true,
	})
	if strings.Contains(config, "enable: true") {
		t.Errorf("no health check may be enabled in the generated configuration:\n%s", config)
	}
	if got := strings.Count(config, providerHealthCheck); got != 2 {
		t.Errorf("both providers must carry the disabled health check, got %d:\n%s", got, config)
	}
	if !strings.Contains(config, "interval: "+strconv.Itoa(testIntervalSec)) {
		t.Errorf("the automatic groups must measure on the slow cadence:\n%s", config)
	}
	if strings.Contains(config, "interval: 60") {
		t.Errorf("no group may still measure every minute:\n%s", config)
	}
}

// The kernel's location is not stored as a default: an empty setting means "the
// copy that came with the package, otherwise the profile", resolved when it is
// used rather than written down, so a folder that is moved keeps working.
func TestLoadSettingsLeavesTheKernelPathUnset(t *testing.T) {
	a := &app{home: t.TempDir()}
	a.loadSettings()
	if a.settings.MihomoPath != "" {
		t.Fatalf("MihomoPath = %q, want it left unset", a.settings.MihomoPath)
	}
	if filepath.Dir(a.mihomoPath()) != a.home {
		t.Fatalf("the kernel resolves beside the rest of the service's files, got %q", a.mihomoPath())
	}
	if filepath.Base(a.mihomoPath()) != "mihomo.exe" {
		t.Fatalf("mihomoPath = %q, want the kernel's own name", a.mihomoPath())
	}

	// A path the user chose wins over the default.
	if err := os.WriteFile(filepath.Join(a.home, "settings.json"),
		[]byte(`{"mihomoPath":"D:\\tools\\mihomo.exe"}`), 0600); err != nil {
		t.Fatal(err)
	}
	a.loadSettings()
	if a.settings.MihomoPath != `D:\tools\mihomo.exe` {
		t.Fatalf("MihomoPath = %q, want the saved value", a.settings.MihomoPath)
	}
}

func TestSubscriptionHash(t *testing.T) {
	first := subscriptionHash("https://example.com/sub?token=abc")
	if first != subscriptionHash("https://example.com/sub?token=abc") {
		t.Fatal("the same subscription URL must hash identically")
	}
	if first == subscriptionHash("https://example.com/sub?token=def") {
		t.Fatal("different subscription URLs must hash differently")
	}
	if len(first) != 64 {
		t.Fatalf("expected a hex sha256 digest, got %q", first)
	}
}

func TestValidateSubscriptionURL(t *testing.T) {
	valid := []string{"https://example.com/sub", "http://example.com/sub?token=abc", "https://example.com"}
	for _, raw := range valid {
		if err := validateSubscriptionURL(raw); err != nil {
			t.Errorf("%q should be accepted: %v", raw, err)
		}
	}
	invalid := []string{"", "not a url", "ftp://example.com/sub", "file:///c:/sub.yaml", "example.com/sub", "://bad"}
	for _, raw := range invalid {
		if err := validateSubscriptionURL(raw); err == nil {
			t.Errorf("%q should be rejected", raw)
		}
	}
}

func TestAnalyzeSubscriptionPayloadEmpty(t *testing.T) {
	report := analyzeSubscriptionPayload(nil)
	if report.Present {
		t.Error("an empty body must not be reported as present")
	}
	if report.Format != "empty" {
		t.Errorf("format = %q, want %q", report.Format, "empty")
	}
	if report.Selectable != 0 {
		t.Errorf("selectable = %d, want 0", report.Selectable)
	}
}

func TestAnalyzeSubscriptionPayloadClashYAML(t *testing.T) {
	body := []byte(`
proxies:
  - name: "香港 01"
    type: vmess
    network: ws
    tls: true
    ws-opts:
      path: /ws
      headers:
        Host: h.example.com
  - name: "日本 02"
    type: trojan
`)
	report := analyzeSubscriptionPayload(body)
	if report.Format != "clash-yaml" {
		t.Errorf("format = %q, want %q", report.Format, "clash-yaml")
	}
	if report.Selectable != 2 {
		t.Errorf("selectable = %d, want 2", report.Selectable)
	}
	if report.ValidVmess != 1 {
		t.Errorf("validVmess = %d, want 1", report.ValidVmess)
	}
	if report.Networks["ws"] != 1 || report.Networks["tcp/default"] != 1 {
		t.Errorf("networks = %v, want one ws and one tcp/default", report.Networks)
	}
	if report.TlsModes["tls"] != 1 || report.TlsModes["none"] != 1 {
		t.Errorf("tlsModes = %v, want one tls and one none", report.TlsModes)
	}
	if report.WsHostSet != 1 || report.WsPathSet != 1 {
		t.Errorf("wsHostSet = %d, wsPathSet = %d, want 1 and 1", report.WsHostSet, report.WsPathSet)
	}
	for scheme, count := range report.UriCounts {
		if count != 0 {
			t.Errorf("uriCounts[%s] = %d, want 0 for a YAML payload", scheme, count)
		}
	}
}

func TestAnalyzeSubscriptionPayloadURIList(t *testing.T) {
	vmess := base64.StdEncoding.EncodeToString([]byte(
		`{"ps":"节点 A","net":"ws","tls":"tls","host":"h.example.com","path":"/ws"}`))
	body := []byte("vmess://" + vmess + "\nvless://uuid@1.2.3.4:443?security=tls#节点 B\n")
	report := analyzeSubscriptionPayload(body)
	if report.Format != "uri-list" {
		t.Errorf("format = %q, want %q", report.Format, "uri-list")
	}
	if report.Selectable != 2 {
		t.Errorf("selectable = %d, want 2", report.Selectable)
	}
	if report.UriCounts["vmess"] != 1 || report.UriCounts["vless"] != 1 {
		t.Errorf("uriCounts = %v, want one vmess and one vless", report.UriCounts)
	}
	if report.UriCounts["ss"] != 0 || report.UriCounts["trojan"] != 0 {
		t.Errorf("uriCounts = %v, want no ss or trojan entries", report.UriCounts)
	}
	if report.ValidVmess != 1 {
		t.Errorf("validVmess = %d, want 1", report.ValidVmess)
	}
	if report.Networks["ws"] != 1 || report.TlsModes["tls"] != 1 {
		t.Errorf("networks = %v, tlsModes = %v, want the vmess websocket details", report.Networks, report.TlsModes)
	}
	if report.WsHostSet != 1 || report.WsPathSet != 1 {
		t.Errorf("wsHostSet = %d, wsPathSet = %d, want 1 and 1", report.WsHostSet, report.WsPathSet)
	}
}

func TestAnalyzeSubscriptionPayloadCountsInfoEntries(t *testing.T) {
	vmess := base64.StdEncoding.EncodeToString([]byte(
		`{"ps":"剩余流量：100GB","net":"tcp","tls":""}`))
	report := analyzeSubscriptionPayload([]byte("vmess://" + vmess + "\n"))
	if report.ValidVmess != 1 {
		t.Errorf("validVmess = %d, want 1", report.ValidVmess)
	}
	if report.InfoEntries != 1 {
		t.Errorf("infoEntries = %d, want 1", report.InfoEntries)
	}
	if report.Selectable != 0 {
		t.Errorf("selectable = %d, want 0 for an announcement-only payload", report.Selectable)
	}
}

func TestAnalyzeSubscriptionPayloadBase64Formats(t *testing.T) {
	yamlBody := "proxies:\n  - name: \"香港 01\"\n    type: vmess\n"
	uriBody := "trojan://secret@1.2.3.4:443#节点 A\n"
	cases := []struct {
		name  string
		body  string
		want  string
		count int
	}{
		{"base64 clash yaml", base64.StdEncoding.EncodeToString([]byte(yamlBody)), "base64-clash-yaml", 1},
		{"base64 uri list", base64.StdEncoding.EncodeToString([]byte(uriBody)), "base64-uri-list", 1},
		{"unknown", "hello world", "unknown", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report := analyzeSubscriptionPayload([]byte(tc.body))
			if report.Format != tc.want {
				t.Errorf("format = %q, want %q", report.Format, tc.want)
			}
			if report.Selectable != tc.count {
				t.Errorf("selectable = %d, want %d", report.Selectable, tc.count)
			}
		})
	}
}

func TestDecodeJSON(t *testing.T) {
	type sample struct {
		Name string `json:"name"`
	}
	var ok sample
	if err := decodeJSON(strings.NewReader(`{"name":"a"}`), &ok); err != nil {
		t.Fatalf("valid JSON rejected: %v", err)
	}
	if ok.Name != "a" {
		t.Fatalf("name = %q, want %q", ok.Name, "a")
	}

	var unknown sample
	if err := decodeJSON(strings.NewReader(`{"name":"a","extra":1}`), &unknown); err == nil {
		t.Fatal("unknown fields must be rejected")
	}

	var malformed sample
	if err := decodeJSON(strings.NewReader(`{"name":`), &malformed); err == nil {
		t.Fatal("malformed JSON must be rejected")
	}

	var oversized sample
	big := `{"name":"` + strings.Repeat("a", 128*1024) + `"}`
	if err := decodeJSON(strings.NewReader(big), &oversized); err == nil {
		t.Fatal("a body over the request limit must be rejected")
	}
}

func TestFreeLoopbackPort(t *testing.T) {
	port, err := freeLoopbackPort()
	if err != nil {
		t.Fatalf("freeLoopbackPort: %v", err)
	}
	if port < 1 || port > 65535 {
		t.Fatalf("port %d is out of range", port)
	}
}

func TestProfileMatchesSubscription(t *testing.T) {
	home := t.TempDir()
	subscriptionURL := "https://example.com/sub?token=abc"
	a := &app{home: home, settings: settings{SubscriptionURL: subscriptionURL}}
	if a.profileMatchesSubscription() {
		t.Fatal("a missing profile must not match")
	}
	if err := os.MkdirAll(filepath.Dir(a.profilePath()), 0700); err != nil {
		t.Fatal(err)
	}
	profile := "proxies:\n  - name: \"香港 01\"\n    type: vmess\n"
	if err := os.WriteFile(a.profilePath(), []byte(profile), 0600); err != nil {
		t.Fatal(err)
	}
	if a.profileMatchesSubscription() {
		t.Fatal("a profile without a recorded source must not match")
	}
	if err := os.WriteFile(a.profilePath()+".source", []byte(subscriptionHash(subscriptionURL)), 0600); err != nil {
		t.Fatal(err)
	}
	if !a.profileMatchesSubscription() {
		t.Fatal("a profile recorded against the current subscription must match")
	}
	if err := os.WriteFile(a.profilePath()+".source", []byte(subscriptionHash("https://other.example/sub")), 0600); err != nil {
		t.Fatal(err)
	}
	if a.profileMatchesSubscription() {
		t.Fatal("a profile recorded against another subscription must not match")
	}
}

func TestProfileMatchesSubscriptionIgnoresNodeFreeProfile(t *testing.T) {
	home := t.TempDir()
	subscriptionURL := "https://example.com/sub"
	a := &app{home: home, settings: settings{SubscriptionURL: subscriptionURL}}
	if err := os.MkdirAll(filepath.Dir(a.profilePath()), 0700); err != nil {
		t.Fatal(err)
	}
	announcementsOnly := "proxies:\n  - name: \"剩余流量：100GB\"\n    type: vmess\n"
	if err := os.WriteFile(a.profilePath(), []byte(announcementsOnly), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.profilePath()+".source", []byte(subscriptionHash(subscriptionURL)), 0600); err != nil {
		t.Fatal(err)
	}
	if a.profileMatchesSubscription() {
		t.Fatal("a profile with no selectable nodes must not count as a usable match")
	}
}

func TestSweepStaleConfigs(t *testing.T) {
	home := t.TempDir()
	a := &app{home: home}
	own := a.configPath()
	stale := []string{
		filepath.Join(home, "config-1.yaml"),
		filepath.Join(home, "config-99999.yaml"),
	}
	keep := []string{
		filepath.Join(home, "settings.json"),
		filepath.Join(home, "nodes-cache.json"),
		filepath.Join(home, "mihomo.log"),
		filepath.Join(home, "proxy-backup.json"),
		filepath.Join(home, "providers", "config-notes.yaml"),
	}
	for _, path := range append(append([]string{own}, stale...), keep...) {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("mixed-port: 1\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	a.sweepStaleConfigs()
	if _, err := os.Stat(own); err != nil {
		t.Fatalf("the current run's config must be kept: %v", err)
	}
	for _, path := range stale {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s should have been swept", filepath.Base(path))
		}
	}
	for _, path := range keep {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s must not be touched: %v", filepath.Base(path), err)
		}
	}
}

func TestRemoveConfigLocked(t *testing.T) {
	home := t.TempDir()
	a := &app{home: home}
	if err := os.WriteFile(a.configPath(), []byte("mixed-port: 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	a.removeConfigLocked()
	if _, err := os.Stat(a.configPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the generated config should be removed when Mihomo stops")
	}
	a.removeConfigLocked() // Removing a missing file must stay harmless.
}

func TestUpdateSettingsKeepsTheRegionLock(t *testing.T) {
	a := &app{
		home: t.TempDir(), health: map[string]nodeHealth{}, regions: map[string]regionRecord{},
		settings: settings{
			SubscriptionURL: "https://example.com/sub",
			SelectionMode:   "auto", ConnectionMode: modeTUN, WintunPath: `C:\dll\wintun.dll`,
		},
		lockedRegion: "JP", lockedNode: "jp-a",
	}
	// The UI saves settings right before every connect, and it does not resend
	// the mode or the lock, so all of them have to survive the overwrite. The
	// subscription is the one that was already in use.
	body := `{"subscriptionUrl":"https://example.com/sub","mihomoPath":"C:\\tools\\mihomo.exe"}`
	recorder := httptest.NewRecorder()
	a.updateSettings(recorder, httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(body)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	if a.settings.LockedRegion != "JP" || a.settings.LockedNode != "jp-a" {
		t.Fatalf("the lock was dropped: region=%q node=%q", a.settings.LockedRegion, a.settings.LockedNode)
	}
	if a.settings.ConnectionMode != modeTUN || a.settings.WintunPath == "" {
		t.Fatalf("an omitted field must not be reset: %+v", a.settings)
	}

	reloaded := &app{home: a.home, health: map[string]nodeHealth{}, regions: map[string]regionRecord{}}
	reloaded.loadSettings()
	if reloaded.settings.LockedRegion != "JP" || reloaded.settings.LockedNode != "jp-a" {
		t.Fatalf("the lock must survive a restart: %+v", reloaded.settings)
	}
	if reloaded.settings.ConnectionMode != modeTUN {
		t.Fatalf("the mode must survive a restart: %+v", reloaded.settings)
	}
}

func TestDisconnectWithoutAConnectionLeavesTheRegistryAlone(t *testing.T) {
	// The proxy helper is not installed next to this test binary, so a stray
	// PowerShell restore call would fail the request instead of reporting ok.
	a := &app{
		home: t.TempDir(), health: map[string]nodeHealth{}, regions: map[string]regionRecord{},
	}
	recorder := httptest.NewRecorder()
	a.disconnect(recorder, httptest.NewRequest(http.MethodPost, "/api/disconnect", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
}

func TestCachedNodesRoundTrip(t *testing.T) {
	home := t.TempDir()
	subscriptionURL := "https://example.com/sub?token=abc"
	a := &app{home: home, settings: settings{SubscriptionURL: subscriptionURL, SelectedNode: "日本 02"}}
	nodes := []proxyNode{
		{Name: "香港 01", Type: "vmess", Network: "ws", TLS: true, WsHost: true, WsPath: true, Alive: true},
		{Name: "日本 02", Type: "trojan"},
	}
	if err := a.saveCachedNodesLocked(nodes); err != nil {
		t.Fatalf("saveCachedNodesLocked: %v", err)
	}

	reloaded := &app{home: home, settings: a.settings}
	reloaded.loadCachedNodes()
	if len(reloaded.cachedNodes) != 2 {
		t.Fatalf("got %d cached nodes, want 2", len(reloaded.cachedNodes))
	}
	first, second := reloaded.cachedNodes[0], reloaded.cachedNodes[1]
	if first.Name != "香港 01" || !first.TLS || !first.WsHost || !first.WsPath || !first.Alive {
		t.Fatalf("cached node lost its details: %+v", first)
	}
	if !second.Selected || first.Selected {
		t.Fatalf("selection must follow the saved node: %+v", reloaded.cachedNodes)
	}
}

func TestCachedNodesDiscardedWhenSubscriptionChanges(t *testing.T) {
	home := t.TempDir()
	a := &app{home: home, settings: settings{SubscriptionURL: "https://example.com/sub?token=abc"}}
	if err := a.saveCachedNodesLocked([]proxyNode{{Name: "香港 01", Type: "vmess"}}); err != nil {
		t.Fatal(err)
	}
	other := &app{home: home, settings: settings{SubscriptionURL: "https://example.com/sub?token=def"}}
	other.loadCachedNodes()
	if len(other.cachedNodes) != 0 {
		t.Fatalf("a cache from another subscription must be dropped, got %+v", other.cachedNodes)
	}
	if _, err := os.Stat(other.cachedNodesPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the stale cache file should be deleted")
	}
}

func TestClearCachedNodesLocked(t *testing.T) {
	home := t.TempDir()
	a := &app{home: home, settings: settings{SubscriptionURL: "https://example.com/sub"}}
	if err := a.saveCachedNodesLocked([]proxyNode{{Name: "香港 01", Type: "vmess"}}); err != nil {
		t.Fatal(err)
	}
	a.cachedNodes = []proxyNode{{Name: "香港 01"}}
	a.clearCachedNodesLocked()
	if a.cachedNodes != nil {
		t.Fatalf("expected the in-memory list to be cleared, got %+v", a.cachedNodes)
	}
	if _, err := os.Stat(a.cachedNodesPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the cache file should be deleted")
	}
}

// A request carries only what the caller sent. Switching the connection mode —
// which is all the mode selector sends — must leave the subscription and
// everything derived from it alone: the cached profile is deleted when the
// subscription changes, so reading an omitted field as "clear it" destroyed a
// working setup on a click.
func TestUpdateSettingsChangesOnlyWhatTheRequestCarries(t *testing.T) {
	a := storeApp(t)
	a.settings = settings{
		SubscriptionURL: "https://example.com/sub",
		MihomoPath:      `C:\tools\mihomo.exe`,
		WintunPath:      `C:\dll\wintun.dll`,
		ConnectionMode:  modeTUN,
		SelectionMode:   "manual",
		SelectedNode:    "节点一",
	}
	a.lockedRegion, a.lockedNode = "JP", "jp-a"
	if err := os.MkdirAll(filepath.Dir(a.profilePath()), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.profilePath(), []byte("proxies:\n  - name: a\n"), 0600); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	a.updateSettings(recorder, httptest.NewRequest(http.MethodPut, "/api/settings",
		strings.NewReader(`{"connectionMode":"system-proxy"}`)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}

	if a.settings.ConnectionMode != modeSystemProxy {
		t.Fatalf("the mode that was sent must be applied: %+v", a.settings)
	}
	if a.settings.SubscriptionURL != "https://example.com/sub" {
		t.Fatalf("the subscription must survive a mode change: %q", a.settings.SubscriptionURL)
	}
	if a.settings.MihomoPath == "" || a.settings.WintunPath == "" {
		t.Fatalf("the paths must survive too: %+v", a.settings)
	}
	if a.settings.SelectedNode != "节点一" || a.settings.SelectionMode != "manual" {
		t.Fatalf("the selection is not this route's to change: %+v", a.settings)
	}
	if a.lockedRegion != "JP" || a.lockedNode != "jp-a" {
		t.Fatalf("the lock is not this route's to change: %q %q", a.lockedRegion, a.lockedNode)
	}
	if _, err := os.Stat(a.profilePath()); err != nil {
		t.Fatalf("the cached profile must survive a mode change: %v", err)
	}

	// An empty string is a caller asking for that field to be cleared, which is
	// not the same thing as leaving it out.
	recorder = httptest.NewRecorder()
	a.updateSettings(recorder, httptest.NewRequest(http.MethodPut, "/api/settings",
		strings.NewReader(`{"subscriptionUrl":"","mihomoPath":""}`)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	if a.settings.SubscriptionURL != "" || a.settings.MihomoPath != "" {
		t.Fatalf("an empty field means cleared: %+v", a.settings)
	}
	if a.settings.WintunPath == "" || a.settings.ConnectionMode != modeSystemProxy {
		t.Fatalf("only the fields that were sent may change: %+v", a.settings)
	}
	if _, err := os.Stat(a.profilePath()); !os.IsNotExist(err) {
		t.Fatal("clearing the subscription must drop the profile that belonged to it")
	}
}

func TestUpdateSettingsRefusesWhatItCannotApply(t *testing.T) {
	a := storeApp(t)
	a.settings = settings{SubscriptionURL: "https://example.com/sub", ConnectionMode: modeTUN}
	for name, body := range map[string]string{
		"an unknown mode":      `{"connectionMode":"tun2"}`,
		"an empty mode":        `{"connectionMode":""}`,
		"a non-dll wintun":     `{"wintunPath":"C:\dll\wintun.exe"}`,
		"a non-exe kernel":     `{"mihomoPath":"C:\tools\mihomo.dll"}`,
		"a subscription that is not one": `{"subscriptionUrl":"not a url"}`,
		"not an object":        `[1,2,3]`,
	} {
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			a.updateSettings(recorder, httptest.NewRequest(http.MethodPut, "/api/settings",
				strings.NewReader(body)))
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("code = %d, want 400: %s", recorder.Code, recorder.Body.String())
			}
			if a.settings.SubscriptionURL != "https://example.com/sub" ||
				a.settings.ConnectionMode != modeTUN {
				t.Fatalf("a refused request must change nothing: %+v", a.settings)
			}
		})
	}
}
