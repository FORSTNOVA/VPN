package main

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"testing"
	"time"
)

type appIndependentSpec struct {
	Base, Choice, Mode                   appServiceReply
	Params                               healthParams
	Health                               []appHealthFact
	Regions                              []appRegionFact
	Running, Cancel, Blocked, Store, Job bool
	Active, Nested, Pin, Fail, Panic     string
	Ticks                                int
}
type appIndependentFX struct {
	s                                                   appIndependentSpec
	trace                                               []appServiceEffect
	owned                                               bool
	releases, ticks, spawns, closes, pathStarts, sweeps int
}

func (f *appIndependentFX) Call(e appServiceEffect) appServiceReply {
	f.trace = append(f.trace, e)
	if e.Kind == f.s.Panic {
		panic("synthetic-" + e.Kind)
	}
	if e.Kind == f.s.Fail {
		return appServiceReply{Err: errors.New("synthetic-" + e.Kind)}
	}
	switch e.Kind {
	case "lock":
		if f.owned {
			panic("double lock")
		}
		f.owned = true
	case "unlock":
		if !f.owned {
			panic("unlock unowned")
		}
		f.owned = false
	case "state-base":
		return f.s.Base
	case "running":
		return appServiceReply{Flag: f.s.Running}
	case "proxy":
		return appServiceReply{Flag: true}
	case "active-get":
		if e.Text == "SmartVPN" {
			return appServiceReply{Text: f.s.Active}
		}
		return appServiceReply{Text: f.s.Nested}
	case "active-region":
		if e.Text != "" {
			return appServiceReply{Text: "JP"}
		}
	case "tun-reason":
		return appServiceReply{Text: "synthetic reason"}
	case "choice":
		return f.s.Choice
	case "mode":
		return f.s.Mode
	case "health-facts":
		return appServiceReply{Health: f.s.Health, Regions: f.s.Regions}
	case "pin":
		return appServiceReply{Text: f.s.Pin}
	case "meta":
		return appServiceReply{Params: f.s.Params, Flag: f.s.Blocked}
	case "commit-params":
		f.s.Params = e.Params
	case "store-present":
		return appServiceReply{Flag: f.s.Store}
	case "cancel-present":
		return appServiceReply{Flag: f.s.Cancel}
	case "cancel-close":
		if !f.s.Cancel {
			panic("double cancel")
		}
		f.s.Cancel = false
		f.closes++
	case "cancel-new":
		if f.s.Cancel {
			panic("publish over cancel")
		}
		f.s.Cancel = true
	case "spawn":
		if !f.s.Cancel {
			panic("spawn before publish")
		}
		f.spawns++
	case "path-start":
		f.pathStarts++
	case "job-running":
		return appServiceReply{Flag: f.s.Job}
	case "unblock", "clear-blocked":
		f.s.Blocked = false
	case "clear-reason":
	case "once":
		f.sweeps++
	case "sweep":
		f.sweeps++
	case "wait":
		f.ticks++
		return appServiceReply{Flag: f.ticks <= f.s.Ticks}
	case "events-read":
		return appServiceReply{Count: 1}
	case "parse-limit":
		n, _ := strconv.Atoi(e.Text)
		return appServiceReply{Count: n}
	case "log", "resolved", "project-choice", "project-mode", "response", "save-params", "now", "delays", "switch", "ticker-new", "ticker-stop", "events-project":
	default:
		panic("unknown fake application port: " + e.Kind)
	}
	return appServiceReply{}
}
func (f *appIndependentFX) Release() { f.releases++; f.owned = false }

type appIndependentRun func(*testing.T, appServiceRequest, appServiceEffects) appServiceResult

func appIndependentKinds(f *appIndependentFX) []string {
	out := make([]string, len(f.trace))
	for i, e := range f.trace {
		out[i] = e.Kind
	}
	return out
}
func appIndependentMatrix(t *testing.T, run appIndependentRun) {
	ops := []string{"state", "snapshot", "params", "patrol", "events", "start", "stop", "sweep", "once", "loop"}
	for i := 0; i < 1800; i++ {
		i := i
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			s := appIndependentSpec{Base: appServiceReply{ProxyOn: i&8 != 0, WebRTC: i&16 != 0, DNS: i&32 != 0, JobNil: i&64 != 0}, Choice: appServiceReply{ChainEnabled: i%7 == 0, LockedRegion: []string{"", "JP"}[(i/3)%2], LockedNode: []string{"", "pin"}[(i/5)%2], SelectionMode: []string{"manual", "fallback", "auto"}[(i/7)%3], SelectedNode: "selected"}, Mode: appServiceReply{Mode: modeTUN, DefaultMode: modeSystemProxy, ModeSupported: i%3 == 0}, Params: defaultHealthParams(), Running: i&16 != 0, Cancel: i&32 != 0, Blocked: i&64 != 0, Store: i&128 != 0, Job: i%11 == 0, Active: []string{"", "DIRECT", blockedChoice, autoGroup, fallbackGroup, "node"}[(i/3)%6], Nested: "nested", Pin: []string{"", "JP"}[(i/7)%2], Fail: []string{"", "save-params", "proxy", "active-get", "delays", "switch", "events-read"}[(i/9)%7], Ticks: i % 4, Health: []appHealthFact{{State: "unknown"}, {State: healthHealthy, Cooldown: i%2 == 0}}, Regions: []appRegionFact{{Country: "JP", Verified: true, Expired: i%3 == 0, Cooldown: i%4 == 0}}}
			q := appServiceRequest{Op: ops[i%10], Params: healthParams{PatrolIntervalSec: []int{-1, 0, 9, 10, 601}[(i/10)%5]}, Limit: []string{"", "0", " 1", "500", "501", "2", "999999999999999999999"}[(i/4)%7], Interval: time.Second}
			a, b := &appIndependentFX{s: s}, &appIndependentFX{s: s}
			g, r := runGoAppService(q, a), run(t, q, b)
			if !reflect.DeepEqual(g, r) || !reflect.DeepEqual(a.trace, b.trace) || !reflect.DeepEqual(a.s, b.s) || a.spawns != b.spawns || a.closes != b.closes || a.sweeps != b.sweeps || a.releases != b.releases || a.owned || b.owned {
				t.Fatalf("q=%+v\nGo=%+v\nRust=%+v\nGo trace=%v\nRust trace=%v", q, g, r, a.trace, b.trace)
			}
		})
	}
}
