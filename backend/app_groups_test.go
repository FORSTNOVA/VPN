package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const groupFixture = `[{"id":"g","name":" Work ","apps":[" App.exe ","app.exe","App.exe"],"enabled":true,"targetType":"direct"}]`

func TestAppGroupValidation(t *testing.T) {
	for _, tc := range []struct {
		android bool
		want    []string
	}{{false, []string{"App.exe"}}, {true, []string{"App.exe", "app.exe"}}} {
		got, err := decodeAppGroups([]byte(groupFixture), tc.android)
		if err != nil || !slices.Equal(got[0].Apps, tc.want) || got[0].Name != "Work" {
			t.Fatalf("android=%v: %+v %v", tc.android, got, err)
		}
	}
	for _, bad := range []string{
		"null", `{}`, strings.Replace(groupFixture, `"g"`, `"x\ny"`, 1),
		strings.Replace(groupFixture, `"g"`, `" "`, 1), strings.Replace(groupFixture, " Work ", "x,y", 1),
		strings.Replace(groupFixture, "App.exe", "*foo", 1), strings.Replace(groupFixture, "App.exe", `foo\u007f`, 1),
		strings.Replace(groupFixture, `[" App.exe ","app.exe","App.exe"]`, `[]`, 1),
		strings.Replace(groupFixture, `"direct"`, `"node"`, 1), strings.Replace(groupFixture, `"direct"`, `"bad"`, 1),
		strings.Replace(groupFixture, `"enabled":true,`, "", 1), strings.Replace(groupFixture, `"enabled":true,`, `"other":false,"enabled":true,`, 1),
	} {
		if _, err := decodeAppGroups([]byte(bad), false); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func TestAppGroupSettingsHTTP(t *testing.T) {
	a := storeApp(t)
	a.proxyStateHook = func() (bool, string, error) { return false, "", nil }
	a.proxyOwnerHook = func() (proxyOwner, bool) { return proxyOwner{}, false }
	state := func() json.RawMessage {
		t.Helper()
		w := httptest.NewRecorder()
		a.state(w, httptest.NewRequest("GET", "/api/state", nil))
		var body map[string]json.RawMessage
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil {
			t.Fatalf("state: %d %s", w.Code, w.Body.String())
		}
		return body["appGroups"]
	}
	update := func(body string, status int) {
		t.Helper()
		w := httptest.NewRecorder()
		a.updateSettings(w, httptest.NewRequest("PUT", "/api/settings", strings.NewReader(body)))
		if w.Code != status {
			t.Fatalf("update: %d %s", w.Code, w.Body.String())
		}
	}
	if got := string(state()); got != "[]" {
		t.Fatal(got)
	}
	update(`{"appGroups":`+groupFixture+`}`, 200)
	update(`{"appGroups":null}`, 400)
	update(`{"dnsProtection":true}`, 200)
	// Discard the in-memory graph to exercise the persisted document.
	a.appGroups = nil
	a.loadSettings()
	var got appGroups
	if err := json.Unmarshal(state(), &got); err != nil || len(got) != 1 || !got[0].Enabled || got[0].ID != "g" {
		t.Fatal(got, err)
	}
	before := a.settingsLocked().AppGroups
	a.kernel = &settingsContractRunningKernel{}
	update(`{"appGroups":[]}`, 409)
	a.kernel = nil
	if !reflect.DeepEqual(before, a.settingsLocked().AppGroups) {
		t.Fatal("conflicting patch mutated settings")
	}
	update(`{"appGroups":[]}`, 200)
	disk, err := os.ReadFile(filepath.Join(a.home, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(disk, &doc); err != nil || string(doc["appGroups"]) != "[]" || string(state()) != "[]" {
		t.Fatal("clear", err)
	}
}

func TestAppGroupConfig(t *testing.T) {
	profile := []byte("proxies:\n- {name: sub, type: socks5, server: localhost, port: 9000}\n- {name: SmartVPN-Group-1}\n")
	manual := []manualNode{{Proxy: map[string]any{"name": "local", "type": "socks5", "server": "localhost", "port": 9001}}}
	o := configOptions{TUN: true, ChinaDirect: true, SubscriptionProvider: true, ManualProvider: true, SourceRuleOrder: true, Rules: []string{"DOMAIN,example.invalid,SmartVPN"}, dnsRelayAddr: "127.0.0.1:8999", trafficPolicy: trafficPolicy{DomesticWeb: true, DNSProtection: true, GameBypass: []string{"game.exe"}, AppGroups: appGroups{
		{Name: "one", Apps: []string{"one.exe"}, Enabled: true, TargetType: "node", TargetNode: "sub"},
		{Name: "two", Apps: []string{"two.exe"}, Enabled: true, TargetType: "node", TargetNode: "local"},
		{Apps: []string{"direct.exe"}, Enabled: true, TargetType: "direct"},
		{Apps: []string{"reject.exe"}, Enabled: true, TargetType: "reject"},
		{Apps: []string{"disabled.exe"}, TargetType: "node", TargetNode: "gone"},
	}}}
	routes, err := buildAppRouting(o, profile, manual, nil)
	if err != nil {
		t.Fatal(err)
	}
	o.appRoutes = routes
	var cfg struct {
		ProcessMode string `yaml:"find-process-mode"`
		Groups      []struct {
			Name    string
			Type    string
			Proxies []string
		} `yaml:"proxy-groups"`
		Rules   []string
		Proxies []map[string]any
	}
	if err := yaml.Unmarshal([]byte(generatedConfig(o)), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.ProcessMode != "always" || len(cfg.Proxies) != 2 || len(cfg.Groups) != 5 {
		t.Fatal(cfg)
	}
	for i, node := range []string{"sub", "local"} {
		g := cfg.Groups[3+i]
		if g.Type != "select" || !slices.Equal(g.Proxies, []string{node}) || cfg.Proxies[i]["name"] != node {
			t.Fatal(g)
		}
	}
	last := -1
	order := []string{dohIPs[0] + "/32", "DST-PORT,53,REJECT"}
	if runtime.GOOS == "windows" {
		order = append(order, "PROCESS-NAME,game.exe")
	}
	for _, part := range append(order, "PROCESS-NAME,one.exe,SmartVPN-Group-2", "PROCESS-NAME,two.exe,SmartVPN-Group-3", "PROCESS-NAME,direct.exe,DIRECT", "PROCESS-NAME,reject.exe,REJECT", "DOMAIN,example.invalid", "RULE-SET,"+chinaProviderName, "MATCH,SmartVPN") {
		i := slices.IndexFunc(cfg.Rules, func(s string) bool { return strings.Contains(s, part) })
		if i <= last {
			t.Fatalf("missing or misplaced %s in %v", part, cfg.Rules)
		}
		last = i
	}
	a := storeApp(t)
	a.appGroups = appGroups{{Name: "missing", Apps: []string{"x"}, Enabled: true, TargetType: "node", TargetNode: "gone"}}
	if _, err := a.kernelConfigOptions(profile, true); err == nil || !strings.Contains(err.Error(), "gone") {
		t.Fatalf("missing target: %v", err)
	}
}

// Exercise Mihomo's actual proxy namespace and provider loading without starting a tunnel.
func TestAppGroupPackagedConfig(t *testing.T) {
	kernel := os.Getenv("SMARTVPN_TEST_MIHOMO")
	if kernel == "" {
		t.Skip("set SMARTVPN_TEST_MIHOMO to validate with the packaged kernel")
	}
	a := storeApp(t)
	profile := []byte("proxies:\n  - {name: alpha, type: socks5, server: 127.0.0.1, port: 9999}\n  - {name: exit, type: socks5, server: 127.0.0.1, port: 9998}\n")
	testEditSettings(a, func(s *settings) {
		s.ConnectionMode = modeSystemProxy
		s.Chains = map[string][]string{"fixture chain": {"alpha", "exit"}}
		s.AppGroups = appGroups{
			{ID: "g", Name: "Fixture", Enabled: true, Apps: []string{"fixture.exe"}, TargetType: "node", TargetNode: "alpha"},
			{ID: "c", Name: "Chain", Enabled: true, Apps: []string{"chain.exe"}, TargetType: "chain", TargetChainID: "fixture chain"},
		}
	})
	opts, err := a.kernelConfigOptions(profile, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(a.home, "providers"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.home, opts.ProviderPath), profile, 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(a.home, "app-group.yaml")
	if err := os.WriteFile(path, []byte(generatedConfig(opts)), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(kernel, "-t", "-d", a.home, "-f", path).CombinedOutput()
	if err != nil {
		t.Fatalf("mihomo -t: %v\n%s", err, out)
	}
	t.Log(strings.TrimSpace(string(out)))
}

func TestAppGroupsShareSavedChain(t *testing.T) {
	profile := []byte("proxies:\n- {name: A, type: socks5, server: localhost, port: 9001}\n- {name: B, type: socks5, server: localhost, port: 9002}\n")
	o := configOptions{trafficPolicy: trafficPolicy{AppGroups: appGroups{
		{Name: "one", Apps: []string{"app.a"}, Enabled: true, TargetType: "chain", TargetChainID: "AB"},
		{Name: "two", Apps: []string{"app.b"}, Enabled: true, TargetType: "chain", TargetChainID: "AB"},
	}}}
	routes, err := buildAppRouting(o, profile, nil, map[string][]string{"AB": {"A", "B"}})
	if err != nil {
		t.Fatal(err)
	}
	var doc struct{ Proxies []map[string]any }
	if err := yaml.Unmarshal(routes.inlineProxies, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Proxies) != 2 || doc.Proxies[0]["port"] != 9001 || doc.Proxies[1]["port"] != 9002 || doc.Proxies[1]["dialer-proxy"] != doc.Proxies[0]["name"] {
		t.Fatal(doc)
	}
	for _, app := range []string{"app.a", "app.b"} {
		if !strings.Contains(routes.rules, "PROCESS-NAME,"+app+","+doc.Proxies[1]["name"].(string)) {
			t.Fatal(routes.rules)
		}
	}
	if _, err := buildAppRouting(o, profile, nil, nil); err == nil {
		t.Fatal("expected missing chain error")
	}
}
