package main

func runGoLabelsService(q labelsServiceRequest, fx labelsServiceEffects) labelsServiceResult {
	defer fx.Release()
	call := func(k string) labelsReply { return fx.Call(labelsEffect{Kind: k}) }
	s := call("mode").State
	if !s.Merged {
		if s.Empty {
			return labelsServiceResult{}
		}
		s = call("single-snapshot").State
		label := s.DefaultLabel
		for _, src := range s.Sources {
			if src.Matches && src.Label != "" {
				label = src.Label
				break
			}
		}
		for i, n := range s.Nodes {
			if !n.Manual && n.Label == "" {
				fx.Call(labelsEffect{Kind: "set-label", Index: i, Text: label})
			}
		}
		return labelsServiceResult{}
	}
	if call("read-original").Err != nil {
		return labelsServiceResult{}
	}
	r := call("decode-original")
	if r.Err != nil || !r.Bool || r.Other {
		return labelsServiceResult{}
	}
	count := call("cached-sources").Count
	s = call("sources-snapshot").State
	enabled := 0
	for _, src := range s.Sources {
		if src.Enabled && src.Nonempty {
			enabled++
		}
	}
	if count != enabled {
		return labelsServiceResult{}
	}
	if call("rebuild").Err != nil {
		return labelsServiceResult{}
	}
	if call("index-original").Err != nil {
		return labelsServiceResult{}
	}
	r = call("index-rebuilt")
	if r.Err != nil || !r.Bool {
		return labelsServiceResult{}
	}
	if call("decode-meta").Err != nil {
		return labelsServiceResult{}
	}
	for _, key := range []string{"proxy-groups", "smartvpn-source-rules", "smartvpn-source-labels", "smartvpn-policy-sections", "dns"} {
		r = fx.Call(labelsEffect{Kind: "key-facts", Name: key})
		if !r.Bool && r.Other {
			fx.Call(labelsEffect{Kind: "patch-key", Name: key})
		}
	}
	if call("marshal").Err != nil {
		return labelsServiceResult{}
	}
	if call("write-temp").Err != nil {
		return labelsServiceResult{}
	}
	if call("rename").Err != nil {
		call("remove-temp")
		return labelsServiceResult{}
	}
	s = call("nodes-snapshot").State
	for i, n := range s.Nodes {
		if !n.Manual {
			r = fx.Call(labelsEffect{Kind: "meta-label", Name: n.Name})
			fx.Call(labelsEffect{Kind: "set-label", Index: i, Text: r.Text})
		}
	}
	call("save-cache")
	return labelsServiceResult{}
}
