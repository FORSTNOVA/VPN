package main

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/metacubex/mihomo/adapter"
	"github.com/metacubex/mihomo/component/dialer"
	"github.com/metacubex/mihomo/component/proxydialer"
	C "github.com/metacubex/mihomo/constant"
	"io"
	"log"
	"maps"
	"net/http"
	"net/http/httptrace"
	"net/netip"
	"os"
	"strings"
	"time"
)

type appChainProbeEffects struct {
	a                           *app
	q                           chainProbeServiceRequest
	owned                       bool
	hooks                       map[string]func(chainProbeEffect) chainProbeReply
	index                       map[string][]map[string]any
	policy                      map[string][]string
	proxies                     []map[string]any
	current                     map[string]any
	urls                        []string
	ctx                         context.Context
	parentCancel, requestCancel context.CancelFunc
	obs                         *probeObservation
	previous                    C.Dialer
	resolverOpt                 dialer.Option
	resolverReset               func()
	resolverECH                 func(context.Context, string) ([]byte, error)
	ech                         []byte
	nodeClosers                 []func()
	idleClose                   func()
	client                      *http.Client
	request                     *http.Request
	body                        io.ReadCloser
	reused                      bool
	encodedPath, encodedResult  string
	records                     []probeRecord
	now                         func() time.Time
	respond                     func(int, string, probeResult, []probeResult)
}

