package main

import (
	"errors"
	"gopkg.in/yaml.v3"
	"maps"
	"os"
	"sort"
	"time"
)

type chainServiceRequest struct {
	Op             string
	Next           proxyChain
	Name, Original string
	Hops           []string
	Profile        []byte
	Manual         []manualNode
	Index          map[string][]map[string]any
}
type chainServiceResult struct {
	Status  int
	Message string
	Err     error
	Next    proxyChain
	Proxies []map[string]any
	Body    []byte
}
type chainServiceState struct {
	Subscription bool
	Chain        proxyChain
	Chains       map[string][]string
	LockedRegion string
}
type chainProxyEntry struct {
	Key   string
	Proxy map[string]any
}
type chainEffect struct {
	Kind, Name, Previous string
	Index                int
	Bool                 bool
	Body                 []byte
	Proxies              []map[string]any
	Next                 proxyChain
}
type chainReply struct {
	State    *chainServiceState
	Err      error
	Bool     bool
	Body     []byte
	Entries  []chainProxyEntry
	Proxy    map[string]any
	Record   regionRecord
	Age, TTL time.Duration
}
type chainServiceEffects interface {
	Call(chainEffect) chainReply
	Release()
}

func cloneChainServiceState(s *chainServiceState) *chainServiceState {
	if s == nil {
		return nil
	}
	v := *s
	v.Chain.Hops = appendPreserveChainSlice(s.Chain.Hops)
	if s.Chains != nil {
		v.Chains = make(map[string][]string, len(s.Chains))
		for k, h := range s.Chains {
			v.Chains[k] = appendPreserveChainSlice(h)
		}
	}
	return &v
}
func appendPreserveChainSlice(s []string) []string {
	if s == nil {
		return nil
	}
	return append([]string{}, s...)
}
func chainStateFromApp(a *app) *chainServiceState {
	graph := a.chainSnapshotLocked()
	return &chainServiceState{Subscription: a.controlMetaLocked().SubscriptionURL != "", Chain: graph.Chain, Chains: graph.Chains, LockedRegion: a.controlMetaLocked().RuntimeLockedRegion}
}

type appChainEffects struct {
	a               *app
	q               chainServiceRequest
	owned, borrowed bool
	transaction     graphCommands
	rolledBack      bool
	oldChain        proxyChain
	oldProfiles     map[string][]string
	entries         []chainProxyEntry
	compiled        []map[string]any
	output          []byte
	running, job    func() bool
	read            func() ([]byte, error)
	decode          func([]byte) ([]map[string]any, error)
	encode          func([]map[string]any) ([]byte, error)
	save            func() error
	now             func() time.Time
}

