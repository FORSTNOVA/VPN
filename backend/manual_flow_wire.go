package main

import (
	"errors"
	"math"
	"os"
	"reflect"
	"strings"
)

type manualFlowSession interface {
	next() ([]byte, uint64, error)
	advance(uint64, []byte) error
	close()
}
type manualFlowStart func([]byte) (manualFlowSession, error)
type manualFrame struct {
	kind       byte
	a, b       int64
	data, text string
	indices    []int64
}

func manualStartInput(q manualServiceRequest) ([]byte, error) {
	op, ok := map[string]byte{"load": 0, "save": 1, "provider": 2, "list": 3, "preview": 4, "add": 5, "delete": 6}[q.Op]
	if !ok {
		return nil, errors.New("unknown manual op")
	}
	name := q.Name
	if q.Op == "add" || q.Op == "preview" || (q.Op == "delete" && !q.Borrowed) {
		name = strings.TrimSpace(name)
	}
	b := registryBool([]byte{op}, q.Borrowed)
	return registryBytes(b, name), nil
}
func decodeManualFrame(b []byte) (manualFrame, error) {
	r := quotaReader{b: b}
	f := manualFrame{kind: r.byte(), a: r.integer(), b: r.integer(), data: r.str(), text: r.str()}
	n := r.integer()
	if n < 0 || n > 100000 || n > int64((len(b)-r.p)/8) {
		r.bad = true
	} else {
		f.indices = make([]int64, n)
		for i := range f.indices {
			f.indices[i] = r.integer()
		}
	}
	if r.bad || r.p != len(b) {
		return f, errors.New("invalid manual frame")
	}
	return f, nil
}

type manualArena struct {
	q             manualServiceRequest
	next          int64
	nodes         map[int64]manualNode
	origins       map[int64]int
	bodies        map[int64][]byte
	errs          map[int64]error
	backups       map[int64][]manualNode
	current       []manualNode
	pendingBackup int64
}

