package main

import (
	"errors"
	"math"
	"reflect"
	"sort"
	"time"
)

// Path state is part of the same instance/epoch/revision as traffic. At is a
// Go clock token; Status.At remains zero in the stored typed representation.
type servicePathState struct {
	Config                                  pathCheckSettings
	Busy, MonitorActive                     bool
	Status                                  pathStatus
	AtToken, Generation, NextLease, LeaseID uint64
	Previous                                []servicePathSample
	Throttle                                []servicePathThrottle
}
type servicePathSample struct {
	ID     string
	Upload int64
}
type servicePathThrottle struct {
	Label     string
	TimeToken uint64
}
type servicePathCommand struct {
	Action                         uint8
	Config                         pathCheckSettings
	Flag                           bool
	Text                           string
	TimeToken, Generation, LeaseID uint64
	Strings                        []string
	Results                        []siteCheckResult
	Samples                        []servicePathSample
}

const (
	pathStateConfig uint8 = 1 + iota
	pathStateOpenMonitor
	pathStateRevokeMonitor
	pathStateBeginRound
	pathStateFinishRound
	pathStateRunning
	pathStatePhase
	pathStateNote
	pathStateAlert
	pathStateDemanded
	pathStateRetryMode
	pathStateRetryAt
	pathStateIncrementAlert
	pathStateReplaceSwitched
	pathStateAppendSwitch
	pathStateAt
	pathStateResults
	pathStateFailures
	pathStateActiveNode
	pathStateSamples
	pathStateThrottle
)

func applyServicePath(s servicePathState, c servicePathCommand) (servicePathState, error) {
	bad := func() (servicePathState, error) { return s, errors.New("invalid path command") }
	switch c.Action {
	case pathStateConfig:
		s.Config = c.Config
	case pathStateOpenMonitor:
		if s.MonitorActive || s.Generation == math.MaxUint64 {
			return bad()
		}
		s.Generation++
		s.MonitorActive = true
		s.Previous = nil
		s.Throttle = nil
	case pathStateRevokeMonitor:
		s.MonitorActive = false
		s.Previous = nil
		s.Throttle = nil
	case pathStateBeginRound:
		if s.Busy || s.NextLease == math.MaxUint64 {
			return bad()
		}
		s.NextLease++
		s.LeaseID = s.NextLease
		s.Busy = true
	case pathStateFinishRound:
		if c.LeaseID == 0 || s.LeaseID != c.LeaseID {
			return bad()
		}
		s.LeaseID = 0
		s.Busy = false
		if c.Flag {
			s.Status.Running = false
		}
	case pathStateRunning:
		s.Status.Running = c.Flag
	case pathStatePhase:
		s.Status.Phase = c.Text
	case pathStateNote:
		s.Status.Note = c.Text
	case pathStateAlert:
		s.Status.Alert = c.Text
	case pathStateDemanded:
		s.Status.Demanded = c.Flag
	case pathStateRetryMode:
		s.Status.RetryMode = c.Text
	case pathStateRetryAt:
		s.Status.RetryAt = c.Text
	case pathStateIncrementAlert:
		s.Status.AlertID++
	case pathStateReplaceSwitched:
		s.Status.Switched = cloneServiceStrings(c.Strings)
	case pathStateAppendSwitch:
		s.Status.Switched = append(s.Status.Switched, c.Text)
	case pathStateAt:
		s.AtToken = c.TimeToken
	case pathStateResults:
		s.Status.Results = cloneServiceResults(c.Results)
	case pathStateFailures:
		s.Status.Failures = cloneServiceStrings(c.Strings)
	case pathStateActiveNode:
		s.Status.ActiveNode = c.Text
	case pathStateSamples:
		if !s.MonitorActive || s.Generation != c.Generation {
			return bad()
		}
		s.Previous = nil
		if c.Samples != nil {
			s.Previous = append([]servicePathSample{}, c.Samples...)
		}
	case pathStateThrottle:
		if !s.MonitorActive || s.Generation != c.Generation || c.TimeToken == 0 {
			return bad()
		}
		found := false
		for i := range s.Throttle {
			if s.Throttle[i].Label == c.Text {
				s.Throttle[i].TimeToken = c.TimeToken
				found = true
				break
			}
		}
		if !found {
			s.Throttle = append(s.Throttle, servicePathThrottle{c.Text, c.TimeToken})
		}
		sort.Slice(s.Throttle, func(i, j int) bool { return s.Throttle[i].Label < s.Throttle[j].Label })
	default:
		return bad()
	}
	if e := validateServicePathState(s); e != nil {
		return s, e
	}
	return s, nil
}
func (s *serviceAuthority) pathState() (servicePathState, error) {
	v, e := s.read()
	if e != nil {
		return servicePathState{}, e
	}
	return decodeServicePathState(v.PathPayload)
}
func (s *serviceAuthority) pathCommand(c servicePathCommand) error {
	v, e := encodeServicePathCommand(c)
	if e != nil {
		return e
	}
	return s.mutate(serviceStateCommand{Kind: 3, PathPayload: v})
}
func (a *app) pathStateLocked() servicePathState {
	s, e := a.serviceStateLocked().pathState()
	if e != nil {
		panic(e)
	}
	return s
}
func (a *app) pathConfigLocked() pathCheckSettings { return a.pathStateLocked().Config }
func (a *app) replacePathConfigLocked(c pathCheckSettings) {
	a.applyPathCommandLocked(servicePathCommand{Action: pathStateConfig, Config: c})
}
func (a *app) applyPathCommandLocked(c servicePathCommand) {
	if e := a.serviceStateLocked().pathCommand(c); e != nil {
		panic(e)
	}
}
func (a *app) pathStatusLocked() pathStatus {
	s := a.pathStateLocked()
	if s.AtToken != 0 {
		var ok bool
		s.Status.At, ok = a.service.clocks[s.AtToken]
		if !ok {
			panic("path clock missing")
		}
	}
	return s.Status
}
func (a *app) pathClockCommandLocked(action uint8, now time.Time, text string, generation uint64) {
	s := a.serviceStateLocked()
	if _, e := s.read(); e != nil {
		panic(e)
	}
	if s.nextToken == math.MaxUint64 {
		panic("path clock exhausted")
	}
	s.nextToken++
	token := s.nextToken
	s.clocks[token] = now
	if s.otherClocks == nil {
		s.otherClocks = map[uint64]bool{}
	}
	s.otherClocks[token] = true
	committed := false
	defer func() {
		if !committed && s.pending == nil {
			delete(s.clocks, token)
			delete(s.otherClocks, token)
		}
	}()
	a.applyPathCommandLocked(servicePathCommand{Action: action, TimeToken: token, Text: text, Generation: generation})
	committed = true
}
func servicePathFieldAction(name string) uint8 {
	switch name {
	case "running":
		return pathStateRunning
	case "phase":
		return pathStatePhase
	case "note":
		return pathStateNote
	case "alert":
		return pathStateAlert
	case "demanded":
		return pathStateDemanded
	case "retry-mode":
		return pathStateRetryMode
	case "retry-at":
		return pathStateRetryAt
	case "alert-increment":
		return pathStateIncrementAlert
	case "switched":
		return pathStateReplaceSwitched
	case "append-switch":
		return pathStateAppendSwitch
	case "results":
		return pathStateResults
	case "failures":
		return pathStateFailures
	case "active":
		return pathStateActiveNode
	}
	panic("unknown path state field")
}

