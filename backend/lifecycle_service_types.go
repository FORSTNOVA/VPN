package main

import (
	"net/http"
	"time"
)

type lifecycleServiceRequest struct{ Op string }
type lifecycleServiceState struct {
	Portable, Android, ChainsNil, StrictPrivacy, HasURL, SelectionEmpty, PathDefault, Store, ProxyOn bool
	Hops, SocksPort, ConnectTimeout, FailureThreshold                                                int
	Selected, Mode                                                                                   string
}
type lifecycleEffect struct {
	Err              error
	Kind, Name, Text string
	Index, Value     int
	Flag             bool
	Indices          []int
	Duration         time.Duration
}
type lifecycleReply struct {
	State *lifecycleServiceState
	Err   error
	Bool  bool
	Value int
	Text  string
	Names []string
	Nodes []proxyNode
}
type lifecycleServiceResult struct {
	Err          error
	Port, Status int
	Message      string
	Server       *http.Server
}
type lifecycleServiceEffects interface {
	Call(lifecycleEffect) lifecycleReply
	Release()
}
