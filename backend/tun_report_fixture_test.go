package main

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

type tunIndependentSpec struct {
	Elevated, Installed, Running, Authorized, Alive, Known bool
	FD                                                     int
	Mode, Host, Lock, Fail                                 string
	Routes, DNS, Answers                                   []string
	Adapter                                                adapterReport
	Tunnel, System                                         exitIPResult
	Region                                                 regionRecord
}
type tunIndependentFX struct {
	s        tunIndependentSpec
	trace    []tunReportEffect
	owned    bool
	releases int
}

func (f *tunIndependentFX) Call(e tunReportEffect) tunReportReply {
	f.trace = append(f.trace, e)
	if e.Kind == f.s.Fail {
		return tunReportReply{Err: errors.New("synthetic-" + e.Kind)}
	}
	switch e.Kind {
	case "lock":
		if f.owned {
			panic("double lock")
		}
		f.owned = true
	case "unlock":
		if !f.owned {
			panic("unlock without lock")
		}
		f.owned = false
	case "quote":
		return tunReportReply{Text: strconv.Quote(e.Text)}
	case "refresh":
	case "elevated":
		return tunReportReply{Flag: f.s.Elevated}
	case "wintun-path":
		return tunReportReply{Text: "synthetic/wintun"}
	case "wintun-stat":
		if !f.s.Installed {
			return tunReportReply{Err: errors.New("missing")}
		}
		return tunReportReply{}
	case "wintun-hash":
		return tunReportReply{Text: "0123456789abcdefXX"}
	case "wintun":
		return tunReportReply{Wintun: wintunStatus{Installed: f.s.Installed, Path: "synthetic/wintun", SHA256: "0123456789abcdefXX"}}
	case "mode":
		return tunReportReply{Text: f.s.Mode}
	case "running":
		return tunReportReply{Flag: f.s.Running}
	case "tunnel-state":
		return tunReportReply{Flag: f.s.Authorized, Number: f.s.FD, Text: "synthetic0", Names: f.s.Routes, Other: f.s.DNS}
	case "descriptor-alive":
		return tunReportReply{Flag: f.s.Alive}
	case "adapter-read":
		if f.s.Fail == "adapter" {
			return tunReportReply{Err: errors.New("synthetic-adapter")}
		}
	case "adapter-parse":
		var names, aliases []string
		if f.s.Adapter.IPv4Route {
			names = append(names, "0.0.0.0/0")
			aliases = append(aliases, tunReportInterface)
		}
		if f.s.Adapter.IPv6Route {
			names = append(names, "::/0")
			aliases = append(aliases, tunReportInterface)
		}
		return tunReportReply{Flag: f.s.Adapter.Present, Text: f.s.Adapter.Status, Names: names, Other: aliases}
	case "adapter-alias":
		return tunReportReply{Flag: e.Text == tunReportInterface}
	case "adapter":
		return tunReportReply{Adapter: f.s.Adapter}
	case "lookup":
		return tunReportReply{Names: f.s.Answers}
	case "fake-address":
		return tunReportReply{Flag: isFakeAddress(e.Text)}
	case "read-profile":
		return tunReportReply{Body: []byte("synthetic")}
	case "hostname":
		return tunReportReply{Text: f.s.Host}
	case "active-node":
		return tunReportReply{Text: "node"}
	case "region":
		return tunReportReply{Region: f.s.Region, Flag: f.s.Known}
	case "locked-region":
		return tunReportReply{Text: f.s.Lock, Flag: strings.EqualFold(f.s.Lock, f.s.Region.Country)}
	case "exit-tunnel":
		return tunReportReply{Exit: f.s.Tunnel}
	case "exit-system":
		return tunReportReply{Exit: f.s.System}
	default:
		panic("unknown fake TUN port: " + e.Kind)
	}
	return tunReportReply{}
}
func (f *tunIndependentFX) Release() {
	f.releases++
	if f.owned {
		f.owned = false
	}
}

type tunIndependentRun func(*testing.T, tunReportRequest, tunReportEffects) tunReportResult

func tunIndependentKinds(f *tunIndependentFX) []string {
	out := make([]string, len(f.trace))
	for i, e := range f.trace {
		out[i] = e.Kind
	}
	return out
}
func tunIndependentMatrix(t *testing.T, run tunIndependentRun) {
	ops := []string{"windows-status", "windows-checks", "android-status", "android-checks", "dns", "node", "path", "region"}
	for i := 0; i < 1600; i++ {
		i := i
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			s := tunIndependentSpec{Elevated: i&8 != 0, Installed: i&16 != 0, Running: i&32 != 0, Authorized: i&64 != 0, Alive: i&128 != 0, Known: i&256 != 0, FD: i%3 - 1, Mode: []string{modeTUN, "system"}[(i/5)%2], Host: []string{"", "node.example"}[(i/2)%2], Lock: []string{"", "JP", "us"}[(i/3)%3], Fail: []string{"", "adapter", "read-profile", "lookup"}[(i/7)%4], Routes: [][]string{nil, {}, {"::/0"}, {"10.0.0.0/8"}}[(i/11)%4], DNS: []string{}, Answers: [][]string{nil, {}, {"1.1.1.1"}, {"1.1.1.1", "198.18.0.1"}}[(i/13)%4], Adapter: adapterReport{Name: "fake", Present: i%2 == 0, Status: []string{"Up", "up", "Down"}[i%3], IPv4Route: i&16 != 0, IPv6Route: i&32 != 0, RouteVia: []string{"r1", "r2"}}, Tunnel: exitIPResult{Address: []string{"", "1.2.3.4", "5.6.7.8"}[i%3], Error: "tunnel-error"}, System: exitIPResult{Address: []string{"", "1.2.3.4", "5.6.7.8"}[(i/3)%3], Error: "system-error"}, Region: regionRecord{ExitIP: "1.2.3.4", Country: "JP"}}
			q := tunReportRequest{Op: ops[i%8], Name: "custom", IPv6: i%2 == 0}
			a, b := &tunIndependentFX{s: s}, &tunIndependentFX{s: s}
			g, r := runGoTunReport(q, a), run(t, q, b)
			a.Release()
			b.Release()
			if !reflect.DeepEqual(g, r) || !reflect.DeepEqual(a.trace, b.trace) || a.owned || b.owned {
				t.Fatalf("q=%+v s=%+v\nGo=%+v\nRust=%+v\nGo trace=%v\nRust trace=%v", q, s, g, r, a.trace, b.trace)
			}
		})
	}
}
