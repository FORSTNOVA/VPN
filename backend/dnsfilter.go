package main

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
	q := policyDnsRequest{Op: "suffix", Path: value}
	fx := &appPolicyDnsEffects{q: q}
	r := runSelectedPolicyDns(q, fx)
	return r.Text, r.Data["ok"].(bool)
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
	q := policyDnsRequest{Op: "merge"}
	fx := &appPolicyDnsEffects{q: q, lists: lists}
	return runSelectedPolicyDns(q, fx).Data["domains"].([]string)
}

func proxyServerDomains(body []byte) []string {
	q := policyDnsRequest{Op: "domains", Body: body}
	fx := &appPolicyDnsEffects{q: q}
	return runSelectedPolicyDns(q, fx).Data["domains"].([]string)
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
	q := policyDnsRequest{Op: "hostname", Body: body}
	fx := &appPolicyDnsEffects{q: q}
	return runSelectedPolicyDns(q, fx).Text
}
