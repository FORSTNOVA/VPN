package main

import (
	"fmt"
)

func runGoRegionJobService(q regionJobServiceRequest, fx regionJobServiceEffects) regionJobServiceResult {
	defer fx.Release()
	var s *regionJobServiceState
	call := func(q regionJobEffect) regionJobReply {
		r := fx.Call(q)
		if r.State != nil {
			s = r.State
		}
		return r
	}
	finish := func(err error) {
		if s.JobExists {
			at := call(regionJobEffect{Kind: "now", Name: "finish"}).Now
			call(regionJobEffect{Kind: "finish", Now: at, Err: err})
		}
	}
	if q.Op == "finish" {
		s = call(regionJobEffect{Kind: "snapshot"}).State
		finish(q.Err)
		return regionJobServiceResult{}
	}
	s = call(regionJobEffect{Kind: "lock"}).State
	unlock := func() { call(regionJobEffect{Kind: "unlock"}) }
	if q.Op == "start" {
		status, message := 200, ""
		if s.Chain {
			status, message = 409, regionJobChainMessage()
		} else if !s.Subscription {
			status, message = 400, "set a subscription URL first"
		} else if s.JobRunning {
			status, message = 409, "an exit region check is already running"
		}
		if status != 200 {
			unlock()
			return regionJobServiceResult{Status: status, Message: message}
		}
		at := call(regionJobEffect{Kind: "now", Name: "start"}).Now
		call(regionJobEffect{Kind: "create", Now: at})
		job := call(regionJobEffect{Kind: "response"}).Job
		if job != nil {
			v := *job
			job = &v
		}
		unlock()
		call(regionJobEffect{Kind: "spawn", Refresh: q.Refresh})
		return regionJobServiceResult{Status: 200, Job: job}
	}
	if !s.JobRunning {
		unlock()
		return regionJobServiceResult{}
	}
	temporary := !call(regionJobEffect{Kind: "running"}).Bool
	if temporary {
		r := call(regionJobEffect{Kind: "start"})
		s = r.State
		if r.Err != nil {
			finish(r.Err)
			unlock()
			return regionJobServiceResult{}
		}
	}
	original := ""
	if r := call(regionJobEffect{Kind: "get", Path: "/proxies/SmartVPN"}); r.Err == nil {
		original, _ = r.Value.(string)
		if original == autoGroup || original == fallbackGroup {
			r = call(regionJobEffect{Kind: "get", Path: "/proxies/" + original})
			if r.Err == nil {
				original, _ = r.Value.(string)
			}
		}
	}
	r := call(regionJobEffect{Kind: "list"})
	nodes := r.Names
	if r.Err != nil {
		if temporary {
			s = call(regionJobEffect{Kind: "stop"}).State
		}
		finish(r.Err)
		unlock()
		return regionJobServiceResult{}
	}
	delays := call(regionJobEffect{Kind: "delays"}).Delays
	call(regionJobEffect{Kind: "total", Count: len(nodes)})
	unlock()
	for _, name := range nodes {
		s = call(regionJobEffect{Kind: "lock"}).State
		if !s.JobRunning {
			unlock()
			break
		}
		if !q.Refresh {
			r := call(regionJobEffect{Kind: "lookup", Name: name})
			if r.Found && r.Record.Status == regionVerified {
				r = call(regionJobEffect{Kind: "age", Name: name})
				if r.Age < r.TTL {
					call(regionJobEffect{Kind: "increment", Name: "skip"})
					unlock()
					continue
				}
			}
		}
		result := regionProbeResult{Status: regionUnreachable}
		if delays[name] <= 0 {
			result.Detail = regionJobDelayMessage()
		} else if call(regionJobEffect{Kind: "put", Name: name}).Err != nil {
			result.Detail = regionJobPutMessage()
		} else {
			answers := make([]regionProbeResult, 0, len(s.Sources))
			for i, source := range s.Sources {
				r := call(regionJobEffect{Kind: "query", Index: i, Name: source})
				if r.Err != nil {
					continue
				}
				answers = append(answers, regionProbeResult{ExitIP: r.IP, Country: r.Country, Sources: []string{source}})
				if len(answers) == 2 {
					break
				}
			}
			result = combineRegionAnswers(answers)
		}
		at := call(regionJobEffect{Kind: "now", Name: "record"}).Now
		record := regionRecord{Name: name, ExitIP: result.ExitIP, Country: result.Country, Status: result.Status, Sources: result.Sources, Detail: result.Detail, VerifiedAt: at}
		s = call(regionJobEffect{Kind: "memory", Record: record}).State
		if s.Store {
			if err := call(regionJobEffect{Kind: "store", Record: record}).Err; err != nil {
				call(regionJobEffect{Kind: "log", Message: fmt.Sprintf("could not persist the exit region of %s: %v", name, err)})
			}
		}
		counter := "failed"
		if result.Status == regionVerified {
			counter = "verified"
		}
		call(regionJobEffect{Kind: "increment", Name: counter})
		unlock()
	}
	s = call(regionJobEffect{Kind: "lock"}).State
	if original != "" && original != "REJECT" {
		if err := call(regionJobEffect{Kind: "put", Name: original}).Err; err != nil {
			call(regionJobEffect{Kind: "log", Message: fmt.Sprintf("could not restore the node after an exit region check: %v", err)})
		}
	}
	if temporary {
		s = call(regionJobEffect{Kind: "stop"}).State
	}
	finish(nil)
	unlock()
	return regionJobServiceResult{}
}
