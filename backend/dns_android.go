//go:build android

package main

import (
	"context"
	"net"
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
// So the service asks resolvers that answer on the open network. They are the
// same ones the kernel is configured with, and the same two, so a lookup that
// works here and a lookup that works there mean the same thing.
func init() {
	servers := []string{"223.5.5.5:53", "119.29.29.29:53"}
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var lastErr error
			for _, server := range servers {
				dialer := net.Dialer{Timeout: 4 * time.Second}
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
