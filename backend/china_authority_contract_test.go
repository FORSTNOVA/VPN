package main

import (
	"reflect"
	"testing"
	"time"
)

func chinaAuthorityGolden(t *testing.T, factory authorityFactory) {
	a := regionAuthorityNew(t, factory)
	other := regionAuthorityNew(t, factory)
	owner := a.service.backend
	now := time.Now()
	if e := a.service.baseline(trafficSample{at: now, up: 71, down: 73}); e != nil {
		t.Fatal(e)
	}
	a.replacePathConfigLocked(pathCheckSettings{Enabled: true, IntervalSec: 57})
	a.writeRegionRecordLocked("key", regionRecord{Name: "other", Country: "JP", VerifiedAt: now})
	a.patchControlLocked(controlSelectedNode, serviceControlState{SelectedNode: "selected"})
	info := &subscriptionUserInfo{Total: 99}
	a.restoreCatalogEntriesLocked([]subscriptionEntry{{ID: "entry", UserInfo: info}})
	status := chinaIPStatus{Available: true, Entries: 1234, UpdatedAt: "2026-01-01T12:00:00+08:00", Source: "synthetic", Error: "warning"}
	a.chinaCommandLocked(serviceChinaCommand{Action: 1, Status: status})
	a.chinaCommandLocked(serviceChinaCommand{Action: 2, Flag: true})
	a.chinaCommandLocked(serviceChinaCommand{Action: 3, Flag: true})
	a.chinaCommandLocked(serviceChinaCommand{Action: 4, Flag: false})
	a.setChinaActiveLocked(true)
	projected := a.chinaIPStatusLocked()
	if !projected.Active || !projected.Refreshing || projected.Entries != 1234 {
		t.Fatal("status overlay", projected)
	}
	projected.Error = "outside"
	if a.chinaStateLocked().Status.Error != "warning" || a.chinaStateLocked().Status.Active || a.chinaStateLocked().Status.Refreshing {
		t.Fatal("projection wrote underlying status")
	}
	a.chinaCommandLocked(serviceChinaCommand{Action: 1, Status: chinaIPStatus{Available: false, Error: "failed refresh"}})
	a.chinaCommandLocked(serviceChinaCommand{Action: 2, Flag: false})
	got := a.chinaStateLocked()
	if !got.Active || !got.Probed || got.RuleOK || got.Fetching {
		t.Fatal("refresh overwrote unrelated bits")
	}
	if other.chinaStateLocked() != (serviceChinaState{}) {
		t.Fatal("instance leak")
	}
	if a.service.backend != owner || a.pathConfigLocked().IntervalSec != 57 || a.controlLocked().SelectedNode != "selected" || !catalogQuotaMatches(a.service, a.catalogEntriesLocked()[0].UserInfo, info, *info) || a.healthDomainLocked().Regions["key"].Country != "JP" {
		t.Fatal("sixdomain interference")
	}
	before := a.chinaStateLocked()
	if e := a.service.transferToGo(); e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(before, a.chinaStateLocked()) || !catalogQuotaMatches(a.service, a.catalogEntriesLocked()[0].UserInfo, info, *info) {
		t.Fatal("handoff lost domain/resource")
	}
}
func TestServiceStateSafeChinaAuthority(t *testing.T) {
	chinaAuthorityGolden(t, newSelectedServiceStateBackend)
}
