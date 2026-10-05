package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"gopkg.in/yaml.v3"
)

// singboxConfig describes the structure of a Sing-Box profile.
type singboxConfig struct {
	Outbounds []json.RawMessage `json:"outbounds"`
}

type singboxOutboundBase struct {
	Type       string `json:"type"`
	Tag        string `json:"tag"`
	Server     string `json:"server"`
	ServerPort int    `json:"server_port"`
}

// parseSingboxProfile extracts both Mihomo proxy maps and UI proxyNode descriptors
// from a Sing-Box configuration.
func parseSingboxProfile(body []byte) ([]map[string]any, []proxyNode) {
	var cfg singboxConfig
	if err := json.Unmarshal(body, &cfg); err != nil || len(cfg.Outbounds) == 0 {
		return nil, nil
	}

	proxies := make([]map[string]any, 0, len(cfg.Outbounds))
	nodes := make([]proxyNode, 0, len(cfg.Outbounds))

	for _, raw := range cfg.Outbounds {
		var base singboxOutboundBase
		if err := json.Unmarshal(raw, &base); err != nil {
			continue
		}
		tag := strings.TrimSpace(base.Tag)
		if tag == "" || isInformationalNode(tag) {
			continue
		}
		server := strings.TrimSpace(base.Server)
		port := base.ServerPort
		kind := strings.ToLower(strings.TrimSpace(base.Type))

		switch kind {
		case "shadowsocks", "ss":
			var ss struct {
				Method   string `json:"method"`
				Password string `json:"password"`
			}
			if json.Unmarshal(raw, &ss) != nil || ss.Method == "" || ss.Password == "" {
				continue
			}
			proxy := map[string]any{
				"name":     tag,
				"type":     "ss",
				"server":   server,
				"port":     port,
				"cipher":   ss.Method,
				"password": ss.Password,
				"udp":      true,
			}
			proxies = append(proxies, proxy)
			nodes = append(nodes, proxyNode{
				Name: tag, Type: "ss", Network: "tcp",
			})

		case "vmess":
			var vmess struct {
				UUID     string `json:"uuid"`
				Security string `json:"security"`
				AlterID  int    `json:"alter_id"`
				TLS      *struct {
					Enabled    bool     `json:"enabled"`
					ServerName string   `json:"server_name"`
					Insecure   bool     `json:"insecure"`
					ALPN       []string `json:"alpn"`
				} `json:"tls"`
				Transport *struct {
					Type    string            `json:"type"`
					Path    string            `json:"path"`
					Headers map[string]string `json:"headers"`
				} `json:"transport"`
			}
			if json.Unmarshal(raw, &vmess) != nil || vmess.UUID == "" {
				continue
			}
			proxy := map[string]any{
				"name":    tag,
				"type":    "vmess",
				"server":  server,
				"port":    port,
				"uuid":    vmess.UUID,
				"cipher":  firstNonEmpty(vmess.Security, "auto"),
				"alterId": vmess.AlterID,
				"udp":     true,
			}
			tls := false
			if vmess.TLS != nil && vmess.TLS.Enabled {
				tls = true
				proxy["tls"] = true
				if vmess.TLS.ServerName != "" {
					proxy["servername"] = vmess.TLS.ServerName
				}
				if vmess.TLS.Insecure {
					proxy["skip-cert-verify"] = true
				}
				if len(vmess.TLS.ALPN) > 0 {
					proxy["alpn"] = vmess.TLS.ALPN
				}
			}
			network := "tcp"
			wsHost, wsPath := false, false
			if vmess.Transport != nil {
				network = vmess.Transport.Type
				host := ""
				if vmess.Transport.Headers != nil {
					host = vmess.Transport.Headers["Host"]
				}
				if host != "" {
					wsHost = true
				}
				if vmess.Transport.Path != "" {
					wsPath = true
				}
				_ = transportOptions(proxy, vmess.Transport.Type, vmess.Transport.Path, host, vmess.Transport.Path)
			}
			proxies = append(proxies, proxy)
			nodes = append(nodes, proxyNode{
				Name: tag, Type: "vmess", Network: network, TLS: tls, WsHost: wsHost, WsPath: wsPath,
			})

		case "vless":
			var vless struct {
				UUID string `json:"uuid"`
				Flow string `json:"flow"`
				TLS  *struct {
					Enabled    bool     `json:"enabled"`
					ServerName string   `json:"server_name"`
					Insecure   bool     `json:"insecure"`
					ALPN       []string `json:"alpn"`
					Reality    *struct {
						Enabled   bool   `json:"enabled"`
						PublicKey string `json:"public_key"`
						ShortID   string `json:"short_id"`
					} `json:"reality"`
				} `json:"tls"`
				Transport *struct {
					Type    string            `json:"type"`
					Path    string            `json:"path"`
					Headers map[string]string `json:"headers"`
				} `json:"transport"`
			}
			if json.Unmarshal(raw, &vless) != nil || vless.UUID == "" {
				continue
			}
			proxy := map[string]any{
				"name":   tag,
				"type":   "vless",
				"server": server,
				"port":   port,
				"uuid":   vless.UUID,
				"udp":    true,
			}
			if vless.Flow != "" {
				proxy["flow"] = vless.Flow
			}
			tls := false
			if vless.TLS != nil && vless.TLS.Enabled {
				tls = true
				proxy["tls"] = true
				if vless.TLS.ServerName != "" {
					proxy["servername"] = vless.TLS.ServerName
				}
				if vless.TLS.Insecure {
					proxy["skip-cert-verify"] = true
				}
				if len(vless.TLS.ALPN) > 0 {
					proxy["alpn"] = vless.TLS.ALPN
				}
				if vless.TLS.Reality != nil && vless.TLS.Reality.Enabled {
					ropts := map[string]any{"public-key": vless.TLS.Reality.PublicKey}
					if vless.TLS.Reality.ShortID != "" {
						ropts["short-id"] = vless.TLS.Reality.ShortID
					}
					proxy["reality-opts"] = ropts
				}
			}
			network := "tcp"
			wsHost, wsPath := false, false
			if vless.Transport != nil {
				network = vless.Transport.Type
				host := ""
				if vless.Transport.Headers != nil {
					host = vless.Transport.Headers["Host"]
				}
				if host != "" {
					wsHost = true
				}
				if vless.Transport.Path != "" {
					wsPath = true
				}
				_ = transportOptions(proxy, vless.Transport.Type, vless.Transport.Path, host, vless.Transport.Path)
			}
			proxies = append(proxies, proxy)
			nodes = append(nodes, proxyNode{
				Name: tag, Type: "vless", Network: network, TLS: tls, WsHost: wsHost, WsPath: wsPath,
			})

		case "trojan":
			var trojan struct {
				Password string `json:"password"`
				TLS      *struct {
					Enabled    bool     `json:"enabled"`
					ServerName string   `json:"server_name"`
					Insecure   bool     `json:"insecure"`
					ALPN       []string `json:"alpn"`
				} `json:"tls"`
				Transport *struct {
					Type    string            `json:"type"`
					Path    string            `json:"path"`
					Headers map[string]string `json:"headers"`
				} `json:"transport"`
			}
			if json.Unmarshal(raw, &trojan) != nil || trojan.Password == "" {
				continue
			}
			proxy := map[string]any{
				"name":     tag,
				"type":     "trojan",
				"server":   server,
				"port":     port,
				"password": trojan.Password,
				"udp":      true,
			}
			tls := true
			if trojan.TLS != nil {
				if trojan.TLS.ServerName != "" {
					proxy["sni"] = trojan.TLS.ServerName
				}
				if trojan.TLS.Insecure {
					proxy["skip-cert-verify"] = true
				}
				if len(trojan.TLS.ALPN) > 0 {
					proxy["alpn"] = trojan.TLS.ALPN
				}
			}
			network := "tcp"
			wsHost, wsPath := false, false
			if trojan.Transport != nil {
				network = trojan.Transport.Type
				host := ""
				if trojan.Transport.Headers != nil {
					host = trojan.Transport.Headers["Host"]
				}
				if host != "" {
					wsHost = true
				}
				if trojan.Transport.Path != "" {
					wsPath = true
				}
				_ = transportOptions(proxy, trojan.Transport.Type, trojan.Transport.Path, host, trojan.Transport.Path)
			}
			proxies = append(proxies, proxy)
			nodes = append(nodes, proxyNode{
				Name: tag, Type: "trojan", Network: network, TLS: tls, WsHost: wsHost, WsPath: wsPath,
			})

		case "hysteria2":
			var hy2 struct {
				Password string `json:"password"`
				TLS      *struct {
					ServerName string   `json:"server_name"`
					Insecure   bool     `json:"insecure"`
					ALPN       []string `json:"alpn"`
				} `json:"tls"`
				Obfs *struct {
					Type     string `json:"type"`
					Password string `json:"password"`
				} `json:"obfs"`
			}
			if json.Unmarshal(raw, &hy2) != nil || hy2.Password == "" {
				continue
			}
			proxy := map[string]any{
				"name":     tag,
				"type":     "hysteria2",
				"server":   server,
				"port":     port,
				"password": hy2.Password,
			}
			if hy2.TLS != nil {
				if hy2.TLS.ServerName != "" {
					proxy["sni"] = hy2.TLS.ServerName
				}
				if hy2.TLS.Insecure {
					proxy["skip-cert-verify"] = true
				}
				if len(hy2.TLS.ALPN) > 0 {
					proxy["alpn"] = hy2.TLS.ALPN
				}
			}
			if hy2.Obfs != nil && hy2.Obfs.Type != "" {
				proxy["obfs"] = hy2.Obfs.Type
				if hy2.Obfs.Password != "" {
					proxy["obfs-password"] = hy2.Obfs.Password
				}
			}
			proxies = append(proxies, proxy)
			nodes = append(nodes, proxyNode{
				Name: tag, Type: "hysteria2", Network: "udp", TLS: true,
			})

		case "tuic":
			var tuic struct {
				UUID                 string `json:"uuid"`
				Password             string `json:"password"`
				CongestionController string `json:"congestion_controller"`
				TLS                  *struct {
					ServerName string   `json:"server_name"`
					Insecure   bool     `json:"insecure"`
					ALPN       []string `json:"alpn"`
				} `json:"tls"`
			}
			if json.Unmarshal(raw, &tuic) != nil || (tuic.UUID == "" && tuic.Password == "") {
				continue
			}
			proxy := map[string]any{
				"name":     tag,
				"type":     "tuic",
				"server":   server,
				"port":     port,
				"uuid":     tuic.UUID,
				"password": tuic.Password,
				"udp":      true,
			}
			if tuic.CongestionController != "" {
				proxy["congestion-controller"] = tuic.CongestionController
			}
			if tuic.TLS != nil {
				if tuic.TLS.ServerName != "" {
					proxy["sni"] = tuic.TLS.ServerName
				}
				if tuic.TLS.Insecure {
					proxy["skip-cert-verify"] = true
				}
				if len(tuic.TLS.ALPN) > 0 {
					proxy["alpn"] = tuic.TLS.ALPN
				}
			}
			proxies = append(proxies, proxy)
			nodes = append(nodes, proxyNode{
				Name: tag, Type: "tuic", Network: "udp", TLS: true,
			})
		}
	}

	return proxies, uniqueNodes(nodes)
}