func cloneServiceStrings(v []string) []string {
	if v == nil {
		return nil
	}
	return append([]string{}, v...)
}
func cloneServiceResults(v []siteCheckResult) []siteCheckResult {
	if v == nil {
		return nil
	}
	return append([]siteCheckResult{}, v...)
}

func validateServicePathState(s servicePathState) error {
	if !s.Status.At.IsZero() || s.Busy != (s.LeaseID != 0) || s.LeaseID > s.NextLease || (s.MonitorActive && s.Generation == 0) {
		return errors.New("invalid path state")
	}
	for i, v := range s.Previous {
		if i > 0 && s.Previous[i-1].ID >= v.ID {
			return errors.New("invalid path samples")
		}
	}
	for i, v := range s.Throttle {
		if v.TimeToken == 0 || (i > 0 && s.Throttle[i-1].Label >= v.Label) {
			return errors.New("invalid path throttle")
		}
	}
	return nil
}

func validateServicePathLast(s servicePathState, c servicePathCommand) error {
	ok := false
	switch c.Action {
	case pathStateConfig:
		ok = reflect.DeepEqual(s.Config, c.Config)
	case pathStateOpenMonitor:
		ok = s.MonitorActive && s.Generation > 0 && s.Previous == nil && s.Throttle == nil
	case pathStateRevokeMonitor:
		ok = !s.MonitorActive && s.Previous == nil && s.Throttle == nil
	case pathStateBeginRound:
		ok = s.Busy && s.LeaseID == s.NextLease && s.LeaseID != 0
	case pathStateFinishRound:
		ok = !s.Busy && s.LeaseID == 0 && (!c.Flag || !s.Status.Running) && c.LeaseID != 0
	case pathStateRunning:
		ok = s.Status.Running == c.Flag
	case pathStatePhase:
		ok = s.Status.Phase == c.Text
	case pathStateNote:
		ok = s.Status.Note == c.Text
	case pathStateAlert:
		ok = s.Status.Alert == c.Text
	case pathStateDemanded:
		ok = s.Status.Demanded == c.Flag
	case pathStateRetryMode:
		ok = s.Status.RetryMode == c.Text
	case pathStateRetryAt:
		ok = s.Status.RetryAt == c.Text
	case pathStateIncrementAlert:
		ok = true // Previous wrapping value is not in the checkpoint.
	case pathStateReplaceSwitched:
		ok = reflect.DeepEqual(s.Status.Switched, c.Strings)
	case pathStateAppendSwitch:
		ok = len(s.Status.Switched) > 0 && s.Status.Switched[len(s.Status.Switched)-1] == c.Text
	case pathStateAt:
		ok = s.AtToken == c.TimeToken
	case pathStateResults:
		ok = reflect.DeepEqual(s.Status.Results, c.Results)
	case pathStateFailures:
		ok = reflect.DeepEqual(s.Status.Failures, c.Strings)
	case pathStateActiveNode:
		ok = s.Status.ActiveNode == c.Text
	case pathStateSamples:
		ok = s.MonitorActive && s.Generation == c.Generation && reflect.DeepEqual(s.Previous, c.Samples)
	case pathStateThrottle:
		if s.MonitorActive && s.Generation == c.Generation {
			for _, v := range s.Throttle {
				if v.Label == c.Text && v.TimeToken == c.TimeToken {
					ok = true
				}
			}
		}
	}
	if !ok {
		return errors.New("path checkpoint command mismatch")
	}
	return nil
}
