package main

import (
	"fmt"
	"log"
	"net/http"
	"time"
)

type regionJobServiceRequest struct {
	Op      string
	Refresh bool
	Err     error
}
type regionJobServiceResult struct {
	Status  int
	Message string
	Job     *regionJob
	Err     error
}
type regionJobServiceState struct {
	Chain, Subscription, JobExists, JobRunning, Store bool
	Sources                                           []string
}
type regionJobEffect struct {
	Kind, Name, Path, Message string
	Index, Count              int
	Now                       time.Time
	Record                    regionRecord
	Err                       error
	Refresh                   bool
}
type regionJobReply struct {
	State       *regionJobServiceState
	Err         error
	Bool        bool
	Value       any
	Now         time.Time
	Names       []string
	Delays      map[string]int
	Record      regionRecord
	Found       bool
	Age, TTL    time.Duration
	IP, Country string
	Job         *regionJob
}
type regionJobServiceEffects interface {
	Call(regionJobEffect) regionJobReply
	Release()
}

func regionJobStateFromApp(a *app) *regionJobServiceState {
	h := a.healthMetaLocked()
	s := &regionJobServiceState{Chain: a.chainEnabledLocked(), Subscription: a.controlMetaLocked().SubscriptionURL != "", JobExists: h.Job != nil, JobRunning: h.Job != nil && h.Job.Running, Store: a.store != nil}
	for _, v := range geoSources {
		s.Sources = append(s.Sources, v.name)
	}
	return s
}

type appRegionJobEffects struct {
	a               *app
	owned, borrowed bool
	now             func() time.Time
	running         func() bool
	start, stop     func() error
	list            func() ([]proxyNode, error)
	delays          func(time.Duration) (map[string]int, error)
	mihomo          func(string, string, any) (map[string]any, error)
	query           func(geoSource, time.Duration) (string, string, error)
	save            func(regionRecord) error
	spawn           func(bool)
	logger          func(string)
}

func newAppRegionJobEffects(a *app, borrowed bool) *appRegionJobEffects {
	return &appRegionJobEffects{a: a, borrowed: borrowed, now: time.Now, running: a.kernelRunningLocked, start: a.startMihomoLocked, stop: a.stopMihomoLocked, list: a.listNodesLocked, delays: a.groupDelaysLocked, mihomo: a.mihomoRequest, query: a.queryGeoSource, save: func(r regionRecord) error { return a.store.saveRegion(r) }, spawn: func(refresh bool) { go a.runRegionJob(refresh) }, logger: func(s string) { log.Print(s) }}
}
func (e *appRegionJobEffects) Release() {
	if e.owned {
		e.owned = false
		e.a.mu.Unlock()
	}
}
func (e *appRegionJobEffects) Call(q regionJobEffect) (r regionJobReply) {
	if q.Kind == "lock" {
		if e.owned || e.borrowed {
			panic("region job duplicate lock")
		}
		e.a.mu.Lock()
		e.owned = true
		r.State = regionJobStateFromApp(e.a)
		return
	}
	if q.Kind == "unlock" {
		if !e.owned {
			panic("region job unlock without ownership")
		}
		e.Release()
		return
	}
	if q.Kind == "spawn" {
		if e.owned || e.borrowed {
			panic("spawn while locked")
		}
		e.spawn(q.Refresh)
		return
	}
	if !e.owned && !e.borrowed {
		panic("region job effect outside lock")
	}
	defer func() { r.State = regionJobStateFromApp(e.a) }()
	switch q.Kind {
	case "snapshot":
	case "now":
		r.Now = e.now()
	case "create":
		e.a.applyHealthCommandLocked(serviceHealthCommand{Action: healthStateCreateJob, Job: &regionJob{Running: true, StartedAt: timestamp(q.Now)}}, nil)
	case "response":
		r.Job = e.a.regionJobSnapshotLocked()
	case "running":
		r.Bool = e.running()
	case "start":
		r.Err = e.start()
	case "stop":
		r.Err = e.stop()
	case "get":
		var g map[string]any
		g, r.Err = e.mihomo(http.MethodGet, q.Path, nil)
		r.Value = g["now"]
	case "list":
		var ns []proxyNode
		ns, r.Err = e.list()
		for _, n := range ns {
			r.Names = append(r.Names, n.Name)
		}
	case "delays":
		r.Delays, r.Err = e.delays(8 * time.Second)
	case "total":
		e.a.applyHealthCommandLocked(serviceHealthCommand{Action: healthStateJobTotal, Count: q.Count}, nil)
	case "lookup":
		r.Record, r.Found = e.a.regionRecordLocked(q.Name)
		if r.Record.Sources != nil {
			r.Record.Sources = append([]string{}, r.Record.Sources...)
		}
	case "age":
		r.Age = e.now().Sub(e.a.regionValueLocked(q.Name).VerifiedAt)
		r.TTL = regionTTL
	case "put":
		_, r.Err = e.mihomo(http.MethodPut, "/proxies/SmartVPN", map[string]string{"name": q.Name})
	case "query":
		r.IP, r.Country, r.Err = e.query(geoSources[q.Index], 8*time.Second)
	case "memory":
		v := q.Record
		if v.Sources != nil {
			v.Sources = append([]string{}, v.Sources...)
		}
		e.a.writeRegionRecordLocked(v.Name, v)
	case "store":
		r.Err = e.save(q.Record)
	case "log":
		e.logger(q.Message)
	case "increment":
		e.a.applyHealthCommandLocked(serviceHealthCommand{Action: healthStateJobAdvance, Text: q.Name}, nil)
	case "finish":
		msg := ""
		if q.Err != nil {
			msg = q.Err.Error()
		}
		e.a.applyHealthCommandLocked(serviceHealthCommand{Action: healthStateJobFinish, Text: timestamp(q.Now), Detail: msg, Flag: q.Err != nil}, nil)
	default:
		panic("unknown region job effect: " + q.Kind)
	}
	return
}
func (a *app) runRegionJobService(q regionJobServiceRequest) (result regionJobServiceResult) {
	defer func() {
		if v := recover(); v != nil {
			result = regionJobServiceResult{Status: 500, Message: "region state unavailable", Err: fmt.Errorf("region job interrupted: %v", v)}
		}
	}()
	fx := newAppRegionJobEffects(a, q.Op == "finish")
	defer fx.Release()
	return runSelectedRegionJobService(q, fx)
}
func cloneRegionJobServiceState(s *regionJobServiceState) *regionJobServiceState {
	if s == nil {
		return nil
	}
	v := *s
	v.Sources = append([]string(nil), s.Sources...)
	return &v
}
