package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// A manual node is one link the user pasted. The proxy object is what the kernel
// consumes; the link is kept as provenance so the original can be shown, and so
// the object can be rebuilt if the parser learns something new.
type manualNode struct {
	Name    string         `json:"name"`
	URI     string         `json:"uri"`
	Type    string         `json:"type"`
	Server  string         `json:"server"`
	Port    int            `json:"port"`
	AddedAt int64          `json:"addedAt"`
	Proxy   map[string]any `json:"proxy"`
}

var (
	// Only these schemes can be turned into a proxy object completely; anything
	// else is refused rather than half-converted.
	errLinkUnsupported = errors.New("不支持的节点链接：支持 vmess / vless / trojan / ss / ssr / hysteria / hysteria2 / tuic")
	errLinkFields      = errors.New("节点链接缺少必要字段")
)

// Ports these schemes conventionally use when the link leaves the port out.
var defaultPorts = map[string]int{
	"vless": 443, "trojan": 443, "hysteria": 443, "hysteria2": 443, "hy2": 443, "tuic": 443,
}

// parseNodeURI turns one pasted link into the node the kernel will run.
func parseNodeURI(raw string) (manualNode, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return manualNode{}, errors.New("请先粘贴节点链接")
	}
	scheme, _, found := strings.Cut(text, "://")
	if !found {
		return manualNode{}, errLinkUnsupported
	}
	scheme = strings.ToLower(scheme)
	if scheme == "vmess" {
		return parseVmessLink(text)
	}
	parsed, err := url.Parse(text)
	if err != nil {
		return manualNode{}, errors.New("节点链接无法解析")
	}
	switch scheme {
	case "vless":
		return parseVlessLink(parsed)
	case "trojan":
		return parseTrojanLink(parsed)
	case "hysteria2", "hy2":
		return parseHysteria2Link(parsed)
	case "hysteria":
		return parseHysteriaLink(parsed)
	case "tuic":
		return parseTuicLink(parsed)
	case "ss":
		return parseShadowsocksLink(text, parsed)
	case "ssr":
		return parseSSRLink(text)
	default:
		return manualNode{}, errLinkUnsupported
	}
}

// --------------------------------------------------------------------------
// Shared pieces

func linkName(parsed *url.URL, fallback string) string {
	if name := strings.TrimSpace(parsed.Fragment); name != "" {
		if decoded, err := url.PathUnescape(name); err == nil && strings.TrimSpace(decoded) != "" {
			return strings.TrimSpace(decoded)
		}
		return name
	}
	return fallback
}

func linkEndpoint(parsed *url.URL, scheme string) (string, int, error) {
	host := strings.TrimSpace(parsed.Hostname())
	if host == "" {
		return "", 0, errLinkFields
	}
	port := 0
	if text := strings.TrimSpace(parsed.Port()); text != "" {
		value, err := strconv.Atoi(text)
		if err != nil {
			return "", 0, errLinkFields
		}
		port = value
	} else if fallback, ok := defaultPorts[scheme]; ok {
		port = fallback
	}
	if port < 1 || port > 65535 {
		return "", 0, errLinkFields
	}
	return host, port, nil
}

func linkSecret(parsed *url.URL) string {
	if parsed.User == nil {
		return ""
	}
	if password, ok := parsed.User.Password(); ok {
		return password
	}
	return parsed.User.Username()
}

func insecureValue(value string) bool {
	trimmed := strings.ToLower(strings.TrimSpace(value))
	return trimmed == "1" || trimmed == "true" || trimmed == "yes"
}

// transportOptions writes the network settings the kernel needs for the
// transport the link asked for.
func transportOptions(proxy map[string]any, network, path, host, serviceName string) error {
	switch strings.ToLower(strings.TrimSpace(network)) {
	case "", "tcp":
		return nil
	case "ws", "websocket":
		proxy["network"] = "ws"
		options := map[string]any{}
		if path != "" {
			options["path"] = path
		}
		if host != "" {
			options["headers"] = map[string]any{"Host": host}
		}
		if len(options) > 0 {
			proxy["ws-opts"] = options
		}
		return nil
	case "grpc":
		proxy["network"] = "grpc"
		if serviceName != "" {
			proxy["grpc-opts"] = map[string]any{"grpc-service-name": serviceName}
		}
		return nil
	case "h2", "http":
		proxy["network"] = "h2"
		options := map[string]any{}
		if path != "" {
			options["path"] = path
		}
		if host != "" {
			options["host"] = []string{host}
		}
		if len(options) > 0 {
			proxy["h2-opts"] = options
		}
		return nil
	default:
		return fmt.Errorf("暂不支持 %q 传输方式", network)
	}
}