func newAppChainProbeEffects(a *app, q chainProbeServiceRequest, w http.ResponseWriter) *appChainProbeEffects {
	ctx := q.Context
	if ctx == nil {
		ctx = context.Background()
	}
	obs := q.Observation
	if obs == nil {
		obs = new(probeObservation)
	}
	e := &appChainProbeEffects{a: a, q: q, proxies: q.Proxies, urls: q.EntryURLs, ctx: ctx, obs: obs, client: q.Client, now: time.Now, hooks: map[string]func(chainProbeEffect) chainProbeReply{}}
	e.respond = func(code int, msg string, r probeResult, rs []probeResult) {
		if code != 200 {
			http.Error(w, msg, code)
		} else if q.Op == "list" {
			writeJSON(w, map[string]any{"results": rs})
		} else {
			writeJSON(w, r)
		}
	}
	return e
}
func (e *appChainProbeEffects) snapshot() *chainProbeState {
	s := &chainProbeState{ProxyCount: len(e.proxies), HasURLs: len(e.urls) > 0}
	if e.a != nil {
		s.Subscription = e.a.controlMetaLocked().SubscriptionURL != ""
		s.Store = e.a.store != nil
	}
	return s
}
func (e *appChainProbeEffects) closeBody() {
	if e.body != nil {
		v := e.body
		e.body = nil
		_ = v.Close()
	}
}
func (e *appChainProbeEffects) cancelRequest() {
	if e.requestCancel != nil {
		v := e.requestCancel
		e.requestCancel = nil
		v()
	}
}
func (e *appChainProbeEffects) closeIdle() {
	if e.idleClose != nil {
		v := e.idleClose
		e.idleClose = nil
		v()
	}
}
func (e *appChainProbeEffects) closeNode(i int) {
	if i >= 0 && i < len(e.nodeClosers) && e.nodeClosers[i] != nil {
		v := e.nodeClosers[i]
		e.nodeClosers[i] = nil
		v()
	}
}
func (e *appChainProbeEffects) resetResolver() {
	if e.resolverReset != nil {
		v := e.resolverReset
		e.resolverReset = nil
		v()
	}
}
func (e *appChainProbeEffects) cancelParent() {
	if e.parentCancel != nil {
		v := e.parentCancel
		e.parentCancel = nil
		v()
	}
}
func (e *appChainProbeEffects) closeNodes(i int) {
	if i < 0 {
		return
	}
	defer e.closeNodes(i - 1)
	e.closeNode(i)
}
func (e *appChainProbeEffects) Release() {
	defer func() {
		defer func() {
			if e.owned {
				e.owned = false
				e.a.mu.Unlock()
			}
		}()
		e.cancelParent()
	}()
	defer e.resetResolver()
	defer e.closeNodes(len(e.nodeClosers) - 1)
	defer e.closeIdle()
	defer e.cancelRequest()
	e.closeBody()
}
func (e *appChainProbeEffects) Call(q chainProbeEffect) (r chainProbeReply) {
	if hook := e.hooks[q.Kind]; hook != nil {
		return hook(q)
	}
	switch q.Kind {
	case "lock":
		e.a.mu.Lock()
		e.owned = true
		r.State = e.snapshot()
	case "unlock":
		e.owned = false
		e.a.mu.Unlock()
	case "snapshot":
		r.State = e.snapshot()
	case "index":
		e.index, r.Err = e.a.currentProxyIndexLocked()
	case "mode":
		r.State = e.snapshot()
		r.State.TUN = e.a.connectionModeLocked() != modeSystemProxy
		r.State.StrictDNS = r.State.TUN && e.a.controlMetaLocked().DNSProtection
	case "policy":
		var b []byte
		if e.a.controlMetaLocked().SubscriptionURL != "" {
			b, _ = os.ReadFile(e.a.profilePath())
			e.policy = entryDNS(b)
		}
		r.Policy = e.policy
	case "compile":
		e.proxies, r.Err = compilePrefix(q.Hops, e.index)
		r.State = &chainProbeState{ProxyCount: len(e.proxies), HasURLs: len(e.urls) > 0}
	case "fingerprint":
		r.Text = probeFingerprint(e.proxies)
	case "parent-context":
		e.ctx, e.parentCancel = context.WithTimeout(e.ctx, 24*time.Second)
	case "parent-cancel":
		e.cancelParent()
	case "entry-policy":
		r.URLs = e.policy[strings.ToLower(stringValue(e.proxies[0]["server"]))]
		r.Index = len(e.policy)
	case "entry-select":
		e.urls = append([]string(nil), q.URLs...)
	case "now":
		r.Now = e.now()
	case "stage":
		e.obs.setStage(q.Text)
	case "node":
		e.current = e.proxies[q.Index]
		delete(e.current, "dialer-proxy")
		p := e.current
		f := chainProbeNodeFacts{Server: stringValue(p["server"]), Interface: stringValue(p["interface-name"]), RoutingMark: intValue(p["routing-mark"]), TFO: tlsEnabled(p["tfo"]), MPTCP: tlsEnabled(p["mptcp"]), IPVersion: strings.ToLower(stringValue(p["ip-version"])), ServerName: stringValue(p["servername"]), SNI: stringValue(p["sni"])}
		if ech, ok := p["ech-opts"].(map[string]any); ok {
			f.ECHPresent = true
			f.ECHEnabled = tlsEnabled(ech["enable"])
			f.ECHConfig = stringValue(ech["config"])
			f.ECHQuery = stringValue(ech["query-server-name"])
		}
		r.Node = f
	case "resolver":
		rs, err := newEntryResolver(e.urls, q.Node.Interface, q.Node.Server)
		r.Err = err
		r.Bool = rs != nil
		if rs != nil {
			e.resolverOpt = dialer.WithResolver(rs)
			e.resolverReset = rs.ResetConnection
			e.resolverECH = rs.ResolveECH
		}
	case "ech":
		ctx, cancel := context.WithDeadline(e.ctx, q.Time)
		defer cancel()
		e.ech, r.Err = e.resolverECH(ctx, q.Name)
		if r.Err != nil && !hasHTTPSResolver(e.urls) {
			if dohRS, derr := newEntryResolver([]string{entryDoH, "https://doh.pub/dns-query"}, stringValue(e.current["interface-name"]), q.Name); derr == nil && dohRS != nil {
				defer dohRS.ResetConnection()
				if ech, eerr := dohRS.ResolveECH(ctx, q.Name); eerr == nil {
					e.ech, r.Err = ech, nil
				}
			}
		}
	case "ech-apply":
		p := maps.Clone(e.current)
		ech := maps.Clone(p["ech-opts"].(map[string]any))
		ech["config"] = base64.StdEncoding.EncodeToString(e.ech)
		p["ech-opts"] = ech
		e.current = p
	case "entry-dialer":
		f := q.Node
		opts := []dialer.Option{}
		if f.Interface != "" {
			opts = append(opts, dialer.WithInterface(f.Interface))
		}
		if f.RoutingMark != 0 {
			opts = append(opts, dialer.WithRoutingMark(f.RoutingMark))
		}
		if f.TFO {
			opts = append(opts, dialer.WithTFO(true))
		}
		if f.MPTCP {
			opts = append(opts, dialer.WithMPTCP(true))
		}
		switch f.IPVersion {
		case "ipv4":
			opts = append(opts, dialer.WithOnlySingleStack(true))
		case "ipv6":
			opts = append(opts, dialer.WithOnlySingleStack(false))
		case "ipv4-prefer":
			opts = append(opts, dialer.WithPreferIPv4())
		case "ipv6-prefer":
			opts = append(opts, dialer.WithPreferIPv6())
		}
		if e.resolverECH != nil { // resolver object captured by dedicated factory below
			opts = append(opts, e.resolverOpt)
		}
		entryStage := ""
		if len(e.proxies) > 1 {
			entryStage = "第 1 跳握手或转发至第 2 跳"
		}
		e.previous = &observedEntry{Dialer: dialer.NewDialer(opts...), obs: e.obs, stage: entryStage}
	case "adapter":
		node, err := adapter.ParseProxy(e.current, adapter.WithDialerForAPI(e.previous))
		r.Err = err
		if err == nil {
			e.nodeClosers = append(e.nodeClosers, func() { node.Close() })
			pd := proxydialer.New(node, false)
			if len(e.proxies) > 1 {
				hop := q.Index + 1
				nextStage := fmt.Sprintf("第 %d 跳代理握手或转发至目标", hop)
				if hop < len(e.proxies) {
					nextStage = fmt.Sprintf("第 %d 跳代理握手或转发至第 %d 跳", hop+1, hop+2)
					if hop+1 == len(e.proxies) {
						nextStage = fmt.Sprintf("第 %d 跳代理握手或转发至目标", hop+1)
					}
				}
				e.previous = &observedHop{Dialer: pd, obs: e.obs, stage: nextStage}
			} else {
				e.previous = pd
			}
		}
	case "transport":
		tr := &http.Transport{DialContext: e.previous.DialContext}
		e.idleClose = tr.CloseIdleConnections
		e.client = &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	case "request-new":
		r.Err = e.newRequest(q.Time, q.Budget, q.Target)
	case "request-do":
		var resp *http.Response
		resp, r.Err = e.client.Do(e.request)
		r.Deadline = errors.Is(r.Err, context.DeadlineExceeded)
		r.Reused = e.reused
		if r.Err == nil {
			e.body = resp.Body
			r.HTTPStatus = resp.StatusCode
		}
	case "elapsed":
		r.Elapsed = e.now().Sub(q.Time).Milliseconds()
	case "body-close":
		e.closeBody()
	case "request-cancel":
		e.cancelRequest()
	case "idle-close":
		e.closeIdle()
	case "node-close":
		e.closeNode(q.Index)
	case "resolver-reset":
		e.resetResolver()
	case "observation":
		e.obs.mu.Lock()
		r.IP = e.obs.ip
		r.Attempt = e.obs.attempt
		r.Stage = e.obs.stage
		e.obs.mu.Unlock()
		ip := r.IP
		if ip == "" {
			ip = r.Attempt
		}
		if v, err := netip.ParseAddr(ip); err == nil {
			r.Family = "IPv6"
			if v.Unmap().Is4() {
				r.Family = "IPv4"
			}
		}
	case "store-check":
		r.Bool = e.a.store != nil
	case "path-encode":
		b, _ := json.Marshal(q.Hops)
		e.encodedPath = string(b)
		r.Text = e.encodedPath
	case "result-encode":
		b, _ := json.Marshal(q.Result)
		e.encodedResult = string(b)
		r.Text = e.encodedResult
	case "save":
		r.Err = e.a.store.saveProbeRecord(q.Report)
	case "records":
		e.records, r.Err = e.a.store.loadProbeRecords()
		r.Records = e.records
	case "details-decode":
		r.Result = q.Result
		if e.records[q.Index].Details != "" {
			r.Err = json.Unmarshal([]byte(e.records[q.Index].Details), &r.Result)
		}
	case "path-decode":
		r.Result = q.Result
		r.Err = json.Unmarshal([]byte(e.records[q.Index].Path), &r.Result.Hops)
	case "response":
		e.respond(q.Status, q.Text, q.Result, q.Results)
	default:
		panic("unknown chain probe effect: " + q.Kind)
	}
	return
}
func (e *appChainProbeEffects) newRequest(start time.Time, budget time.Duration, target string) error {
	ctx, cancel := context.WithDeadline(e.ctx, start.Add(budget))
	e.requestCancel = cancel
	e.reused = false
	trace := &httptrace.ClientTrace{TLSHandshakeStart: func() { e.obs.setStage(chainProbeStage(9)) }, TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
		if err == nil {
			e.obs.setStage(chainProbeStage(10))
		}
	}, GotConn: func(info httptrace.GotConnInfo) { e.reused = info.Reused; e.obs.setStage(chainProbeStage(10)) }, WroteRequest: func(info httptrace.WroteRequestInfo) {
		if info.Err == nil {
			e.obs.setStage(chainProbeStage(11))
		}
	}}
	var err error
	e.request, err = http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), "GET", target, nil)
	return err
}

func (a *app) runChainProbeService(q chainProbeServiceRequest, w http.ResponseWriter) (out chainProbeServiceResult) {
	responded := false
	defer func() {
		if recover() != nil {
			out = chainProbeServiceResult{Status: 500, Message: "chain probe operation interrupted", Err: errors.New("chain probe operation interrupted")}
		}
		if out.Err != nil && w != nil && !responded && (q.Op == "probe" || q.Op == "list") {
			responded = true
			http.Error(w, "chain probe operation interrupted", 500)
		}
	}()
	e := newAppChainProbeEffects(a, q, w)
	original := e.respond
	e.respond = func(code int, msg string, r probeResult, rs []probeResult) {
		responded = true
		original(code, msg, r, rs)
	}
	defer e.Release()
	r := runSelectedChainProbeService(q, e)
	if r.Err != nil && (q.Op == "probe" || q.Op == "list") {
		log.Print("chain probe workflow incomplete")
	}
	return r
}

func hasHTTPSResolver(urls []string) bool {
	for _, u := range urls {
		if strings.HasPrefix(u, "https://") {
			return true
		}
	}
	return false
}
