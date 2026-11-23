package main

import "sync"

type chainProbeMirrorEvent struct {
	effect             chainProbeEffect
	result             chainProbeServiceResult
	terminal, panicked bool
}
type chainProbeMirror struct {
	events       chan chainProbeMirrorEvent
	replies      chan chainProbeReply
	cancel, done chan struct{}
	once         sync.Once
}

func cloneChainProbeReply(r chainProbeReply) chainProbeReply { return r }
func (m *chainProbeMirror) Call(q chainProbeEffect) chainProbeReply {

	select {
	case m.events <- chainProbeMirrorEvent{effect: q}:
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
func (m *chainProbeMirror) Release() {}
func newChainProbeMirror(q chainProbeServiceRequest) *chainProbeMirror {
	m := &chainProbeMirror{events: make(chan chainProbeMirrorEvent), replies: make(chan chainProbeReply), cancel: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(m.done)
		defer func() {
			if p := recover(); p != nil {
				if _, ok := p.(subscriptionMirrorCancelled); ok {
					return
				}
				select {
				case m.events <- chainProbeMirrorEvent{terminal: true, panicked: true}:
				case <-m.cancel:
				}
			}
		}()
		r := runGoChainProbeService(q, m)
		select {
		case m.events <- chainProbeMirrorEvent{terminal: true, result: r}:
		case <-m.cancel:
		}
	}()
	return m
}
func (m *chainProbeMirror) close() { m.once.Do(func() { close(m.cancel) }); <-m.done }
func (m *chainProbeMirror) next() (chainProbeMirrorEvent, bool) {
	select {
	case e := <-m.events:
		return e, true
	case <-m.done:
		return chainProbeMirrorEvent{}, false
	}
}
func (m *chainProbeMirror) resume(r chainProbeReply) {
	r = cloneChainProbeReply(r)
	select {
	case m.replies <- r:
	case <-m.done:
	}
}
