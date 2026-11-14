package main

import (
	"github.com/metacubex/mihomo/component/dialer"
	"net"
	"net/url"
	"strings"

	D "github.com/metacubex/mihomo/dns"
	"gopkg.in/yaml.v3"
)

// Probes can run before the first kernel configuration is loaded.
func init() { dialer.SetTcpConcurrent(true) }

// Only exact node hostnames can inherit a subscription's HTTPS resolvers.
func entryDNS(profile []byte) map[string][]string {
	var source struct {
		Proxies []subscriptionProxy
		DNS     struct {
			Policy map[string]any `yaml:"nameserver-policy"`
		}
	}
	if yaml.Unmarshal(profile, &source) != nil || len(source.DNS.Policy) == 0 {
		return nil
	}
	out := map[string][]string{}
	for _, p := range source.Proxies {
		host := strings.ToLower(p.Server)
		if _, ok := hostnameSuffix(host); !ok {
			continue
		}
		if _, ok := out[host]; ok {
			continue
		}
		var values []any
		switch v := source.DNS.Policy[host].(type) {
		case string:
			values = []any{v}
		case []any:
			values = v
		}
		for _, v := range values {
			raw, ok := v.(string)
			if !ok {
				continue
			}
			u, err := url.Parse(raw)
			if err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.Fragment == "" {
				out[host] = append(out[host], raw)
			}
		}
	}
	return out
}

func newEntryResolver(urls []string, iface, host string) (*D.Resolver, error) {
	if net.ParseIP(host) != nil {
		return nil, nil
	}
	var main, bootstrap []D.NameServer
	if len(urls) > 0 {
		for _, url := range urls {
			main = append(main, D.NameServer{Net: "https", Addr: url, ProxyName: iface})
		}
		for _, server := range []string{"223.5.5.5:53", "119.29.29.29:53"} {
			bootstrap = append(bootstrap, D.NameServer{Net: "udp", Addr: server, ProxyName: iface})
		}
	} else if iface != "" {
		servers, err := probeInterfaceDNS(iface)
		if err != nil {
			return nil, err
		}
		for _, server := range servers {
			main = append(main, D.NameServer{Net: "udp", Addr: net.JoinHostPort(server, "53"), ProxyName: iface})
		}
		bootstrap = main
	}
	if len(main) == 0 {
		return nil, nil
	}
	rs := D.NewResolver(D.Config{Main: main, Default: bootstrap, IPv6: true})
	return rs.Resolver, nil
}

func entryDNSSection(policy map[string][]string) string {
	if len(policy) == 0 {
		return ""
	}
	body, _ := yaml.Marshal(map[string]any{"proxy-server-nameserver-policy": policy})
	return "  " + strings.ReplaceAll(strings.TrimSuffix(string(body), "\n"), "\n", "\n  ") + "\n"
}
