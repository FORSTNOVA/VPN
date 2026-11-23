package main

import (
	"errors"
	"log"
	"net/http"
	"time"
)

type trafficServiceRequest struct{ Op, Period, Date, Group, Offset, Limit, App string }
type trafficServiceState struct {
	Store, Cancel, Matches bool
	SamplingError          string
}
type trafficServiceResult struct {
	Status  int
	Message string
	Err     error
	Data    map[string]any
	Report  *trafficHistoryReport
}
type trafficEffect struct {
	Kind, Text, Period, Date, Offset, Limit, App string
	Now                                          time.Time
	Sample                                       trafficSample
	Report                                       trafficHistoryReport
}
type trafficReply struct {
	State         *trafficServiceState
	Err           error
	Bool          bool
	Traffic       kernelTraffic
	Rows          []connectionRow
	Now           time.Time
	Text          string
	Offset, Limit int
	Up, Down      int64
	Report        trafficHistoryReport
}
type trafficServiceEffects interface {
	Call(trafficEffect) trafficReply
	Release()
}

func cloneTrafficServiceState(s *trafficServiceState) *trafficServiceState {
	if s == nil {
		return nil
	}
	r := *s
	return &r
}

type appTrafficEffects struct {
	a                     *app
	owned, borrowed       bool
	op                    string
	loopCancel, installed chan struct{}
	ticker                *time.Ticker
	tickerOwned           bool
	traffic               kernelTraffic
	now                   func() time.Time
	running               func() bool
	get                   func() (kernelTraffic, error)
	record                func(string, kernelTraffic, time.Time) error
	read                  func(trafficHistoryReport, string) (trafficHistoryReport, error)
	spawn                 func(chan struct{})
	tickerStart           func()
	tickerStop            func()
	wait                  func() bool
}

func newAppTrafficEffects(a *app, q trafficServiceRequest, cancel chan struct{}) *appTrafficEffects {
	e := &appTrafficEffects{a: a, op: q.Op, borrowed: q.Op == "start" || q.Op == "stop" || q.Op == "collect", loopCancel: cancel, now: time.Now, running: a.kernelRunningLocked, get: a.kernelTraffic, record: func(session string, k kernelTraffic, now time.Time) error {
		return a.store.recordTraffic(session, k, now)
	}, read: func(r trafficHistoryReport, key string) (trafficHistoryReport, error) {
		return a.store.readTrafficHistory(r, key)
	}}
	e.spawn = func(cancel chan struct{}) {
		go func() {
			r := a.runTrafficService(trafficServiceRequest{Op: "loop"}, cancel)
			if r.Err != nil {
				log.Print("traffic history loop incomplete")
			}
		}()
	}
	e.tickerStart = func() { e.ticker = time.NewTicker(time.Second) }
	e.tickerStop = func() {
		if e.ticker != nil {
			e.ticker.Stop()
		}
	}
	e.wait = func() bool {
		select {
		case <-e.loopCancel:
			return false
		case <-e.ticker.C:
			return true
		}
	}
	return e
}
func (e *appTrafficEffects) snapshot() *trafficServiceState {
	r := &trafficServiceState{Store: e.a.store != nil, Cancel: e.a.trafficHistoryCancel != nil, Matches: e.a.trafficHistoryCancel == e.loopCancel}
	// An old loop must test channel identity before touching a closed authority.
	if e.op == "loop" && (!r.Matches || e.loopCancel == nil) {
		r.Matches = false
		return r
	}
	v, err := e.a.serviceStateLocked().read()
	if err != nil {
		panic(err)
	}
	r.SamplingError = v.SamplingError
	return r
}
func (e *appTrafficEffects) stopTicker() {
	if e.tickerOwned {
		e.tickerOwned = false
		e.tickerStop()
	}
}
func (e *appTrafficEffects) Release() {
	if e.owned {
		e.owned = false
		e.a.mu.Unlock()
	}
	e.stopTicker()
}
func (e *appTrafficEffects) Call(q trafficEffect) (r trafficReply) {
	switch q.Kind {
	case "lock":
		if e.owned || e.borrowed {
			panic("traffic duplicate lock")
		}
		e.a.mu.Lock()
		e.owned = true
		r.State = e.snapshot()
		return
	case "unlock":
		if !e.owned {
			panic("traffic unlock without owner")
		}
		e.owned = false
		e.a.mu.Unlock()
		return
	case "ticker-start":
		// Reserve cleanup before creation: a creator that publishes then panics
		// must not strand its invocation-owned ticker. Default Stop is nil-safe.
		e.tickerOwned = true
		e.tickerStart()
		return
	case "ticker-stop":
		e.stopTicker()
		return
	case "wait":
		r.Bool = e.wait()
		return
	case "date":
		r.Now = e.now()
		r.Text, r.Err = historyDate(q.Period, q.Date, r.Now)
		return
	case "pagination":
		r.Offset, r.Limit, r.Err = projectionPagination(q.Offset, q.Limit)
		return
	}
	if !e.owned && !e.borrowed {
		panic("traffic effect outside lock")
	}
	switch q.Kind {
	case "snapshot":
		r.State = e.snapshot()
	case "running":
		r.Bool = e.running()
	case "get":
		r.Traffic, r.Err = e.get()
		e.traffic = r.Traffic
	case "now":
		r.Now = e.now()
	case "rate":
		var err error
		r.Up, r.Down, err = e.a.serviceStateLocked().rate(q.Sample)
		if err != nil {
			panic(err)
		}
	case "baseline":
		if err := e.a.serviceStateLocked().baseline(q.Sample); err != nil {
			panic(err)
		}
	case "rows":
		r.Rows = connectionRows(e.traffic.Connections, q.Now)
	case "error":
		if err := e.a.serviceStateLocked().samplingError(q.Text); err != nil {
			panic(err)
		}
	case "record":
		r.Err = e.record(e.a.started.Format(time.RFC3339Nano), e.traffic, q.Now)
	case "install":
		e.installed = make(chan struct{})
		e.a.trafficHistoryCancel = e.installed
	case "spawn":
		e.spawn(e.installed)
	case "close":
		close(e.a.trafficHistoryCancel)
		e.a.trafficHistoryCancel = nil
	case "timezone":
		r.Text = e.now().Format("MST -07:00")
	case "read":
		r.Report, r.Err = e.read(q.Report, q.App)
	default:
		panic("unknown traffic effect: " + q.Kind)
	}
	return
}
func (a *app) runTrafficService(q trafficServiceRequest, cancel chan struct{}) (result trafficServiceResult) {
	defer func() {
		if recover() != nil {
			result = trafficServiceResult{Status: 500, Message: "traffic operation interrupted", Err: errors.New("traffic operation interrupted")}
		}
	}()
	e := newAppTrafficEffects(a, q, cancel)
	defer e.Release()
	return runSelectedTrafficService(q, e)
}

func writeTrafficServiceResult(w http.ResponseWriter, r trafficServiceResult) {
	if r.Err != nil {
		status := r.Status
		if status < 400 {
			status = 500
		}
		http.Error(w, r.Message, status)
		return
	}
	if r.Report != nil {
		writeJSON(w, r.Report)
	} else {
		writeJSON(w, r.Data)
	}
}
