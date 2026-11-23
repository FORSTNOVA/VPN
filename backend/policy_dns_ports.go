package main

import (
	"gopkg.in/yaml.v3"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// The flat source contains codec facts only. Classification remains in the flow.
type policyDnsFacts struct {
	names []string
	at    int
}

func (s *policyDnsFacts) str() string { v := s.names[s.at]; s.at++; return v }
func (s *policyDnsFacts) num() int    { v, _ := strconv.Atoi(s.str()); return v }
func (s *policyDnsFacts) list() []string {
	n := s.num()
	v := make([]string, n)
	for i := range v {
		v[i] = s.str()
	}
	return v
}
func (s *policyDnsFacts) put(v ...string) { s.names = append(s.names, v...) }
func (s *policyDnsFacts) count(v int)     { s.put(strconv.Itoa(v)) }
func (s *policyDnsFacts) add(v []string)  { s.count(len(v)); s.put(v...) }
func policyDnsHostFacts(v string) (string, string) {
	h := strings.TrimSuffix(strings.Trim(strings.ToLower(strings.TrimSpace(v)), `"'`), ".")
	ip := "0"
	if net.ParseIP(h) != nil {
		ip = "1"
	}
	return h, ip
}
func policyDnsRuleFacts(s *policyDnsFacts, raws []string) {
	s.count(len(raws))
	for _, raw := range raws {
		s.put(raw)
		parts := strings.Split(raw, ",")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		s.add(parts)
		family := "0"
		if len(parts) > 1 {
			ip, _, err := net.ParseCIDR(parts[1])
			if err == nil {
				family = "6"
				if ip.To4() != nil {
					family = "4"
				}
			}
		}
		s.put(family)
		upper := ""
		if len(parts) > 2 {
			upper = strings.ToUpper(parts[2])
		}
		s.put(upper)
	}
}

type appPolicyDnsEffects struct {
	a               *app
	q               policyDnsRequest
	parsed          *decodedSubscriptionPolicy
	policy          sourcePolicy
	lists           [][]string
	owned           bool
	matchBody       []byte
	subscriptionURL string
	readFile        func(string) ([]byte, error)
}

func (f *appPolicyDnsEffects) Release() {
	if f.owned {
		f.owned = false
		f.a.mu.Unlock()
	}
}
func (f *appPolicyDnsEffects) read(path string) ([]byte, error) {
	if f.readFile != nil {
		return f.readFile(path)
	}
	return os.ReadFile(path)
}
func (f *appPolicyDnsEffects) Call(e policyDnsEffect) policyDnsReply {
	switch e.Kind {
	case "lock":
		f.a.mu.Lock()
		f.owned = true
		return policyDnsReply{}
	case "subscription":
		f.subscriptionURL = f.a.controlMetaLocked().SubscriptionURL
		merged := int64(0)
		if f.subscriptionURL == mergedSubscriptionURL {
			merged = 1
		}
		return policyDnsReply{Flag: f.subscriptionURL != "", Number: merged}
	case "match-read":
		b, err := f.read(f.a.profilePath())
		f.matchBody = b
		return policyDnsReply{Err: err}
	case "match-nodes":
		return policyDnsReply{Number: int64(len(parseProfileNodes(f.matchBody)))}
	case "match-source":
		b, err := f.read(f.a.profilePath() + ".source")
		return policyDnsReply{Text: strings.TrimSpace(string(b)), Err: err}
	case "hash":
		return policyDnsReply{Text: subscriptionHash(f.subscriptionURL)}
	case "read-policy":
		b, err := f.read(f.a.profilePath())
		if err == nil {
			f.policy, err = f.a.policyPreview.read(b)
		}
		return policyDnsReply{Err: err}
	case "enabled":
		return policyDnsReply{Flag: f.a.controlMetaLocked().ImportPolicy}
	case "running":
		return policyDnsReply{Flag: f.a.kernelRunningLocked()}
	case "source":
		return f.source(e.Text)
	}
	panic("unexpected policy DNS effect: " + e.Kind)
}
func (f *appPolicyDnsEffects) source(op string) policyDnsReply {
	s := &policyDnsFacts{}
	switch op {
	case "targets", "rules":
		p := f.policy
		s.count(len(p.Nodes))
		for _, n := range p.Nodes {
			s.put(n.Name)
		}
		s.count(len(p.Groups))
		for _, g := range p.Groups {
			s.put(g.Name)
			s.count(len(g.Use))
			if g.IncludeAll {
				s.put("1")
			} else {
				s.put("0")
			}
			s.add(g.Members)
		}
		// Capture Go map iteration as an input fact, preserving cycle traversal semantics.
		groups := map[string]bool{}
		for _, g := range p.Groups {
			groups[g.Name] = true
		}
		var order []string
		for k := range groups {
			order = append(order, k)
		}
		s.add(order)
		if op == "rules" {
			policyDnsRuleFacts(s, append(p.Rules, p.MergedRules...))
		}
	case "subrules":
		var p struct {
			Rules []string `yaml:"rules"`
		}
		if parsed := f.parsed; parsed != nil {
			p.Rules = parsed.policy.Rules
		} else if err := yaml.Unmarshal(f.q.Body, &p); err != nil {
			return policyDnsReply{Err: err}
		}
		policyDnsRuleFacts(s, p.Rules)
		s.add(rejectGroupHints)
		s.add(domesticGroupHints)
	case "suffix":
		h, ip := policyDnsHostFacts(f.q.Path)
		s.put(h, ip)
	case "merge":
		s.count(len(f.lists))
		for _, list := range f.lists {
			s.add(list)
		}
	case "domains":
		lines := strings.Split(string(f.q.Body), "\n")
		s.count(len(lines))
		for _, line := range lines {
			line = strings.TrimSpace(line)
			line = strings.TrimPrefix(line, "- ")
			s.put(line)
			value := strings.TrimSpace(strings.TrimPrefix(line, "server:"))
			if cut := strings.IndexAny(value, " \t#"); cut >= 0 {
				value = value[:cut]
			}
			h, ip := policyDnsHostFacts(value)
			s.put(h, ip)
		}
	case "hostname":
		var p struct {
			Proxies []subscriptionProxy `yaml:"proxies"`
		}
		if err := yaml.Unmarshal(f.q.Body, &p); err != nil {
			return policyDnsReply{Err: err}
		}
		s.count(len(p.Proxies))
		for _, p := range p.Proxies {
			host := strings.TrimSpace(p.Server)
			h, ip := policyDnsHostFacts(host)
			originalIP := "0"
			if net.ParseIP(host) != nil {
				originalIP = "1"
			}
			s.put(host, h, ip, originalIP)
		}
	case "entrydns":
		var p struct {
			Proxies []subscriptionProxy
			DNS     struct {
				Policy map[string]any `yaml:"nameserver-policy"`
			}
		}
		var servers []string
		if f.parsed != nil {
			servers, p.DNS.Policy = f.parsed.servers, f.parsed.dns
		} else {
			if err := yaml.Unmarshal(f.q.Body, &p); err != nil {
				return policyDnsReply{Err: err}
			}
			for _, proxy := range p.Proxies {
				servers = append(servers, proxy.Server)
			}
		}
		s.count(len(p.DNS.Policy))
		s.count(len(servers))
		for _, server := range servers {
			host := strings.ToLower(server)
			h, ip := policyDnsHostFacts(host)
			s.put(host, h, ip)
			var values []any
			switch v := p.DNS.Policy[host].(type) {
			case string:
				values = []any{v}
			case []any:
				values = v
			}
			s.count(len(values))
			for _, v := range values {
				raw, ok := v.(string)
				if !ok {
					s.put("0", "", "", "", "0", "")
					continue
				}
				u, err := url.Parse(raw)
				if err != nil {
					s.put("1", raw, "", "", "1", "")
					continue
				}
				user := "0"
				if u.User != nil {
					user = "1"
				}
				s.put("1", raw, u.Scheme, u.Hostname(), user, u.Fragment)
			}
		}
	default:
		panic("unknown policy DNS source")
	}
	return policyDnsReply{Names: s.names}
}
