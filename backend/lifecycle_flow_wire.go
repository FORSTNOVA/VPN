package main

import (
	"errors"
	"reflect"
)

type lifecycleFlowSession interface {
	next() ([]byte, uint64, error)
	advance(uint64, []byte) error
	close()
}
type lifecycleFlowStart func([]byte) (lifecycleFlowSession, error)
type lifecycleFrame struct {
	kind    byte
	a, b    int64
	name    string
	indices []int64
	flags   []bool
}

func lifecycleStartInput(q lifecycleServiceRequest) ([]byte, error) {
	op, ok := map[string]byte{"new": 0, "load-settings": 1, "prepare": 2, "serve": 3, "cleanup": 4, "shutdown": 5, "identify": 6, "reapply": 7, "cache": 8, "bundle": 9, "sweep": 10}[q.Op]
	if !ok {
		return nil, errors.New("invalid lifecycle operation")
	}
	return []byte{op}, nil
}
func decodeLifecycleFrame(b []byte) (lifecycleFrame, error) {
	r := quotaReader{b: b}
	f := lifecycleFrame{kind: r.byte(), a: r.integer(), b: r.integer(), name: r.str()}
	n := r.integer()
	if n < 0 || n > 100000 {
		r.bad = true
	} else {
		for ; n > 0; n-- {
			f.indices = append(f.indices, r.integer())
		}
	}
	n = r.integer()
	if n < 0 || n > 100000 {
		r.bad = true
	} else {
		for ; n > 0; n-- {
			v := r.byte()
			if v > 1 {
				r.bad = true
			}
			f.flags = append(f.flags, v != 0)
		}
	}
	if r.bad || r.p != len(b) {
		return f, errors.New("invalid lifecycle frame")
	}
	return f, nil
}

type lifecycleArena struct {
	q     lifecycleServiceRequest
	errs  map[int64]error
	paths []string
}

func newLifecycleArena(q lifecycleServiceRequest) *lifecycleArena {
	return &lifecycleArena{q: q, errs: map[int64]error{}}
}
func (a *lifecycleArena) replyBytes(f lifecycleFrame, r lifecycleReply) ([]byte, error) {
	if len(r.Names) > 100000 || len(r.Nodes) > 100000 {
		return nil, errors.New("lifecycle count limit")
	}
	token := int64(0)
	if r.Err != nil {
		token = int64(f.kind)
		a.errs[token] = r.Err
	}
	if f.kind == 12 {
		a.paths = append([]string(nil), r.Names...)
	}
	if f.kind == 13 {
		token = -1
		for i, p := range a.paths {
			if p == r.Text {
				token = int64(i) + 1
				break
			}
		}
	}
	b := []byte{f.kind}
	b = registryBool(b, r.Err == nil)
	b = registryBool(b, r.Bool)
	b = registryInt(b, int64(r.Value))
	b = registryInt(b, token)
	name := ""
	if f.kind == 44 {
		name = r.Text
	}
	b = registryBytes(b, name)
	b = registryInt(b, int64(len(r.Names)))
	pathTokens := map[string]int64{}
	for i, p := range r.Names {
		tok, ok := pathTokens[p]
		if !ok {
			tok = int64(i) + 1
			pathTokens[p] = tok
		}
		b = registryInt(b, tok)
		b = registryBool(b, false)
	}
	b = registryInt(b, int64(len(r.Nodes)))
	for i, n := range r.Nodes {
		b = registryInt(b, int64(i)+1)
		b = registryBytes(b, n.Name)
	}
	b = registryBool(b, r.State != nil)
	if s := r.State; s != nil {
		for _, v := range []bool{s.Portable, s.Android, s.ChainsNil, s.StrictPrivacy, s.HasURL, s.SelectionEmpty, s.PathDefault, s.Store, s.ProxyOn} {
			b = registryBool(b, v)
		}
		for _, v := range []int{s.Hops, s.SocksPort, s.ConnectTimeout, s.FailureThreshold} {
			b = registryInt(b, int64(v))
		}
		b = registryBytes(b, s.Selected)
		b = registryBytes(b, s.Mode)
	}
	if len(b) > 8<<20 {
		return nil, errors.New("lifecycle frame limit")
	}
	return b, nil
}
func lifecycleMessage(code int64) string {
	switch code {
	case 0:
		return ""
	case 1:
		return "connect before enabling the Windows proxy"
	case 2:
		return "this platform has no machine proxy setting to reapply"
	case 3:
		return "could not re-enable the Windows system proxy"
	}
	return "invalid lifecycle message"
}
func (a *lifecycleArena) port(f lifecycleFrame) (lifecycleEffect, bool) {
	ks := []string{"", "init", "snapshot", "read-settings", "decode-settings", "open-secret", "set", "cache-read", "cache-decode", "cache-hash", "cache-remove", "cache-apply", "glob", "own-config", "remove-config", "bundle-source", "bundle-target-stat", "bundle-source-stat", "bundle-read", "bundle-write", "store-open", "load-params", "load-health", "load-regions", "manual", "subscriptions", "source-labels", "proxy-other", "proxy", "redactor", "scrub", "china-refresh", "listen", "listener-close", "server-new", "server-serve", "lock", "unlock", "stop-auto", "stop-kernel", "store-close", "disconnect", "signal", "running", "mode", "response", "log"}
	if f.kind == 0 || int(f.kind) >= len(ks) {
		return lifecycleEffect{}, false
	}
	e := lifecycleEffect{Kind: ks[f.kind]}
	switch f.kind {
	case 6:
		e.Name = f.name
		e.Value = int(f.a)
		e.Flag = f.b != 0
	case 11:
		e.Indices = make([]int, len(f.indices))
		for i, v := range f.indices {
			if v < 0 || v > 100000 {
				return e, false
			}
			e.Indices[i] = int(v)
		}
	case 14, 32, 33, 35:
		e.Index = int(f.a)
	case 16, 17, 18, 19:
		e.Name = f.name
	case 28:
		e.Flag = f.b != 0
	case 45:
		e.Value = int(f.a)
		e.Text = lifecycleMessage(f.b)
		e.Name = f.name
		if len(f.flags) > 0 {
			e.Flag = f.flags[0]
		}
	case 46:
		e.Index = int(f.a)
		e.Name = f.name
		if f.b != 0 {
			var ok bool
			e.Err, ok = a.errs[f.b]
			if !ok {
				return e, false
			}
		}
	}
	return e, true
}
func (a *lifecycleArena) resultMatches(f lifecycleFrame, r lifecycleServiceResult) bool {
	if len(f.indices) != 2 {
		return false
	}
	v := lifecycleServiceResult{Status: int(f.a), Port: int(f.indices[0]), Message: lifecycleMessage(f.indices[1])}
	if f.b != 0 {
		var ok bool
		v.Err, ok = a.errs[f.b]
		if !ok {
			return false
		}
	}
	if (v.Err == nil) != (r.Err == nil) {
		return false
	}
	if v.Err != nil && !errors.Is(r.Err, v.Err) {
		return false
	}
	v.Err = nil
	r.Err = nil
	return reflect.DeepEqual(v, r)
}
