//go:build rustcore && ((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"runtime"
	"strconv"
	"unsafe"
)

func rustNodeFieldsAvailable() bool { return rustCoreAvailable() && rustCore.nodeFields != 0 }
func appendNodeField(out []byte, s string) []byte {
	out = binary.LittleEndian.AppendUint64(out, uint64(len(s)))
	return append(out, s...)
}
func rustExtendedNodeFieldsAvailable() bool {
	return rustCoreAvailable() && rustCore.nodeFieldsExtended != 0
}
func rustNodeFields(parsed *url.URL, kind string) (manualNode, error, bool) {
	if !rustNodeFieldsAvailable() {
		return manualNode{}, nil, false
	}
	proc := rustCore.nodeFields
	if kind == "tuic" || kind == "hysteria" {
		if !rustExtendedNodeFieldsAvailable() {
			return manualNode{}, nil, false
		}
		proc = rustCore.nodeFieldsExtended
	}
	server, port, err := linkEndpoint(parsed, kind)
	if err != nil {
		return manualNode{}, err, true
	}
	name := linkName(parsed, fmt.Sprintf("%s:%d", server, port))
	input := []byte{}
	for _, s := range []string{kind, server, strconv.Itoa(port), name, linkSecret(parsed)} {
		input = appendNodeField(input, s)
	}
	query := parsed.Query()
	for _, key := range []string{"security", "sni", "fp", "allowInsecure", "insecure", "pbk", "sid", "flow", "type", "path", "host", "serviceName", "peer", "alpn", "obfs", "obfs-password", "server_name", "allow_insecure", "congestion_controller", "congestion_control", "cc", "udp_relay_mode", "auth", "upmbps", "up", "downmbps", "down"} {
		input = appendNodeField(input, key)
		input = appendNodeField(input, query.Get(key))
	}
	if kind == "tuic" {
		username, password := "", ""
		if parsed.User != nil {
			username = parsed.User.Username()
			password, _ = parsed.User.Password()
		}
		input = appendNodeField(input, "__smartvpn_tuic_username")
		input = appendNodeField(input, username)
		input = appendNodeField(input, "__smartvpn_tuic_password")
		input = appendNodeField(input, password)
	}
	if len(input) > 8*1024*1024 {
		return manualNode{}, nil, false
	}
	output := make([]byte, len(input)+256)
	for attempt := 0; attempt < 2; attempt++ {
		var written uintptr
		code, _, _ := nativeCall(proc, uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)), uintptr(unsafe.Pointer(&output[0])), uintptr(len(output)), uintptr(unsafe.Pointer(&written)))
		runtime.KeepAlive(input)
		runtime.KeepAlive(output)
		status := int32(code)
		if status == -2 && written > uintptr(len(output)) && written <= 16*1024*1024 {
			output = make([]byte, int(written))
			continue
		}
		if written > uintptr(len(output)) {
			return manualNode{}, nil, false
		}
		out := output[:written]
		if status == 1 {
			return manualNode{}, errors.New(string(out)), true
		}
		if status == 2 {
			return manualNode{}, fmt.Errorf("暂不支持 %q 传输方式", string(out)), true
		}
		if status != 0 {
			return manualNode{}, nil, false
		}
		proxy, ok := decodeRustNodeValue(out)
		if !ok {
			return manualNode{}, nil, false
		}
		if proxy["name"] != name || proxy["server"] != server || proxy["port"] != port || proxy["type"] != kind {
			return manualNode{}, nil, false
		}
		node, err := finishManualNode(parsed.String(), name, kind, server, port, proxy)
		return node, err, true
	}
	return manualNode{}, nil, false
}
func parseVlessLink(parsed *url.URL) (manualNode, error) {
	if n, e, ok := rustNodeFields(parsed, "vless"); ok {
		return n, e
	}
	return parseVlessLinkGo(parsed)
}
func parseTrojanLink(parsed *url.URL) (manualNode, error) {
	if n, e, ok := rustNodeFields(parsed, "trojan"); ok {
		return n, e
	}
	return parseTrojanLinkGo(parsed)
}
func parseHysteria2Link(parsed *url.URL) (manualNode, error) {
	if n, e, ok := rustNodeFields(parsed, "hysteria2"); ok {
		return n, e
	}
	return parseHysteria2LinkGo(parsed)
}

func parseTuicLink(parsed *url.URL) (manualNode, error) {
	if n, e, ok := rustNodeFields(parsed, "tuic"); ok {
		return n, e
	}
	return parseTuicLinkGo(parsed)
}
func parseHysteriaLink(parsed *url.URL) (manualNode, error) {
	if n, e, ok := rustNodeFields(parsed, "hysteria"); ok {
		return n, e
	}
	return parseHysteriaLinkGo(parsed)
}

func rustLegacyNodeFieldsAvailable() bool {
	return rustCoreAvailable() && rustCore.nodeFieldsLegacy != 0
}

// Pure normalized field boundary. Never parses network URLs or opens sockets.
func rustLegacyNodeFields(uri, kind, server string, port int, name, secret string, query map[string]string) (manualNode, error, bool) {
	if !rustLegacyNodeFieldsAvailable() {
		return manualNode{}, nil, false
	}
	input := []byte{}
	for _, value := range []string{kind, server, strconv.Itoa(port), name, secret} {
		input = appendNodeField(input, value)
	}
	for key, value := range query {
		input = appendNodeField(input, key)
		input = appendNodeField(input, value)
	}
	if len(input) > 8*1024*1024 {
		return manualNode{}, nil, false
	}
	output := make([]byte, len(input)+256)
	for attempt := 0; attempt < 2; attempt++ {
		var written uintptr
		code, _, _ := nativeCall(rustCore.nodeFieldsLegacy, uintptr(unsafe.Pointer(&input[0])), uintptr(len(input)), uintptr(unsafe.Pointer(&output[0])), uintptr(len(output)), uintptr(unsafe.Pointer(&written)))
		runtime.KeepAlive(input)
		runtime.KeepAlive(output)
		status := int32(code)
		if status == -2 && written > uintptr(len(output)) && written <= 16*1024*1024 {
			output = make([]byte, int(written))
			continue
		}
		if written > uintptr(len(output)) {
			return manualNode{}, nil, false
		}
		out := output[:written]
		if status == 1 {
			return manualNode{}, errors.New(string(out)), true
		}
		if status == 2 {
			return manualNode{}, fmt.Errorf("暂不支持 %q 传输方式", string(out)), true
		}
		if status != 0 {
			return manualNode{}, nil, false
		}
		proxy, ok := decodeRustNodeValue(out)
		if !ok || proxy["name"] != name || proxy["server"] != server || proxy["port"] != port || proxy["type"] != kind {
			return manualNode{}, nil, false
		}
		node, err := finishManualNode(uri, name, kind, server, port, proxy)
		return node, err, true
	}
	return manualNode{}, nil, false
}
func parseVmessLink(text string) (manualNode, error) {
	if !rustLegacyNodeFieldsAvailable() {
		return parseVmessLinkGo(text)
	}
	return parseVmessLinkWithFields(text, true)
}
func parseShadowsocksLink(text string, parsed *url.URL) (manualNode, error) {
	if !rustLegacyNodeFieldsAvailable() {
		return parseShadowsocksLinkGo(text, parsed)
	}
	return parseShadowsocksLinkWithFields(text, parsed, true)
}
func parseSSRLink(text string) (manualNode, error) {
	if !rustLegacyNodeFieldsAvailable() {
		return parseSSRLinkGo(text)
	}
	return parseSSRLinkWithFields(text, true)
}
