package main

import (
	"encoding/json"
	"fmt"
	"gopkg.in/yaml.v3"
	"net/url"
	"reflect"
	"strings"
)

type appConverterEffects struct {
	publishMetadata bool
	policyToken     int
	policy          *decodedSubscriptionPolicy
	arena           []any
	used            map[string]struct{}
}

func newConverterEffects() *appConverterEffects { return &appConverterEffects{arena: []any{nil}} }
func (e *appConverterEffects) put(v any) int    { e.arena = append(e.arena, v); return len(e.arena) - 1 }
func (e *appConverterEffects) get(t int) any {
	if t <= 0 || t >= len(e.arena) {
		panic("invalid converter resource")
	}
	return e.arena[t]
}
func (e *appConverterEffects) bytes(t int) []byte { return e.get(t).([]byte) }
func (e *appConverterEffects) Release()           {}
func converterNodeMetadata(n proxyNode) converterNode {
	return converterNode{n.Name, n.Type, n.Network, n.SourceLabel, n.TLS, n.WsHost, n.WsPath}
}
func converterNodeValue(n converterNode) proxyNode {
	return proxyNode{Name: n.Name, Type: n.Type, Network: n.Network, SourceLabel: n.SourceLabel, TLS: n.TLS, WsHost: n.WsHost, WsPath: n.WsPath}
}
func (e *appConverterEffects) fields(v any) []converterField {
	out := []converterField{}
	var walk func(reflect.Value, string)
	walk = func(v reflect.Value, key string) {
		if v.Kind() == reflect.Interface {
			if v.IsNil() {
				out = append(out, converterField{Key: key, Empty: true})
				return
			}
			v = v.Elem()
		}
		if v.Kind() == reflect.Pointer {
			if v.IsNil() {
				out = append(out, converterField{Key: key, Empty: true})
				return
			}
			out = append(out, converterField{Key: key, Ref: e.put(v.Interface()), Count: 1})
			walk(v.Elem(), key)
			return
		}
		if v.Kind() == reflect.Struct {
			ty := v.Type()
			for i := 0; i < v.NumField(); i++ {
				name := strings.Split(ty.Field(i).Tag.Get("json"), ",")[0]
				if name == "" {
					name = ty.Field(i).Name
				}
				if key != "" {
					name = key + "." + name
				}
				walk(v.Field(i), name)
			}
			return
		}
		f := converterField{Key: key, Ref: e.put(v.Interface())}
		switch v.Kind() {
		case reflect.String:
			s := v.String()
			f.Empty = s == ""
			f.TrimEmpty = strings.TrimSpace(s) == ""
			f.TrimRef = e.put(strings.TrimSpace(s))
			f.Count = len(s)
			// Only decision text crosses native; secret/endpoint/transport data stay references.
			switch key {
			case "type", "tag", "security", "net", "tls", "transport.type", "obfs.type", "ps", "scy", "flow", "method", "congestion_controller":
				f.Text = s
				f.Trim = strings.TrimSpace(s)
				f.Lower = strings.ToLower(f.Trim)
			}
		case reflect.Bool:
			f.Bool = v.Bool()
		case reflect.Int, reflect.Int64:
			f.Int = v.Int()
		case reflect.Float64:
			f.Int = int64(v.Float())
		case reflect.Slice:
			f.Count = v.Len()
			f.Empty = v.Len() == 0
		case reflect.Map:
			f.Count = v.Len()
			f.Empty = v.Len() == 0
			if key == "transport.headers" {
				x := v.MapIndex(reflect.ValueOf("Host"))
				if x.IsValid() {
					walk(x, "transport.host")
				}
			}
		}
		out = append(out, f)
	}
	walk(reflect.ValueOf(v), "")
	return out
}
func (e *appConverterEffects) assignments(m map[string]any, as []converterAssignment) {
	for _, a := range as {
		var v any
		switch a.Kind {
		case 0:
			v = e.get(a.Ref)
		case 1:
			v = a.Text
		case 2:
			v = int(a.Value)
		case 3:
			v = a.Value != 0
		default:
			panic("invalid converter assignment")
		}
		parts := strings.Split(a.Key, ".")
		dst := m
		for _, p := range parts[:len(parts)-1] {
			if _, ok := dst[p]; !ok {
				dst[p] = map[string]any{}
			}
			dst = dst[p].(map[string]any)
		}
		dst[parts[len(parts)-1]] = v
	}
}
func (e *appConverterEffects) Call(q converterServiceEffect) (r converterServiceReply) {
	switch q.Kind {
	case "body-facts":
		s := strings.TrimSpace(string(e.bytes(q.Token)))
		r.Flag = s == ""
		r.Text = ""
		if strings.HasPrefix(s, "{") {
			r.Text = "json"
		}
		r.Count = 0
		if strings.HasPrefix(s, "proxies:") || strings.Contains(s, "\nproxies:") {
			r.Count = 1
		}
		r.Fields = []converterField{{Key: "uri", Bool: hasProxyURI(s)}}
	case "base64":
		b, err := decodeSubscriptionBase64(string(e.bytes(q.Token)))
		r.Err = err
		if err == nil {
			r.Token = e.put(b)
		}
	case "decode-singbox":
		var c singboxConfig
		r.Err = json.Unmarshal(e.bytes(q.Token), &c)
		if r.Err == nil {
			for _, raw := range c.Outbounds {
				r.Tokens = append(r.Tokens, e.put([]byte(raw)))
			}
		}
		r.Count = len(r.Tokens)
	case "decode-base":
		var b singboxOutboundBase
		r.Err = json.Unmarshal(e.bytes(q.Token), &b)
		if r.Err == nil {
			r.Fields = e.fields(b)
		}
	case "decode-outbound":
		var v any
		switch q.Key {
		case "ss", "shadowsocks":
			v = &converterSs{}
		case "vmess":
			v = &converterVmess{}
		case "vless":
			v = &converterVless{}
		case "trojan":
			v = &converterTrojan{}
		case "hysteria2":
			v = &converterHy2{}
		case "tuic":
			v = &converterTuic{}
		default:
			panic("invalid outbound codec")
		}
		r.Err = json.Unmarshal(e.bytes(q.Token), v)
		if r.Err == nil {
			r.Fields = e.fields(reflect.ValueOf(v).Elem().Interface())
		}
	case "decode-clash":
		var c struct {
			Proxies []map[string]any `yaml:"proxies"`
		}
		body := e.bytes(q.Token)
		var ok bool
		if c.Proxies, ok = nativeProfileObjects(body); !ok {
			r.Err = yaml.Unmarshal(body, &c)
		}
		if r.Err == nil {
			for _, p := range c.Proxies {
				r.Tokens = append(r.Tokens, e.put(p))
			}
		}
		r.Count = len(r.Tokens)
	case "proxy-facts":
		p := e.get(q.Token).(map[string]any)
		r.Text = stringValue(p["name"])
		r.Node = converterNode{Name: r.Text}
		r.Node.Network, _ = p["network"].(string)
		r.Node.TLS, _ = p["tls"].(bool)
		if opts, ok := p["ws-opts"].(map[string]any); ok {
			s, ok := opts["path"].(string)
			r.Node.WsPath = ok && s != ""
			_, r.Node.WsHost = opts["headers"]
		}
		r.Names = []string{fmt.Sprintf("%s:%v", stringValue(p["server"]), p["port"])}
	case "info":
		r.Flag = isInformationalNode(q.Text)
	case "map-new":
		m := map[string]any{}
		e.assignments(m, q.Fields)
		r.Token = e.put(m)
	case "map-assign":
		e.assignments(e.get(q.Token).(map[string]any), q.Fields)
	case "transport-child":
		// This is the Phase39 complete transport planner, not a Go field-policy port.
		val := func(t int) string {
			if t == 0 {
				return ""
			}
			return e.get(t).(string)
		}
		r.Err = transportOptions(e.get(q.Token).(map[string]any), q.Text, val(q.Tokens[0]), val(q.Tokens[1]), val(q.Tokens[2]))
	case "node-new":
		r.Token = e.put(converterNodeValue(q.Node))
	case "proxies-build":
		ps := make([]map[string]any, 0, len(q.Tokens))
		if !q.Flag && len(q.Tokens) == 0 {
			ps = nil
		}
		for _, t := range q.Tokens {
			ps = append(ps, e.get(t).(map[string]any))
		}
		r.Token = e.put(ps)
	case "nodes-build":
		ns := make([]proxyNode, 0, len(q.Tokens))
		if !q.Flag && len(q.Tokens) == 0 {
			ns = nil
		}
		for _, t := range q.Tokens {
			ns = append(ns, e.get(t).(proxyNode))
		}
		r.Token = e.put(ns)
	case "proxies-items":
		for _, p := range e.get(q.Token).([]map[string]any) {
			r.Tokens = append(r.Tokens, e.put(p))
		}
	case "nodes-items":
		for _, n := range e.get(q.Token).([]proxyNode) {
			r.Tokens = append(r.Tokens, e.put(n))
		}
	case "node-facts":
		r.Node = converterNodeMetadata(e.get(q.Token).(proxyNode))
	case "nodes-unique-child":
		r.Token = e.put(uniqueNodes(e.get(q.Token).([]proxyNode)))
	case "yaml-nodes-child":
		r.Token = e.put(yamlProfileNodes(e.bytes(q.Token)))
		r.Count = len(e.get(r.Token).([]proxyNode))
	case "lines":
		for _, s := range strings.Split(strings.TrimSpace(string(e.bytes(q.Token))), "\n") {
			r.Tokens = append(r.Tokens, e.put([]byte(s)))
		}
	case "line-facts":
		s := strings.TrimSpace(string(e.bytes(q.Token)))
		r.Token = e.put([]byte(s))
		kind, _, found := strings.Cut(s, "://")
		r.Text = strings.ToLower(kind)
		r.Flag = found
		r.Fields = []converterField{{Key: "empty", Bool: s == ""}, {Key: "comment", Bool: strings.HasPrefix(s, "#")}}
	case "vmess-base64":
		b, err := decodeSubscriptionBase64(strings.TrimPrefix(string(e.bytes(q.Token)), "vmess://"))
		r.Err = err
		if err == nil {
			r.Token = e.put(b)
		}
	case "decode-vmess":
		var v vmessLink
		r.Err = json.Unmarshal(e.bytes(q.Token), &v)
		if r.Err == nil {
			r.Fields = e.fields(v)
			p, _ := portValue(v.Port)
			r.Fields = append(r.Fields, converterField{Key: "port-number", Int: int64(p)}, converterField{Key: "aid-number", Int: int64(intValue(v.AlterID))}, converterField{Key: "fallback", Text: fmt.Sprintf("%v:%v", v.Server, v.Port)})
		}
	case "decode-uri":
		u, err := url.Parse(string(e.bytes(q.Token)))
		r.Err = err
		if err == nil {
			r.Token = e.put(u)
			r.Text = linkName(u, u.Host)
			r.Names = []string{u.Fragment}
			v := u.Query()
			for _, k := range []string{"security", "tls", "type", "host", "path"} {
				f := converterField{Key: k, Empty: v.Get(k) == ""}
				if k != "host" && k != "path" {
					f.Text = v.Get(k)
				}
				r.Fields = append(r.Fields, f)
			}
		}
	case "uri-child":
		n, err := parseNodeURI(string(e.bytes(q.Token)))
		r.Err = err
		if err == nil {
			r.Token = e.put(n.Proxy)
			r.Node = converterNode{Name: n.Name, Type: n.Type}
		}
	case "marshal-proxies":
		b, err := yaml.Marshal(map[string]any{"proxies": e.get(q.Token).([]map[string]any)})
		r.Err = err
		if err == nil {
			r.Token = e.put(b)
		}
	case "used-name":
		_, r.Flag = e.used[q.Text]
	case "use-name":
		e.used[q.Text] = struct{}{}
	default:
		return e.mergeCall(q)
	}
	return
}
