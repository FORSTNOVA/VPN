package main

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
)

var subHTTPIndependentError = errors.New("synthetic subscription failure")

type subHTTPIndependentFX struct {
	Seed                   int
	Fail                   string
	Trace                  []subscriptionHTTPServiceEffect
	Locked, Cached, Cancel bool
	Releases, Waits, Ticks int
}

func (f *subHTTPIndependentFX) Release() { f.Releases++; f.Locked = false }
func (f *subHTTPIndependentFX) Call(e subscriptionHTTPServiceEffect) (r subscriptionHTTPServiceReply) {
	f.Trace = append(f.Trace, e)
	if e.Kind == f.Fail {
		r.Err = subHTTPIndependentError
	}
	switch e.Kind {
	case "lock":
		if f.Locked {
			panic("double lock")
		}
		f.Locked = true
	case "unlock":
		if !f.Locked {
			panic("unlock without lock")
		}
		f.Locked = false
	case "running":
		r.Flag = f.Seed%2 == 0
	case "fetch-profile", "format-read", "provider-put", "wait-nodes", "list-nodes", "save-nodes", "diagnostic-format", "body-close", "fetch-meta", "fetch-error", "payload-commit", "query-keys", "request-new", "request-do", "body-truncate", "response", "auto-tick":
	case "cache-nodes":
		f.Cached = true
	case "source-snapshot":
		r.Flag = f.Seed%7 != 0
	case "url-parse":
		r.Names = []string{"z", "a", "a"}
	case "body-read":
		r.Count = []int{0, 10, 10 * 1024 * 1024, 10*1024*1024 + 1}[f.Seed%4]
	case "cache-read":
	case "source-read":
		r.Flag = f.Seed%3 != 0
	case "payload-start":
		r.Count = f.Seed % 5
		r.Other = f.Seed % 3
	case "payload-text":
		r.Clash = f.Seed%4 == 0
		r.JSON = f.Seed%4 == 1
		r.URI = f.Seed%4 == 2
	case "payload-base64":
		r.Clash = f.Seed%3 == 0
		r.JSON = f.Seed%3 == 1
		r.URI = f.Seed%3 == 2
	case "uri-count":
		r.Count = len(e.Text) % 3
	case "payload-nodes":
		r.Nodes = []subscriptionHTTPNodeFact{{Vmess: true, Network: "ws", TLS: true, WsHost: true, WsPath: true}, {Network: ""}}
	case "vmess-matches":
		r.Count = f.Seed % 4
	case "vmess-decode":
		if (f.Seed+e.Count)%3 == 0 && !e.Flag {
			r.Err = subHTTPIndependentError
		}
	case "vmess-json":
		r.Text = "normal"
		if f.Seed%2 == 0 {
			r.Text = "剩余流量"
		}
		r.Network = []string{"", "ws", "grpc"}[f.Seed%3]
		r.TLS = "reality"
		r.Host = true
		r.Path = true
	case "cancel-present":
		r.Flag = f.Cancel
	case "cancel-new":
		f.Cancel = true
	case "cancel-close":
		f.Cancel = false
	case "spawn":
		if !f.Cancel {
			panic("spawn before publish")
		}
	case "ticker-new":
	case "wait":
		f.Waits++
		r.Flag = f.Waits <= f.Seed%4
	case "ticker-stop":
	case "tick-child":
		f.Ticks++
	default:
		panic("unknown unsafe subscription port: " + e.Kind)
	}
	return
}

type subHTTPIndependentRunner func(subscriptionHTTPServiceRequest, subscriptionHTTPServiceEffects) subscriptionHTTPServiceResult

func subHTTPIndependentMatrix(t *testing.T, run subHTTPIndependentRunner) {
	t.Helper()
	ops := []string{"fetch", "refresh", "diagnostics", "analyze", "start", "stop", "loop", "tick"}
	fails := []string{"", "fetch-profile", "provider-put", "wait-nodes", "list-nodes", "save-nodes", "url-parse", "request-new", "request-do", "body-read", "cache-read", "source-read", "payload-base64", "vmess-decode", "vmess-json"}
	for i := 0; i < 1600; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			q := subscriptionHTTPServiceRequest{Op: ops[i%8]}
			a := &subHTTPIndependentFX{Seed: i, Fail: fails[(i/8)%len(fails)], Cancel: i%3 == 0}
			b := *a
			ga := runGoSubscriptionHTTPService(q, a)
			gb := run(q, &b)
			if !reflect.DeepEqual(ga, gb) || !reflect.DeepEqual(a, &b) {
				t.Fatalf("op %s seed %d fail %s\ngo=%+v rust=%+v\nGO=%+v\nACT=%+v", q.Op, i, a.Fail, ga, gb, a, &b)
			}
		})
	}
}
func subHTTPKinds(f *subHTTPIndependentFX) []string {
	var a []string
	for _, e := range f.Trace {
		a = append(a, e.Kind)
	}
	return a
}
func subHTTPIndependentGoldens(t *testing.T, run subHTTPIndependentRunner) {
	f := &subHTTPIndependentFX{Seed: 2, Fail: "save-nodes"}
	run(subscriptionHTTPServiceRequest{Op: "refresh"}, f)
	want := []string{"lock", "running", "fetch-profile", "provider-put", "wait-nodes", "list-nodes", "cache-nodes", "save-nodes", "response", "unlock"}
	if !reflect.DeepEqual(subHTTPKinds(f), want) || !f.Cached || f.Releases != 1 || f.Trace[8].Status != 500 {
		t.Fatalf("partial cache %+v", f)
	}
	f = &subHTTPIndependentFX{Seed: 3, Fail: "body-read"}
	run(subscriptionHTTPServiceRequest{Op: "diagnostics"}, f)
	var errorsSeen []string
	for i, e := range f.Trace {
		if e.Kind == "request-do" && (i < 3 || f.Trace[2].Kind != "unlock") {
			t.Fatal("HTTP before unlock")
		}
		if e.Kind == "fetch-error" {
			errorsSeen = append(errorsSeen, e.Text)
		}
		if e.Kind == "query-keys" && !reflect.DeepEqual(e.Names, []string{"a", "a", "z"}) {
			t.Fatal(e.Names)
		}
	}
	if !reflect.DeepEqual(errorsSeen, []string{"large"}) || f.Trace[len(f.Trace)-1].Kind != "body-close" {
		t.Fatalf("truncate priority/close %+v", f)
	}
	f = &subHTTPIndependentFX{Seed: 3}
	run(subscriptionHTTPServiceRequest{Op: "loop"}, f)
	if f.Ticks != 3 || f.Trace[0].Kind != "ticker-new" || f.Trace[0].Interval.String() != "10m0s" || f.Trace[1].Kind != "wait" || f.Trace[len(f.Trace)-1].Kind != "ticker-stop" {
		t.Fatalf("loop %+v", f)
	}
}
