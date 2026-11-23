package main

import (
	"errors"
	"net/http"
)

type privacyLogRequest struct {
	Op, Path string
	Body     []byte
}
type privacyLogEffect struct {
	Kind, Text, Other string
	Number            int64
}
type privacyLogReply struct {
	Text   string
	Number int64
	Flag   bool
	Names  []string
	Bytes  []byte
	Err    error
}
type privacyLogResult struct {
	Status int
	Text   string
	Data   map[string]any
	Err    error
}
type privacyLogEffects interface {
	Call(privacyLogEffect) privacyLogReply
	Release()
}
type privacyLogOracle struct{ fx privacyLogEffects }

func (a *privacyLogOracle) call(k, t, o string, n int64) privacyLogReply {
	return a.fx.Call(privacyLogEffect{Kind: k, Text: t, Other: o, Number: n})
}
func runPrivacyLogWithEffects(q privacyLogRequest, fx privacyLogEffects) (out privacyLogResult) {
	defer func() {
		if recover() != nil {
			out = privacyLogResult{Status: 500, Err: errors.New("privacy log operation interrupted")}
		}
	}()
	defer fx.Release()
	return runSelectedPrivacyLog(q, fx)
}
func runPrivacyLogHTTPWithEffects(w http.ResponseWriter, q privacyLogRequest, fx privacyLogEffects) {
	tracked := &authorityHTTPWriter{ResponseWriter: w}
	defer fx.Release()
	defer tracked.recoverFailure()
	r := runSelectedPrivacyLog(q, fx)
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
