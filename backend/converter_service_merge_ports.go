package main

import (
	"errors"
	"gopkg.in/yaml.v3"
	"sort"
)

type converterMergeResources struct {
	// The Clash reader removes duplicate summaries before the merge assigns
	// distinct proxy names. Reproject those batches after names are final.
	sourceStart int
	reproject   map[string]bool
	Proxies     []map[string]any
	Nodes       []proxyNode
	Policies    map[string][]string
	Labels      map[string]string
	Sections    []policySection
	Groups      []sourceGroup
	Rules       []string
}

func (e *appConverterEffects) mergeCall(q converterServiceEffect) (r converterServiceReply) {
	switch q.Kind {
	case "merge-new":
		r.Token = e.put(&converterMergeResources{Policies: map[string][]string{}, Labels: map[string]string{}})
	case "dns-child":
		body := e.bytes(q.Token)
		e.policyToken, e.policy = q.Token, nativeSubscriptionPolicy(body)
		p := entryDNSParsed(body, e.policy)
		for k := range p {
			r.Names = append(r.Names, k)
		}
		sort.Strings(r.Names)
		for _, k := range r.Names {
			r.Tokens = append(r.Tokens, e.put(p[k]))
		}
	case "merge-dns":
		e.get(q.Token).(*converterMergeResources).Policies[q.Text] = e.get(q.Ref).([]string)
	case "merge-proxy":
		m := e.get(q.Token).(*converterMergeResources)
		m.Proxies = append(m.Proxies, e.get(q.Ref).(map[string]any))
		m.Labels[q.Text] = q.Key
	case "nodes-rename":
		ns := e.get(q.Token).([]proxyNode)
		ns[q.Index].Name = q.Text
		ns[q.Index].SourceLabel = q.Key
	case "merge-nodes":
		m := e.get(q.Token).(*converterMergeResources)
		summaries := e.get(q.Ref).([]proxyNode)
		batch := m.Proxies[m.sourceStart:]
		m.sourceStart = len(m.Proxies)
		if e.publishMetadata && len(batch) != len(summaries) {
			if m.reproject == nil {
				m.reproject = make(map[string]bool)
			}
			for _, proxy := range batch {
				m.reproject[stringValue(proxy["name"])] = true
			}
		}
		m.Nodes = append(m.Nodes, summaries...)
	case "policy-decode":
		if e.policyToken != q.Token {
			e.policy = nativeSubscriptionPolicy(e.bytes(q.Token))
		}
		p, err := decodeSourcePolicy(e.bytes(q.Token), e.policy)
		e.policyToken, e.policy = 0, nil
		r.Err = err
		if r.Err == nil {
			r.Token = e.put(&p)
		}
	case "section-append":
		p := e.get(q.Ref).(*sourcePolicy)
		m := e.get(q.Token).(*converterMergeResources)
		m.Sections = append(m.Sections, policySection{Label: q.Text, Groups: p.Groups, Rules: append(append([]string{}, p.Rules...), p.MergedRules...)})
	case "policy-clone-groups":
		p := e.get(q.Token).(*sourcePolicy)
		p.Groups = append([]sourceGroup(nil), p.Groups...)
		for i := range p.Groups {
			p.Groups[i].Members = append([]string(nil), p.Groups[i].Members...)
			r.Tokens = append(r.Tokens, e.put(&p.Groups[i]))
		}
	case "group-facts":
		g := e.get(q.Token).(*sourceGroup)
		r.Text = g.Name
		r.Names = append([]string(nil), g.Members...)
	case "group-name":
		e.get(q.Token).(*sourceGroup).Name = q.Text
	case "group-member":
		e.get(q.Token).(*sourceGroup).Members[q.Index] = q.Text
	case "merge-group":
		m := e.get(q.Token).(*converterMergeResources)
		m.Groups = append(m.Groups, *e.get(q.Ref).(*sourceGroup))
	case "policy-rules":
		p := e.get(q.Token).(*sourcePolicy)
		r.Names = append(p.Rules, p.MergedRules...)
	case "merge-rule":
		m := e.get(q.Token).(*converterMergeResources)
		m.Rules = append(m.Rules, q.Text)
	case "merge-marshal":
		m := e.get(q.Token).(*converterMergeResources)
		out := map[string]any{"proxies": m.Proxies, "smartvpn-source-labels": m.Labels, "smartvpn-policy-sections": m.Sections}
		if q.Flag {
			out["dns"] = map[string]any{"nameserver-policy": m.Policies}
		}
		if q.Index != 0 {
			out["proxy-groups"] = m.Groups
		}
		if q.Value != 0 {
			out["smartvpn-source-rules"] = m.Rules
		}
		b, ok := nativeMergedYAML(out)
		var err error
		if !ok {
			b, err = yaml.Marshal(out)
		}
		r.Err = err
		if err == nil {
			if len(m.reproject) > 0 {
				byName := make(map[string]proxyNode, len(m.Nodes))
				for _, n := range m.Nodes {
					if !m.reproject[n.Name] {
						byName[n.Name] = n
					}
				}
				for _, n := range yamlProfileNodes(b) {
					if m.reproject[n.Name] {
						byName[n.Name] = n
					}
				}
				m.Nodes = nil
				for _, proxy := range m.Proxies {
					if n, found := byName[stringValue(proxy["name"])]; found {
						m.Nodes = append(m.Nodes, n)
					}
				}
			}
			r.Token = e.put(b)
		}
	case "merge-node-list":
		r.Token = e.put(e.get(q.Token).(*converterMergeResources).Nodes)
	default:
		r.Err = errors.New("unknown converter effect")
	}
	return
}
