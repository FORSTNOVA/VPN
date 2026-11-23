package main

import "sync"

type bootstrapMirrorEvent struct {
	effect             bootstrapServiceEffect
	result             bootstrapServiceResult
	terminal, panicked bool
}
type bootstrapMirror struct {
	events       chan bootstrapMirrorEvent
	replies      chan bootstrapServiceReply
	cancel, done chan struct{}
	once         sync.Once
}

func cloneBootstrapReply(r bootstrapServiceReply) bootstrapServiceReply { return r }
func (m *bootstrapMirror) Call(q bootstrapServiceEffect) bootstrapServiceReply {

	select {
	case m.events <- bootstrapMirrorEvent{effect: q}:
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
func (m *bootstrapMirror) Release() {}
func newBootstrapMirror(q bootstrapServiceRequest) *bootstrapMirror {
	m := &bootstrapMirror{events: make(chan bootstrapMirrorEvent), replies: make(chan bootstrapServiceReply), cancel: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(m.done)
		defer func() {
			if p := recover(); p != nil {
				if _, ok := p.(subscriptionMirrorCancelled); ok {
					return
				}
				select {
				case m.events <- bootstrapMirrorEvent{terminal: true, panicked: true}:
				case <-m.cancel:
				}
			}
		}()
		r := runGoBootstrapService(q, m)
		select {
		case m.events <- bootstrapMirrorEvent{terminal: true, result: r}:
		case <-m.cancel:
		}
	}()
	return m
}
func (m *bootstrapMirror) close() { m.once.Do(func() { close(m.cancel) }); <-m.done }
func (m *bootstrapMirror) next() (bootstrapMirrorEvent, bool) {
	select {
	case e := <-m.events:
		return e, true
	case <-m.done:
		return bootstrapMirrorEvent{}, false
	}
}
func (m *bootstrapMirror) resume(r bootstrapServiceReply) {
	r = cloneBootstrapReply(r)
	select {
	case m.replies <- r:
	case <-m.done:
	}
}
