package main

import "errors"

type labelsFlowSession interface {
	next() ([]byte, uint64, error)
	advance(uint64, []byte) error
	close()
}
type labelsFlowStart func([]byte) (labelsFlowSession, error)
type labelsFrame struct {
	kind       byte
	index      int64
	name, text string
}

func labelsStartInput(q labelsServiceRequest) ([]byte, error) {
	if q.Op != "restore" {
		return nil, errors.New("invalid labels operation")
	}
	return []byte{0}, nil
}
func decodeLabelsFrame(b []byte) (labelsFrame, error) {
	r := quotaReader{b: b}
	f := labelsFrame{kind: r.byte(), index: r.integer(), name: r.str(), text: r.str()}
	if r.bad || r.p != len(b) {
		return f, errors.New("invalid labels frame")
	}
	return f, nil
}

type labelsArena struct{}

func newLabelsArena(labelsServiceRequest) *labelsArena { return &labelsArena{} }
func (a *labelsArena) replyBytes(f labelsFrame, r labelsReply) ([]byte, error) {
	b := []byte{f.kind}
	b = registryBool(b, r.Err == nil)
	b = registryBool(b, r.Bool)
	b = registryBool(b, r.Other)
	b = registryInt(b, int64(r.Count))
	b = registryBytes(b, r.Text)
	b = registryBool(b, r.State != nil)
	if s := r.State; s != nil {
		if len(s.Sources) > 100000 || len(s.Nodes) > 100000 {
			return nil, errors.New("labels count limit")
		}
		b = registryBool(b, s.Merged)
		b = registryBool(b, s.Empty)
		b = registryBytes(b, s.DefaultLabel)
		b = registryInt(b, int64(len(s.Sources)))
		for _, v := range s.Sources {
			b = registryBool(b, v.Enabled)
			b = registryBool(b, v.Nonempty)
			b = registryBool(b, v.Matches)
			b = registryBytes(b, v.Label)
		}
		b = registryInt(b, int64(len(s.Nodes)))
		for _, v := range s.Nodes {
			b = registryBytes(b, v.Name)
			b = registryBytes(b, v.Label)
			b = registryBool(b, v.Manual)
		}
	}
	if len(b) > 8<<20 {
		return nil, errors.New("labels frame limit")
	}
	return b, nil
}
func (a *labelsArena) port(f labelsFrame) (labelsEffect, bool) {
	ks := []string{"", "mode", "single-snapshot", "set-label", "read-original", "decode-original", "cached-sources", "sources-snapshot", "rebuild", "index-original", "index-rebuilt", "decode-meta", "key-facts", "patch-key", "marshal", "write-temp", "rename", "remove-temp", "nodes-snapshot", "meta-label", "save-cache"}
	if f.kind == 0 || int(f.kind) >= len(ks) {
		return labelsEffect{}, false
	}
	e := labelsEffect{Kind: ks[f.kind]}
	switch f.kind {
	case 3:
		if f.index < 0 || f.index > 100000 {
			return e, false
		}
		e.Index = int(f.index)
		e.Text = f.text
	case 12, 13, 19:
		e.Name = f.name
	}
	return e, true
}
func (a *labelsArena) resultMatches(f labelsFrame, r labelsServiceResult) bool {
	return f.kind == 63 && r.Err == nil && f.index == 0 && f.name == "" && f.text == ""
}
