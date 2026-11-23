package main

import "sync"

type nodeQueryMirrorEvent struct {
	effect             nodeQueryEffect
	result             nodeQueryServiceResult
	terminal, panicked bool
}
type nodeQueryMirror struct {
	events       chan nodeQueryMirrorEvent
	replies      chan nodeQueryReply
	cancel, done chan struct{}
	once         sync.Once
}

func cloneNodeQueryReply(r nodeQueryReply) nodeQueryReply {
	r.Metadata = cloneNodeQueryMetadata(r.Metadata)
	if r.Nodes != nil {
		r.Nodes = append([]proxyNode{}, r.Nodes...)
	}
	if r.Provider != nil {
		r.Provider = append([]nodeQueryProvider{}, r.Provider...)
	}
	if r.Delays != nil {
		old := r.Delays
		r.Delays = make(map[string]int, len(old))
		for k, v := range old {
			r.Delays[k] = v
		}
	}
	return r
}
func (m *nodeQueryMirror) Call(q nodeQueryEffect) nodeQueryReply {

	select {
	case m.events <- nodeQueryMirrorEvent{effect: q}:
	case <-m.cancel:
		panic(subscriptionMirrorCancelled{})
	}
	select {
	case r := <-m.replies:
		return r
	case <-m.cancel:
		panic(subscriptionMirrorCancelled{})
	}
}
func (m *nodeQueryMirror) Release() {}
func newNodeQueryMirror(q nodeQueryServiceRequest) *nodeQueryMirror {
	m := &nodeQueryMirror{events: make(chan nodeQueryMirrorEvent), replies: make(chan nodeQueryReply), cancel: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(m.done)
		defer func() {
			if p := recover(); p != nil {
				if _, ok := p.(subscriptionMirrorCancelled); ok {
					return
				}
				select {
				case m.events <- nodeQueryMirrorEvent{terminal: true, panicked: true}:
				case <-m.cancel:
				}
			}
		}()
		r := runGoNodeQueryService(q, m)
		select {
		case m.events <- nodeQueryMirrorEvent{terminal: true, result: r}:
		case <-m.cancel:
		}
	}()
	return m
}
func (m *nodeQueryMirror) close() { m.once.Do(func() { close(m.cancel) }); <-m.done }
func (m *nodeQueryMirror) next() (nodeQueryMirrorEvent, bool) {
	select {
	case e := <-m.events:
		return e, true
	case <-m.done:
		return nodeQueryMirrorEvent{}, false
	}
}
func (m *nodeQueryMirror) resume(r nodeQueryReply) {
	r = cloneNodeQueryReply(r)
	select {
	case m.replies <- r:
	case <-m.done:
	}
}
