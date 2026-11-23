package main

import "sync"

type speedMirrorEvent struct {
	effect             speedServiceEffect
	result             speedServiceResult
	terminal, panicked bool
}
type speedMirror struct {
	events       chan speedMirrorEvent
	replies      chan speedServiceReply
	cancel, done chan struct{}
	once         sync.Once
}

func cloneSpeedReply(r speedServiceReply) speedServiceReply { return r }
func (m *speedMirror) Call(q speedServiceEffect) speedServiceReply {

	select {
	case m.events <- speedMirrorEvent{effect: q}:
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
func (m *speedMirror) Release() {}
func newSpeedMirror(q speedServiceRequest) *speedMirror {
	m := &speedMirror{events: make(chan speedMirrorEvent), replies: make(chan speedServiceReply), cancel: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(m.done)
		defer func() {
			if p := recover(); p != nil {
				if _, ok := p.(subscriptionMirrorCancelled); ok {
					return
				}
				select {
				case m.events <- speedMirrorEvent{terminal: true, panicked: true}:
				case <-m.cancel:
				}
			}
		}()
		r := runGoSpeedService(q, m)
		select {
		case m.events <- speedMirrorEvent{terminal: true, result: r}:
		case <-m.cancel:
		}
	}()
	return m
}
func (m *speedMirror) close() { m.once.Do(func() { close(m.cancel) }); <-m.done }
func (m *speedMirror) next() (speedMirrorEvent, bool) {
	select {
	case e := <-m.events:
		return e, true
	case <-m.done:
		return speedMirrorEvent{}, false
	}
}
func (m *speedMirror) resume(r speedServiceReply) {
	r = cloneSpeedReply(r)
	select {
	case m.replies <- r:
	case <-m.done:
	}
}
