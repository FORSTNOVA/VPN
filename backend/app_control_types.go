package main

import "time"

// Request contains facts only. HTTP decoding and channel identity stay in Go.
type appServiceRequest struct {
	Op       string
	Params   healthParams
	Limit    string
	Interval time.Duration
}
type appHealthFact struct {
	State    string
	Cooldown bool
}
type appRegionFact struct {
	Country                     string
	Verified, Expired, Cooldown bool
}
type appCount struct {
	Name  string
	Count int
}
type appServicePlan struct {
	Running, ProxyEnabled, ZeroJob, StrictPrivacy, TunActive bool
	ActiveNode, ActiveRegion, TunReason, LockedRegion        string
	Counts                                                   []appCount
	PoolSize                                                 int
}
type appServiceEffect struct {
	Kind, Text string
	Count      int
	Interval   time.Duration
	Params     healthParams
	Plan       appServicePlan
	Status     int
}
type appServiceReply struct {
	Err                                                                      error
	Flag, Other                                                              bool
	ProxyOn, WebRTC, DNS, JobNil, ChainEnabled, ModeSupported                bool
	SelectionMode, SelectedNode, LockedRegion, LockedNode, Mode, DefaultMode string
	Text                                                                     string
	Count                                                                    int
	Params                                                                   healthParams
	Health                                                                   []appHealthFact
	Regions                                                                  []appRegionFact
}
type appServiceResult struct {
	Err  error
	Plan appServicePlan
}
type appServiceEffects interface {
	Call(appServiceEffect) appServiceReply
	Release()
}
type appResponsePanic struct{ Value any }

// Wire kinds (shared with app_flow):
// 1 lock, 2 unlock, 3 state-base, 4 running, 5 proxy, 6 log,
// 7 active-get, 8 active-region, 9 tun-reason, 10 resolved,
// 11 choice, 12 mode, 13 health-facts, 14 pin, 15 response,
// 16 meta, 17 commit-params, 18 store-present, 19 save-params,
// 20 cancel-present, 21 cancel-close, 22 cancel-new, 23 spawn,
// 24 path-start, 25 job-running, 26 now, 27 delays, 28 sweep,
// 29 unblock, 30 clear-blocked, 31 clear-reason, 32 switch,
// 33 ticker-new, 34 wait, 35 ticker-stop, 36 parse-limit,
// 37 events-read, 38 events-project.

// 39 project-choice, 40 project-mode, 41 once (selected child usecase).
