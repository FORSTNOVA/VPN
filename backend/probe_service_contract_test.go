package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"reflect"
	"strings"
	"testing"
	"time"
)

type migrationProbeRoundTrip func(*http.Request) (*http.Response, error)

func (f migrationProbeRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type migrationProbeBody struct{ close func() }

func (b migrationProbeBody) Read([]byte) (int, error) { return 0, io.EOF }
func (b migrationProbeBody) Close() error             { b.close(); return nil }
func TestMigrationChainProbeSafeHTTPGolden(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		failAt int
		err    error
		reuse  bool
		want   int
	}{{"success", 204, -1, nil, true, 2}, {"cold-status", 200, -1, nil, true, 1}, {"warm-nonreuse", 204, -1, nil, false, 2}, {"cold-cancel", 204, 0, context.Canceled, true, 1}, {"cold-deadline", 204, 0, context.DeadlineExceeded, true, 1}, {"warm-failure", 204, 1, errors.New("synthetic network"), true, 2}} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			attempt := 0
			started := time.Now()
			var deadlines []time.Time
			client := &http.Client{Transport: migrationProbeRoundTrip(func(r *http.Request) (*http.Response, error) {
				i := attempt
				attempt++
				calls = append(calls, "GET")
				if r.Method != "GET" || r.URL.String() != probeTarget {
					t.Fatal("request target/method")
				}
				d, ok := r.Context().Deadline()
				if !ok {
					t.Fatal("missing deadline")
				}
				deadlines = append(deadlines, d)
				if i == tc.failAt {
					return nil, tc.err
				}
				if tr := httptrace.ContextClientTrace(r.Context()); tr != nil && tr.GotConn != nil {
					tr.GotConn(httptrace.GotConnInfo{Reused: i > 0 && tc.reuse})
				}
				return &http.Response{StatusCode: tc.status, Header: make(http.Header), Body: migrationProbeBody{close: func() { calls = append(calls, "close") }}}, nil
			})}
			cold, warm, err := timedProbeRequests(context.Background(), client, probeTarget, new(probeObservation), 12*time.Second, started)
			if attempt != tc.want {
				t.Fatalf("attempt count %d", attempt)
			}
			if !deadlines[0].Equal(started.Add(12 * time.Second)) {
				t.Fatal("cold deadline did not include construction time")
			}
			if len(deadlines) > 1 && deadlines[1].Before(deadlines[0]) {
				t.Fatal("warm deadline was not renewed")
			}
			if tc.name == "success" {
				if cold < 1 || warm < 1 || err != nil || !reflect.DeepEqual(calls, []string{"GET", "close", "GET", "close"}) {
					t.Fatalf("success %d %d %v calls=%v", cold, warm, err, calls)
				}
			} else if err == nil {
				t.Fatal("missing expected error")
			}
			if strings.HasPrefix(tc.name, "cold") && warm != 0 {
				t.Fatal("cold failure performed warm")
			}
			if tc.name == "warm-nonreuse" && (cold < 1 || warm != 0) {
				t.Fatal("nonreuse discarded cold/retained warm")
			}
		})
	}
}
func TestMigrationChainProbeSafeObservationGolden(t *testing.T) {
	o := new(probeObservation)
	o.address("[::ffff:192.0.2.8]:443", false)
	if o.attempt != "192.0.2.8" {
		t.Fatal("mapped IPv4 not unmapped")
	}
	o.address("[2001:db8::9]:443", true)
	o.address("bad", true)
	if o.ip != "2001:db8::9" || o.attempt != "192.0.2.8" {
		t.Fatal("connection/attempt facts lost")
	}
	o.address("example.com:443", false)
	if o.attempt != "192.0.2.8" {
		t.Fatal("non-IP overwrote fact")
	}
}
func TestMigrationChainProbeSafeErrorIdentityAndCloseOrder(t *testing.T) {
	var messages []string
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded, fmt.Errorf("wrapped: %w", context.DeadlineExceeded), errors.New(context.DeadlineExceeded.Error())} {
		client := &http.Client{Transport: migrationProbeRoundTrip(func(*http.Request) (*http.Response, error) { return nil, cause })}
		_, _, err := timedProbeRequests(context.Background(), client, probeTarget, new(probeObservation), 12*time.Second, time.Now())
		if err == nil {
			t.Fatal("missing synthetic error")
		}
		messages = append(messages, err.Error())
	}
	if messages[1] != messages[2] || messages[0] != messages[3] || messages[0] == messages[1] {
		t.Fatal("DeadlineExceeded identity lost", messages)
	}
	o := new(probeObservation)
	atClose := ""
	client := &http.Client{Transport: migrationProbeRoundTrip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 500, Header: make(http.Header), Body: migrationProbeBody{close: func() { atClose = o.stage }}}, nil
	})}
	_, _, err := timedProbeRequests(context.Background(), client, probeTarget, o, 12*time.Second, time.Now())
	if err == nil || atClose == "" || atClose == o.stage {
		t.Fatal("body must close before status-validation stage")
	}
}
func TestMigrationChainProbeSafeDeadlineOwnership(t *testing.T) {
	for _, short := range []bool{false, true} {
		ctx := context.Background()
		cancel := func() {}
		if short {
			ctx, cancel = context.WithTimeout(ctx, 5*time.Second)
		}
		func() {
			defer cancel()
			q := chainProbeServiceRequest{Op: "timed-requests", Context: ctx}
			e := newAppChainProbeEffects(nil, q, nil)
			defer e.Release()
			before := time.Now()
			e.Call(chainProbeEffect{Kind: "parent-context"})
			parent, ok := e.ctx.Deadline()
			if !ok {
				t.Fatal("missing 24s parent")
			}
			if short {
				original, _ := ctx.Deadline()
				if !parent.Equal(original) {
					t.Fatal("parent deadline expanded")
				}
			} else if parent.Before(before.Add(24*time.Second)) || parent.After(time.Now().Add(24*time.Second)) {
				t.Fatal("parent budget changed")
			}
			for _, start := range []time.Time{before, before.Add(15 * time.Second)} {
				r := e.Call(chainProbeEffect{Kind: "request-new", Time: start, Budget: 12 * time.Second, Target: probeTarget})
				if r.Err != nil {
					t.Fatal(r.Err)
				}
				got, _ := e.request.Context().Deadline()
				want := start.Add(12 * time.Second)
				if parent.Before(want) {
					want = parent
				}
				if !got.Equal(want) {
					t.Fatal("child budget not bounded by parent")
				}
				requestCtx := e.request.Context()
				e.Call(chainProbeEffect{Kind: "request-cancel"})
				if requestCtx.Err() != context.Canceled {
					t.Fatal("request cancel not released")
				}
			}
			parentCtx := e.ctx
			e.Call(chainProbeEffect{Kind: "parent-cancel"})
			if parentCtx.Err() != context.Canceled {
				t.Fatal("parent not canceled")
			}
		}()
	}
}
