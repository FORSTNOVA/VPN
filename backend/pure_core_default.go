//go:build !rustcore || !((windows && amd64) || (android && (arm64 || amd64) && cgo))

package main

import (
	"io"
	"net/url"
	"time"
)

// The Go implementation remains the default on every platform.
func selectSTUNRelayReply(packet, request []byte) []byte {
	return selectSTUNRelayReplyGo(packet, request)
}
func selectDNSRelayReply(packet []byte, id uint16) []byte   { return selectDNSRelayReplyGo(packet, id) }
func relayHandshakeFrame(method []byte) ([]byte, bool)      { return relayHandshakeFrameGo(method) }
func readRelayEndpoint(r io.Reader) (string, error)         { return readRelayEndpointGo(r) }
func encodeSTUNRequest(id [12]byte) []byte                  { return encodeSTUNRequestGo(id) }
func matchesSTUNReply(request, reply []byte) bool           { return matchesSTUNReplyGo(request, reply) }
func encodeDNSQuery(name string, id uint16) ([]byte, error) { return encodeDNSQueryGo(name, id) }
func encodeRelayHeader(kind byte, address []byte, port uint16) []byte {
	return encodeRelayHeaderGo(kind, address, port)
}
func socks5UDPPayload(packet []byte) ([]byte, error)  { return socks5UDPPayloadGo(packet) }
func parseDNSFirstA(message []byte) (string, error)   { return parseDNSFirstAGo(message) }
func parseSTUNAddress(message []byte) (string, error) { return parseSTUNAddressGo(message) }
func throughputMbps(bytes int64, elapsed time.Duration) float64 {
	return throughputMbpsGo(bytes, elapsed)
}

func nativeQueryNodes([]byte) ([]proxyNode, bool)                   { return nil, false }
func nativeLabelDocument([]byte) (map[string]any, bool)             { return nil, false }
func nativeNodeAnnotations([]proxyNode, *string) ([]byte, bool)     { return nil, false }
func nativeManualDetails([]manualNode) (map[string]proxyNode, bool) { return nil, false }
func nativeSubscriptionPolicy([]byte) *decodedSubscriptionPolicy    { return nil }
func nativeProfileObjects([]byte) ([]map[string]any, bool)          { return nil, false }
func nativeProfileNodes([]byte) ([]proxyNode, bool)                 { return nil, false }
func nativeMergedYAML(map[string]any) ([]byte, bool)                { return nil, false }

func parseSubscriptionUserInfo(header string) *subscriptionUserInfo {
	return parseSubscriptionUserInfoGo(header)
}
func mergeRuleTarget(rule string) (int, int, bool) { return mergeRuleTargetGo(rule) }

func decodeSubscriptionBase64(input string) ([]byte, error) { return decodeSubscriptionBase64Go(input) }
func hasProxyURI(input string) bool                         { return hasProxyURIGo(input) }
func isInformationalNode(input string) bool                 { return isInformationalNodeGo(input) }
func isProxyURILink(input string) bool                      { return isProxyURILinkGo(input) }
func isInlineSubscription(input string) bool                { return isInlineSubscriptionGo(input) }
func categorizeSubscriptionInput(input string) ([]string, []string) {
	return categorizeSubscriptionInputGo(input)
}

func parseVlessLink(parsed *url.URL) (manualNode, error)     { return parseVlessLinkGo(parsed) }
func parseTrojanLink(parsed *url.URL) (manualNode, error)    { return parseTrojanLinkGo(parsed) }
func parseHysteria2Link(parsed *url.URL) (manualNode, error) { return parseHysteria2LinkGo(parsed) }

func parseTuicLink(parsed *url.URL) (manualNode, error)     { return parseTuicLinkGo(parsed) }
func parseHysteriaLink(parsed *url.URL) (manualNode, error) { return parseHysteriaLinkGo(parsed) }

func parseVmessLink(text string) (manualNode, error) { return parseVmessLinkGo(text) }
func parseShadowsocksLink(text string, parsed *url.URL) (manualNode, error) {
	return parseShadowsocksLinkGo(text, parsed)
}
func parseSSRLink(text string) (manualNode, error) { return parseSSRLinkGo(text) }
func rustLegacyNodeFields(uri, kind, server string, port int, name, secret string, query map[string]string) (manualNode, error, bool) {
	return manualNode{}, nil, false
}
