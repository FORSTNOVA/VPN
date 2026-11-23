package main

import (
	"errors"
	"reflect"
)

type converterFlowSession interface {
	next() ([]byte, uint64, error)
	advance(uint64, []byte) error
	close()
}
type converterFlowStart func([]byte) (converterFlowSession, error)
type converterFrame struct {
	kind       string
	effect     converterServiceEffect
	errorCode  byte
	errorToken int64
}

func converterStartInput(q converterServiceRequest) ([]byte, error) {
	op, ok := map[string]byte{"singbox": 0, "parse-body": 1, "ensure-yaml": 2, "merge-sources": 3, "unique-name": 4}[q.Op]
	if !ok {
		return nil, errors.New("unsupported converter operation")
	}
	if len(q.Sources) > 100000 {
		return nil, errors.New("converter source limit")
	}
	b := []byte{op}
	b = registryInt(b, int64(q.Body))
	b = registryInt(b, int64(len(q.Sources)))
	for _, s := range q.Sources {
		b = registryInt(b, int64(s.Body))
		b = registryBytes(b, s.Label)
		b = registryBytes(b, s.Prefix)
	}
	b = registryBytes(b, q.Name)
	b = registryBool(b, q.Sources != nil)
	if len(b) > 8<<20 {
		return nil, errors.New("converter request limit")
	}
	return b, nil
}
func converterReadCount(r *quotaReader) int {
	n := r.integer()
	if n < 0 || n > 100000 {
		r.bad = true
		return 0
	}
	return int(n)
}
func converterReadNode(r *quotaReader) converterNode {
	return converterNode{Name: r.str(), Type: r.str(), Network: r.str(), SourceLabel: r.str(), TLS: serviceReadBool(r), WsHost: serviceReadBool(r), WsPath: serviceReadBool(r)}
}
func converterWriteNode(b []byte, n converterNode) []byte {
	for _, v := range []string{n.Name, n.Type, n.Network, n.SourceLabel} {
		b = registryBytes(b, v)
	}
	for _, v := range []bool{n.TLS, n.WsHost, n.WsPath} {
		b = registryBool(b, v)
	}
	return b
}
func decodeConverterFrame(b []byte) (converterFrame, error) {
	r := quotaReader{b: b}
	f := converterFrame{kind: r.str()}
	e := &f.effect
	e.Kind = f.kind
	e.Token = int(r.integer())
	e.Ref = int(r.integer())
	e.Index = int(r.integer())
	e.Key = r.str()
	e.Text = r.str()
	e.Value = r.integer()
	e.Flag = serviceReadBool(&r)
	n := converterReadCount(&r)
	e.Tokens = make([]int, n)
	for i := range e.Tokens {
		e.Tokens[i] = int(r.integer())
	}
	n = converterReadCount(&r)
	e.Fields = make([]converterAssignment, n)
	for i := range e.Fields {
		e.Fields[i] = converterAssignment{Key: r.str(), Ref: int(r.integer()), Text: r.str(), Value: r.integer(), Kind: int(r.byte())}
	}
	e.Node = converterReadNode(&r)
	f.errorCode = r.byte()
	f.errorToken = r.integer()
	tp, fp := serviceReadBool(&r), serviceReadBool(&r)
	if !tp {
		if len(e.Tokens) != 0 {
			r.bad = true
		}
		e.Tokens = nil
	}
	if !fp {
		if len(e.Fields) != 0 {
			r.bad = true
		}
		e.Fields = nil
	}
	return f, serviceWireDone(&r)
}

type converterArena struct {
	q          converterServiceRequest
	errs       map[int64]error
	errorKinds map[string]int64
}

func newConverterArena(q converterServiceRequest) *converterArena {
	return &converterArena{q: q, errs: map[int64]error{}, errorKinds: map[string]int64{}}
}
func (a *converterArena) port(f converterFrame) (converterServiceEffect, bool) {
	return f.effect, f.kind != "result" && f.kind != "done" && f.kind != ""
}
func (a *converterArena) resultMatches(f converterFrame, r converterServiceResult) bool {
	if r.Body != f.effect.Token || r.Proxies != f.effect.Ref || r.Nodes != f.effect.Index || r.Text != f.effect.Text {
		return false
	}
	if f.errorCode == 0 {
		return r.Err == nil
	}
	switch f.errorCode {
	case 2:
		return r.Err == errNoUsableNodes
	case 3:
		return r.Err != nil && r.Err.Error() == "failed to marshal converted proxies to YAML"
	case 4:
		return r.Err != nil && r.Err.Error() == "could not marshal merged proxies"
	}
	if f.errorCode != 1 {
		return false
	}
	want := a.errs[f.errorToken]
	return want != nil && r.Err != nil && reflect.TypeOf(want) == reflect.TypeOf(r.Err) && reflect.TypeOf(want).Comparable() && want == r.Err
}
func (a *converterArena) replyBytes(f converterFrame, r converterServiceReply) ([]byte, error) {
	if len(r.Fields) > 100000 || len(r.Tokens) > 100000 || len(r.Names) > 100000 {
		return nil, errors.New("converter reply count")
	}
	b := registryBool(nil, r.Err == nil)
	b = registryInt(b, int64(r.Token))
	b = registryInt(b, int64(r.Count))
	b = registryBytes(b, r.Text)
	b = registryBool(b, r.Flag)
	b = registryInt(b, int64(len(r.Tokens)))
	for _, v := range r.Tokens {
		b = registryInt(b, int64(v))
	}
	b = registryInt(b, int64(len(r.Fields)))
	for _, v := range r.Fields {
		b = registryBytes(b, v.Key)
		b = registryInt(b, int64(v.Ref))
		b = registryInt(b, int64(v.TrimRef))
		b = registryBytes(b, v.Text)
		b = registryBytes(b, v.Trim)
		b = registryBool(b, v.Empty)
		b = registryBool(b, v.Bool)
		b = registryInt(b, v.Int)
		b = registryInt(b, int64(v.Count))
		b = registryBool(b, v.TrimEmpty)
		b = registryBytes(b, v.Lower)
	}
	b = converterWriteNode(b, r.Node)
	b = registryInt(b, int64(len(r.Names)))
	for _, v := range r.Names {
		b = registryBytes(b, v)
	}
	token := int64(0)
	if r.Err != nil {
		token = a.errorKinds[f.kind]
		if token == 0 {
			if len(a.errorKinds) >= 256 {
				return nil, errors.New("converter error slots")
			}
			token = int64(len(a.errorKinds) + 1)
			a.errorKinds[f.kind] = token
		}
		a.errs[token] = r.Err
	}
	b = registryInt(b, token)
	for _, v := range []bool{r.Tokens != nil, r.Fields != nil, r.Names != nil} {
		b = registryBool(b, v)
	}
	if len(b) > 8<<20 {
		return nil, errors.New("converter reply limit")
	}
	return b, nil
}