func parseSingboxNodes(body []byte) []proxyNode {
	_, nodes := parseSingboxProfile(body)
	return nodes
}

// isProxyURILink reports whether text starts with a supported node proxy scheme.
func isProxyURILink(text string) bool {
	t := strings.ToLower(strings.TrimSpace(text))
	for _, scheme := range []string{"vmess://", "vless://", "ss://", "ssr://", "trojan://", "hysteria2://", "hy2://", "hysteria://", "tuic://", "anytls://"} {
		if strings.HasPrefix(t, scheme) {
			return true
		}
	}
	return false
}

// isInlineSubscription reports whether text contains node links, Base64 URI list, or JSON/YAML profile directly.
func isInlineSubscription(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return false
	}
	if strings.HasPrefix(trimmed, "proxies:") || strings.Contains(trimmed, "\nproxies:") {
		return true
	}
	if strings.HasPrefix(trimmed, "{") {
		return true
	}
	if hasProxyURI(trimmed) {
		return true
	}
	// Check if base64 decodes to valid proxy content
	if decoded, err := decodeSubscriptionBase64(trimmed); err == nil && len(decoded) > 0 {
		decStr := strings.TrimSpace(string(decoded))
		if hasProxyURI(decStr) || strings.HasPrefix(decStr, "proxies:") || strings.HasPrefix(decStr, "{") {
			return true
		}
	}
	return false
}

