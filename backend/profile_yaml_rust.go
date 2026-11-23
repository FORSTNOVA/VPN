//go:build rustcore && ((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

import (
	"bytes"
	"encoding/json"
	"math"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

const yamlFloatKey = "\x00smartvpn.yaml.float"

func nativeProfileNodes(body []byte) ([]proxyNode, bool) {
	return profileNodeOperation(9, body)
}
func profileNodeOperation(op uint32, body []byte) ([]proxyNode, bool) {
	out, status, ok := rustSubscriptionCall(op, string(body))
	if !ok || status != 0 {
		return nil, false
	}
	var nodes []proxyNode
	if json.Unmarshal(out, &nodes) != nil {
		return nil, false
	}
	return nodes, true
}

// Phase 91 cores accept integer-only merged documents on operation 10.
var mergedYAMLOp = sync.OnceValue(func() uint32 {
	if _, status, ok := rustSubscriptionCall(12, "{}"); ok && status == 0 {
		return 12
	}
	return 10
})

func nativeMergedYAML(doc map[string]any) ([]byte, bool) {
	if !rustSubscriptionAvailable() {
		return nil, false
	}
	value, ok := profileWireValue(reflect.ValueOf(doc), 0)
	if !ok {
		return nil, false
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, false
	}
	result, status, ok := rustSubscriptionCall(mergedYAMLOp(), string(data))
	return result, ok && status == 0 && len(result) > 0
}

// Translate the two schema structs explicitly: their JSON tags differ from
// their YAML tags. Unrepresentable extension types retain the Go YAML codec.
func profileWireValue(v reflect.Value, depth int) (any, bool) {
	if depth > 96 {
		return nil, false
	}
	if !v.IsValid() {
		return nil, true
	}
	if v.Kind() != reflect.Struct && v.Type().PkgPath() != "" {
		return nil, false
	}
	switch v.Kind() {
	case reflect.Interface:
		return profileWireValue(v.Elem(), depth+1)
	case reflect.Bool:
		return v.Bool(), true
	case reflect.String:
		return v.String(), utf8.ValidString(v.String())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int(), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return v.Uint(), true
	case reflect.Float32, reflect.Float64:
		f := v.Float()
		if mergedYAMLOp() != 12 {
			return nil, false
		}
		var token string
		switch {
		case math.IsNaN(f):
			token = ".nan"
		case math.IsInf(f, 1):
			token = ".inf"
		case math.IsInf(f, -1):
			token = "-.inf"
		default:
			token = strconv.FormatFloat(f, 'g', -1, v.Type().Bits())
		}
		return map[string]string{yamlFloatKey: token}, true
	case reflect.Slice:
		rows := make([]any, v.Len())
		for i := range rows {
			value, ok := profileWireValue(v.Index(i), depth+1)
			if !ok {
				return nil, false
			}
			rows[i] = value
		}
		return rows, true
	case reflect.Map:
		if v.Type().Key().Kind() != reflect.String {
			return nil, false
		}
		out := make(map[string]any, v.Len())
		for iter := v.MapRange(); iter.Next(); {
			key := iter.Key().String()
			value, ok := profileWireValue(iter.Value(), depth+1)
			if !ok || !utf8.ValidString(key) || key == yamlFloatKey {
				return nil, false
			}
			out[key] = value
		}
		return out, true
	case reflect.Struct:
		fields := map[string]any{}
		switch x := v.Interface().(type) {
		case policySection:
			fields = map[string]any{"label": x.Label, "groups": x.Groups, "rules": x.Rules}
		case sourceGroup:
			fields = map[string]any{"name": x.Name, "type": x.Type, "proxies": x.Members}
			if len(x.Use) > 0 {
				fields["use"] = x.Use
			}
			if x.IncludeAll {
				fields["include-all"] = true
			}
			for k, value := range x.Extra {
				switch k {
				case "name", "type", "proxies", "use", "include-all":
					return nil, false
				}
				fields[k] = value
			}
		default:
			return nil, false
		}
		return profileWireValue(reflect.ValueOf(fields), depth+1)
	}
	return nil, false
}

func nativeProfileObjects(body []byte) ([]map[string]any, bool) {
	op := uint32(11)
	extended := mergedYAMLOp() == 12
	if extended {
		op = 13
	}
	data, status, ok := rustSubscriptionCall(op, string(body))
	if !ok || status != 0 {
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var rows []map[string]any
	if decoder.Decode(&rows) != nil {
		return nil, false
	}
	for _, row := range rows {
		if _, ok = profileObjectNumbers(row, extended); !ok {
			return nil, false
		}
	}
	return rows, true
}

// yaml.v3 uses int for machine-sized integers and uint64 for larger positives.
func profileObjectNumbers(value any, extended bool) (any, bool) {
	switch v := value.(type) {
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return int(i), true
		}
		if strings.ContainsAny(string(v), ".eE") {
			f, err := v.Float64()
			return f, err == nil
		}
		u, err := strconv.ParseUint(string(v), 10, 64)
		return u, err == nil
	case map[string]any:
		if token, exists := v[yamlFloatKey]; extended && exists {
			if len(v) != 1 {
				return nil, false
			}
			switch token {
			case ".nan":
				return math.NaN(), true
			case ".inf":
				return math.Inf(1), true
			case "-.inf":
				return math.Inf(-1), true
			default:
				return nil, false
			}
		}
		for k, item := range v {
			n, ok := profileObjectNumbers(item, extended)
			if !ok {
				return nil, false
			}
			v[k] = n
		}
	case []any:
		for i, item := range v {
			n, ok := profileObjectNumbers(item, extended)
			if !ok {
				return nil, false
			}
			v[i] = n
		}
	}
	return value, true
}

func nativeSubscriptionPolicy(body []byte) *decodedSubscriptionPolicy {
	data, status, ok := rustSubscriptionCall(14, string(body))
	if !ok || status != 0 {
		return nil
	}
	type group struct {
		sourceGroup
		Extra map[string]any
	}
	type section struct {
		Label  string
		Groups []group
		Rules  []string
	}
	var wire struct {
		Nodes              []struct{ Name, Server string }
		Groups             []group
		Sections           []section
		Rules, MergedRules []string
		DNS                map[string]any
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if d.Decode(&wire) != nil {
		return nil
	}
	groups := func(rows []group) ([]sourceGroup, bool) {
		if rows == nil {
			return nil, true
		}
		out := make([]sourceGroup, len(rows))
		for i, row := range rows {
			if _, ok := profileObjectNumbers(row.Extra, true); !ok {
				return nil, false
			}
			out[i] = row.sourceGroup
			out[i].Extra = row.Extra
		}
		return out, true
	}
	p := &decodedSubscriptionPolicy{dns: wire.DNS}
	if _, ok := profileObjectNumbers(p.dns, true); !ok {
		return nil
	}
	p.policy.Rules, p.policy.MergedRules = wire.Rules, wire.MergedRules
	if p.policy.Groups, ok = groups(wire.Groups); !ok {
		return nil
	}
	if wire.Sections != nil {
		p.policy.Sections = make([]policySection, len(wire.Sections))
		for i, s := range wire.Sections {
			gs, ok := groups(s.Groups)
			if !ok {
				return nil
			}
			p.policy.Sections[i] = policySection{Label: s.Label, Rules: s.Rules, Groups: gs}
		}
	}
	if wire.Nodes != nil {
		p.policy.Nodes = make([]struct {
			Name string `yaml:"name"`
		}, len(wire.Nodes))
		p.servers = make([]string, len(wire.Nodes))
		for i, n := range wire.Nodes {
			p.policy.Nodes[i].Name, p.servers[i] = n.Name, n.Server
		}
	}
	return p
}

func nativeQueryNodes(body []byte) ([]proxyNode, bool) { return profileNodeOperation(15, body) }

func nativeLabelDocument(body []byte) (map[string]any, bool) {
	data, status, ok := rustSubscriptionCall(16, string(body))
	if !ok || status != 0 {
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var doc map[string]any
	if decoder.Decode(&doc) != nil || doc == nil {
		return nil, false
	}
	_, ok = profileObjectNumbers(doc, true)
	return doc, ok
}

func nativeManualDetails(nodes []manualNode) (map[string]proxyNode, bool) {
	if !rustSubscriptionAvailable() {
		return nil, false
	}
	rows := make([][6]any, len(nodes))
	for i, n := range nodes {
		network, _ := n.Proxy["network"].(string)
		tls, _ := n.Proxy["tls"].(bool)
		ws, _ := n.Proxy["ws-opts"].(map[string]any)
		path, _ := ws["path"].(string)
		_, headers := ws["headers"]
		if !utf8.ValidString(n.Name) || !utf8.ValidString(n.Type) || !utf8.ValidString(network) || !utf8.ValidString(path) {
			return nil, false
		}
		rows[i] = [6]any{n.Name, n.Type, network, tls, path, headers}
	}
	body, err := json.Marshal(rows)
	if err != nil {
		return nil, false
	}
	out, status, ok := rustSubscriptionCall(17, string(body))
	var result map[string]proxyNode
	if !ok || status != 0 || json.Unmarshal(out, &result) != nil || result == nil {
		return nil, false
	}
	return result, true
}

func nativeNodeAnnotations(nodes []proxyNode, selected *string) ([]byte, bool) {
	if !rustSubscriptionAvailable() || selected != nil && !utf8.ValidString(*selected) {
		return nil, false
	}
	rows := make([][3]any, len(nodes))
	for i, n := range nodes {
		if !utf8.ValidString(n.Name) {
			return nil, false
		}
		rows[i] = [3]any{n.Name, n.Manual, n.SourceLabel != ""}
	}
	body, err := json.Marshal([2]any{selected, rows})
	if err != nil {
		return nil, false
	}
	flags, status, ok := rustSubscriptionCall(18, string(body))
	if !ok || status != 0 || len(flags) != len(nodes) {
		return nil, false
	}
	for _, f := range flags {
		if f > 3 {
			return nil, false
		}
	}
	return flags, true
}
