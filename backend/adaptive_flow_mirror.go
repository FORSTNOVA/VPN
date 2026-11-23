package main

import "sync"

type adaptiveMirrorEvent struct {
	effect             adaptiveServiceEffect
	result             adaptiveServiceResult
	terminal, panicked bool
}
type adaptiveMirror struct {
	events       chan adaptiveMirrorEvent
	replies      chan adaptiveServiceReply
	cancel, done chan struct{}
	once         sync.Once
}

func cloneAdaptiveReply(r adaptiveServiceReply) adaptiveServiceReply { return r }
func (m *adaptiveMirror) Call(q adaptiveServiceEffect) adaptiveServiceReply {

	select {
	case m.events <- adaptiveMirrorEvent{effect: q}:
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
func (m *adaptiveMirror) Release() {}
func newAdaptiveMirror(q adaptiveServiceRequest) *adaptiveMirror {
	m := &adaptiveMirror{events: make(chan adaptiveMirrorEvent), replies: make(chan adaptiveServiceReply), cancel: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(m.done)
		defer func() {
			if p := recover(); p != nil {
				if _, ok := p.(subscriptionMirrorCancelled); ok {
					return
				}
				select {
				case m.events <- adaptiveMirrorEvent{terminal: true, panicked: true}:
				case <-m.cancel:
				}
			}
		}()
		r := runGoAdaptiveService(q, m)
		select {
		case m.events <- adaptiveMirrorEvent{terminal: true, result: r}:
		case <-m.cancel:
		}
	}()
	return m
}
func (m *adaptiveMirror) close() { m.once.Do(func() { close(m.cancel) }); <-m.done }
func (m *adaptiveMirror) next() (adaptiveMirrorEvent, bool) {
	select {
	case e := <-m.events:
		return e, true
	case <-m.done:
		return adaptiveMirrorEvent{}, false
	}
}
func (m *adaptiveMirror) resume(r adaptiveServiceReply) {
	r = cloneAdaptiveReply(r)
	select {
	case m.replies <- r:
	case <-m.done:
	}
}
