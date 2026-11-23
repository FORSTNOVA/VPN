package main

import (
	"errors"
	"fmt"
	"io"
	"reflect"
	"testing"
	"time"
)

type speedIndependentSpec struct {
	Fail              string
	Temporary         bool
	Node              string
	Down, Up, Started int64
	Elapsed           time.Duration
	Reads             []speedServiceReply
}
type speedIndependentFX struct {
	s             speedIndependentSpec
	trace         []speedServiceEffect
	owned, upload bool
	total         int64
	read          int
	releases      int
}

func (f *speedIndependentFX) Call(e speedServiceEffect) speedServiceReply {
	f.trace = append(f.trace, e)
	if e.Kind == f.s.Fail {
		return speedServiceReply{Err: errors.New("synthetic-" + e.Kind)}
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
	case "ensure":
		return speedServiceReply{Flag: f.s.Temporary}
	case "active":
		return speedServiceReply{Text: f.s.Node}
	case "measure-begin":
		f.upload = e.Upload
		f.total = 0
	case "batch-start":
		if e.Upload {
			f.total = f.s.Up
		} else {
			f.total = f.s.Down
		}
	case "totals":
		return speedServiceReply{Bytes: f.total, Started: f.s.Started}
	case "total-load":
		return speedServiceReply{Bytes: f.total}
	case "elapsed":
		return speedServiceReply{Elapsed: f.s.Elapsed}
	case "rate":
		return speedServiceReply{Rate: throughputMbpsGo(e.Bytes, e.Budget)}
	case "body-read":
		if f.read >= len(f.s.Reads) {
			return speedServiceReply{Err: io.EOF}
		}
		r := f.s.Reads[f.read]
		f.read++
		return r
	case "download-count", "zero-count":
		f.total += e.Bytes
	case "endpoint", "stop", "response", "measure-end", "batch-join", "request-new", "request-do", "body-close", "upload-drain", "zero-fill", "zero-remaining":
	default:
		panic("unexpected fake speed port: " + e.Kind)
	}
	return speedServiceReply{}
}
func (f *speedIndependentFX) Release() {
	f.releases++
	if f.owned {
		f.owned = false
	}
}

type speedIndependentRun func(*testing.T, speedServiceRequest, speedServiceEffects) speedServiceResult

func speedIndependentGo(_ *testing.T, q speedServiceRequest, fx speedServiceEffects) speedServiceResult {
	return runGoSpeedService(q, fx)
}
func speedIndependentError(e error) string {
	if e == nil {
		return ""
	}
	return e.Error()
}
func speedIndependentParity(t *testing.T, q speedServiceRequest, s speedIndependentSpec, run speedIndependentRun) {
	t.Helper()
	a, b := &speedIndependentFX{s: s}, &speedIndependentFX{s: s}
	g, r := runGoSpeedService(q, a), run(t, q, b)
	ge, re := speedIndependentError(g.Err), speedIndependentError(r.Err)
	g.Err = nil
	r.Err = nil
	if ge != re || !reflect.DeepEqual(g, r) || !reflect.DeepEqual(a.trace, b.trace) || a.total != b.total || a.owned || b.owned || a.releases != b.releases {
		t.Fatalf("q=%+v spec=%+v\nGo=%+v err=%q\nRust=%+v err=%q\nGo trace=%+v\nRust trace=%+v", q, s, g, ge, r, re, a.trace, b.trace)
	}
}
func speedIndependentMatrix(t *testing.T, run speedIndependentRun) {
	ops := []string{"handler", "download", "upload", "down-stream", "up-stream", "zero-read"}
	for i := 0; i < 1200; i++ {
		i := i
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			s := speedIndependentSpec{Temporary: (i/6)%2 == 0, Node: []string{"node", "", "rEjEcT", "  node  "}[(i/7)%4], Down: int64(i%5) * 1001, Up: int64(i%7) * 503, Started: int64(i % 3), Elapsed: time.Duration(i%5-1) * time.Millisecond, Reads: []speedServiceReply{{Bytes: int64(i % 9)}, {Bytes: 7, Err: io.EOF}}}
			s.Fail = []string{"", "ensure", "request-new", "request-do"}[(i/4)%4]
			q := speedServiceRequest{Op: ops[i%len(ops)], MegaBytes: []int{-1, 0, 9, 10, 31, 100, 101}[i%7], Allowance: int64(i % 11), Budget: 10 * time.Second, Remaining: int64(i%5 - 1), Length: i % 7}
			speedIndependentParity(t, q, s, run)
		})
	}
}
