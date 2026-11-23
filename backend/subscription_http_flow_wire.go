package main

import (
	"errors"
	"reflect"
	"time"
)

type subscriptionHTTPFlowSession interface {
	next() ([]byte, uint64, error)
	advance(uint64, []byte) error
	close()
}
type subscriptionHTTPFlowStart func([]byte) (subscriptionHTTPFlowSession, error)
type subscriptionHTTPFrame struct {
	kind   byte
	effect subscriptionHTTPServiceEffect
}

func subscriptionHTTPStartInput(q subscriptionHTTPServiceRequest) ([]byte, error) {
	op, ok := map[string]byte{"fetch": 0, "refresh": 1, "diagnostics": 2, "analyze": 3, "start": 4, "stop": 5, "loop": 6, "tick": 7}[q.Op]
	if !ok {
		return nil, errors.New("unsupported subscription HTTP op")
	}
	return []byte{op}, nil
}
func subscriptionHTTPReadCount(r *quotaReader, width int64) int {
	n := r.integer()
	if n < 0 || n > 100000 || n > int64(len(r.b)-r.p)/width {
		r.bad = true
		return 0
	}
	return int(n)
}
func decodeSubscriptionHTTPFrame(b []byte) (subscriptionHTTPFrame, error) {
	r := quotaReader{b: b}
	f := subscriptionHTTPFrame{kind: r.byte()}
	e := &f.effect
	e.Text = r.str()
	e.Count = int(r.integer())
	e.Status = int(r.integer())
	e.Flag = serviceReadBool(&r)
	e.Interval = time.Duration(r.integer())
	n := subscriptionHTTPReadCount(&r, 8)
	if n > 0 {
		e.Names = make([]string, n)
	}
	for i := range e.Names {
		e.Names[i] = r.str()
	}
	a := &e.Analysis
	a.Present = serviceReadBool(&r)
	a.Format = r.str()
	a.Bytes = int(r.integer())
	a.Selectable = int(r.integer())
	a.ValidVmess = int(r.integer())
	a.InfoEntries = int(r.integer())
	a.WsHostSet = int(r.integer())
	a.WsPathSet = int(r.integer())
	read := func() []subscriptionHTTPValue {
		n := subscriptionHTTPReadCount(&r, 16)
		var v []subscriptionHTTPValue
		if n > 0 {
			v = make([]subscriptionHTTPValue, n)
		}
		for i := range v {
			v[i] = subscriptionHTTPValue{Name: r.str(), Count: int(r.integer())}
		}
		return v
	}
	a.UriCounts = read()
	a.Networks = read()
	a.TlsModes = read()
	return f, serviceWireDone(&r)
}

type subscriptionHTTPArena struct {
	q subscriptionHTTPServiceRequest
}

func newSubscriptionHTTPArena(q subscriptionHTTPServiceRequest) *subscriptionHTTPArena {
	return &subscriptionHTTPArena{q: q}
}
func (a *subscriptionHTTPArena) replyBytes(f subscriptionHTTPFrame, r subscriptionHTTPServiceReply) ([]byte, error) {
	b := []byte{f.kind}
	for _, v := range []bool{r.Err == nil, r.Flag, r.Clash, r.JSON, r.URI, r.Info, r.Host, r.Path} {
		b = registryBool(b, v)
	}
	b = registryInt(b, int64(r.Count))
	b = registryInt(b, int64(r.Other))
	for _, v := range []string{r.Text, r.Network, r.TLS} {
		b = registryBytes(b, v)
	}
	if len(r.Names) > 100000 || len(r.Nodes) > 100000 {
		return nil, errors.New("subscription HTTP reply count limit")
	}
	b = registryInt(b, int64(len(r.Names)))
	for _, v := range r.Names {
		b = registryBytes(b, v)
	}
	b = registryInt(b, int64(len(r.Nodes)))
	for _, v := range r.Nodes {
		b = registryBool(b, v.Vmess)
		b = registryBytes(b, v.Network)
		b = registryBool(b, v.TLS)
		b = registryBool(b, v.WsHost)
		b = registryBool(b, v.WsPath)
	}
	if len(b) > 8<<20 {
		return nil, errors.New("subscription HTTP reply limit")
	}
	return b, nil
}
func (a *subscriptionHTTPArena) port(f subscriptionHTTPFrame) (subscriptionHTTPServiceEffect, bool) {
	e := f.effect
	k, ok := subscriptionHTTPPortKinds[f.kind]
	e.Kind = k
	if f.kind == 13 && e.Names == nil {
		e.Names = []string{}
	}
	return e, ok
}
func (a *subscriptionHTTPArena) resultMatches(f subscriptionHTTPFrame, r subscriptionHTTPServiceResult) bool {
	return r.Err == nil && reflect.DeepEqual(r.Analysis, f.effect.Analysis)
}

var subscriptionHTTPPortKinds = map[byte]string{
	1: "lock", 2: "unlock", 3: "running", 4: "fetch-profile", 5: "format-read", 6: "provider-put", 7: "wait-nodes", 8: "list-nodes", 9: "cache-nodes", 10: "save-nodes", 11: "source-snapshot", 12: "url-parse", 13: "query-keys", 14: "request-new", 15: "request-do", 16: "body-read", 17: "body-truncate", 18: "fetch-meta", 19: "fetch-error", 20: "cache-read", 21: "source-read", 22: "diagnostic-format", 23: "payload-start", 24: "payload-text", 25: "payload-base64", 26: "uri-count", 27: "payload-nodes", 28: "vmess-matches", 29: "vmess-decode", 30: "vmess-json", 31: "payload-commit", 32: "response", 33: "body-close", 34: "cancel-present", 35: "cancel-new", 36: "spawn", 37: "cancel-close", 38: "ticker-new", 39: "wait", 40: "ticker-stop", 41: "tick-child", 42: "auto-tick"}
