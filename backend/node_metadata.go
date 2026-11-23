package main

import "gopkg.in/yaml.v3"

// Index consumers retain all duplicates, including unnamed maps. Query
// summaries select a single visible node; they cannot stand in for this input.
func profileIndexRows(body []byte) ([]map[string]any, error) {
	if rows, ok := nativeProfileObjects(body); ok {
		return rows, nil
	}
	var v struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	err := yaml.Unmarshal(body, &v)
	return v.Proxies, err
}

func decodeLabelDocument(body []byte, target any) error {
	if doc, ok := target.(*map[string]any); ok {
		if rows, ok := nativeLabelDocument(body); ok {
			*doc = rows
			return nil
		}
	}
	return yaml.Unmarshal(body, target)
}

// Update the caller's original backing array and leave unrelated fields intact.
func annotateProfileNodes(nodes []proxyNode, label string, selected *string) {
	if len(nodes) == 0 {
		return
	}
	if flags, ok := nativeNodeAnnotations(nodes, selected); ok {
		for i, flag := range flags {
			if flag&1 != 0 {
				nodes[i].SourceLabel = label
			}
			if selected != nil {
				nodes[i].Selected = flag&2 != 0
			}
		}
		return
	}
	for i := range nodes {
		if !nodes[i].Manual && nodes[i].SourceLabel == "" {
			nodes[i].SourceLabel = label
		}
		if selected != nil {
			nodes[i].Selected = nodes[i].Name == *selected
		}
	}
}