func newManualArena(q manualServiceRequest) *manualArena {
	return &manualArena{q: q, nodes: map[int64]manualNode{}, origins: map[int64]int{}, bodies: map[int64][]byte{}, errs: map[int64]error{}, backups: map[int64][]manualNode{}}
}
func (a *manualArena) id() int64 { a.next++; return a.next }
func (a *manualArena) addNode(n manualNode) int64 {
	id := a.id()
	a.nodes[id] = cloneManualNode(n)
	a.origins[id] = -1
	return id
}
func (a *manualArena) nodeBytes(b []byte, id int64) []byte {
	n := a.nodes[id]
	b = registryInt(b, id)
	b = registryBytes(b, n.Name)
	b = registryBool(b, n.URI != "")
	return registryInt(b, int64(len(n.Proxy)))
}
func (a *manualArena) replyBytes(f manualFrame, r manualReply) ([]byte, error) {
	token, nodeid := int64(0), int64(0)
	msg := ""
	flag := r.Bool
	if r.Err != nil {
		token = a.id()
		a.errs[token] = r.Err
		msg = "opaque manual error"
	}
	if f.kind == 12 {
		flag = r.Found
	}
	if f.kind == 19 {
		flag = errors.Is(r.Err, os.ErrNotExist)
	}
	switch f.kind {
	case 4:
		if r.Err == nil {
			nodeid = a.addNode(r.Node)
		}
	case 5:
		a.nodes[f.a] = cloneManualNode(r.Node)
		nodeid = f.a
	case 9:
		token = a.pendingBackup
	case 12, 15, 17:
		if r.Err == nil {
			token = a.id()
			if r.Body != nil {
				a.bodies[token] = append([]byte{}, r.Body...)
			} else {
				a.bodies[token] = nil
			}
		}
	}
	b := registryBool([]byte{f.kind}, r.Err == nil)
	b = registryBool(b, flag)
	b = registryInt(b, token)
	b = registryInt(b, r.Timestamp)
	b = registryBytes(b, msg)
	b = a.nodeBytes(b, nodeid)
	b = registryInt(b, int64(len(r.Nodes)))
	for i, n := range r.Nodes {
		id := a.addNode(n)
		a.origins[id] = i
		b = a.nodeBytes(b, id)
	}
	b = registryBool(b, r.State != nil)
	if s := r.State; s != nil {
		a.current = cloneManualNodes(s.Nodes)
		b = registryBool(b, s.Store)
		b = registryInt(b, int64(len(s.Nodes)))
		for _, n := range s.Nodes {
			b = a.nodeBytes(b, a.addNode(n))
		}
		b = profileStrings(b, s.CachedNames)
	}
	if len(b) > 8<<20 {
		return nil, errors.New("manual reply limit")
	}
	return b, nil
}
func (a *manualArena) port(f manualFrame) (manualEffect, bool) {
	q := manualEffect{}
	switch f.kind {
	case 1:
		q.Kind = "lock"
	case 2:
		q.Kind = "snapshot"
	case 3:
		q.Kind = "running"
	case 4:
		q.Kind = "parse"
		q.URI = a.q.URI
	case 5:
		q.Kind = "name"
		var ok bool
		q.Node, ok = a.nodes[f.a]
		if !ok {
			return q, false
		}
		q.Name = f.data
		q.Bool = f.b != 0
		q.Index = a.origins[f.a]
	case 6:
		q.Kind = "now"
	case 7:
		n, ok := a.nodes[f.a]
		if !ok || len(f.data) != 1 {
			return q, false
		}
		if f.data[0] == 1 {
			n.AddedAt = f.b
			a.nodes[f.a] = n
		} else if f.data[0] != 0 {
			return q, false
		}
		q.Kind = "append"
		q.Node = n
		a.current = append(a.current, n)
	case 8:
		if len(a.current) == 0 {
			return q, false
		}
		a.current = a.current[:len(a.current)-1]
		q.Kind = "truncate"
		q.Count = len(a.current)
	case 9:
		kept := make([]manualNode, 0, len(f.indices))
		seen := map[int64]bool{}
		for _, i := range f.indices {
			if i < 0 || i >= int64(len(a.current)) || seen[i] {
				return q, false
			}
			seen[i] = true
			kept = append(kept, a.current[i])
		}
		a.pendingBackup = a.id()
		a.backups[a.pendingBackup] = a.current
		a.current = kept
		q.Kind = "remove-indices"
		q.Nodes = kept
		q.Indices = make([]int, len(f.indices))
		for i, v := range f.indices {
			q.Indices[i] = int(v)
		}
	case 10:
		ns, ok := a.backups[f.a]
		if !ok {
			return q, false
		}
		a.current = ns
		q.Kind = "restore"
	case 11:
		q.Kind = "reset"
		a.current = nil
	case 12:
		q.Kind = "get"
	case 13:
		q.Kind = "decode"
		var ok bool
		q.Body, ok = a.bodies[f.a]
		if !ok {
			return q, false
		}
	case 14:
		q.Kind = "log"
		q.Message = "stored manual nodes were unreadable and are ignored"
	case 15:
		q.Kind = "encode"
		q.Nodes = a.current
	case 16:
		q.Kind = "set"
		var ok bool
		q.Body, ok = a.bodies[f.a]
		if !ok {
			return q, false
		}
	case 17:
		q.Kind = "yaml"
		q.Nodes = a.current
	case 18:
		q.Kind = "mkdir"
		if f.a != 0700 {
			return q, false
		}
	case 19:
		q.Kind = "remove"
	case 20:
		q.Kind = "write"
		if f.b != 0600 {
			return q, false
		}
		var ok bool
		q.Body, ok = a.bodies[f.a]
		if !ok {
			return q, false
		}
	case 21:
		q.Kind = "rename"
	default:
		return q, false
	}
	return q, true
}
func (a *manualArena) resultMatches(f manualFrame, r manualServiceResult) bool {
	if int64(r.Status) != f.a {
		return false
	}
	if f.b == 1 {
		if len(f.indices) != 1 || r.Err == nil {
			return false
		}
		e := a.errs[f.indices[0]]
		return e != nil && errors.Is(r.Err, e) && f.text == "opaque manual error" && (r.Message == "" || r.Message == r.Err.Error())
	}
	if f.b == 0 {
		return r.Err == nil && r.Message == ""
	}
	if f.b == 11 {
		if len(f.data) != 1 || f.data[0] > 1 {
			return false
		}
		ns := make([]manualNode, len(f.indices))
		if len(f.indices) == 0 && a.current == nil {
			ns = nil
		}
		for i, id := range f.indices {
			n, ok := a.nodes[id]
			if !ok {
				return false
			}
			ns[i] = n
		}
		return r.Err == nil && r.Message == "" && r.Running == (f.data[0] == 1) && manualOpaqueEqual(ns, r.Nodes)
	}
	if f.b == 12 {
		if len(f.indices) != 1 {
			return false
		}
		n, ok := a.nodes[f.indices[0]]
		return ok && r.Err == nil && r.Message == "" && manualOpaqueEqual(n, r.Node)
	}
	msg := map[int64]string{2: manualRunningMessage(), 3: "missing node name", 4: manualDuplicateMessage0(f.data), 5: manualDuplicateMessage1(f.data), 6: manualErrorMessage0(), 7: manualErrorMessage1(), 8: manualErrorMessage2(), 9: manualErrorMessage3(), 10: manualErrorMessage4()}[f.b]
	return msg != "" && r.Err != nil && r.Err.Error() == msg && r.Message == msg
}

