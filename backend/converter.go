package main

import (
	"encoding/json"
	"strings"
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
	q, e := converterBodyRequest("singbox", body)
	p, n, err := converterProxyResult(e, runConverterService(q, e))
	if err != nil {
		panic(err)
	}
	return p, n
}

func parseSingboxNodes(body []byte) []proxyNode {
	_, nodes := parseSingboxProfile(body)
	return nodes
}

// isProxyURILink reports whether text starts with a supported node proxy scheme.
func isProxyURILinkGo(text string) bool {
	t := strings.ToLower(strings.TrimSpace(text))
	for _, scheme := range []string{"vmess://", "vless://", "ss://", "ssr://", "trojan://", "hysteria2://", "hy2://", "hysteria://", "tuic://", "anytls://"} {
		if strings.HasPrefix(t, scheme) {
			return true
		}
	}
	return false
}

// isInlineSubscription reports whether text contains node links, Base64 URI list, or JSON/YAML profile directly.
func isInlineSubscriptionGo(raw string) bool {
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
	if hasProxyURIGo(trimmed) {
		return true
	}
	// Check if base64 decodes to valid proxy content
	if decoded, err := decodeSubscriptionBase64Go(trimmed); err == nil && len(decoded) > 0 {
		decStr := strings.TrimSpace(string(decoded))
		if hasProxyURIGo(decStr) || strings.HasPrefix(decStr, "proxies:") || strings.HasPrefix(decStr, "{") {
			return true
		}
	}
	return false
}

// categorizeSubscriptionInput divides user input containing URLs or node links
// into remote HTTP(S) download targets and inline node lines.
func categorizeSubscriptionInputGo(raw string) ([]string, []string) {
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
		} else if isProxyURILinkGo(part) {
			inlineLines = append(inlineLines, part)
		} else if isInlineSubscriptionGo(part) {
			inlineLines = append(inlineLines, part)
		}
	}
	// If fieldsFunc split didn't catch it (e.g. single block of YAML, Base64, or single link)
	if len(remoteURLs) == 0 && len(inlineLines) == 0 && isInlineSubscriptionGo(raw) {
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
	q, e := converterBodyRequest("parse-body", body)
	return converterProxyResult(e, runConverterService(q, e))
}

// ensureClashYAML ensures that the subscription body stored in providers/subscription.yaml
// is valid Clash YAML that Mihomo can always parse without error.
func ensureClashYAML(body []byte) ([]byte, []proxyNode, error) {
	q, e := converterBodyRequest("ensure-yaml", body)
	return converterByteResult(e, runConverterService(q, e))
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
	return mergeSubscriptionRows(sources, false)
}

// The converter API retains its historical positional summaries. A published
// profile needs query metadata keyed by its final, unique provider names.
func mergeSubscriptionSourcesForCache(sources []SubscriptionSource) ([]byte, []proxyNode, error) {
	return mergeSubscriptionRows(sources, true)
}

func mergeSubscriptionRows(sources []SubscriptionSource, publish bool) ([]byte, []proxyNode, error) {
	e := newConverterEffects()
	e.publishMetadata = publish
	q := converterServiceRequest{Op: "merge-sources"}
	for _, s := range sources {
		q.Sources = append(q.Sources, converterSource{Body: e.put(s.Body), Label: s.Label, Prefix: s.Prefix})
	}
	return converterByteResult(e, runConverterService(q, e))
}

// uniqueMergedNodeName also checks names produced by suffixing. A later source
// may already contain "Node (2)", so a duplicate "Node" must skip that name.
func uniqueMergedNodeName(name string, used map[string]struct{}) string {
	e := newConverterEffects()
	e.used = used
	r := runConverterService(converterServiceRequest{Op: "unique-name", Name: name}, e)
	if r.Err != nil {
		panic(r.Err)
	}
	return r.Text
}

// mergeSubscriptionProfiles merges multiple profile bodies into a single Clash YAML profile,
// deduplicating duplicate node names across sources.
func mergeSubscriptionProfiles(profiles [][]byte) ([]byte, []proxyNode, error) {
	sources := make([]SubscriptionSource, len(profiles))
	for i, p := range profiles {
		sources[i] = SubscriptionSource{Body: p}
	}
	return mergeSubscriptionSourcesForCache(sources)
}
