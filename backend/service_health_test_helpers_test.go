package main

import "reflect"

func testEditHealth(a *app, edit func(*healthDomainProjection)) {
	old := a.healthDomainLocked()
	next := a.healthDomainLocked()
	edit(&next)
	if old.Params != next.Params {
		a.replaceHealthParamsLocked(next.Params)
	}
	if !reflect.DeepEqual(old.Health, next.Health) {
		a.restoreHealthRecordsLocked(next.Health)
	}
	if !reflect.DeepEqual(old.Regions, next.Regions) {
		a.restoreRegionRecordsLocked(next.Regions)
	}
	if !reflect.DeepEqual(old.Job, next.Job) {
		a.applyHealthCommandLocked(serviceHealthCommand{Action: healthStateCreateJob, Job: next.Job}, nil)
	}
	if old.Blocked != next.Blocked {
		a.setHealthBlockedLocked(next.Blocked)
	}
	if old.BlockReason != next.BlockReason {
		a.setHealthReasonLocked(next.BlockReason)
	}
	if old.SweepNote != next.SweepNote {
		a.setHealthNoteLocked(next.SweepNote)
	}
	if old.LastSwitch != next.LastSwitch {
		a.setHealthLastSwitchLocked(next.LastSwitch)
	}
}
