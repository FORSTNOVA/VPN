package main

import "errors"

type exitFlowSession interface {
	next() ([]byte, uint64, error)
	advance(uint64, []byte) error
	close()
}
type exitFlowStart func([]byte) (exitFlowSession, error)
type exitFrame struct {
	kind byte
	text string
}

func exitStartInput(q exitServiceRequest) ([]byte, error) { return []byte{0}, nil }
func decodeExitFrame(b []byte) (exitFrame, error) {
	r := quotaReader{b: b}
	f := exitFrame{kind: r.byte(), text: r.str()}
	if r.bad || r.p != len(b) {
		return f, errors.New("invalid exit frame")
	}
	return f, nil
}
func exitReplyBytes(f exitFrame, r exitReply) ([]byte, error) {
	b := registryBool([]byte{f.kind}, r.Err == nil)
	msg := ""
	if r.Err != nil {
		msg = r.Err.Error()
	}
	b = registryBytes(b, msg)
	b = registryBool(b, r.State != nil)
	if s := r.State; s != nil {
		for _, v := range []bool{s.Matches, s.RelayPresent, s.PatrolPresent, s.ProxyOn} {
			b = registryBool(b, v)
		}
	}
	if len(b) > 8<<20 {
		return nil, errors.New("exit reply limit")
	}
	return b, nil
}
func exitPort(f exitFrame) (exitEffect, bool) {
	q := exitEffect{Kind: map[byte]string{1: "lock", 2: "close-relay", 3: "history-stop", 4: "clear-kernel", 5: "remove-config", 6: "close-patrol", 7: "clear-block", 8: "proxy-off", 9: "log", 10: "clear-proxy", 11: "drop-tunnel"}[f.kind]}
	if f.kind == 9 {
		q.Message = "could not restore system proxy after Mihomo exit: " + f.text
	}
	return q, q.Kind != ""
}
