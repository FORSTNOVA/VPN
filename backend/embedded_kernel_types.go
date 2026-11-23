package main

import (
	"errors"
	"net/http"
)

type embeddedKernelRequest struct {
	Op, Path string
	Body     []byte
}
type embeddedKernelEffect struct {
	Kind, Text, Other string
	Number            int64
}
type embeddedKernelReply struct {
	Text   string
	Number int64
	Flag   bool
	Names  []string
	Bytes  []byte
	Err    error
}
type embeddedKernelResult struct {
	Status int
	Text   string
	Data   map[string]any
	Err    error
}
type embeddedKernelEffects interface {
	Call(embeddedKernelEffect) embeddedKernelReply
	Release()
}
type embeddedKernelOracle struct{ fx embeddedKernelEffects }

func (a *embeddedKernelOracle) call(k, t, o string, n int64) embeddedKernelReply {
	return a.fx.Call(embeddedKernelEffect{Kind: k, Text: t, Other: o, Number: n})
}
func runEmbeddedKernelWithEffects(q embeddedKernelRequest, fx embeddedKernelEffects) (out embeddedKernelResult) {
	defer func() {
		if recover() != nil {
			out = embeddedKernelResult{Status: 500, Err: errors.New("embedded kernel operation interrupted")}
		}
	}()
	defer fx.Release()
	return runSelectedEmbeddedKernel(q, fx)
}
func runEmbeddedKernelHTTPWithEffects(w http.ResponseWriter, q embeddedKernelRequest, fx embeddedKernelEffects) {
	tracked := &authorityHTTPWriter{ResponseWriter: w}
	defer fx.Release()
	defer tracked.recoverFailure()
	r := runSelectedEmbeddedKernel(q, fx)
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
