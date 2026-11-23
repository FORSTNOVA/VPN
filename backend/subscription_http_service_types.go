package main

import "time"

type subscriptionHTTPServiceRequest struct{ Op string }
type subscriptionHTTPNodeFact struct {
	Vmess               bool
	Network             string
	TLS, WsHost, WsPath bool
}
type subscriptionHTTPValue struct {
	Name  string
	Count int
}
type subscriptionHTTPAnalysis struct {
	Present                                                          bool
	Format                                                           string
	Bytes, Selectable, ValidVmess, InfoEntries, WsHostSet, WsPathSet int
	UriCounts, Networks, TlsModes                                    []subscriptionHTTPValue
}
type subscriptionHTTPServiceEffect struct {
	Kind, Text    string
	Count, Status int
	Flag          bool
	Interval      time.Duration
	Names         []string
	Analysis      subscriptionHTTPAnalysis
}
type subscriptionHTTPServiceReply struct {
	Err                                      error
	Flag, Clash, JSON, URI, Info, Host, Path bool
	Count, Other                             int
	Text, Network, TLS                       string
	Names                                    []string
	Nodes                                    []subscriptionHTTPNodeFact
}
type subscriptionHTTPServiceResult struct {
	Err      error
	Analysis subscriptionHTTPAnalysis
}
type subscriptionHTTPServiceEffects interface {
	Call(subscriptionHTTPServiceEffect) subscriptionHTTPServiceReply
	Release()
}
type subscriptionHTTPResponsePanic struct{ Value any }

// Ops: 0 fetch,1 refresh,2 diagnostics,3 analyze,4 start,5 stop,6 loop,7 tick.
// Ports: 1 lock,2 unlock,3 running,4 fetch-profile,5 format-read,
// 6 provider-put,7 wait-nodes,8 list-nodes,9 cache-nodes,10 save-nodes,
// 11 source-snapshot,12 url-parse,13 query-keys,14 request-new,15 request-do,
// 16 body-read,17 body-truncate,18 fetch-meta,19 fetch-error,20 cache-read,
// 21 source-read,22 diagnostic-format,23 payload-start,24 payload-text,
// 25 payload-base64,26 uri-count,27 payload-nodes,28 vmess-matches,
// 29 vmess-decode,30 vmess-json,31 payload-commit,32 response,33 body-close,
// 34 cancel-present,35 cancel-new,36 spawn,37 cancel-close,38 ticker-new,
// 39 wait,40 ticker-stop,41 tick-child,42 auto-tick.
