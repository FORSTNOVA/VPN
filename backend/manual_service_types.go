package main

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"time"
)

type manualServiceRequest struct {
	Op, URI, Name string
	Borrowed      bool
}
type manualServiceResult struct {
	Status  int
	Message string
	Err     error
	Node    manualNode
	Nodes   []manualNode
	Running bool
}
type manualServiceState struct {
	Store       bool
	Nodes       []manualNode
	CachedNames []string
}
type manualEffect struct {
	Kind, Name, URI, Message string
	Node                     manualNode
	Nodes                    []manualNode
	Body                     []byte
	Count                    int
	Index                    int
	Indices                  []int
	Bool                     bool
}
type manualReply struct {
	State       *manualServiceState
	Err         error
	Found, Bool bool
	Node        manualNode
	Nodes       []manualNode
	Body        []byte
	Timestamp   int64
}
type manualServiceEffects interface {
	Call(manualEffect) manualReply
	Release()
}

func cloneManualValue(v any) any { return cloneManualGraph(v, make(map[manualCloneKey]any)) }

type manualCloneKey struct {
	kind             byte
	ptr              uintptr
	length, capacity int
}

func cloneManualGraph(v any, seen map[manualCloneKey]any) any {
	switch x := v.(type) {
	case map[string]any:
		if x == nil {
			return map[string]any(nil)
		}
		key := manualCloneKey{kind: 1, ptr: uintptr(reflect.ValueOf(x).UnsafePointer())}
		if old, ok := seen[key]; ok {
			return old
		}
		r := make(map[string]any, len(x))
		seen[key] = r
		for k, v := range x {
			r[k] = cloneManualGraph(v, seen)
		}
		return r
	case []any:
		if x == nil {
			return []any(nil)
		}
		key := manualCloneKey{kind: 2, ptr: reflect.ValueOf(x).Pointer(), length: len(x), capacity: cap(x)}
		if old, ok := seen[key]; ok {
			return old
		}
		r := make([]any, len(x))
		seen[key] = r
		for i, v := range x {
			r[i] = cloneManualGraph(v, seen)
		}
		return r
	case map[string]string:
		if x == nil {
			return map[string]string(nil)
		}
		r := make(map[string]string, len(x))
		for k, v := range x {
			r[k] = v
		}
		return r
	case []string:
		if x == nil {
			return []string(nil)
		}
		return append([]string{}, x...)
	default:
		return v
	}
}
func cloneManualNode(n manualNode) manualNode {
	if n.Proxy != nil {
		n.Proxy = cloneManualValue(n.Proxy).(map[string]any)
	}
	return n
}
func cloneManualNodes(ns []manualNode) []manualNode {
	if ns == nil {
		return nil
	}
	r := make([]manualNode, len(ns))
	for i, n := range ns {
		r[i] = cloneManualNode(n)
	}
	return r
}
func cloneManualServiceState(s *manualServiceState) *manualServiceState {
	if s == nil {
		return nil
	}
	r := *s
	r.Nodes = cloneManualNodes(s.Nodes)
	if s.CachedNames != nil {
		r.CachedNames = append([]string{}, s.CachedNames...)
	}
	return &r
}
func manualStateFromApp(a *app) *manualServiceState {
	s := &manualServiceState{Store: a.store != nil, Nodes: a.manualSnapshotLocked()}
	for _, n := range a.cachedNodeSnapshotLocked() {
		s.CachedNames = append(s.CachedNames, n.Name)
	}
	return s
}

type appManualEffects struct {
	a                *app
	owned, borrowed  bool
	rollbackProvider bool
	originalRecord   []manualNode
	original         []manualNode
	undoGraph        graphCommands
	originalHashes   [][32]byte
	decoded          []manualNode
	prepared, added  manualNode
	hasAdded         bool
	now              func() int64
	running          func() bool
	get              func() (string, bool, error)
	set              func(string) error
	parse            func(string) (manualNode, error)
	decode           func([]byte) ([]manualNode, error)
	encode           func([]manualNode) ([]byte, error)
	yaml             func([]manualNode) ([]byte, error)
	remove           func(string) error
	mkdir            func(string, os.FileMode) error
	write            func(string, []byte, os.FileMode) error
	rename           func(string, string) error
	logger           func(string)
}