func newAppChainEffects(a *app, q chainServiceRequest) *appChainEffects {
	if q.Op == "save-profile" {
		q.Name = chainTrimName(q.Name)
	}
	e := &appChainEffects{a: a, q: q, borrowed: q.Op == "build", now: time.Now, decode: profileIndexRows, encode: func(ps []map[string]any) ([]byte, error) { return yaml.Marshal(map[string]any{"proxies": ps}) }}
	if a != nil {
		e.running = a.kernelRunningLocked
		e.job = a.regionJobRunningLocked
		e.read = func() ([]byte, error) { return os.ReadFile(a.profilePath()) }
		e.save = a.saveSettings
	}
	return e
}
func (e *appChainEffects) beginEdit() {
	if e.transaction != nil {
		return
	}
	if g := e.a.graphLocked(); supportsChainTransactions(g) {
		g.call(graphBeginChains, 0, nil)
		e.transaction = g
	}
}
func (e *appChainEffects) finishEdit(commit bool) {
	if g := e.transaction; g != nil {
		if commit {
			g.call(graphCommitChains, 0, nil)
		} else {
			g.call(graphRollbackChains, 0, nil)
			e.a.rememberChains(chainGraphSettings{e.oldChain, e.oldProfiles})
			e.rolledBack = true
		}
		e.transaction = nil
	}
}
func (e *appChainEffects) Release() {
	e.finishEdit(false)
	if e.owned {
		e.owned = false
		e.a.mu.Unlock()
	}
}
func (e *appChainEffects) Call(q chainEffect) (r chainReply) {
	if q.Kind == "lock" {
		if e.owned || e.borrowed {
			panic("chain duplicate lock")
		}
		e.a.mu.Lock()
		e.owned = true
	}
	if e.a != nil && !e.owned && !e.borrowed {
		panic("chain effect outside lock")
	}
	switch q.Kind {
	case "lock", "snapshot":
		if e.a != nil {
			s := e.a.borrowChainsLocked()
			e.oldChain, e.oldProfiles = s.Chain, s.Chains
			r.State = chainStateFromApp(e.a)
		} else {
			r.State = &chainServiceState{}
		}
	case "running":
		r.Bool = e.running()
	case "job":
		r.Bool = e.job()
	case "read":
		r.Body, r.Err = e.read()
	case "decode":
		var proxies []map[string]any
		proxies, r.Err = e.decode(q.Body)
		if r.Err != nil {
			return
		}
		e.entries = nil
		e.compiled = nil
		manual := e.q.Manual
		if e.a != nil {
			manual = e.a.manualSnapshotLocked()
		}
		for _, m := range manual {
			proxies = append(proxies, m.Proxy)
		}
		for _, p := range proxies {
			e.entries = append(e.entries, chainProxyEntry{Key: stringValue(p["name"]), Proxy: p})
		}
		r.Entries = e.entries
	case "index":
		e.entries = nil
		e.compiled = nil
		keys := make([]string, 0, len(e.q.Index))
		for k := range e.q.Index {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			for _, p := range e.q.Index[k] {
				e.entries = append(e.entries, chainProxyEntry{Key: k, Proxy: p})
			}
		}
		r.Entries = e.entries
	case "clone":
		p := maps.Clone(e.entries[q.Index].Proxy)
		p["name"] = q.Name
		if q.Bool {
			p["dialer-proxy"] = q.Previous
		}
		e.compiled = append(e.compiled, p)
		r.Proxy = p
	case "region":
		r.Record = e.a.regionValueLocked(q.Name)
		r.Record.Sources = append([]string(nil), r.Record.Sources...)
	case "age":
		r.Age = e.now().Sub(e.a.regionValueLocked(q.Name).VerifiedAt)
		r.TTL = regionTTL
	case "yaml":
		r.Body, r.Err = e.encode(e.compiled)
		if r.Err == nil {
			e.output = r.Body
		}
	case "commit-chain":
		e.beginEdit()
		if e.q.Op == "update" {
			e.a.setActiveChainLocked(e.q.Next)
		} else {
			e.a.setActiveChainLocked(proxyChain{Name: e.q.Name, Enabled: true, Hops: e.q.Hops})
		}
	case "commit-profiles":
		e.beginEdit()
		e.a.setChainProfileLocked(e.oldProfiles, e.q.Original, e.q.Name, e.q.Hops)
	case "restore-chain":
		if e.rolledBack {
			break
		}
		e.a.setActiveChainLocked(e.oldChain)
	case "restore-profiles":
		if e.rolledBack {
			break
		}
		s := e.a.settings
		s.Chains = e.oldProfiles
		e.a.replaceChainsLocked(s)
	case "save":
		func() {
			defer func() {
				if recover() != nil {
					r.Err = errors.New("chain settings save interrupted")
				}
			}()
			r.Err = e.save()
		}()
		e.finishEdit(r.Err == nil)
	default:
		panic("unknown chain effect: " + q.Kind)
	}
	return
}
func (e *appChainEffects) FinalizeChainResult(q chainServiceRequest, r chainServiceResult) chainServiceResult {
	if r.Err == nil {
		if q.Op == "compile" || q.Op == "prefix" || q.Op == "chain" {
			r.Proxies = e.compiled
		}
		if q.Op == "build" {
			r.Body = e.output
		}
		if q.Op == "update" {
			r.Next = e.q.Next
		}
	}
	return r
}
func (a *app) runChainService(q chainServiceRequest) (out chainServiceResult) {
	defer func() {
		if recover() != nil {
			out = chainServiceResult{Status: 500, Message: "chain operation interrupted", Err: errors.New("chain operation interrupted")}
		}
	}()
	if q.Op == "save-profile" {
		q.Name = chainTrimName(q.Name)
	}
	e := newAppChainEffects(a, q)
	defer e.Release()
	return e.FinalizeChainResult(q, runSelectedChainService(q, e))
}
