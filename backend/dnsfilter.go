package main

import (
	"net"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// maxProxyServerDomains bounds how many entries a provider may contribute to
// the generated fake-ip filter. Subscriptions in the wild list a few hundred
// nodes at most, and the suffixes collapse, so this only guards against a
// hostile or broken file.
const maxProxyServerDomains = 256

// hostnameSuffix reports whether value looks like a plain DNS name and, when it
// has more than one label, returns the last two labels. The provider file is
// third-party input that ends up inside our own generated configuration, so
// anything that is not a bare hostname is dropped rather than escaped.
func hostnameSuffix(value string) (string, bool) {
	host := strings.ToLower(strings.TrimSpace(value))
	host = strings.Trim(host, `"'`)
	host = strings.TrimSuffix(host, ".")
	if host == "" || len(host) > 253 || net.ParseIP(host) != nil {
		return "", false
	}
	labels := strings.Split(host, ".")
	for _, label := range labels {
		if label == "" || len(label) > 63 {
			return "", false
		}
		for _, char := range label {
			switch {
			case char >= 'a' && char <= 'z', char >= '0' && char <= '9', char == '-':
			default:
				return "", false
			}
		}
		if strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return "", false
		}
	}
	if len(labels) > 2 {
		labels = labels[len(labels)-2:]
	}
	suffix := strings.Join(labels, ".")
	// A filter entry without a dot would exclude an entire top-level domain.
	if !strings.Contains(suffix, ".") {
		return "", false
	}
	return "+." + suffix, true
}

// proxyServerDomains turns the node hostnames of a downloaded subscription into
// fake-ip filter entries.
//
// Every outbound connection starts by resolving one of these names. If a name
// is answered from the fake-ip pool, the kernel dials the fake address, which
// belongs to the TUN itself: the connection is captured back into the tunnel and
// the node can never be reached. Mihomo resolves proxy server names through the
// same resolver, so they have to be excluded explicitly.
// mergeProxyServerDomains combines filter entries from several sources, dropping
// duplicates and sorting the result so the generated config is stable.
func mergeProxyServerDomains(lists ...[]string) []string {
	seen := map[string]bool{}
	merged := make([]string, 0, 8)
	for _, list := range lists {
		for _, entry := range list {
			if entry == "" || seen[entry] {
				continue
			}
			seen[entry] = true
			merged = append(merged, entry)
		}
	}
	sort.Strings(merged)
	return merged
}

func proxyServerDomains(body []byte) []string {
	seen := map[string]bool{}
	domains := make([]string, 0, 8)
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "- ")
		if !strings.HasPrefix(line, "server:") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(line, "server:"))
		if cut := strings.IndexAny(value, " \t#"); cut >= 0 {
			value = value[:cut]
		}
		entry, ok := hostnameSuffix(value)
		if !ok || seen[entry] {
			continue
		}
		seen[entry] = true
		domains = append(domains, entry)
		if len(domains) >= maxProxyServerDomains {
			break
		}
	}
	sort.Strings(domains)
	return domains
}

// proxyServerHostname returns one of the subscription's own node hostnames, as
// it is written in the profile.
//
// It is not the same thing as the suffixes above, and the difference is the
// point: a subscription whose nodes live under `qos.onl` has hostnames like
// `a957b84f-....qos.onl`, and the suffix itself is a name that resolves to
// nothing. A check that asked for the suffix would report every node as
// unreachable, which is a false alarm about the whole subscription.
func proxyServerHostname(body []byte) string {
	var profile struct {
		Proxies []subscriptionProxy `yaml:"proxies"`
	}
	if yaml.Unmarshal(body, &profile) != nil {
		return ""
	}
	for _, proxy := range profile.Proxies {
		host := strings.TrimSpace(proxy.Server)
		if host == "" || net.ParseIP(host) != nil {
			continue
		}
		if _, ok := hostnameSuffix(host); !ok {
			continue
		}
		return host
	}
	return ""
}