func newAppManualEffects(a *app, borrowed bool) *appManualEffects {
	return &appManualEffects{a: a, borrowed: borrowed, now: func() int64 { return time.Now().Unix() }, running: a.kernelRunningLocked, get: func() (string, bool, error) { return a.store.getValue(manualNodesKey) }, set: func(s string) error { return a.store.setValue(manualNodesKey, s) }, parse: parseNodeURI, decode: func(b []byte) ([]manualNode, error) {
		var v manualNodeList
		err := json.Unmarshal(b, &v)
		return v.Nodes, err
	}, encode: func(_ []manualNode) ([]byte, error) {
		return json.Marshal(manualNodeList{Nodes: a.manualSnapshotLocked()})
	}, yaml: func(_ []manualNode) ([]byte, error) { return manualProviderBody(a.manualSnapshotLocked()) }, remove: os.Remove, mkdir: os.MkdirAll, write: os.WriteFile, rename: os.Rename, logger: func(s string) { log.Print(s) }}
}
func (e *appManualEffects) Release() {
	if e.undoGraph != nil {
		e.undoGraph.call(graphCommitFull, 0, nil)
		e.undoGraph = nil
	}
	if e.owned {
		e.owned = false
		e.a.mu.Unlock()
	}
}
func (e *appManualEffects) Call(q manualEffect) (r manualReply) {
	if e.rollbackProvider {
		if q.Kind == "append" || q.Kind == "remove-indices" {
			e.originalRecord = e.a.manualSnapshotLocked()
		}
		switch q.Kind {
		case "remove", "yaml", "mkdir", "write", "rename":
			defer func() {
				if r.Err == nil || (q.Kind == "remove" && errors.Is(r.Err, os.ErrNotExist)) {
					return
				}
				if e.undoGraph != nil {
					e.Call(manualEffect{Kind: "restore"})
				} else {
					e.a.replaceManualLocked(e.original)
				}
				if e.a.store != nil {
					b, err := json.Marshal(manualNodeList{Nodes: e.originalRecord})
					if err == nil {
						err = e.set(string(b))
					}
					if err != nil {
						e.logger("manual node rollback failed: " + err.Error())
					}
				}
				if q.Kind == "rename" {
					_ = os.Remove(e.a.manualPath() + ".tmp")
				}
			}()
		}
	}
	if q.Kind == "lock" {
		if e.owned || e.borrowed {
			panic("manual duplicate lock")
		}
		e.a.mu.Lock()
		e.owned = true
		e.original = e.a.manualNodes
		r.State = manualStateFromApp(e.a)
		return
	}
	// Parse and rename are also used by lockless preview; no app state is read.
	if q.Kind == "parse" {
		r.Node, r.Err = e.parse(q.URI)
		e.prepared = r.Node
		return
	}
	if q.Kind == "name" {
		r.Node = e.prepared
		if q.Index >= 0 {
			r.Node = e.decoded[q.Index]
		}
		r.Node.Name = q.Name
		if q.Bool {
			r.Node.Proxy["name"] = q.Name
		}
		e.prepared = r.Node
		return
	}
	if !e.owned && !e.borrowed {
		panic("manual effect outside lock")
	}
	switch q.Kind {
	case "snapshot":
		e.original = e.a.manualNodes
		r.State = manualStateFromApp(e.a)
	case "running":
		r.Bool = e.running()
	case "get":
		var s string
		s, r.Found, r.Err = e.get()
		r.Body = []byte(s)
	case "decode":
		r.Nodes, r.Err = e.decode(q.Body)
		e.decoded = r.Nodes
	case "encode":
		r.Body, r.Err = e.encode(e.a.manualNodes)
	case "set":
		r.Err = e.set(string(q.Body))
	case "yaml":
		r.Body, r.Err = e.yaml(e.a.manualNodes)
	case "reset":
		e.a.replaceManualLocked(nil)
	case "append":
		e.prepared.AddedAt = q.Node.AddedAt
		e.a.appendManualLocked(e.prepared)
		e.added = e.prepared
		e.hasAdded = true
	case "remove-indices":
		kept := make([]manualNode, 0, len(e.original))
		for _, i := range q.Indices {
			kept = append(kept, e.original[i])
		}
		if g := e.a.graphLocked(); g != nil {
			var indices []byte
			for _, idx := range q.Indices {
				indices = registryInt(indices, int64(idx))
			}
			if supportsFullManual(g) {
				g.call(graphKeepFull, 0, indices)
				e.undoGraph = g
				e.originalHashes = e.a.manualRecordHashes
				hashes := make([][32]byte, len(q.Indices))
				for i, idx := range q.Indices {
					hashes[i] = e.a.manualRecordHashes[idx]
				}
				e.a.manualRecordHashes = hashes
			} else {
				g.call(graphKeepManual, 0, indices)
			}
		}
		e.a.manualNodes = kept
	case "restore":
		if e.undoGraph != nil {
			e.undoGraph.call(graphRestoreFull, 0, nil)
			e.undoGraph = nil
			e.a.manualNodes, e.a.manualRecordHashes = e.original, e.originalHashes
		} else {
			e.a.replaceManualLocked(e.original)
		}
	case "truncate":
		e.a.truncateManualLocked(q.Count)
	case "now":
		r.Timestamp = e.now()
	case "remove":
		r.Err = e.remove(e.a.manualPath())
	case "mkdir":
		r.Err = e.mkdir(filepath.Dir(e.a.manualPath()), 0700)
	case "write":
		r.Err = e.write(e.a.manualPath()+".tmp", q.Body, 0600)
	case "rename":
		r.Err = e.rename(e.a.manualPath()+".tmp", e.a.manualPath())
	case "log":
		e.logger(q.Message)
	default:
		panic("unknown manual effect: " + q.Kind)
	}
	return
}
func (a *app) runManualService(q manualServiceRequest) manualServiceResult {
	fx := newAppManualEffects(a, q.Borrowed)
	fx.rollbackProvider = q.Op == "add" || q.Op == "delete"
	defer fx.Release()
	r := runSelectedManualService(q, fx)
	if q.Op == "add" && r.Err == nil && fx.hasAdded {
		r.Node = fx.added
	}
	return r
}

// FinalizeManualResult projects the invocation-owned real node, not a mirror copy.
// It never applies a snapshot back to the app.
func (e *appManualEffects) FinalizeManualResult(q manualServiceRequest, r manualServiceResult) manualServiceResult {
	if q.Op == "add" && r.Err == nil && e.hasAdded {
		r.Node = e.added
	}
	return r
}
