package main

import "sync"

type manualMirrorEvent struct {
	effect             manualEffect
	result             manualServiceResult
	terminal, panicked bool
}
type manualMirror struct {
	events       chan manualMirrorEvent
	replies      chan manualReply
	cancel, done chan struct{}
	once         sync.Once
}

func cloneManualReply(r manualReply) manualReply {
	r.State = cloneManualServiceState(r.State)
	r.Node = cloneManualNode(r.Node)
	r.Nodes = cloneManualNodes(r.Nodes)
	if r.Body != nil {
		r.Body = append([]byte{}, r.Body...)
	}
	return r
}
func (m *manualMirror) Call(q manualEffect) manualReply {
	if q.Indices != nil {
		q.Indices = append([]int{}, q.Indices...)
	}
	q.Node = cloneManualNode(q.Node)
	q.Nodes = cloneManualNodes(q.Nodes)
	if q.Body != nil {
		q.Body = append([]byte{}, q.Body...)
	}

	select {
	case m.events <- manualMirrorEvent{effect: q}:
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
func (m *manualMirror) Release() {}
func newManualMirror(q manualServiceRequest) *manualMirror {
	m := &manualMirror{events: make(chan manualMirrorEvent), replies: make(chan manualReply), cancel: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(m.done)
		defer func() {
			if p := recover(); p != nil {
				if _, ok := p.(subscriptionMirrorCancelled); ok {
					return
				}
				select {
				case m.events <- manualMirrorEvent{terminal: true, panicked: true}:
				case <-m.cancel:
				}
			}
		}()
		r := runGoManualService(q, m)
		select {
		case m.events <- manualMirrorEvent{terminal: true, result: r}:
		case <-m.cancel:
		}
	}()
	return m
}
func (m *manualMirror) close() { m.once.Do(func() { close(m.cancel) }); <-m.done }
func (m *manualMirror) next() (manualMirrorEvent, bool) {
	select {
	case e := <-m.events:
		return e, true
	case <-m.done:
		return manualMirrorEvent{}, false
	}
}
func (m *manualMirror) resume(r manualReply) {
	r = cloneManualReply(r)
	select {
	case m.replies <- r:
	case <-m.done:
	}
}
