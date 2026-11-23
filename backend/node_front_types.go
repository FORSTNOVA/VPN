package main

import (
	"errors"
	"net/http"
)

type nodeFrontRequest struct {
	Op, Path string
	Body     []byte
}
type nodeFrontEffect struct {
	Kind, Text, Other string
	Number            int64
}
type nodeFrontReply struct {
	Text   string
	Number int64
	Flag   bool
	Names  []string
	Bytes  []byte
	Err    error
}
type nodeFrontResult struct {
	Status int
	Text   string
	Data   map[string]any
	Err    error
}
type nodeFrontEffects interface {
	Call(nodeFrontEffect) nodeFrontReply
	Release()
}
type nodeFrontOracle struct{ fx nodeFrontEffects }

func (a *nodeFrontOracle) call(k, t, o string, n int64) nodeFrontReply {
	return a.fx.Call(nodeFrontEffect{Kind: k, Text: t, Other: o, Number: n})
}
func runNodeFrontWithEffects(q nodeFrontRequest, fx nodeFrontEffects) (out nodeFrontResult) {
	defer func() {
		if recover() != nil {
			out = nodeFrontResult{Status: 500, Err: errors.New("node frontend operation interrupted")}
		}
	}()
	defer fx.Release()
	return runSelectedNodeFront(q, fx)
}
func runNodeFrontHTTPWithEffects(w http.ResponseWriter, q nodeFrontRequest, fx nodeFrontEffects) {
	tracked := &authorityHTTPWriter{ResponseWriter: w}
	defer fx.Release()
	defer tracked.recoverFailure()
	r := runSelectedNodeFront(q, fx)
	if r.Err != nil {
		status := r.Status
		if status < 400 {
			status = 500
		}
		http.Error(tracked, r.Err.Error(), status)
		return
	}
	writeJSON(tracked, r.Data)
}
