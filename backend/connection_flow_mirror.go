package main

import "sync"

type connectionMirrorEvent struct {
	effect             connectionEffect
	result             connectionServiceResult
	terminal, panicked bool
}
type connectionMirror struct {
	events       chan connectionMirrorEvent
	replies      chan connectionReply
	cancel, done chan struct{}
	once         sync.Once
}

func cloneConnectionReply(r connectionReply) connectionReply {
	if r.State != nil {
		v := *r.State
		v.CachedNodes = connectionCopyNodes(v.CachedNodes)
		r.State = &v
	}
	r.Nodes = connectionCopyNodes(r.Nodes)
	r.Strings = append([]string(nil), r.Strings...)
	r.Region.Sources = append([]string(nil), r.Region.Sources...)
	return r
}
func (m *connectionMirror) Call(q connectionEffect) connectionReply {
	q.Nodes = connectionCopyNodes(q.Nodes)
	select {
	case m.events <- connectionMirrorEvent{effect: q}:
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
func (m *connectionMirror) Release() {}
func newConnectionMirror(q connectionServiceRequest) *connectionMirror {
	m := &connectionMirror{events: make(chan connectionMirrorEvent), replies: make(chan connectionReply), cancel: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(m.done)
		defer func() {
			if p := recover(); p != nil {
				if _, ok := p.(subscriptionMirrorCancelled); ok {
					return
				}
				select {
				case m.events <- connectionMirrorEvent{terminal: true, panicked: true}:
				case <-m.cancel:
				}
			}
		}()
		r := runGoConnectionService(q, m)
		select {
		case m.events <- connectionMirrorEvent{terminal: true, result: r}:
		case <-m.cancel:
		}
	}()
	return m
}
func (m *connectionMirror) close() { m.once.Do(func() { close(m.cancel) }); <-m.done }
func (m *connectionMirror) next() (connectionMirrorEvent, bool) {
	select {
	case e := <-m.events:
		return e, true
	case <-m.done:
		return connectionMirrorEvent{}, false
	}
}
func (m *connectionMirror) resume(r connectionReply) {
	r = cloneConnectionReply(r)
	select {
	case m.replies <- r:
	case <-m.done:
	}
}