// --------------------------------------------------------------------------
// vmess

type vmessLink struct {
	Name    string `json:"ps"`
	Server  string `json:"add"`
	Port    any    `json:"port"`
	ID      string `json:"id"`
	AlterID any    `json:"aid"`
	Cipher  string `json:"scy"`
	Network string `json:"net"`
	Header  string `json:"type"`
	Host    string `json:"host"`
	Path    string `json:"path"`
	TLS     string `json:"tls"`
	SNI     string `json:"sni"`
	Alpn    any    `json:"alpn"`
	FP      string `json:"fp"`
}

func parseVmessLink(text string) (manualNode, error) {
	encoded := strings.TrimPrefix(text, "vmess://")
	if cut := strings.IndexAny(encoded, "#?"); cut >= 0 {
		encoded = encoded[:cut]
	}
	decoded, err := decodeSubscriptionBase64(encoded)
	if err != nil {
		return manualNode{}, errors.New("vmess 链接的 Base64 无法解码")
	}
	var link vmessLink
	if err := json.Unmarshal(decoded, &link); err != nil {
		return manualNode{}, errors.New("vmess 链接的 JSON 无法解析")
	}
	server := strings.TrimSpace(link.Server)
	port, err := portValue(link.Port)
	if server == "" || port == 0 || strings.TrimSpace(link.ID) == "" {
		return manualNode{}, errLinkFields
	}
	name := strings.TrimSpace(link.Name)
	if name == "" {
		name = fmt.Sprintf("%s:%d", server, port)
	}
	proxy := map[string]any{
		"name":    name,
		"type":    "vmess",
		"server":  server,
		"port":    port,
		"uuid":    strings.TrimSpace(link.ID),
		"alterId": intValue(link.AlterID),
		"cipher":  vmessCipher(link.Cipher),
		"udp":     true,
	}
	if tls := strings.ToLower(strings.TrimSpace(link.TLS)); tls != "" && tls != "none" && tls != "false" {
		proxy["tls"] = true
		if sni := firstNonEmpty(link.SNI, link.Host); sni != "" {
			proxy["servername"] = sni
		}
	}
	if fingerprint := strings.TrimSpace(link.FP); fingerprint != "" {
		proxy["client-fingerprint"] = fingerprint
	}
	if alpn := stringList(link.Alpn); len(alpn) > 0 {
		proxy["alpn"] = alpn
	}
	if err := transportOptions(proxy, link.Network, link.Path, link.Host, link.Path); err != nil {
		return manualNode{}, err
	}
	return finishManualNode(text, name, "vmess", server, port, proxy)
}

func vmessCipher(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "auto":
		return "auto"
	default:
		return strings.TrimSpace(value)
	}
}

// --------------------------------------------------------------------------
// vless / trojan / hysteria2, which are all URL-shaped

func parseVlessLink(parsed *url.URL) (manualNode, error) {
	server, port, err := linkEndpoint(parsed, "vless")
	if err != nil {
		return manualNode{}, err
	}
	uuid := linkSecret(parsed)
	if strings.TrimSpace(uuid) == "" {
		return manualNode{}, errLinkFields
	}
	query := parsed.Query()
	proxy := map[string]any{
		"name":   linkName(parsed, fmt.Sprintf("%s:%d", server, port)),
		"type":   "vless",
		"server": server,
		"port":   port,
		"uuid":   uuid,
		"udp":    true,
	}
	security := strings.ToLower(strings.TrimSpace(query.Get("security")))
	switch security {
	case "tls", "reality":
		proxy["tls"] = true
		if sni := strings.TrimSpace(query.Get("sni")); sni != "" {
			proxy["servername"] = sni
		}
		if fingerprint := strings.TrimSpace(query.Get("fp")); fingerprint != "" {
			proxy["client-fingerprint"] = fingerprint
		}
		if insecureValue(query.Get("allowInsecure")) {
			proxy["skip-cert-verify"] = true
		}
		if security == "reality" {
			publicKey := strings.TrimSpace(query.Get("pbk"))
			if publicKey == "" {
				return manualNode{}, errors.New("vless reality 链接缺少 public key（pbk）")
			}
			reality := map[string]any{"public-key": publicKey}
			if shortID := strings.TrimSpace(query.Get("sid")); shortID != "" {
				reality["short-id"] = shortID
			}
			proxy["reality-opts"] = reality
		}
	case "", "none":
	default:
		return manualNode{}, fmt.Errorf("暂不支持 vless 的 security=%s", security)
	}
	if flow := strings.TrimSpace(query.Get("flow")); flow != "" {
		proxy["flow"] = flow
	}
	if err := transportOptions(proxy,
		query.Get("type"), query.Get("path"),
		firstNonEmpty(query.Get("host"), query.Get("sni")),
		query.Get("serviceName")); err != nil {
		return manualNode{}, err
	}
	return finishManualNode(parsed.String(), stringValue(proxy["name"]), "vless", server, port, proxy)
}