// categorizeSubscriptionInput divides user input containing URLs or node links
// into remote HTTP(S) download targets and inline node lines.
func categorizeSubscriptionInput(raw string) ([]string, []string) {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")
	scanner := func(c rune) bool {
		return c == '|' || c == '\n'
	}
	var remoteURLs []string
	var inlineLines []string

	for _, part := range strings.FieldsFunc(raw, scanner) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.HasPrefix(part, "http://") || strings.HasPrefix(part, "https://") {
			remoteURLs = append(remoteURLs, part)
		} else if isProxyURILink(part) {
			inlineLines = append(inlineLines, part)
		} else if isInlineSubscription(part) {
			inlineLines = append(inlineLines, part)
		}
	}
	// If fieldsFunc split didn't catch it (e.g. single block of YAML, Base64, or single link)
	if len(remoteURLs) == 0 && len(inlineLines) == 0 && isInlineSubscription(raw) {
		inlineLines = append(inlineLines, raw)
	}
	return remoteURLs, inlineLines
}

// splitSubscriptionURLs divides user input containing multiple subscription URLs
// joined by '|', newlines, or semicolons.
func splitSubscriptionURLs(raw string) []string {
	remoteURLs, _ := categorizeSubscriptionInput(raw)
	return remoteURLs
}

