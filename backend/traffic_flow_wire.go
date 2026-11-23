package main

import (
	"errors"
	"reflect"
	"time"
)

type trafficFlowSession interface {
	next() ([]byte, uint64, error)
	advance(uint64, []byte) error
	close()
}
type trafficFlowStart func([]byte) (trafficFlowSession, error)
type trafficFrame struct {
	kind       byte
	a, b       int64
	text, data string
}

func trafficStartInput(q trafficServiceRequest) ([]byte, error) {
	op, ok := map[string]byte{"current": 0, "collect": 1, "start": 2, "stop": 3, "loop": 4, "query": 5}[q.Op]
	if !ok {
		return nil, errors.New("invalid traffic op")
	}
	b := registryBytes([]byte{op}, q.Period)
	b = registryBytes(b, q.Date)
	return registryBytes(b, q.Group), nil
}
func decodeTrafficFrame(b []byte) (trafficFrame, error) {
	r := quotaReader{b: b}
	f := trafficFrame{kind: r.byte(), a: r.integer(), b: r.integer(), text: r.str(), data: r.str()}
	if r.bad || r.p != len(b) {
		return f, errors.New("invalid traffic frame")
	}
	return f, nil
}

type trafficArena struct {
	q        trafficServiceRequest
	s        *trafficServiceState
	k        kernelTraffic
	now      time.Time
	rates    trafficReply
	rows     []connectionRow
	date     string
	dateErr  error
	page     trafficReply
	timezone string
	report   trafficHistoryReport
}

func newTrafficArena(q trafficServiceRequest) *trafficArena {
	if q.Period == "" {
		q.Period = "day"
	}
	if q.Group == "" {
		q.Group = "app"
	}
	return &trafficArena{q: q}
}
func (a *trafficArena) sample() trafficSample {
	return trafficSample{at: a.now, up: a.k.UploadTotal, down: a.k.DownloadTotal}
}
func (a *trafficArena) port(f trafficFrame) (trafficEffect, bool) {
	e := trafficEffect{}
	switch f.kind {
	case 1:
		e.Kind = "lock"
	case 2:
		e.Kind = "snapshot"
	case 3:
		e.Kind = "running"
	case 4:
		e.Kind = "get"
	case 5:
		e.Kind = "baseline"
	case 6:
		if f.a == 1 {
			e.Kind = "timezone"
		} else {
			e.Kind = "now"
		}
	case 7:
		e.Kind = "rate"
		e.Sample = a.sample()
	case 8:
		e.Kind = "baseline"
		e.Sample = a.sample()
	case 9:
		e.Kind = "rows"
		e.Now = a.now
	case 10:
		e.Kind = "record"
		e.Now = a.now
	case 11:
		e.Kind = "error"
		switch f.a {
		case 0:
		case 1:
			e.Text = trafficCollectMessage0()
		case 2:
			e.Text = trafficCollectMessage1()
		default:
			return e, false
		}
	case 12:
		e.Kind = "install"
	case 13:
		e.Kind = "spawn"
	case 14:
		e.Kind = "close"
	case 15:
		e.Kind = "ticker-start"
	case 16:
		e.Kind = "wait"
	case 18:
		e.Kind = "unlock"
	case 19:
		e.Kind = "ticker-stop"
	case 20:
		e.Kind = "date"
		e.Period = f.text
		e.Date = f.data
	case 21:
		e.Kind = "pagination"
		e.Offset = a.q.Offset
		e.Limit = a.q.Limit
	case 22:
		if a.s == nil {
			return e, false
		}
		e.Kind = "read"
		e.App = a.q.App
		e.Report = trafficHistoryReport{Period: a.q.Period, Date: a.date, Group: a.q.Group, Offset: a.page.Offset, Limit: a.page.Limit, Timezone: a.timezone, SamplingError: a.s.SamplingError}
	default:
		return e, false
	}
	return e, true
}
func (a *trafficArena) replyBytes(f trafficFrame, r trafficReply) ([]byte, error) {
	if r.State != nil {
		a.s = cloneTrafficServiceState(r.State)
	}
	switch f.kind {
	case 4:
		a.k = r.Traffic
	case 6:
		if f.a == 1 {
			a.timezone = r.Text
		} else {
			a.now = r.Now
		}
	case 7:
		a.rates = r
	case 9:
		a.rows = r.Rows
	case 20:
		a.date = r.Text
		a.dateErr = r.Err
	case 21:
		a.page = r
	case 22:
		a.report = r.Report
	}
	b := registryBool([]byte{f.kind}, r.Err == nil)
	b = registryBool(b, r.Bool)
	b = registryInt(b, 0)
	b = registryInt(b, 0)
	b = registryBytes(b, "")
	b = registryBool(b, r.State != nil)
	if r.State != nil {
		b = registryBool(b, r.State.Store)
		b = registryBool(b, r.State.Cancel)
		b = registryBool(b, r.State.Matches)
	}
	return b, nil
}
func (a *trafficArena) resultMatches(f trafficFrame, r trafficServiceResult) bool {
	want := trafficServiceResult{}
	fail := func(status int, msg string) { want.Status = status; want.Message = msg; want.Err = errors.New(msg) }
	switch f.a {
	case 0:
	case 1, 2:
		msg := trafficCurrentMessage0()
		if f.a == 2 {
			msg = trafficCurrentMessage1()
		}
		want.Status = 200
		want.Data = map[string]any{"available": false, "reason": msg}
	case 3:
		want.Status = 200
		want.Data = map[string]any{"available": true, "uploadTotal": a.k.UploadTotal, "downloadTotal": a.k.DownloadTotal, "uploadRate": a.rates.Up, "downloadRate": a.rates.Down, "connectionCount": len(a.k.Connections), "shownCount": len(a.rows), "connections": a.rows}
	case 10, 11:
		if a.dateErr == nil {
			return false
		}
		fail(400, a.dateErr.Error())
	case 12:
		fail(400, trafficHistoryMessage0())
	case 13:
		fail(400, trafficHistoryMessage1())
	case 14:
		fail(503, trafficHistoryMessage2())
	case 15:
		fail(500, trafficHistoryMessage3())
	case 16:
		want.Status = 200
		v := a.report
		want.Report = &v
	default:
		return false
	}
	if (want.Err == nil) != (r.Err == nil) {
		return false
	}
	if want.Err != nil && want.Err.Error() != r.Err.Error() {
		return false
	}
	want.Err = nil
	r.Err = nil
	return reflect.DeepEqual(want, r)
}
