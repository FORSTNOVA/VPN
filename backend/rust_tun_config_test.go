package main

import (
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestRustTunMaterializesReferencedProviders(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "nodes.yaml"), []byte("proxies: [{name: first, type: tuic}, {name: pinned, type: ss}]"), 0600); err != nil {
		t.Fatal(err)
	}
	body := []byte(`mixed-port: 7890
proxies: [{name: pinned, type: ss}]
proxy-providers:
  sub: {type: file, path: nodes.yaml}
  unused: {type: file, path: unused.yaml}
proxy-groups:
  - {name: SmartVPN, proxies: [REJECT], use: [sub], type: select}
  - {name: SmartVPN-Group-1, proxies: [pinned], type: select}
  - {name: auto, use: [sub], type: url-test}
rules: ["MATCH,SmartVPN"]
`)
	out, err := materializeRustTun(home, body, "pinned")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Groups    []rustTunGroup `yaml:"proxy-groups"`
		Proxies   []map[string]any
		Providers map[string]any `yaml:"proxy-providers"`
		Rules     []string
	}
	if err := yaml.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Providers) != 0 || len(got.Proxies) != 2 || !slices.Equal(got.Rules, []string{"MATCH,SmartVPN"}) {
		t.Fatal(string(out))
	}
	for i, want := range [][]string{{"pinned", "REJECT", "first"}, {"pinned"}, {"first", "pinned"}} {
		if len(got.Groups[i].Use) != 0 || !slices.Equal(got.Groups[i].Members, want) {
			t.Fatal(got.Groups[i])
		}
	}
	if _, err := materializeRustTun(home, body, "missing"); err == nil {
		t.Fatal("accepted missing selection")
	}
}
