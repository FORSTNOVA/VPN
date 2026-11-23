package main

import "time"

type pathMonitorServiceRequest struct {
	Op       string
	Settings pathCheckSettings
	Guard    bool
	Interval time.Duration
	Now      time.Time
	Previous map[string]pathConnectionSample
}
type pathMonitorServiceResult struct {
	Err      error
	Status   int
	Mode     string
	Labels   []string
	Settings pathCheckSettings
}
type pathMonitorServiceState struct {
	Identity, Cancel, Enabled, Busy bool
	Settings                        pathCheckSettings
	Status                          pathStatus
	Presets                         []pathTarget
}
type pathMonitorURLFacts struct {
	Raw, Label, Host string
	Valid            bool
	Runes            int
}
type pathMonitorConnection struct {
	ID, Start, Host, Port, Process string
	Upload                         int64
}
type pathMonitorServiceEffect struct {
	Kind, Name, Text string
	Flag             bool
	Duration         time.Duration
	Time             time.Time
	Settings         pathCheckSettings
	Target           pathTarget
	Samples          map[string]pathConnectionSample
	Status           int
}
type pathMonitorServiceReply struct {
	State       *pathMonitorServiceState
	Bool        bool
	Err         error
	Now         time.Time
	Text        string
	URL         pathMonitorURLFacts
	Connections []pathMonitorConnection
	Samples     map[string]pathConnectionSample
}
type pathMonitorServiceEffects interface {
	Call(pathMonitorServiceEffect) pathMonitorServiceReply
	Release()
}

func clonePathMonitorState(s *pathMonitorServiceState) *pathMonitorServiceState {
	if s == nil {
		return nil
	}
	v := *s
	v.Settings.Targets = append([]pathTarget(nil), s.Settings.Targets...)
	v.Presets = append([]pathTarget(nil), s.Presets...)
	p := clonePathServiceState(&pathServiceState{Status: s.Status})
	v.Status = p.Status
	return &v
}
