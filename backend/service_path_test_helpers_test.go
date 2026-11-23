package main

import (
	"reflect"
)

// Test edits use the same typed authority commands, never a writable app mirror.
func testEditPath(a *app, edit func(*servicePathState)) {
	old := a.pathStateLocked()
	next := old
	next.Config.Targets = appendPathTargets(old.Config.Targets)
	next.Status = a.pathStatusLocked()
	next.Status.Results = cloneServiceResults(next.Status.Results)
	next.Status.Failures = cloneServiceStrings(next.Status.Failures)
	next.Status.Switched = cloneServiceStrings(next.Status.Switched)
	edit(&next)
	if !reflect.DeepEqual(old.Config, next.Config) {
		a.replacePathConfigLocked(next.Config)
	}
	if old.Busy != next.Busy {
		if next.Busy {
			a.applyPathCommandLocked(servicePathCommand{Action: pathStateBeginRound})
		} else {
			a.applyPathCommandLocked(servicePathCommand{Action: pathStateFinishRound, LeaseID: old.LeaseID})
		}
	}
	o, n := old.Status, next.Status
	for _, v := range []struct {
		a         uint8
		old, next string
	}{{pathStatePhase, o.Phase, n.Phase}, {pathStateNote, o.Note, n.Note}, {pathStateAlert, o.Alert, n.Alert}, {pathStateRetryMode, o.RetryMode, n.RetryMode}, {pathStateRetryAt, o.RetryAt, n.RetryAt}, {pathStateActiveNode, o.ActiveNode, n.ActiveNode}} {
		if v.old != v.next {
			a.applyPathCommandLocked(servicePathCommand{Action: v.a, Text: v.next})
		}
	}
	if o.Running != n.Running {
		a.applyPathCommandLocked(servicePathCommand{Action: pathStateRunning, Flag: n.Running})
	}
	if o.Demanded != n.Demanded {
		a.applyPathCommandLocked(servicePathCommand{Action: pathStateDemanded, Flag: n.Demanded})
	}
	if n.AlertID < o.AlertID || n.AlertID-o.AlertID > 100 {
		panic("unsupported test counter edit")
	}
	for i := o.AlertID; i < n.AlertID; i++ {
		a.applyPathCommandLocked(servicePathCommand{Action: pathStateIncrementAlert})
	}
	if !reflect.DeepEqual(o.Results, n.Results) {
		a.applyPathCommandLocked(servicePathCommand{Action: pathStateResults, Results: n.Results})
	}
	if !reflect.DeepEqual(o.Failures, n.Failures) {
		a.applyPathCommandLocked(servicePathCommand{Action: pathStateFailures, Strings: n.Failures})
	}
	if !reflect.DeepEqual(o.Switched, n.Switched) {
		a.applyPathCommandLocked(servicePathCommand{Action: pathStateReplaceSwitched, Strings: n.Switched})
	}
	if !n.At.IsZero() {
		a.pathClockCommandLocked(pathStateAt, n.At, "", 0)
	}
}
func appendPathTargets(v []pathTarget) []pathTarget {
	if v == nil {
		return nil
	}
	return append([]pathTarget{}, v...)
}
