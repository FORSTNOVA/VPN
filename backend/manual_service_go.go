package main

import (
	"errors"
	"os"
	"strings"
)

func runGoManualService(q manualServiceRequest, fx manualServiceEffects) manualServiceResult {
	defer fx.Release()
	call := fx.Call
	fail := func(err error) manualServiceResult {
		return manualServiceResult{Status: 400, Message: err.Error(), Err: err}
	}
	if q.Op == "preview" {
		r := call(manualEffect{Kind: "parse", URI: q.URI})
		if r.Err != nil {
			return fail(r.Err)
		}
		n := r.Node
		if name := strings.TrimSpace(q.Name); name != "" {
			n = call(manualEffect{Kind: "name", Index: -1, Node: n, Name: name}).Node
		}
		return manualServiceResult{Status: 200, Node: n}
	}
	// HTTP delete's empty-name validation occurs before acquiring the mutex.
	if q.Op == "delete" && !q.Borrowed {
		q.Name = strings.TrimSpace(q.Name)
		if q.Name == "" {
			return fail(errors.New("missing node name"))
		}
	}
	kind := "lock"
	if q.Borrowed {
		kind = "snapshot"
	}
	s := call(manualEffect{Kind: kind}).State
	if (q.Op == "add" || q.Op == "delete") && !q.Borrowed && call(manualEffect{Kind: "running"}).Bool {
		return manualServiceResult{Status: 409, Message: manualRunningMessage(), Err: errors.New(manualRunningMessage())}
	}
	save := func(nodes []manualNode) error {
		if !s.Store {
			return nil
		}
		r := call(manualEffect{Kind: "encode", Nodes: nodes})
		if r.Err != nil {
			return r.Err
		}
		return call(manualEffect{Kind: "set", Body: r.Body}).Err
	}
	provider := func(nodes []manualNode) error {
		if len(nodes) == 0 {
			err := call(manualEffect{Kind: "remove"}).Err
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		r := call(manualEffect{Kind: "yaml", Nodes: nodes})
		if r.Err != nil {
			return r.Err
		}
		if err := call(manualEffect{Kind: "mkdir"}).Err; err != nil {
			return err
		}
		if err := call(manualEffect{Kind: "write", Body: r.Body}).Err; err != nil {
			return err
		}
		return call(manualEffect{Kind: "rename"}).Err
	}
	switch q.Op {
	case "load":
		call(manualEffect{Kind: "reset"})
		if !s.Store {
			return manualServiceResult{}
		}
		r := call(manualEffect{Kind: "get"})
		if r.Err != nil || !r.Found {
			return manualServiceResult{}
		}
		r = call(manualEffect{Kind: "decode", Body: r.Body})
		if r.Err != nil {
			call(manualEffect{Kind: "log", Message: "stored manual nodes were unreadable and are ignored"})
			return manualServiceResult{}
		}
		for i, n := range r.Nodes {
			if n.Name == "" || n.URI == "" || len(n.Proxy) == 0 {
				continue
			}
			n = call(manualEffect{Kind: "name", Index: i, Node: n, Name: n.Name, Bool: true}).Node
			call(manualEffect{Kind: "append", Node: n})
		}
		return manualServiceResult{}
	case "save":
		return manualServiceResult{Err: save(s.Nodes)}
	case "provider":
		return manualServiceResult{Err: provider(s.Nodes)}
	case "list":
		return manualServiceResult{Status: 200, Nodes: s.Nodes, Running: call(manualEffect{Kind: "running"}).Bool}
	case "add":
		r := call(manualEffect{Kind: "parse", URI: q.URI})
		if r.Err != nil {
			return fail(r.Err)
		}
		n := r.Node
		if name := strings.TrimSpace(q.Name); name != "" {
			n = call(manualEffect{Kind: "name", Index: -1, Node: n, Name: name, Bool: true}).Node
		}
		for _, v := range s.Nodes {
			if v.Name == n.Name {
				return fail(errors.New(manualDuplicateMessage0(n.Name)))
			}
		}
		for _, v := range s.CachedNames {
			if v == n.Name {
				return fail(errors.New(manualDuplicateMessage1(n.Name)))
			}
		}
		n.AddedAt = call(manualEffect{Kind: "now"}).Timestamp
		call(manualEffect{Kind: "append", Node: n})
		nodes := append(append([]manualNode(nil), s.Nodes...), n)
		if save(nodes) != nil {
			call(manualEffect{Kind: "truncate", Count: len(s.Nodes)})
			return fail(errors.New(manualErrorMessage0()))
		}
		if provider(nodes) != nil {
			return fail(errors.New(manualErrorMessage1()))
		}
		return manualServiceResult{Status: 200, Node: n}
	case "delete":
		kept := make([]manualNode, 0, len(s.Nodes))
		found := false
		indices := make([]int, 0, len(s.Nodes))
		for i, n := range s.Nodes {
			if n.Name == q.Name {
				found = true
				continue
			}
			kept = append(kept, n)
			indices = append(indices, i)
		}
		if !found {
			return fail(errors.New(manualErrorMessage2()))
		}
		call(manualEffect{Kind: "remove-indices", Indices: indices, Nodes: kept})
		if save(kept) != nil {
			call(manualEffect{Kind: "restore"})
			return fail(errors.New(manualErrorMessage3()))
		}
		if provider(kept) != nil {
			return fail(errors.New(manualErrorMessage4()))
		}
		return manualServiceResult{Status: 200}
	default:
		return fail(errors.New("unknown manual service operation"))
	}
}
