package main

import "sync"

type exitMirrorEvent struct {
	effect             exitEffect
	result             exitServiceResult
	terminal, panicked bool
}
type exitMirror struct {
	events       chan exitMirrorEvent
	replies      chan exitReply
	cancel, done chan struct{}
	once         sync.Once
}

func cloneExitReply(r exitReply) exitReply {
	if r.State != nil {
		v := *r.State
		r.State = &v
	}
	return r
}
func (m *exitMirror) Call(q exitEffect) exitReply {
	select {
	case m.events <- exitMirrorEvent{effect: q}:
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
func (m *exitMirror) Release() {}
func newExitMirror(q exitServiceRequest) *exitMirror {
	m := &exitMirror{events: make(chan exitMirrorEvent), replies: make(chan exitReply), cancel: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(m.done)
		defer func() {
			if p := recover(); p != nil {
				if _, ok := p.(subscriptionMirrorCancelled); ok {
					return
				}
				select {
				case m.events <- exitMirrorEvent{terminal: true, panicked: true}:
				case <-m.cancel:
				}
			}
		}()
		r := runGoExitService(q, m)
		select {
		case m.events <- exitMirrorEvent{terminal: true, result: r}:
		case <-m.cancel:
		}
	}()
	return m
}
func (m *exitMirror) close() { m.once.Do(func() { close(m.cancel) }); <-m.done }
func (m *exitMirror) next() (exitMirrorEvent, bool) {
	select {
	case e := <-m.events:
		return e, true
	case <-m.done:
		return exitMirrorEvent{}, false
	}
}
func (m *exitMirror) resume(r exitReply) {
	r = cloneExitReply(r)
	select {
	case m.replies <- r:
	case <-m.done:
	}
}
