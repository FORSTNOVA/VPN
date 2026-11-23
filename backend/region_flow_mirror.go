package main

import "sync"

type regionMirrorEvent struct {
	effect             regionEffect
	result             regionServiceResult
	terminal, panicked bool
}
type regionMirror struct {
	events       chan regionMirrorEvent
	replies      chan regionReply
	cancel, done chan struct{}
	once         sync.Once
}

func cloneRegionReply(r regionReply) regionReply {
	r.State = cloneRegionServiceState(r.State)
	return r
}
func (m *regionMirror) Call(q regionEffect) regionReply {
	select {
	case m.events <- regionMirrorEvent{effect: q}:
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
func (m *regionMirror) Release() {}
func newRegionMirror(q regionServiceRequest) *regionMirror {
	m := &regionMirror{events: make(chan regionMirrorEvent), replies: make(chan regionReply), cancel: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(m.done)
		defer func() {
			if p := recover(); p != nil {
				if _, ok := p.(subscriptionMirrorCancelled); ok {
					return
				}
				select {
				case m.events <- regionMirrorEvent{terminal: true, panicked: true}:
				case <-m.cancel:
				}
			}
		}()
		r := runGoRegionService(q, m)
		select {
		case m.events <- regionMirrorEvent{terminal: true, result: r}:
		case <-m.cancel:
		}
	}()
	return m
}
func (m *regionMirror) close() { m.once.Do(func() { close(m.cancel) }); <-m.done }
func (m *regionMirror) next() (regionMirrorEvent, bool) {
	select {
	case e := <-m.events:
		return e, true
	case <-m.done:
		return regionMirrorEvent{}, false
	}
}
func (m *regionMirror) resume(r regionReply) {
	r = cloneRegionReply(r)
	select {
	case m.replies <- r:
	case <-m.done:
	}
}
