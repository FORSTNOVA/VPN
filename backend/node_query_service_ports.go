package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

type appNodeQueryEffects struct {
	responded       bool
	a               *app
	q               nodeQueryServiceRequest
	w               http.ResponseWriter
	locked, owns    bool
	port            int
	secret, profile string
	data            []byte
	body            io.ReadCloser
	delayQuery      string
	delayBudget     time.Duration
	hooks           map[string]func(nodeQueryEffect) nodeQueryReply
	now             func() time.Time
	sleep           func(time.Duration)
	readFile        func(string) ([]byte, error)
	parse           func([]byte) []proxyNode
	controller      func(string) (map[string]any, error)
	do              func(*http.Request, time.Duration) (*http.Response, error)
}

func newAppNodeQueryEffects(a *app, q nodeQueryServiceRequest, w http.ResponseWriter) *appNodeQueryEffects {
	e := &appNodeQueryEffects{a: a, q: q, w: w, locked: q.Borrowed, now: time.Now, sleep: time.Sleep, readFile: os.ReadFile, parse: parseProfileNodes}
	e.controller = func(route string) (map[string]any, error) {
		captured := &app{ctrlPort: e.port, ctrlSecret: e.secret}
		return captured.mihomoRequest(http.MethodGet, route, nil)
	}
	e.do = func(req *http.Request, d time.Duration) (*http.Response, error) {
		c := http.Client{Timeout: d}
		return c.Do(req)
	}
	return e
}
func (e *appNodeQueryEffects) closeBody() {
	if e.body != nil {
		b := e.body
		e.body = nil
		_ = b.Close()
	}
}
func (e *appNodeQueryEffects) Release() {
	defer func() {
		if e.owns {
			e.owns = false
			e.locked = false
			e.a.mu.Unlock()
		}
	}()
	e.closeBody()
}
func (e *appNodeQueryEffects) Call(q nodeQueryEffect) (r nodeQueryReply) {
	if q.Kind == "response" {
		e.responded = true
	}
	if h := e.hooks[q.Kind]; h != nil {
		return h(q)
	}
	a := e.a
	switch q.Kind {
	case "lock":
		if e.locked {
			panic("node query cannot relock")
		}
		a.mu.Lock()
		e.locked = true
		e.owns = true
	case "unlock":
		if !e.owns {
			panic("node query cannot release borrowed lock")
		}
		e.owns = false
		e.locked = false
		a.mu.Unlock()
	case "running":
		r.Bool = a.kernelRunningLocked()
	case "endpoint":
		e.port = a.ctrlPort
		e.secret = a.ctrlSecret
	case "metadata":
		m := &nodeQueryMetadata{}
		if q.Name == "nodes" {
			m.Cached = a.cachedNodeSnapshotLocked()
			if len(m.Cached) == 0 {
				m.Cached = nil
			}
			m.Selected = a.controlMetaLocked().SelectedNode
			m.ProxyEnabled = a.proxyOn
		}
		if q.Name == "nodes" || q.Name == "list" {
			details := manualNodeDetails(a.manualSnapshotLocked())
			m.Manual = make([]proxyNode, 0, len(details))
			for _, n := range details {
				m.Manual = append(m.Manual, n)
			}
		}
		if q.Name == "list" {
			e.profile = a.profilePath()
			m.SubscriptionURL = a.controlMetaLocked().SubscriptionURL
			entries := a.catalogEntriesLocked()
			m.Sources = make([]nodeQuerySource, 0, len(entries))
			for _, s := range entries {
				m.Sources = append(m.Sources, nodeQuerySource{URL: s.URL, Label: s.Label})
			}
		}
		if q.Name == "timeout" {
			m.ConnectTimeout = time.Duration(a.healthMetaLocked().Params.ConnectTimeoutMs) * time.Millisecond
		}
		r.Metadata = m
	case "group":
		var v map[string]any
		v, r.Err = e.controller("/proxies/" + q.Name)
		r.Text, _ = v["now"].(string)
	case "read-profile":
		e.data, r.Err = e.readFile(e.profile)
	case "parse-profile":
		r.Nodes = e.parse(e.data)
	case "provider":
		var v map[string]any
		v, r.Err = e.controller("/providers/proxies/" + q.Name)
		if r.Err == nil {
			items, _ := v["proxies"].([]any)
			r.Provider = make([]nodeQueryProvider, 0, len(items))
			for _, item := range items {
				x, ok := item.(map[string]any)
				p := nodeQueryProvider{Valid: ok}
				if ok {
					p.Name, _ = x["name"].(string)
					p.Type, _ = x["type"].(string)
					p.Network, _ = x["network"].(string)
					p.Alive, _ = x["alive"].(bool)
				}
				r.Provider = append(r.Provider, p)
			}
		}
	case "delay-request":
		values := url.Values{}
		values.Set("url", q.Text)
		values.Set("timeout", strconv.FormatInt(q.Duration.Milliseconds(), 10))
		e.delayQuery = values.Encode()
		e.delayBudget = q.Duration + 12*time.Second
		endpoint := "http://127.0.0.1:" + strconv.Itoa(e.port) + "/group/SmartVPN/delay?" + values.Encode()
		var req *http.Request
		req, r.Err = http.NewRequest(http.MethodGet, endpoint, nil)
		if r.Err != nil {
			return
		}
		req.Header.Set("Authorization", "Bearer "+e.secret)
		var response *http.Response
		response, r.Err = e.do(req, q.Duration+12*time.Second)
		if r.Err == nil {
			e.body = response.Body
			r.Status = response.StatusCode
		}
	case "delay-decode":
		r.Delays = map[string]int{}
		r.Err = json.NewDecoder(io.LimitReader(e.body, 256*1024)).Decode(&r.Delays)
		if _, groupOnly := r.Delays["SmartVPN"]; r.Err == nil && len(r.Delays) == 1 && groupOnly {
			r.Delays, r.Err = e.memberDelays()
		}
	case "delay-value":
		r.Value = q.Result.Delays[q.Name]
	case "body-close":
		e.closeBody()
	case "deadline":
		r.Now = e.now().Add(q.Duration)
	case "before":
		r.Bool = e.now().Before(q.Time)
	case "sleep":
		e.sleep(q.Duration)
	case "response":
		if e.w != nil {
			v := q.Result
			if v.Status != 0 {
				http.Error(e.w, v.Message, v.Status)
			} else {
				switch q.Name {
				case "nodes":
					writeJSON(e.w, map[string]any{"nodes": v.Nodes, "proxyEnabled": v.ProxyEnabled})
				case "latency":
					writeJSON(e.w, map[string]any{"latencyMs": v.Latency, "selectedNode": v.Selected})
				case "node-latency":
					writeJSON(e.w, map[string]any{"latency": v.Delays})
				}
			}
		}
	}
	return
}
func (e *appNodeQueryEffects) memberDelays() (map[string]int, error) {
	group, err := e.controller("/proxies/SmartVPN")
	if err != nil {
		return nil, err
	}
	members, ok := group["all"].([]any)
	if !ok {
		return nil, errors.New("SmartVPN group has no member list")
	}
	delays := make(map[string]int)
	var names []string
	for _, m := range members {
		name, ok := m.(string)
		if !ok || name == "" {
			return nil, fmt.Errorf("invalid SmartVPN member %v", m)
		}
		names = append(names, name)
	}
	var workers errgroup.Group
	workers.SetLimit(8)
	var mu sync.Mutex
	for _, name := range names {
		switch name {
		case "DIRECT", "REJECT", "GLOBAL", "SmartVPN", "SmartVPNAuto", "SmartVPNFallback":
			continue
		}
		workers.Go(func() error {
			endpoint := "http://127.0.0.1:" + strconv.Itoa(e.port) + "/proxies/" + url.PathEscape(name) + "/delay?" + e.delayQuery
			req, err := http.NewRequest(http.MethodGet, endpoint, nil)
			if err != nil {
				return err
			}
			req.Header.Set("Authorization", "Bearer "+e.secret)
			res, err := e.do(req, e.delayBudget)
			if err != nil {
				return err
			}
			defer res.Body.Close()
			if res.StatusCode != 200 {
				return fmt.Errorf("delay for %q: HTTP %d", name, res.StatusCode)
			}
			var result struct{ Delay *int }
			if err := json.NewDecoder(io.LimitReader(res.Body, 16<<10)).Decode(&result); err != nil {
				return fmt.Errorf("delay for %q: %w", name, err)
			}
			if result.Delay == nil {
				return fmt.Errorf("missing delay for %q", name)
			}
			mu.Lock()
			delays[name] = *result.Delay
			mu.Unlock()
			return nil
		})
	}
	if err := workers.Wait(); err != nil {
		return nil, err
	}
	return delays, nil
}

func runAppNodeQueryService(a *app, q nodeQueryServiceRequest, w http.ResponseWriter) nodeQueryServiceResult {
	e := newAppNodeQueryEffects(a, q, w)
	return runNodeQueryServiceWithEffects(q, e, runSelectedNodeQueryService)
}

func (e *appNodeQueryEffects) complete(r nodeQueryServiceResult) {
	if e.w != nil && !e.responded && (r.Err != nil || r.Status != 0) {
		out := r
		if out.Status == 0 {
			out.Status = 500
		}
		if out.Message == "" {
			out.Message = "node query operation interrupted"
		}
		e.Call(nodeQueryEffect{Kind: "response", Name: e.q.Op, Result: out})
	}
}

func runNodeQueryServiceWithEffects(q nodeQueryServiceRequest, e *appNodeQueryEffects, run func(nodeQueryServiceRequest, nodeQueryServiceEffects) nodeQueryServiceResult) (out nodeQueryServiceResult) {
	defer func() {
		if recover() != nil {
			out = nodeQueryServiceResult{Status: 500, Message: "node query operation interrupted", Err: errors.New("node query operation interrupted")}
			e.complete(out)
		}
	}()
	defer e.Release()
	r := run(q, e)
	e.complete(r)
	return r
}
