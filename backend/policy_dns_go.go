package main

import (
	"errors"
	"sort"
	"strings"
)

func policyDnsSuffix(h, ip string) (string, bool) {
	if h == "" || len(h) > 253 || ip == "1" {
		return "", false
	}
	labels := strings.Split(h, ".")
	for _, label := range labels {
		if label == "" || len(label) > 63 {
			return "", false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return "", false
			}
		}
		if strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return "", false
		}
	}
	if len(labels) > 2 {
		labels = labels[len(labels)-2:]
	}
	suffix := strings.Join(labels, ".")
	if !strings.Contains(suffix, ".") {
		return "", false
	}
	return "+." + suffix, true
}
func policyDnsTargets(s *policyDnsFacts) map[string]string {
	targets := map[string]string{"DIRECT": "DIRECT", "REJECT": "REJECT", "SmartVPN": "SmartVPN"}
	for n := s.num(); n > 0; n-- {
		targets[s.str()] = "SmartVPN"
	}
	groups := map[string]sourceGroup{}
	for n := s.num(); n > 0; n-- {
		g := sourceGroup{Name: s.str()}
		use := s.num()
		g.Use = make([]string, use)
		g.IncludeAll = s.str() == "1"
		g.Members = s.list()
		groups[g.Name] = g
	}
	order := s.list()
	visiting := map[string]bool{}
	var resolve func(string) string
	resolve = func(name string) string {
		if v, ok := targets[name]; ok {
			return v
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
	for _, name := range order {
		resolve(name)
	}
	return targets
}

type policyDnsRule struct {
	raw           string
	parts         []string
	family, upper string
}

func policyDnsRulesFacts(s *policyDnsFacts) []policyDnsRule {
	v := make([]policyDnsRule, s.num())
	for i := range v {
		v[i] = policyDnsRule{s.str(), s.list(), s.str(), s.str()}
	}
	return v
}
func policyDnsSupported(s *policyDnsFacts) ([]previewRule, []string) {
	targets := policyDnsTargets(s)
	var preview []previewRule
	var active []string
	for _, r := range policyDnsRulesFacts(s) {
		parts := r.parts
		effective := ""
		if len(parts) == 3 || (len(parts) == 4 && parts[3] == "no-resolve") {
			matcher := parts[1]
			valid := false
			switch parts[0] {
			case "DOMAIN", "DOMAIN-SUFFIX", "DOMAIN-KEYWORD":
				valid = len(parts) == 3 && matcher != "" && !strings.ContainsAny(matcher, " \r\n\t")
			case "IP-CIDR":
				valid = r.family == "4"
			case "IP-CIDR6":
				valid = r.family == "6"
			}
			if target := targets[parts[2]]; valid && target != "" {
				parts[2] = target
				effective = strings.Join(parts, ",")
			}
		}
		if effective != "" {
			active = append(active, effective)
		}
		preview = append(preview, previewRule{r.raw, effective})
	}
	return preview, active
}
func runGoPolicyDns(q policyDnsRequest, fx policyDnsEffects) policyDnsResult {
	a := policyDnsOracle{fx}
	op := q.Op
	if op == "http" {
		a.call("lock", "", "", 0)
		if a.call("subscription", "", "", 0).Flag && a.matches() {
			if a.call("read-policy", "", "", 0).Err != nil {
				return policyDnsResult{Status: 400, Err: errors.New(policyDnsReadError)}
			}
		}
		op = "rules"
	}
	r := a.call("source", op, "", 0)
	s := &policyDnsFacts{names: r.Names}
	out := policyDnsResult{Data: map[string]any{}}
	switch op {
	case "targets":
		out.Data["targets"] = policyDnsTargets(s)
	case "rules":
		preview, active := policyDnsSupported(s)
		out.Data["preview"] = preview
		out.Data["active"] = active
	case "subrules":
		var rules []string
		if r.Err == nil {
			facts := policyDnsRulesFacts(s)
			reject, domestic := s.list(), s.list()
			rules = make([]string, 0, len(facts))
			for _, r := range facts {
				p := r.parts
				if len(p) < 3 {
					continue
				}
				switch p[0] {
				case "DOMAIN", "DOMAIN-SUFFIX", "DOMAIN-KEYWORD", "IP-CIDR", "IP-CIDR6":
				default:
					continue
				}
				if p[1] == "" || strings.ContainsAny(p[1], "\r\n") {
					continue
				}
				target := "SmartVPN"
				if r.upper == "PASS" {
					continue
				}
				found := false
				for _, hint := range reject {
					if strings.Contains(r.upper, hint) {
						target = "REJECT"
						found = true
						break
					}
				}
				if !found {
					for _, hint := range domestic {
						if strings.Contains(r.upper, hint) {
							target = "DIRECT"
							break
						}
					}
				}
				clean := p[0] + "," + p[1] + "," + target
				if len(p) > 3 && p[3] == "no-resolve" {
					clean += ",no-resolve"
				}
				rules = append(rules, clean)
				if len(rules) == 3000 {
					break
				}
			}
		}
		out.Data["rules"] = rules
	case "suffix":
		h, ip := s.str(), s.str()
		suffix, ok := policyDnsSuffix(h, ip)
		out.Text = suffix
		out.Data["ok"] = ok
	case "merge":
		seen := map[string]bool{}
		merged := make([]string, 0, 8)
		for n := s.num(); n > 0; n-- {
			for _, entry := range s.list() {
				if entry != "" && !seen[entry] {
					seen[entry] = true
					merged = append(merged, entry)
				}
			}
		}
		sort.Strings(merged)
		out.Data["domains"] = merged
	case "domains":
		seen := map[string]bool{}
		domains := make([]string, 0, 8)
		for n := s.num(); n > 0; n-- {
			line, h, ip := s.str(), s.str(), s.str()
			if !strings.HasPrefix(line, "server:") {
				continue
			}
			entry, ok := policyDnsSuffix(h, ip)
			if !ok || seen[entry] {
				continue
			}
			seen[entry] = true
			domains = append(domains, entry)
			if len(domains) >= maxProxyServerDomains {
				break
			}
		}
		sort.Strings(domains)
		out.Data["domains"] = domains
	case "hostname":
		if r.Err == nil {
			for n := s.num(); n > 0; n-- {
				host, h, ip, originalIP := s.str(), s.str(), s.str(), s.str()
				if host == "" || originalIP == "1" {
					continue
				}
				if _, ok := policyDnsSuffix(h, ip); ok {
					out.Text = host
					break
				}
			}
		}
	case "entrydns":
		var result map[string][]string
		if r.Err == nil {
			count := s.num()
			if count > 0 {
				result = map[string][]string{}
			}
			for n := s.num(); n > 0; n-- {
				host, h, ip := s.str(), s.str(), s.str()
				_, valid := policyDnsSuffix(h, ip)
				_, exists := result[host]
				for v := s.num(); v > 0; v-- {
					isString, raw, scheme, hostname, user, fragment := s.str(), s.str(), s.str(), s.str(), s.str(), s.str()
					if count > 0 && valid && !exists && isString == "1" && scheme == "https" && hostname != "" && user == "0" && fragment == "" {
						result[host] = append(result[host], raw)
					}
				}
			}
		}
		out.Data["policy"] = result
	default:
		panic("unknown policy DNS operation")
	}
	if q.Op == "http" {
		out.Data = map[string]any{"rules": out.Data["preview"], "enabled": a.call("enabled", "", "", 0).Flag, "running": a.call("running", "", "", 0).Flag}
	}
	return out
}

func (a *policyDnsOracle) matches() bool {
	if a.call("match-read", "", "", 0).Err != nil {
		return false
	}
	if a.call("match-nodes", "", "", 0).Number == 0 {
		return false
	}
	r := a.call("match-source", "", "", 0)
	if r.Err != nil {
		return false
	}
	if a.call("subscription", "", "", 0).Number == 1 {
		return r.Text == mergedSubscriptionURL
	}
	a.call("subscription", "", "", 0)
	return r.Text == a.call("hash", "", "", 0).Text
}
