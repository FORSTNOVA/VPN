package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

type appGroup struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Apps       []string `json:"apps"`
	TargetType string   `json:"targetType"`
	TargetNode string   `json:"targetNode"`
	Enabled    bool     `json:"enabled"`
}

type appGroups []appGroup

func (g appGroups) clone() appGroups {
	out := slices.Clone(g)
	for i := range out {
		out[i].Apps = slices.Clone(out[i].Apps)
	}
	return out
}

func (g appGroups) MarshalJSON() ([]byte, error) {
	if g == nil {
		return []byte("[]"), nil
	}
	return json.Marshal([]appGroup(g))
}

func (g *appGroups) UnmarshalJSON(body []byte) error {
	decoded, err := decodeAppGroups(body, runtime.GOOS == "android")
	if err == nil {
		*g = decoded
	}
	return err
}

func decodeAppGroups(body []byte, android bool) (appGroups, error) {
	// A missing enabled field must not silently disable the group.
	var rows []struct {
		appGroup
		Enabled *bool `json:"enabled"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&rows); err != nil {
		return nil, fmt.Errorf("appGroups: %w", err)
	}
	if rows == nil {
		return nil, fmt.Errorf("appGroups must be an array")
	}
	out := make(appGroups, 0, len(rows))
	for i, row := range rows {
		g := row.appGroup
		if !validAppGroupText(g.ID) || !validAppGroupText(g.Name) || len(g.Apps) == 0 || row.Enabled == nil {
			return nil, fmt.Errorf("appGroups[%d] requires valid id, name, apps and enabled", i)
		}
		g.ID = strings.TrimSpace(g.ID)
		g.Name = strings.TrimSpace(g.Name)
		g.Enabled = *row.Enabled
		switch g.TargetType {
		case "node":
			if strings.TrimSpace(g.TargetNode) == "" {
				return nil, fmt.Errorf("appGroups[%d] requires a target node", i)
			}
		case "direct", "reject":
		default:
			return nil, fmt.Errorf("appGroups[%d] has invalid targetType %q", i, g.TargetType)
		}
		seen := map[string]bool{}
		apps := g.Apps[:0]
		for _, app := range g.Apps {
			if !validAppGroupText(app) {
				return nil, fmt.Errorf("appGroups[%d] has invalid application %q", i, app)
			}
			app = strings.TrimSpace(app)
			key := app
			if !android {
				key = strings.ToLower(app)
			}
			if !seen[key] {
				apps = append(apps, app)
				seen[key] = true
			}
		}
		g.Apps = apps
		out = append(out, g)
	}
	return out, nil
}

func validAppGroupText(s string) bool {
	return strings.TrimSpace(s) != "" && !strings.ContainsAny(s, ",*") && strings.IndexFunc(s, unicode.IsControl) < 0
}

type appRouting struct {
	inlineProxies []byte
	groups, rules string
}

func buildAppRouting(o configOptions, profile []byte, manual []manualNode) (appRouting, error) {
	var groups, rules strings.Builder
	var index map[string][]map[string]any
	var inline []map[string]any
	// Explicit group members resolve against Mihomo's top-level proxies, not file providers.
	// Materialize each selected provider node once, preserving chain copies in the same YAML list.
	occupied := map[string]bool{"DIRECT": true, "REJECT": true, "REJECT-DROP": true, "COMPATIBLE": true, "GLOBAL": true, "PASS": true, "PASS-RULE": true, appName: true, autoGroup: true, fallbackGroup: true}
	members := map[string]string{}
	nextGroup, nextNode := 1, 1
	nextName := func(prefix string, n *int) string {
		for {
			name := prefix + strconv.Itoa(*n)
			(*n)++
			if !occupied[name] && len(index[name]) == 0 {
				occupied[name] = true
				return name
			}
		}
	}
	for _, g := range o.AppGroups {
		if !g.Enabled {
			continue
		}
		target := strings.ToUpper(g.TargetType)
		if g.TargetType == "node" {
			if index == nil {
				var err error
				index, err = proxyIndex(profile, manual)
				if err != nil {
					return appRouting{}, err
				}
				inline, err = profileIndexRows(o.ChainProxies)
				if err != nil {
					return appRouting{}, fmt.Errorf("decode chain proxies: %w", err)
				}
				for _, p := range inline {
					occupied[stringValue(p["name"])] = true
				}
			}
			node := g.TargetNode
			switch len(index[node]) {
			case 0:
				return appRouting{}, fmt.Errorf("app group %q: unknown target node %q", g.Name, node)
			case 1:
			default:
				return appRouting{}, fmt.Errorf("app group %q: ambiguous target node %q", g.Name, node)
			}
			member, exists := members[node]
			if !exists {
				member = node
				if occupied[node] {
					member = nextName("SmartVPN-Node-", &nextNode)
				}
				p := maps.Clone(index[node][0])
				p["name"] = member
				inline = append(inline, p)
				members[node] = member
			}
			target = nextName("SmartVPN-Group-", &nextGroup)
			fmt.Fprintf(&groups, "  - name: %s\n    type: select\n    proxies: [%s]\n", target, strconv.Quote(member))
		}
		for _, app := range g.Apps {
			fmt.Fprintf(&rules, "  - %s\n", strconv.Quote("PROCESS-NAME,"+app+","+target))
		}
	}
	result := appRouting{groups: groups.String(), rules: rules.String()}
	if len(members) > 0 {
		var err error
		result.inlineProxies, err = yaml.Marshal(map[string]any{"proxies": inline})
		return result, err
	}
	return result, nil
}
