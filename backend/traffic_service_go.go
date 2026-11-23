package main

import (
	"errors"
)

func runGoTrafficService(q trafficServiceRequest, fx trafficServiceEffects) trafficServiceResult {
	defer fx.Release()
	call := fx.Call
	fail := func(code int, message string) trafficServiceResult {
		return trafficServiceResult{Status: code, Message: message, Err: errors.New(message)}
	}
	collect := func(s *trafficServiceState) {
		if !s.Store || !call(trafficEffect{Kind: "running"}).Bool {
			return
		}
		r := call(trafficEffect{Kind: "get"})
		if r.Err != nil {
			call(trafficEffect{Kind: "error", Text: trafficCollectMessage0()})
			return
		}
		now := call(trafficEffect{Kind: "now"}).Now
		if call(trafficEffect{Kind: "record", Now: now}).Err != nil {
			call(trafficEffect{Kind: "error", Text: trafficCollectMessage1()})
			return
		}
		call(trafficEffect{Kind: "error", Text: ""})
	}
	switch q.Op {
	case "start":
		s := call(trafficEffect{Kind: "snapshot"}).State
		if s.Store && !s.Cancel {
			call(trafficEffect{Kind: "install"})
			call(trafficEffect{Kind: "spawn"})
		}
		return trafficServiceResult{}
	case "stop":
		s := call(trafficEffect{Kind: "snapshot"}).State
		if s.Cancel {
			call(trafficEffect{Kind: "close"})
		}
		return trafficServiceResult{}
	case "collect":
		s := call(trafficEffect{Kind: "snapshot"}).State
		collect(s)
		return trafficServiceResult{}
	case "loop":
		call(trafficEffect{Kind: "ticker-start"})
		for call(trafficEffect{Kind: "wait"}).Bool {
			s := call(trafficEffect{Kind: "lock"}).State
			if !s.Matches {
				call(trafficEffect{Kind: "unlock"})
				break
			}
			collect(s)
			call(trafficEffect{Kind: "unlock"})
		}
		call(trafficEffect{Kind: "ticker-stop"})
		return trafficServiceResult{}
	case "current":
		call(trafficEffect{Kind: "lock"})
		if !call(trafficEffect{Kind: "running"}).Bool {
			return trafficServiceResult{Status: 200, Data: map[string]any{"available": false, "reason": trafficCurrentMessage0()}}
		}
		r := call(trafficEffect{Kind: "get"})
		if r.Err != nil {
			call(trafficEffect{Kind: "baseline"})
			return trafficServiceResult{Status: 200, Data: map[string]any{"available": false, "reason": trafficCurrentMessage1()}}
		}
		now := call(trafficEffect{Kind: "now"}).Now
		sample := trafficSample{at: now, up: r.Traffic.UploadTotal, down: r.Traffic.DownloadTotal}
		rates := call(trafficEffect{Kind: "rate", Sample: sample})
		call(trafficEffect{Kind: "baseline", Sample: sample})
		rows := call(trafficEffect{Kind: "rows", Now: now}).Rows
		return trafficServiceResult{Status: 200, Data: map[string]any{"available": true, "uploadTotal": r.Traffic.UploadTotal, "downloadTotal": r.Traffic.DownloadTotal, "uploadRate": rates.Up, "downloadRate": rates.Down, "connectionCount": len(r.Traffic.Connections), "shownCount": len(rows), "connections": rows}}
	case "query":
		period, group := q.Period, q.Group
		if period == "" {
			period = "day"
		}
		if group == "" {
			group = "app"
		}
		d := call(trafficEffect{Kind: "date", Period: period, Date: q.Date})
		if d.Err != nil {
			return fail(400, d.Err.Error())
		}
		if group != "app" && group != "connection" {
			return fail(400, trafficHistoryMessage0())
		}
		page := call(trafficEffect{Kind: "pagination", Offset: q.Offset, Limit: q.Limit})
		if page.Err != nil {
			return fail(400, trafficHistoryMessage1())
		}
		s := call(trafficEffect{Kind: "lock"}).State
		if !s.Store {
			return fail(503, trafficHistoryMessage2())
		}
		tz := call(trafficEffect{Kind: "timezone"}).Text
		r := call(trafficEffect{Kind: "read", App: q.App, Report: trafficHistoryReport{Period: period, Date: d.Text, Group: group, Offset: page.Offset, Limit: page.Limit, Timezone: tz, SamplingError: s.SamplingError}})
		if r.Err != nil {
			return fail(500, trafficHistoryMessage3())
		}
		report := r.Report
		return trafficServiceResult{Status: 200, Report: &report}
	default:
		return fail(500, "unknown traffic service operation")
	}
}
