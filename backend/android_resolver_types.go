package main

import (
	"errors"
	"net/http"
)

type androidResolverRequest struct {
	Op, Path string
	Body     []byte
}
type androidResolverEffect struct {
	Kind, Text, Other string
	Number            int64
}
type androidResolverReply struct {
	Text   string
	Number int64
	Flag   bool
	Names  []string
	Bytes  []byte
	Err    error
}
type androidResolverResult struct {
	Status int
	Text   string
	Data   map[string]any
	Err    error
}
type androidResolverEffects interface {
	Call(androidResolverEffect) androidResolverReply
	Release()
}
type androidResolverOracle struct{ fx androidResolverEffects }

func (a *androidResolverOracle) call(k, t, o string, n int64) androidResolverReply {
	return a.fx.Call(androidResolverEffect{Kind: k, Text: t, Other: o, Number: n})
}
func runAndroidResolverWithEffects(q androidResolverRequest, fx androidResolverEffects) (out androidResolverResult) {
	defer func() {
		if recover() != nil {
			out = androidResolverResult{Status: 500, Err: errors.New("Android resolver operation interrupted")}
		}
	}()
	defer fx.Release()
	return runSelectedAndroidResolver(q, fx)
}
func runAndroidResolverHTTPWithEffects(w http.ResponseWriter, q androidResolverRequest, fx androidResolverEffects) {
	tracked := &authorityHTTPWriter{ResponseWriter: w}
	defer fx.Release()
	defer tracked.recoverFailure()
	r := runSelectedAndroidResolver(q, fx)
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