// parseSubscriptionBodyToProxies parses Clash YAML, Sing-Box JSON, or Base64 URI lists
// into canonical Mihomo proxy maps and proxyNode items.
func parseSubscriptionBodyToProxies(body []byte) ([]map[string]any, []proxyNode, error) {
	text := strings.TrimSpace(string(body))
	if text == "" {
		return nil, nil, errNoUsableNodes
	}

	// 1. Direct Sing-Box JSON
	if strings.HasPrefix(text, "{") {
		if proxies, nodes := parseSingboxProfile(body); len(proxies) > 0 {
			return proxies, nodes, nil
		}
	}

	// 2. Direct Clash YAML
	var clashDoc struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	if yaml.Unmarshal(body, &clashDoc) == nil && len(clashDoc.Proxies) > 0 {
		cleanProxies := make([]map[string]any, 0, len(clashDoc.Proxies))
		for _, p := range clashDoc.Proxies {
			name := stringValue(p["name"])
			if name != "" && !isInformationalNode(name) {
				cleanProxies = append(cleanProxies, p)
			}
		}
		if len(cleanProxies) > 0 {
			return cleanProxies, yamlProfileNodes(body), nil
		}
	}

	// 3. Base64 decode attempt
	if !hasProxyURI(text) {
		if decoded, err := decodeSubscriptionBase64(text); err == nil {
			decText := strings.TrimSpace(string(decoded))
			if strings.HasPrefix(decText, "{") {
				if proxies, nodes := parseSingboxProfile(decoded); len(proxies) > 0 {
					return proxies, nodes, nil
				}
			}
			var decClash struct {
				Proxies []map[string]any `yaml:"proxies"`
			}
			if yaml.Unmarshal(decoded, &decClash) == nil && len(decClash.Proxies) > 0 {
				cleanProxies := make([]map[string]any, 0, len(decClash.Proxies))
				for _, p := range decClash.Proxies {
					name := stringValue(p["name"])
					if name != "" && !isInformationalNode(name) {
						cleanProxies = append(cleanProxies, p)
					}
				}
				if len(cleanProxies) > 0 {
					return cleanProxies, yamlProfileNodes(decoded), nil
				}
			}
			text = decText
		}
	}

	// 4. Line-by-line URI parsing
	var proxies []map[string]any
	var nodes []proxyNode

	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		kind, _, hasScheme := strings.Cut(line, "://")
		if !hasScheme {
			continue
		}
		kind = strings.ToLower(kind)
		if kind == "vmess" {
			encoded := strings.TrimPrefix(line, "vmess://")
			decoded, err := decodeSubscriptionBase64(encoded)
			if err != nil {
				continue
			}
			var vmess vmessLink
			if json.Unmarshal(decoded, &vmess) != nil {
				continue
			}
			name := strings.TrimSpace(vmess.Name)
			if name == "" {
				name = fmt.Sprintf("%v:%v", vmess.Server, vmess.Port)
			}
			if isInformationalNode(name) {
				continue
			}
			tls := strings.ToLower(strings.TrimSpace(vmess.TLS))
			hasTLS := tls != "" && tls != "none" && tls != "false"
			network := vmess.Network
			if network == "" {
				network = "tcp"
			}
			wsHost := vmess.Host != ""
			wsPath := vmess.Path != ""
			nodes = append(nodes, proxyNode{
				Name: name, Type: "Vmess", Network: network,
				TLS: hasTLS, WsHost: wsHost, WsPath: wsPath,
			})
			server := strings.TrimSpace(vmess.Server)
			port, _ := portValue(vmess.Port)
			proxy := map[string]any{
				"name":    name,
				"type":    "vmess",
				"server":  server,
				"port":    port,
				"uuid":    firstNonEmpty(strings.TrimSpace(vmess.ID), "00000000-0000-0000-0000-000000000000"),
				"alterId": intValue(vmess.AlterID),
				"cipher":  vmessCipher(vmess.Cipher),
				"udp":     true,
			}
			if hasTLS {
				proxy["tls"] = true
				if sni := firstNonEmpty(vmess.SNI, vmess.Host); sni != "" {
					proxy["servername"] = sni
				}
			}
			_ = transportOptions(proxy, vmess.Network, vmess.Path, vmess.Host, vmess.Path)
			proxies = append(proxies, proxy)
			continue
		}

		if kind == "vless" || kind == "trojan" || kind == "ss" || kind == "hysteria2" || kind == "hy2" || kind == "anytls" {
			parsed, err := url.Parse(line)
			if err != nil {
				continue
			}
			name := linkName(parsed, parsed.Host)
			if name == "" {
				name = parsed.Fragment
			}
			if name == "" || isInformationalNode(name) {
				continue
			}
			query := parsed.Query()
			tls := tlsEnabled(query.Get("security")) || query.Get("tls") == "1"
			if kind == "trojan" && (query.Get("security") == "" && query.Get("tls") == "") {
				tls = true
			}
			if query.Get("security") == "none" {
				tls = false
			}
			nodes = append(nodes, proxyNode{
				Name: name, Type: kind, Network: query.Get("type"),
				TLS: tls, WsHost: query.Get("host") != "", WsPath: query.Get("path") != "",
			})
			if mNode, err := parseNodeURI(line); err == nil {
				proxies = append(proxies, mNode.Proxy)
			}
			continue
		}

		node, err := parseNodeURI(line)
		if err != nil {
			continue
		}
		if isInformationalNode(node.Name) {
			continue
		}
		proxies = append(proxies, node.Proxy)
		network, _ := node.Proxy["network"].(string)
		tls, _ := node.Proxy["tls"].(bool)
		wsHost, wsPath := false, false
		if opts, ok := node.Proxy["ws-opts"].(map[string]any); ok {
			if path, ok := opts["path"].(string); ok && path != "" {
				wsPath = true
			}
			if _, ok := opts["headers"]; ok {
				wsHost = true
			}
		}
		nodes = append(nodes, proxyNode{
			Name: node.Name, Type: node.Type, Network: network,
			TLS: tls, WsHost: wsHost, WsPath: wsPath,
		})
	}

	if len(proxies) == 0 {
		return nil, nil, errNoUsableNodes
	}
	return proxies, uniqueNodes(nodes), nil
}