// NaN and cyclic JSON-style graphs retain exact values. Non-nil functions are
// deliberately not equated by code pointer; the same Go continuation handles them.
func manualOpaqueEqual(a, b any) bool {
	type pair struct {
		a, b uintptr
		t    reflect.Type
		n    int
	}
	seen := map[pair]bool{}
	var eq func(reflect.Value, reflect.Value) bool
	eq = func(x, y reflect.Value) bool {
		if !x.IsValid() || !y.IsValid() {
			return x.IsValid() == y.IsValid()
		}
		if x.Type() != y.Type() {
			return false
		}
		switch x.Kind() {
		case reflect.Interface:
			if x.IsNil() || y.IsNil() {
				return x.IsNil() == y.IsNil()
			}
			return eq(x.Elem(), y.Elem())
		case reflect.Map:
			if x.IsNil() || y.IsNil() {
				return x.IsNil() == y.IsNil()
			}
			if x.Len() != y.Len() {
				return false
			}
			p := pair{uintptr(x.UnsafePointer()), uintptr(y.UnsafePointer()), x.Type(), 0}
			if seen[p] {
				return true
			}
			seen[p] = true
			it := x.MapRange()
			for it.Next() {
				v := y.MapIndex(it.Key())
				if !v.IsValid() || !eq(it.Value(), v) {
					return false
				}
			}
			return true
		case reflect.Slice:
			if x.IsNil() || y.IsNil() {
				return x.IsNil() == y.IsNil()
			}
			if x.Len() != y.Len() {
				return false
			}
			p := pair{x.Pointer(), y.Pointer(), x.Type(), x.Len()}
			if seen[p] {
				return true
			}
			seen[p] = true
			for i := 0; i < x.Len(); i++ {
				if !eq(x.Index(i), y.Index(i)) {
					return false
				}
			}
			return true
		case reflect.Struct:
			for i := 0; i < x.NumField(); i++ {
				if !eq(x.Field(i), y.Field(i)) {
					return false
				}
			}
			return true
		case reflect.Array:
			for i := 0; i < x.Len(); i++ {
				if !eq(x.Index(i), y.Index(i)) {
					return false
				}
			}
			return true
		case reflect.Float64:
			return math.Float64bits(x.Float()) == math.Float64bits(y.Float())
		case reflect.Float32:
			return math.Float32bits(float32(x.Float())) == math.Float32bits(float32(y.Float()))
		case reflect.Func:
			return x.IsNil() && y.IsNil()
		default:
			if x.CanInterface() && y.CanInterface() {
				return reflect.DeepEqual(x.Interface(), y.Interface())
			}
			return false
		}
	}
	return eq(reflect.ValueOf(a), reflect.ValueOf(b))
}
