package main

import (
	"errors"
	"reflect"
	"time"
)

type kernelAdmissionFlowSession interface {
	next() ([]byte, uint64, error)
	advance(uint64, []byte) error
	close()
}
type kernelAdmissionFlowStart func([]byte) (kernelAdmissionFlowSession, error)
type kernelAdmissionFrame struct {
	kind       string
	effect     kernelAdmissionEffect
	errorCode  byte
	errorToken int64
	flag       bool
}

func kernelAdmissionStartInput(q kernelAdmissionRequest) ([]byte, error) {
	op, ok := map[string]byte{"path": 0, "look-for": 1, "new": 2, "running": 3, "start": 4, "watch": 5, "stop": 6, "ready": 7, "owner": 8, "privacy": 9, "check-config": 10}[q.Op]
	if !ok {
		return nil, errors.New("unsupported kernel admission operation")
	}
	b := []byte{op}
	b = registryBytes(b, q.Name)
	b = registryInt(b, int64(q.Port))
	b = registryInt(b, int64(q.Timeout))
	if len(b) > 8<<20 {
		return nil, errors.New("kernel admission request limit")
	}
	return b, nil
}
func decodeKernelAdmissionFrame(b []byte) (kernelAdmissionFrame, error) {
	r := quotaReader{b: b}
	f := kernelAdmissionFrame{kind: r.str()}
	f.effect = kernelAdmissionEffect{Kind: f.kind, Text: r.str(), Index: int(r.integer()), Duration: time.Duration(r.integer())}
	f.errorCode = r.byte()
	f.errorToken = r.integer()
	f.flag = serviceReadBool(&r)
	return f, serviceWireDone(&r)
}

type kernelAdmissionArena struct {
	q     kernelAdmissionRequest
	errs  map[int64]error
	kinds map[string]int64
}

func newKernelAdmissionArena(q kernelAdmissionRequest) *kernelAdmissionArena {
	return &kernelAdmissionArena{q: q, errs: map[int64]error{}, kinds: map[string]int64{}}
}
func (a *kernelAdmissionArena) port(f kernelAdmissionFrame) (kernelAdmissionEffect, bool) {
	return f.effect, f.kind != "" && f.kind != "result" && f.kind != "done"
}
func (a *kernelAdmissionArena) resultMatches(f kernelAdmissionFrame, r kernelAdmissionResult) bool {
	if r.Text != f.effect.Text || r.Bool != f.flag {
		return false
	}
	switch f.errorCode {
	case 0:
		return r.Err == nil
	case 2:
		return r.Err != nil && r.Err.Error() == "startup timeout"
	case 3:
		return r.Err != nil && r.Err.Error() == "no kernel to check the configuration with"
	case 1:
		want := a.errs[f.errorToken]
		return r.Err != nil && want != nil && reflect.TypeOf(r.Err) == reflect.TypeOf(want) && reflect.TypeOf(want).Comparable() && r.Err == want
	}
	return false
}
func (a *kernelAdmissionArena) replyBytes(f kernelAdmissionFrame, r kernelAdmissionReply) ([]byte, error) {
	if len(r.Names) > 100000 {
		return nil, errors.New("kernel admission count limit")
	}
	b := registryBool(nil, r.Err == nil)
	b = registryBool(b, r.Bool)
	b = registryBool(b, r.Other)
	b = registryBytes(b, r.Text)
	b = registryInt(b, int64(r.Count))
	b = registryInt(b, int64(len(r.Names)))
	for _, n := range r.Names {
		b = registryBytes(b, n)
	}
	token := int64(0)
	if r.Err != nil {
		token = a.kinds[f.kind]
		if token == 0 {
			if len(a.kinds) >= 256 {
				return nil, errors.New("kernel admission error slots")
			}
			token = int64(len(a.kinds) + 1)
			a.kinds[f.kind] = token
		}
		a.errs[token] = r.Err
	}
	b = registryInt(b, token)
	if len(b) > 8<<20 {
		return nil, errors.New("kernel admission reply limit")
	}
	return b, nil
}
