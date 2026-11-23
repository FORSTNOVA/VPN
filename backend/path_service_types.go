package main

import "time"

type pathServiceRequest struct{ Op, Mode, Target, Region, Trigger, Evidence string }
type pathServiceResult struct{ Err error }
type pathServiceState struct {
	Busy, Enabled, Chain bool
	LockedRegion         string
	MixedPort            int
	Targets              []pathTarget
	Status               pathStatus
}
type pathCandidateFact struct {
	Name, Country      string
	Verified, Cooldown bool
	Age                time.Duration
}
type pathServiceEffect struct {
	Kind, Name, Text string
	Flag             bool
	Target           pathTarget
	Results          []siteCheckResult
	Strings          []string
	Time             time.Time
	Event            switchEvent
}
type pathServiceReply struct {
	State      *pathServiceState
	Bool       bool
	Text       string
	Err        error
	Now        time.Time
	Result     siteCheckResult
	Record     regionRecord
	Found      bool
	Candidates []pathCandidateFact
}
type pathServiceEffects interface {
	Call(pathServiceEffect) pathServiceReply
	Release()
}

func clonePathServiceState(s *pathServiceState) *pathServiceState {
	if s == nil {
		return nil
	}
	v := *s
	v.Targets = append([]pathTarget(nil), s.Targets...)
	v.Status.Results = append([]siteCheckResult(nil), s.Status.Results...)
	v.Status.Failures = append([]string(nil), s.Status.Failures...)
	v.Status.Switched = append([]string(nil), s.Status.Switched...)
	return &v
}