func parseTrojanLink(parsed *url.URL) (manualNode, error) {
	server, port, err := linkEndpoint(parsed, "trojan")
	if err != nil {
		return manualNode{}, err
	}
	password := linkSecret(parsed)
	if strings.TrimSpace(password) == "" {
		return manualNode{}, errLinkFields
	}
	query := parsed.Query()
	proxy := map[string]any{
		"name":     linkName(parsed, fmt.Sprintf("%s:%d", server, port)),
		"type":     "trojan",
		"server":   server,
		"port":     port,
		"password": password,
		"udp":      true,
	}
	if sni := firstNonEmpty(query.Get("sni"), query.Get("peer")); sni != "" {
		proxy["sni"] = sni
	}
	if insecureValue(firstNonEmpty(query.Get("allowInsecure"), query.Get("insecure"))) {
		proxy["skip-cert-verify"] = true
	}
	if alpn := splitList(query.Get("alpn")); len(alpn) > 0 {
		proxy["alpn"] = alpn
	}
	if err := transportOptions(proxy,
		query.Get("type"), query.Get("path"),
		query.Get("host"), query.Get("serviceName")); err != nil {
		return manualNode{}, err
	}
	return finishManualNode(parsed.String(), stringValue(proxy["name"]), "trojan", server, port, proxy)
}

func parseHysteria2Link(parsed *url.URL) (manualNode, error) {
	server, port, err := linkEndpoint(parsed, "hysteria2")
	if err != nil {
		return manualNode{}, err
	}
	// hysteria2 puts "user:password" or just "password" in the userinfo.
	password := linkSecret(parsed)
	if strings.TrimSpace(password) == "" {
		return manualNode{}, errLinkFields
	}
	query := parsed.Query()
	proxy := map[string]any{
		"name":     linkName(parsed, fmt.Sprintf("%s:%d", server, port)),
		"type":     "hysteria2",
		"server":   server,
		"port":     port,
		"password": password,
	}
	if sni := strings.TrimSpace(query.Get("sni")); sni != "" {
		proxy["sni"] = sni
	}
	if insecureValue(query.Get("insecure")) {
		proxy["skip-cert-verify"] = true
	}
	if obfs := strings.TrimSpace(query.Get("obfs")); obfs != "" {
		proxy["obfs"] = obfs
		if obfsPassword := strings.TrimSpace(query.Get("obfs-password")); obfsPassword != "" {
			proxy["obfs-password"] = obfsPassword
		}
	}
	if alpn := splitList(query.Get("alpn")); len(alpn) > 0 {
		proxy["alpn"] = alpn
	}
	return finishManualNode(parsed.String(), stringValue(proxy["name"]), "hysteria2", server, port, proxy)
}

// --------------------------------------------------------------------------
// shadowsocks

