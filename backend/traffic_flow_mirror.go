package main

import "sync"

type trafficMirrorEvent struct {
	effect             trafficEffect
	result             trafficServiceResult
	terminal, panicked bool
}
type trafficMirror struct {
	events       chan trafficMirrorEvent
	replies      chan trafficReply
	cancel, done chan struct{}
	once         sync.Once
}

func cloneTrafficReply(r trafficReply) trafficReply {
	r.State = cloneTrafficServiceState(r.State)
	return r
}
func (m *trafficMirror) Call(q trafficEffect) trafficReply {

	select {
	case m.events <- trafficMirrorEvent{effect: q}:
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
func (m *trafficMirror) Release() {}
func newTrafficMirror(q trafficServiceRequest) *trafficMirror {
	m := &trafficMirror{events: make(chan trafficMirrorEvent), replies: make(chan trafficReply), cancel: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(m.done)
		defer func() {
			if p := recover(); p != nil {
				if _, ok := p.(subscriptionMirrorCancelled); ok {
					return
				}
				select {
				case m.events <- trafficMirrorEvent{terminal: true, panicked: true}:
				case <-m.cancel:
				}
			}
		}()
		r := runGoTrafficService(q, m)
		select {
		case m.events <- trafficMirrorEvent{terminal: true, result: r}:
		case <-m.cancel:
		}
	}()
	return m
}
func (m *trafficMirror) close() { m.once.Do(func() { close(m.cancel) }); <-m.done }
func (m *trafficMirror) next() (trafficMirrorEvent, bool) {
	select {
	case e := <-m.events:
		return e, true
	case <-m.done:
		return trafficMirrorEvent{}, false
	}
}
func (m *trafficMirror) resume(r trafficReply) {
	r = cloneTrafficReply(r)
	select {
	case m.replies <- r:
	case <-m.done:
	}
}
