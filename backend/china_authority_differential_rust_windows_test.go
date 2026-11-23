//go:build windows && rustcore

package main

import (
	"math/rand"
	"reflect"
	"testing"
	"time"
)

func TestServiceStateActualChinaAuthority(t *testing.T) {
	authorityRequireRust(t)
	chinaAuthorityGolden(t, authorityRustFactory)
}
func TestServiceStateActualChinaSixDomains(t *testing.T) {
	authorityRequireRust(t)
	a := regionAuthorityNew(t, authorityRustFactory)
	b := regionAuthorityNew(t, func() (serviceStateBackend, error) { return newGoServiceStateBackend(), nil })
	owner := a.service.backend
	now := time.Now()
	rng := rand.New(rand.NewSource(33081))
	for i := 0; i < 1800; i++ {
		op := rng.Intn(8)
		v := rng.Intn(9999)
		for _, x := range []*app{a, b} {
			switch op {
			case 0:
				x.chinaCommandLocked(serviceChinaCommand{Action: 1, Status: chinaIPStatus{Available: i%2 == 0, Entries: v, Error: "synthetic"}})
			case 1:
				x.chinaCommandLocked(serviceChinaCommand{Action: uint8(2 + i%4), Flag: i%3 == 0})
			case 2:
				x.patchControlLocked(controlSocksPort, serviceControlState{SocksPort: v})
			case 3:
				x.replacePathConfigLocked(pathCheckSettings{IntervalSec: v})
			case 4:
				x.writeHealthRecordLocked("key", nodeHealth{Name: "other", UpdatedAt: now})
			case 5:
				x.setProfileFromCacheLocked(i%2 == 0)
			case 6:
				if e := x.service.baseline(trafficSample{at: now.Add(time.Duration(i)), up: int64(v)}); e != nil {
					t.Fatal(e)
				}
			case 7:
				x.setHealthBlockedLocked(i%2 == 0)
			}
		}
		if !reflect.DeepEqual(a.chinaStateLocked(), b.chinaStateLocked()) || !reflect.DeepEqual(a.controlLocked(), b.controlLocked()) || !reflect.DeepEqual(a.healthDomainLocked(), b.healthDomainLocked()) || !reflect.DeepEqual(a.pathConfigLocked(), b.pathConfigLocked()) || a.profileFromCacheLocked() != b.profileFromCacheLocked() {
			t.Fatalf("sixdomain mismatch %d", i)
		}
		av, bv := authoritySnapshot(t, a.service), authoritySnapshot(t, b.service)
		av.Epoch = 0
		bv.Epoch = 0
		if !reflect.DeepEqual(av, bv) || a.service.backend != owner {
			t.Fatalf("metadata/owner %d", i)
		}
	}
}
