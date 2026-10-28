//go:build android

package main

import (
	"context"
	"net"
	"sync/atomic"
	"time"
)

// This process resolves names through servers of its own rather than through
// the system's resolver.
//
// The reason is peculiar to this platform and worth spelling out. This app is
// excluded from its own tunnel, on purpose: a subscription has to be fetched
// before there is any node to fetch it through, and the kernel's connections to
// the proxy servers are the tunnel's own upstream — they cannot be carried by
// what they carry. Android honours that exclusion for routing, but not for the
// resolver: an app excluded from a VPN is still told to use the VPN's DNS
// address. That address only exists inside the tunnel, so every lookup from
// here would go to an address that cannot be reached, and a lookup that cannot
// be answered is not a quick failure — it is a timeout, which is how a checks
// page ends up holding the service for minutes.
//
// So the service asks resolvers that answer on the open network. Multiple
// resilient upstream servers are configured with round-robin rotation so that
// packet drops or port-53 UDP interception on cellular networks do not stall
// retries on the same unreachable upstream.
var (
	androidDNSServers = []string{
		"223.5.5.5:53",      // AliDNS primary
		"119.29.29.29:53",    // DNSPod / Tencent primary
		"114.114.114.114:53", // 114DNS (carrier-friendly nationwide)
		"223.6.6.6:53",      // AliDNS secondary
		"180.76.76.76:53",    // Baidu DNS
	}
	androidDNSCounter uint32
)

func init() {
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			count := len(androidDNSServers)
			start := int(atomic.AddUint32(&androidDNSCounter, 1)) % count
			var lastErr error
			for i := 0; i < count; i++ {
				server := androidDNSServers[(start+i)%count]
				dialer := net.Dialer{Timeout: 2500 * time.Millisecond}
				conn, err := dialer.DialContext(ctx, network, server)
				if err == nil {
					return conn, nil
				}
				lastErr = err
			}
			return nil, lastErr
		},
	}
}
