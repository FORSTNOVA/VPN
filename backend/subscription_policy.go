package main

import (
	"gopkg.in/yaml.v3"
	"net"
	"net/http"
	"os"
	"strings"
)

type policySection struct {
	Label  string        `yaml:"label" json:"label"`
	Groups []sourceGroup `yaml:"groups" json:"groups"`
	Rules  []string      `yaml:"rules" json:"rules"`
}
type sourceGroup struct {
	Extra      map[string]any `yaml:",inline" json:"-"`
	Name       string         `yaml:"name" json:"name"`
	Type       string         `yaml:"type" json:"type"`
	Members    []string       `yaml:"proxies" json:"members"`
	Use        []string       `yaml:"use,omitempty" json:"providers,omitempty"`
	IncludeAll bool           `yaml:"include-all,omitempty" json:"includeAll,omitempty"`
}
type sourcePolicy struct {
	Sections []policySection `yaml:"smartvpn-policy-sections" json:"-"`
	Nodes    []struct {
		Name string `yaml:"name"`
	} `yaml:"proxies" json:"-"`
	Groups      []sourceGroup `yaml:"proxy-groups" json:"groups"`
	Rules       []string      `yaml:"rules" json:"rules"`
	MergedRules []string      `yaml:"smartvpn-source-rules" json:"-"`
}
type previewRule struct {
	Source  string `json:"source"`
	Applied string `json:"applied,omitempty"`
}

// Resolve homogeneous policy groups, including nested groups. Proxy-only
// groups use the current SmartVPN exit so locks and chains still apply.
func policyTargets(p sourcePolicy) map[string]string {
	targets := map[string]string{"DIRECT": "DIRECT", "REJECT": "REJECT", "SmartVPN": "SmartVPN"}
	for _, node := range p.Nodes {
		targets[node.Name] = "SmartVPN"
	}
	groups := map[string]sourceGroup{}
	for _, g := range p.Groups {
		groups[g.Name] = g
	}
	visiting := map[string]bool{}
	var resolve func(string) string
	resolve = func(name string) string {
		if value, ok := targets[name]; ok {
			return value
		}
		g, ok := groups[name]
		if !ok || visiting[name] || len(g.Use) > 0 || g.IncludeAll || len(g.Members) == 0 {
			return ""
		}
		visiting[name] = true
		result := resolve(g.Members[0])
		for _, m := range g.Members[1:] {
			if result == "" || resolve(m) != result {
				result = ""
				break
			}
		}
		delete(visiting, name)
		targets[name] = result
		return result
	}
	for name := range groups {
		resolve(name)
	}
	return targets
}

func supportedRules(p sourcePolicy) ([]previewRule, []string) {
	targets := policyTargets(p)
	var preview []previewRule
	var active []string
	for _, raw := range append(p.Rules, p.MergedRules...) {
		parts := strings.Split(raw, ",")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		effective := ""
		if len(parts) == 3 || (len(parts) == 4 && parts[3] == "no-resolve") {
			matcher := parts[1]
			valid := false
			switch parts[0] {
			case "DOMAIN", "DOMAIN-SUFFIX", "DOMAIN-KEYWORD":
				valid = len(parts) == 3 && matcher != "" && !strings.ContainsAny(matcher, " \r\n\t")
			case "IP-CIDR", "IP-CIDR6":
				ip, _, err := net.ParseCIDR(matcher)
				valid = err == nil && ((ip.To4() != nil) == (parts[0] == "IP-CIDR"))
			}
			if target := targets[parts[2]]; valid && target != "" {
				parts[2] = target
				effective = strings.Join(parts, ",")
			}

		}
		if effective != "" {
			active = append(active, effective)
		}
		preview = append(preview, previewRule{raw, effective})
	}
	return preview, active
}

func (a *app) subscriptionPolicy(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var p sourcePolicy
	if a.settings.SubscriptionURL != "" && a.profileMatchesSubscription() {
		body, err := os.ReadFile(a.profilePath())
		if err != nil || yaml.Unmarshal(body, &p) != nil {
			http.Error(w, "无法解析订阅", 400)
			return
		}
	}
	rules, _ := supportedRules(p)
	writeJSON(w, map[string]any{"sections": p.Sections, "groups": p.Groups, "rules": rules, "enabled": a.settings.ImportPolicy, "running": a.kernelRunningLocked()})
}