// parseShadowsocksLink handles both link shapes in the wild: SIP002, where only
// the userinfo is base64, and the older form where method, password, host and
// port are all wrapped together.
func parseShadowsocksLink(text string, parsed *url.URL) (manualNode, error) {
	body := strings.TrimPrefix(strings.TrimSpace(text), "ss://")
	if queryAt := strings.IndexAny(body, "?"); queryAt >= 0 {
		if plugin := parsed.Query().Get("plugin"); plugin != "" {
			return manualNode{}, errors.New("暂不支持带 plugin 的 shadowsocks 链接")
		}
		body = body[:queryAt]
	}
	if cut := strings.Index(body, "#"); cut >= 0 {
		body = body[:cut]
	}

	var credentials string
	var host, port string
	if at := strings.LastIndex(body, "@"); at >= 0 {
		decoded, err := decodeSubscriptionBase64(body[:at])
		if err != nil {
			// Some providers leave the credentials unencoded.
			decoded = []byte(body[:at])
		}
		credentials = string(decoded)
		host, port, _ = strings.Cut(body[at+1:], ":")
	} else {
		decoded, err := decodeSubscriptionBase64(body)
		if err != nil {
			return manualNode{}, errors.New("ss 链接的 Base64 无法解码")
		}
		account, endpoint, found := strings.Cut(string(decoded), "@")
		if !found {
			return manualNode{}, errLinkFields
		}
		credentials = account
		host, port, _ = strings.Cut(endpoint, ":")
	}

	cipher, password, found := strings.Cut(credentials, ":")
	if !found || strings.TrimSpace(cipher) == "" || password == "" {
		return manualNode{}, errLinkFields
	}
	host = strings.TrimSpace(host)
	value := atoiPort(port)
	if host == "" || value == 0 {
		return manualNode{}, errLinkFields
	}
	proxy := map[string]any{
		"name":     linkName(parsed, host),
		"type":     "ss",
		"server":   host,
		"port":     value,
		"cipher":   strings.TrimSpace(cipher),
		"password": password,
		"udp":      true,
	}
	return finishManualNode(parsed.String(), stringValue(proxy["name"]), "ss", host, value, proxy)
}

// --------------------------------------------------------------------------
// ssr / tuic / hysteria

func parseSSRLink(text string) (manualNode, error) {
	encoded := strings.TrimPrefix(strings.TrimSpace(text), "ssr://")
	decodedBytes, err := decodeSubscriptionBase64(encoded)
	if err != nil {
		return manualNode{}, errors.New("ssr 链接的 Base64 无法解码")
	}
	decoded := string(decodedBytes)
	mainPart, queryPart, foundQuery := strings.Cut(decoded, "/?")
	if !foundQuery {
		mainPart, queryPart, _ = strings.Cut(decoded, "?")
	}
	mainPart = strings.TrimSuffix(mainPart, "/")

	parts := strings.Split(mainPart, ":")
	if len(parts) < 6 {
		return manualNode{}, errLinkFields
	}
	base64Pass := parts[len(parts)-1]
	obfs := parts[len(parts)-2]
	method := parts[len(parts)-3]
	protocol := parts[len(parts)-4]
	portStr := parts[len(parts)-5]
	host := strings.Join(parts[:len(parts)-5], ":")
	host = strings.Trim(strings.TrimSpace(host), "[]")
	port := atoiPort(portStr)
	if host == "" || port == 0 {
		return manualNode{}, errLinkFields
	}

	password := base64Pass
	if passBytes, err := decodeSubscriptionBase64(base64Pass); err == nil && len(passBytes) > 0 {
		password = string(passBytes)
	}

	name := fmt.Sprintf("%s:%d", host, port)
	var obfsparam, protoparam string
	if queryPart != "" {
		for _, param := range strings.Split(queryPart, "&") {
			k, v, found := strings.Cut(param, "=")
			if !found {
				continue
			}
			k = strings.ToLower(strings.TrimSpace(k))
			v = strings.TrimSpace(v)
			decVal, _ := decodeSubscriptionBase64(v)
			valStr := string(decVal)
			switch k {
			case "remarks":
				if valStr != "" {
					name = valStr
				}
			case "obfsparam":
				obfsparam = valStr
			case "protoparam":
				protoparam = valStr
			}
		}
	}

	proxy := map[string]any{
		"name":     name,
		"type":     "ssr",
		"server":   host,
		"port":     port,
		"cipher":   strings.TrimSpace(method),
		"password": password,
		"protocol": strings.TrimSpace(protocol),
		"obfs":     strings.TrimSpace(obfs),
		"udp":      true,
	}
	if obfsparam != "" {
		proxy["obfs-param"] = obfsparam
	}
	if protoparam != "" {
		proxy["protocol-param"] = protoparam
	}
	return finishManualNode(text, name, "ssr", host, port, proxy)
}

