package main

import (
	"context"
	"net/http"
	"time"
)

type chainProbeServiceRequest struct {
	Op          string
	Hops        []string
	EntryURLs   []string
	Proxies     []map[string]any
	Target      string
	Context     context.Context
	Client      *http.Client
	Observation *probeObservation
	Budget      time.Duration
	Started     time.Time
}
type chainProbeServiceResult struct {
	Status     int
	Message    string
	Err        error
	Cold, Warm int64
	Result     probeResult
	Results    []probeResult
}
type chainProbeState struct {
	Subscription, TUN, StrictDNS, Store, HasURLs bool
	ProxyCount                                   int
}
type chainProbeNodeFacts struct {
	Server, Interface, IPVersion         string
	RoutingMark                          int
	TFO, MPTCP, ECHPresent, ECHEnabled   bool
	ECHConfig, ECHQuery, ServerName, SNI string
}
type chainProbeEffect struct {
	Kind, Text, Name, Target string
	Index, Status            int
	Results                  []probeResult
	URLs                     []string
	Hops                     []string
	Time                     time.Time
	Budget                   time.Duration
	Node                     chainProbeNodeFacts
	Result                   probeResult
	Report                   probeRecord
}
type chainProbeReply struct {
	State                            *chainProbeState
	Err                              error
	Bool, Deadline, Reused           bool
	Index                            int
	Text, IP, Attempt, Stage, Family string
	Now                              time.Time
	Elapsed                          int64
	HTTPStatus                       int
	URLs                             []string
	Policy                           map[string][]string
	Node                             chainProbeNodeFacts
	Records                          []probeRecord
	Result                           probeResult
}
type chainProbeServiceEffects interface {
	Call(chainProbeEffect) chainProbeReply
	Release()
}

func cloneChainProbeState(s *chainProbeState) *chainProbeState {
	if s == nil {
		return nil
	}
	v := *s
	return &v
}