// ensureClashYAML ensures that the subscription body stored in providers/subscription.yaml
// is valid Clash YAML that Mihomo can always parse without error.
func ensureClashYAML(body []byte) ([]byte, []proxyNode, error) {
	text := strings.TrimSpace(string(body))
	// If already a valid Clash YAML with proxies, keep it
	if (strings.HasPrefix(text, "proxies:") || strings.Contains(text, "\nproxies:")) &&
		!strings.HasPrefix(text, "{") {
		nodes := yamlProfileNodes(body)
		if len(nodes) > 0 {
			return body, nodes, nil
		}
	}

	proxies, nodes, err := parseSubscriptionBodyToProxies(body)
	if err != nil {
		return nil, nil, err
	}
	marshaled, err := yaml.Marshal(map[string]any{"proxies": proxies})
	if err != nil {
		return nil, nil, errors.New("failed to marshal converted proxies to YAML")
	}
	return marshaled, nodes, nil
}

// SubscriptionSource represents one subscription source during a multi-subscription merge.
type SubscriptionSource struct {
	ID     string
	Label  string
	Prefix string
	Body   []byte
}

// mergeSubscriptionSources merges multiple subscription sources into a single Clash YAML profile,
// adding optional prefixes to node names and deduplicating names across sources.
func mergeSubscriptionSources(sources []SubscriptionSource) ([]byte, []proxyNode, error) {
	var allProxies []map[string]any
	var allNodes []proxyNode
	nameCounts := make(map[string]int)

	for _, src := range sources {
		proxies, nodes, err := parseSubscriptionBodyToProxies(src.Body)
		if err != nil || len(proxies) == 0 {
			continue
		}
		prefix := strings.TrimSpace(src.Prefix)
		for i, p := range proxies {
			name := stringValue(p["name"])
			if name == "" {
				name = fmt.Sprintf("%s:%v", stringValue(p["server"]), p["port"])
			}
			if prefix != "" && !strings.HasPrefix(name, prefix) {
				name = prefix + " " + name
			}
			if count, seen := nameCounts[name]; seen {
				nameCounts[name] = count + 1
				newName := fmt.Sprintf("%s (%d)", name, count+1)
				p["name"] = newName
				if i < len(nodes) {
					nodes[i].Name = newName
				}
				nameCounts[newName] = 1
			} else {
				p["name"] = name
				if i < len(nodes) {
					nodes[i].Name = name
				}
				nameCounts[name] = 1
			}
			allProxies = append(allProxies, p)
		}
		allNodes = append(allNodes, nodes...)
	}

	if len(allProxies) == 0 {
		return nil, nil, errNoUsableNodes
	}
	mergedYAML, err := yaml.Marshal(map[string]any{"proxies": allProxies})
	if err != nil {
		return nil, nil, errors.New("could not marshal merged proxies")
	}
	return mergedYAML, uniqueNodes(allNodes), nil
}

// mergeSubscriptionProfiles merges multiple profile bodies into a single Clash YAML profile,
// deduplicating duplicate node names across sources.
func mergeSubscriptionProfiles(profiles [][]byte) ([]byte, []proxyNode, error) {
	sources := make([]SubscriptionSource, len(profiles))
	for i, p := range profiles {
		sources[i] = SubscriptionSource{Body: p}
	}
	return mergeSubscriptionSources(sources)
}