func parseTuicLink(parsed *url.URL) (manualNode, error) {
	server, port, err := linkEndpoint(parsed, "tuic")
	if err != nil {
		return manualNode{}, err
	}
	uuid := linkSecret(parsed)
	password := ""
	if parsed.User != nil {
		uuid = parsed.User.Username()
		password, _ = parsed.User.Password()
	}
	if strings.TrimSpace(uuid) == "" && strings.TrimSpace(password) == "" {
		return manualNode{}, errLinkFields
	}
	if uuid == "" {
		uuid = password
	}
	query := parsed.Query()
	name := linkName(parsed, fmt.Sprintf("%s:%d", server, port))
	proxy := map[string]any{
		"name":     name,
		"type":     "tuic",
		"server":   server,
		"port":     port,
		"uuid":     uuid,
		"password": password,
		"udp":      true,
	}
	if sni := firstNonEmpty(query.Get("sni"), query.Get("server_name")); sni != "" {
		proxy["sni"] = sni
	}
	if insecureValue(firstNonEmpty(query.Get("allow_insecure"), query.Get("insecure"), query.Get("allowInsecure"))) {
		proxy["skip-cert-verify"] = true
	}
	if cc := firstNonEmpty(query.Get("congestion_controller"), query.Get("congestion_control"), query.Get("cc")); cc != "" {
		proxy["congestion-controller"] = cc
	}
	if alpn := splitList(query.Get("alpn")); len(alpn) > 0 {
		proxy["alpn"] = alpn
	}
	if mode := query.Get("udp_relay_mode"); mode != "" {
		proxy["udp-relay-mode"] = mode
	}
	return finishManualNode(parsed.String(), name, "tuic", server, port, proxy)
}

func parseHysteriaLink(parsed *url.URL) (manualNode, error) {
	server, port, err := linkEndpoint(parsed, "hysteria")
	if err != nil {
		return manualNode{}, err
	}
	query := parsed.Query()
	auth := firstNonEmpty(linkSecret(parsed), query.Get("auth"))
	name := linkName(parsed, fmt.Sprintf("%s:%d", server, port))
	proxy := map[string]any{
		"name":   name,
		"type":   "hysteria",
		"server": server,
		"port":   port,
	}
	if auth != "" {
		proxy["auth_str"] = auth
	}
	if up := atoiPort(firstNonEmpty(query.Get("upmbps"), query.Get("up"))); up > 0 {
		proxy["up"] = up
	}
	if down := atoiPort(firstNonEmpty(query.Get("downmbps"), query.Get("down"))); down > 0 {
		proxy["down"] = down
	}
	if sni := firstNonEmpty(query.Get("peer"), query.Get("sni")); sni != "" {
		proxy["sni"] = sni
	}
	if insecureValue(firstNonEmpty(query.Get("insecure"), query.Get("allowInsecure"))) {
		proxy["skip-cert-verify"] = true
	}
	if alpn := splitList(query.Get("alpn")); len(alpn) > 0 {
		proxy["alpn"] = alpn
	}
	if obfs := query.Get("obfs"); obfs != "" {
		proxy["obfs"] = obfs
	}
	return finishManualNode(parsed.String(), name, "hysteria", server, port, proxy)
}

// --------------------------------------------------------------------------
// Helpers

func finishManualNode(uri, name, kind, server string, port int, proxy map[string]any) (manualNode, error) {
	if strings.TrimSpace(name) == "" {
		name = fmt.Sprintf("%s:%d", server, port)
	}
	proxy["name"] = name
	return manualNode{
		Name: name, URI: uri, Type: kind, Server: server, Port: port, Proxy: proxy,
	}, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func portValue(raw any) (int, error) {
	switch value := raw.(type) {
	case string:
		return atoiPort(value), nil
	case float64:
		return int(value), nil
	case int:
		return value, nil
	case nil:
		return 0, errLinkFields
	default:
		return 0, errLinkFields
	}
}

func atoiPort(text string) int {
	value, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil || value < 1 || value > 65535 {
		return 0
	}
	return value
}

func intValue(raw any) int {
	switch value := raw.(type) {
	case float64:
		return int(value)
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return 0
		}
		return parsed
	case int:
		return value
	default:
		return 0
	}
}

// stringList reads the two shapes an "alpn" field arrives in: a JSON array or a
// comma separated string.
func stringList(raw any) []string {
	switch value := raw.(type) {
	case []any:
		result := make([]string, 0, len(value))
		for _, entry := range value {
			if text, ok := entry.(string); ok && strings.TrimSpace(text) != "" {
				result = append(result, strings.TrimSpace(text))
			}
		}
		return result
	case string:
		return splitList(value)
	default:
		return nil
	}
}

func splitList(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}
