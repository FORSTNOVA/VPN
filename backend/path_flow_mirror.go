package main

import "sync"

type pathMirrorEvent struct {
	effect             pathServiceEffect
	result             pathServiceResult
	terminal, panicked bool
}
type pathMirror struct {
	events       chan pathMirrorEvent
	replies      chan pathServiceReply
	cancel, done chan struct{}
	once         sync.Once
}

func clonePathReply(r pathServiceReply) pathServiceReply {
	r.State = clonePathServiceState(r.State)
	if r.Candidates != nil {
		r.Candidates = append([]pathCandidateFact{}, r.Candidates...)
	}
	return r
}
func (m *pathMirror) Call(q pathServiceEffect) pathServiceReply {

	select {
	case m.events <- pathMirrorEvent{effect: q}:
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
func (m *pathMirror) Release() {}
func newPathMirror(q pathServiceRequest) *pathMirror {
	m := &pathMirror{events: make(chan pathMirrorEvent), replies: make(chan pathServiceReply), cancel: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(m.done)
		defer func() {
			if p := recover(); p != nil {
				if _, ok := p.(subscriptionMirrorCancelled); ok {
					return
				}
				select {
				case m.events <- pathMirrorEvent{terminal: true, panicked: true}:
				case <-m.cancel:
				}
			}
		}()
		r := runGoPathService(q, m)
		select {
		case m.events <- pathMirrorEvent{terminal: true, result: r}:
		case <-m.cancel:
		}
	}()
	return m
}
func (m *pathMirror) close() { m.once.Do(func() { close(m.cancel) }); <-m.done }
func (m *pathMirror) next() (pathMirrorEvent, bool) {
	select {
	case e := <-m.events:
		return e, true
	case <-m.done:
		return pathMirrorEvent{}, false
	}
}
func (m *pathMirror) resume(r pathServiceReply) {
	r = clonePathReply(r)
	select {
	case m.replies <- r:
	case <-m.done:
	}
}
