package main

import "net/http"

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
	q := policyDnsRequest{Op: "targets"}
	fx := &appPolicyDnsEffects{q: q, policy: p}
	return runSelectedPolicyDns(q, fx).Data["targets"].(map[string]string)
}

func supportedRules(p sourcePolicy) ([]previewRule, []string) {
	q := policyDnsRequest{Op: "rules"}
	fx := &appPolicyDnsEffects{q: q, policy: p}
	r := runSelectedPolicyDns(q, fx)
	return r.Data["preview"].([]previewRule), r.Data["active"].([]string)
}

func (a *app) subscriptionPolicy(w http.ResponseWriter, r *http.Request) {
	q := policyDnsRequest{Op: "http"}
	fx := &appPolicyDnsEffects{a: a, q: q}
	runPolicyDnsHTTPWithGraph(w, q, fx)
}
