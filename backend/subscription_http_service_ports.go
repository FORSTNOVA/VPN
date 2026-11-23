package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type appSubscriptionHTTPEffects struct {
	a                              *app
	q                              subscriptionHTTPServiceRequest
	w                              http.ResponseWriter
	hooks                          map[string]func(subscriptionHTTPServiceEffect) subscriptionHTTPServiceReply
	owned, deferRelease, responded bool
	writerPanic                    any
	nodes                          []proxyNode
	lastErr                        error
	format                         string
	subscriptionURL, home          string
	request                        *http.Request
	response                       *http.Response
	body                           io.ReadCloser
	payload, decoded               []byte
	text                           string
	vmess                          [][]string
	report                         subscriptionReport
	do                             func(*http.Request) (*http.Response, error)
	readFile                       func(string) ([]byte, error)
	parseNodes                     func([]byte) []proxyNode
	cancel                         <-chan struct{}
	pendingCancel                  chan struct{}
	ticker                         <-chan time.Time
	stopTicker                     func()
	newTicker                      func(time.Duration) (<-chan time.Time, func())
	spawn                          func(<-chan struct{})
	tick                           func()
}

func newAppSubscriptionHTTPEffects(a *app, q subscriptionHTTPServiceRequest) *appSubscriptionHTTPEffects {
	e := &appSubscriptionHTTPEffects{a: a, q: q, readFile: os.ReadFile, parseNodes: parseProfileNodes, newTicker: func(d time.Duration) (<-chan time.Time, func()) { t := time.NewTicker(d); return t.C, t.Stop }}
	if a != nil {
		e.spawn = func(c <-chan struct{}) { go a.autoMergeLoop(c) }
		e.tick = a.autoMergeTickSafe
	}
	return e
}
func (e *appSubscriptionHTTPEffects) closeBody() {
	if b := e.body; b != nil {
		e.body = nil
		_ = b.Close()
	}
}
func (e *appSubscriptionHTTPEffects) closeTicker() {
	if f := e.stopTicker; f != nil {
		e.stopTicker = nil
		f()
	}
}
func (e *appSubscriptionHTTPEffects) Release() {
	if e.deferRelease {
		return
	}
	defer func() {
		if e.owned {
			e.owned = false
			e.a.mu.Unlock()
		}
	}()
	defer e.closeTicker()
	defer e.closeBody()
	if c := e.pendingCancel; c != nil {
		e.pendingCancel = nil
		if e.a.autoMergeCancel == c {
			e.a.autoMergeCancel = nil
		}
		close(c)
	}
}
func subscriptionHTTPTextFacts(text string) subscriptionHTTPServiceReply {
	return subscriptionHTTPServiceReply{Clash: strings.HasPrefix(text, "proxies:") || strings.Contains(text, "\nproxies:"), JSON: strings.HasPrefix(text, "{"), URI: hasProxyURIGo(text)}
}
func (e *appSubscriptionHTTPEffects) Call(q subscriptionHTTPServiceEffect) (r subscriptionHTTPServiceReply) {
	if q.Kind == "response" {
		defer func() {
			if p := recover(); p != nil {
				if e.responded {
					e.writerPanic = p
					panic(subscriptionHTTPResponsePanic{p})
				}
				panic(p)
			}
		}()
	}
	if f := e.hooks[q.Kind]; f != nil {
		return f(q)
	}
	switch q.Kind {
	case "lock":
		if e.owned {
			panic("subscription HTTP double lock")
		}
		e.a.mu.Lock()
		e.owned = true
	case "unlock":
		if e.deferRelease && e.q.Op != "diagnostics" {
			return
		}
		if e.owned {
			e.owned = false
			e.a.mu.Unlock()
		}
	case "running":
		r.Flag = e.a.kernelRunningLocked()
	case "fetch-profile":
		e.nodes, r.Err = e.a.fetchProfileLocked()
		e.lastErr = r.Err
	case "format-read":
		b, _ := e.readFile(e.a.profilePath() + ".format")
		e.format = strings.TrimSpace(string(b))
	case "provider-put":
		_, r.Err = e.a.mihomoRequest(http.MethodPut, "/providers/proxies/SmartVPNSubscription", nil)
	case "wait-nodes":
		r.Err = e.a.waitForNodesLocked(q.Interval)
	case "list-nodes":
		e.nodes, r.Err = e.a.listNodesLocked()
	case "cache-nodes":
		e.a.replaceCachedNodesLocked(append([]proxyNode(nil), e.nodes...), false)
	case "save-nodes":
		r.Err = e.a.saveCachedNodesLocked(e.nodes)
	case "source-snapshot":
		e.subscriptionURL = e.a.controlMetaLocked().SubscriptionURL
		e.home = e.a.home
		r.Flag = e.subscriptionURL != ""
	case "url-parse":
		var u *url.URL
		u, r.Err = url.ParseRequestURI(e.subscriptionURL)
		if r.Err == nil {
			r.Names = make([]string, 0, len(u.Query()))
			for k := range u.Query() {
				r.Names = append(r.Names, k)
			}
		}
	case "query-keys":
		e.report = subscriptionReport{RequestQueryKeys: append([]string{}, q.Names...), UserAgent: subscriptionUserAgent, FlagAdded: false}
	case "request-new":
		e.request, r.Err = http.NewRequest(http.MethodGet, e.subscriptionURL, nil)
		if r.Err == nil {
			e.request.Header.Set("User-Agent", subscriptionUserAgent)
		}
	case "request-do":
		do := e.do
		if do == nil {
			do = newSubscriptionHTTPClient().Do
		}
		e.response, r.Err = do(e.request)
		if r.Err == nil {
			e.body = e.response.Body
		}
	case "body-read":
		e.payload, r.Err = io.ReadAll(io.LimitReader(e.body, 10*1024*1024+1))
		r.Count = len(e.payload)
	case "body-truncate":
		e.payload = e.payload[:q.Count]
	case "fetch-meta":
		e.report.Fetch.StatusCode = e.response.StatusCode
		e.report.Fetch.ContentType = e.response.Header.Get("Content-Type")
		e.report.Fetch.Bytes = q.Count
	case "fetch-error":
		switch q.Text {
		case "network":
			e.report.Fetch.Error = subscriptionHTTPNetworkError
		case "large":
			e.report.Fetch.Error = subscriptionHTTPLargeError
		case "read":
			e.report.Fetch.Error = subscriptionHTTPReadError
		default:
			panic("unknown fetch error")
		}
	case "cache-read":
		e.payload, r.Err = e.readFile(filepath.Join(e.home, "providers", "subscription.yaml"))
	case "source-read":
		var b []byte
		b, r.Err = e.readFile(filepath.Join(e.home, "providers", "subscription.yaml.source"))
		r.Flag = r.Err == nil && strings.TrimSpace(string(b)) == subscriptionHash(e.subscriptionURL)
	case "diagnostic-format":
		b, err := e.readFile(filepath.Join(e.home, "providers", "subscription.yaml.format"))
		if err == nil {
			e.report.AppliedFormat = strings.TrimSpace(string(b))
		}
	case "payload-start":
		r.Count = len(e.payload)
		r.Other = len(e.parseNodes(e.payload))
	case "payload-text":
		e.text = strings.TrimSpace(string(e.payload))
		r = subscriptionHTTPTextFacts(e.text)
	case "payload-base64":
		var b []byte
		b, r.Err = base64.StdEncoding.DecodeString(strings.Join(strings.Fields(e.text), ""))
		if r.Err == nil {
			e.text = strings.TrimSpace(string(b))
			r = subscriptionHTTPTextFacts(e.text)
		}
	case "uri-count":
		r.Count = len(regexp.MustCompile(`(?m)^`+q.Text+`://`).FindAllStringIndex(e.text, -1))
	case "payload-nodes":
		for _, n := range e.parseNodes(e.payload) {
			r.Nodes = append(r.Nodes, subscriptionHTTPNodeFact{strings.EqualFold(n.Type, "vmess"), n.Network, n.TLS, n.WsHost, n.WsPath})
		}
	case "vmess-matches":
		e.vmess = regexp.MustCompile(`(?m)^vmess://([^\s]+)`).FindAllStringSubmatch(e.text, -1)
		r.Count = len(e.vmess)
	case "vmess-decode":
		encoded := e.vmess[q.Count][1]
		if q.Flag {
			e.decoded, r.Err = base64.RawStdEncoding.DecodeString(strings.TrimRight(encoded, "="))
		} else {
			e.decoded, r.Err = base64.StdEncoding.DecodeString(encoded)
		}
	case "vmess-json":
		var d vmessDetails
		r.Err = json.Unmarshal(e.decoded, &d)
		if r.Err == nil {
			r.Text = d.Name
			r.Network = d.Net
			r.TLS = d.TLS
			r.Host = d.Host != ""
			r.Path = d.Path != ""
		}
	case "payload-commit":
		if q.Text == "fetch" {
			e.report.Fetch.Payload = subscriptionHTTPPayload(q.Analysis)
		} else {
			e.report.Cache = subscriptionHTTPPayload(q.Analysis)
		}
	case "response":
		e.respond(q)
	case "body-close":
		e.closeBody()
	case "cancel-present":
		r.Flag = e.a.autoMergeCancel != nil
	case "cancel-new":
		c := make(chan struct{})
		e.a.autoMergeCancel = c
		e.pendingCancel = c
	case "spawn":
		c := e.pendingCancel
		e.pendingCancel = nil
		e.spawn(c)
	case "cancel-close":
		c := e.a.autoMergeCancel
		e.a.autoMergeCancel = nil
		if e.pendingCancel == c {
			e.pendingCancel = nil
		}
		if c != nil {
			close(c)
		}
	case "ticker-new":
		e.ticker, e.stopTicker = e.newTicker(q.Interval)
	case "wait":
		select {
		case <-e.cancel:
		case <-e.ticker:
			r.Flag = true
		}
	case "ticker-stop":
		e.closeTicker()
	case "tick-child":
		e.tick()
	case "auto-tick":
		e.a.autoMergeTickLocked()
	default:
		panic("unknown subscription HTTP effect: " + q.Kind)
	}
	return
}
func subscriptionHTTPPayload(p subscriptionHTTPAnalysis) payloadReport {
	out := payloadReport{Present: p.Present, Format: p.Format, Bytes: p.Bytes, Selectable: p.Selectable, ValidVmess: p.ValidVmess, InfoEntries: p.InfoEntries, WsHostSet: p.WsHostSet, WsPathSet: p.WsPathSet, UriCounts: map[string]int{}, Networks: map[string]int{}, TlsModes: map[string]int{}}
	for _, v := range p.UriCounts {
		out.UriCounts[v.Name] = v.Count
	}
	for _, v := range p.Networks {
		out.Networks[v.Name] = v.Count
	}
	for _, v := range p.TlsModes {
		out.TlsModes[v.Name] = v.Count
	}
	return out
}
func subscriptionHTTPMessage(key string) string {
	switch key {
	case "use-refresh":
		return "use refresh while connected"
	case "connect":
		return "connect before refreshing nodes"
	case "provider":
		return "subscription refresh failed; check the subscription and network"
	case "wait":
		return "subscription returned no usable nodes"
	case "list":
		return "could not read Mihomo nodes"
	case "save":
		return "could not save the refreshed node list"
	case "no-url":
		return "set a subscription URL first"
	case "url":
		return "invalid subscription URL"
	}
	return "subscription state unavailable"
}
func (e *appSubscriptionHTTPEffects) respond(q subscriptionHTTPServiceEffect) {
	if q.Status != 200 {
		message := subscriptionHTTPMessage(q.Text)
		if q.Text == "fetch" && e.lastErr != nil {
			message = e.lastErr.Error()
		}
		e.responded = true
		http.Error(e.w, message, q.Status)
		return
	}
	var data any
	switch q.Text {
	case "fetch":
		data = map[string]any{"nodes": e.nodes, "proxyEnabled": false, "appliedFormat": e.format}
	case "refresh":
		data = map[string]any{"nodes": e.nodes}
	case "diagnostics":
		data = e.report
	default:
		panic("unknown subscription HTTP response")
	}
	e.responded = true
	writeJSON(e.w, data)
}
func runSubscriptionHTTPWithEffects(q subscriptionHTTPServiceRequest, e *appSubscriptionHTTPEffects) (out subscriptionHTTPServiceResult) {
	if e.w != nil {
		e.deferRelease = true
	}
	defer func() { e.deferRelease = false; e.Release() }()
	defer func() {
		if p := recover(); p != nil {
			if e.writerPanic != nil {
				panic(e.writerPanic)
			}
			out.Err = fmt.Errorf("subscription operation interrupted: %v", p)
		}
		// Diagnostics used a short locked capture before its outer HTTP boundary.
		// On capture failure the mutex must also be released before the 500.
		if out.Err != nil && q.Op == "diagnostics" && e.owned {
			e.owned = false
			e.a.mu.Unlock()
		}
		if out.Err != nil && e.w != nil && !e.responded {
			e.responded = true
			http.Error(e.w, "subscription state unavailable", 500)
		}
	}()
	return runSelectedSubscriptionHTTPService(q, e)
}
func (a *app) runSubscriptionHTTP(op string, w http.ResponseWriter) {
	q := subscriptionHTTPServiceRequest{Op: op}
	e := newAppSubscriptionHTTPEffects(a, q)
	e.w = w
	runSubscriptionHTTPWithEffects(q, e)
}
func (a *app) autoMergeLoop(c <-chan struct{}) {
	q := subscriptionHTTPServiceRequest{Op: "loop"}
	e := newAppSubscriptionHTTPEffects(a, q)
	e.cancel = c
	if r := runSubscriptionHTTPWithEffects(q, e); r.Err != nil {
		log.Print("automatic merge interrupted")
	}
}

// A fresh direct client preserves the original timeout/transport policy.
func newSubscriptionHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &http.Client{Timeout: 20 * time.Second, Transport: transport}
}
