//go:build android

package androidhosts

import (
	"runtime"
	"sync"
	"testing"

	"github.com/metacubex/mihomo/component/resolver/hosts"
)

// Exercise Mihomo's actual system hosts cache without DNS, SmartVPN or a VPN.
func TestConcurrentStaticHostLookup(t *testing.T) {
	const rounds = 50000
	addresses, canonical := hosts.LookupStaticHost("localhost")
	if len(addresses) == 0 || canonical != "localhost" {
		t.Fatal("device must have a localhost entry in /etc/hosts")
	}
	var workers sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := 0; i < rounds; i++ {
				name := [...]string{"localhost", "LoCaLhOsT", "127.0.0.1"}[i%3]
				got, target := hosts.LookupStaticHost(name)
				if name != "127.0.0.1" && (len(got) == 0 || got[0] != addresses[0] || target != canonical) {
					t.Errorf("incorrect static lookup for %q", name)
					return
				}
				if i%2048 == 0 {
					runtime.GC()
				}
			}
		}()
	}
	workers.Wait()
}
