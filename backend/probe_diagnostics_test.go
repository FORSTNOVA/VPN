package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestProbeSeparateBudgets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { time.Sleep(180 * time.Millisecond); w.WriteHeader(204) }))
	defer server.Close()
	first, warm, err := timeProbeRequests(context.Background(), server.Client(), server.URL, new(probeObservation), 320*time.Millisecond)
	if err != nil || first < 170 || warm < 170 {
		t.Fatalf("delays %d %d: %v", first, warm, err)
	}
}
func TestProbeReportsWarmStatusFailure(t *testing.T) {
	n := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n++
		if n == 1 {
			w.WriteHeader(204)
		} else {
			w.WriteHeader(503)
		}
	}))
	defer server.Close()
	obs := new(probeObservation)
	first, warm, err := timeProbeRequests(context.Background(), server.Client(), server.URL, obs, time.Second)
	if err == nil || first == 0 || warm != 0 || obs.stage != "校验 HTTP 状态" {
		t.Fatal(first, warm, err, obs.stage)
	}
}
