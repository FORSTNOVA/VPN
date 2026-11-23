package main

import (
	"errors"
	"reflect"
	"time"
)

type appFlowSession interface {
	next() ([]byte, uint64, error)
	advance(uint64, []byte) error
	close()
}
type appFlowStart func([]byte) (appFlowSession, error)
type appFrame struct {
	kind   byte
	effect appServiceEffect
}

func appParamsBytes(b []byte, p healthParams) []byte {
	for _, n := range []int{p.ConnectTimeoutMs, p.PatrolIntervalSec, p.FailureThreshold, p.CooldownSec, p.MinDwellSec, p.RecoverySuccesses} {
		b = registryInt(b, int64(n))
	}
	return b
}
func appReadParams(r *quotaReader) healthParams {
	return healthParams{ConnectTimeoutMs: int(r.integer()), PatrolIntervalSec: int(r.integer()), FailureThreshold: int(r.integer()), CooldownSec: int(r.integer()), MinDwellSec: int(r.integer()), RecoverySuccesses: int(r.integer())}
}
func appStartInput(q appServiceRequest) ([]byte, error) {
	op, ok := map[string]byte{"state": 0, "snapshot": 1, "params": 2, "patrol": 3, "events": 4, "start": 5, "stop": 6, "loop": 7, "once": 8, "sweep": 9}[q.Op]
	if !ok {
		return nil, errors.New("unsupported app op")
	}
	b := appParamsBytes([]byte{op}, q.Params)
	b = registryBytes(b, q.Limit)
	b = registryInt(b, int64(q.Interval))
	if len(b) > 8<<20 {
		return nil, errors.New("app input limit")
	}
	return b, nil
}
func decodeAppFrame(b []byte) (appFrame, error) {
	r := quotaReader{b: b}
	f := appFrame{kind: r.byte()}
	e := &f.effect
	e.Text = r.str()
	e.Count = int(r.integer())
	e.Interval = time.Duration(r.integer())
	e.Params = appReadParams(&r)
	p := &e.Plan
	p.Running = serviceReadBool(&r)
	p.ProxyEnabled = serviceReadBool(&r)
	p.ZeroJob = serviceReadBool(&r)
	p.StrictPrivacy = serviceReadBool(&r)
	p.TunActive = serviceReadBool(&r)
	p.ActiveNode = r.str()
	p.ActiveRegion = r.str()
	p.TunReason = r.str()
	p.LockedRegion = r.str()
	n := r.integer()
	if n < 0 || n > 100000 || n > int64((len(b)-r.p)/16) {
		return f, errors.New("app count limit")
	}
	if n > 0 {
		p.Counts = make([]appCount, n)
	}
	for i := range p.Counts {
		p.Counts[i] = appCount{Name: r.str(), Count: int(r.integer())}
	}
	p.PoolSize = int(r.integer())
	e.Status = int(r.integer())
	return f, serviceWireDone(&r)
}

type appArena struct{ q appServiceRequest }

func newAppArena(q appServiceRequest) *appArena { return &appArena{q: q} }
func (a *appArena) replyBytes(f appFrame, r appServiceReply) ([]byte, error) {
	b := []byte{f.kind}
	for _, v := range []bool{r.Err == nil, r.Flag, r.Other, r.ProxyOn, r.WebRTC, r.DNS, r.JobNil, r.ChainEnabled, r.ModeSupported} {
		b = registryBool(b, v)
	}
	for _, v := range []string{r.SelectionMode, r.SelectedNode, r.LockedRegion, r.LockedNode, r.Mode, r.DefaultMode, r.Text} {
		b = registryBytes(b, v)
	}
	b = registryInt(b, int64(r.Count))
	b = appParamsBytes(b, r.Params)
	if len(r.Health) > 100000 || len(r.Regions) > 100000 {
		return nil, errors.New("app facts limit")
	}
	b = registryInt(b, int64(len(r.Health)))
	for _, v := range r.Health {
		b = registryBytes(b, v.State)
		b = registryBool(b, v.Cooldown)
	}
	b = registryInt(b, int64(len(r.Regions)))
	for _, v := range r.Regions {
		b = registryBytes(b, v.Country)
		b = registryBool(b, v.Verified)
		b = registryBool(b, v.Expired)
		b = registryBool(b, v.Cooldown)
	}
	if len(b) > 8<<20 {
		return nil, errors.New("app reply limit")
	}
	return b, nil
}
func (a *appArena) port(f appFrame) (appServiceEffect, bool) {
	e := f.effect
	k, ok := appPortKinds[f.kind]
	e.Kind = k
	return e, ok
}
func (a *appArena) resultMatches(f appFrame, r appServiceResult) bool {
	return r.Err == nil && reflect.DeepEqual(r.Plan, f.effect.Plan)
}

var appPortKinds = map[byte]string{
	1: "lock", 2: "unlock", 3: "state-base", 4: "running", 5: "proxy", 6: "log", 7: "active-get", 8: "active-region", 9: "tun-reason", 10: "resolved", 11: "choice", 12: "mode", 13: "health-facts", 14: "pin", 15: "response", 16: "meta", 17: "commit-params", 18: "store-present", 19: "save-params", 20: "cancel-present", 21: "cancel-close", 22: "cancel-new", 23: "spawn", 24: "path-start", 25: "job-running", 26: "now", 27: "delays", 28: "sweep", 29: "unblock", 30: "clear-blocked", 31: "clear-reason", 32: "switch", 33: "ticker-new", 34: "wait", 35: "ticker-stop", 36: "parse-limit", 37: "events-read", 38: "events-project", 39: "project-choice", 40: "project-mode", 41: "once"}
