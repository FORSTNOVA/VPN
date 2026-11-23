package main

import "sync"

type lifecycleMirrorEvent struct {
	effect             lifecycleEffect
	result             lifecycleServiceResult
	terminal, panicked bool
}
type lifecycleMirror struct {
	events       chan lifecycleMirrorEvent
	replies      chan lifecycleReply
	cancel, done chan struct{}
	once         sync.Once
}

func cloneLifecycleReply(r lifecycleReply) lifecycleReply {
	if r.State != nil {
		c := *r.State
		r.State = &c
	}
	if r.Names != nil {
		r.Names = append([]string{}, r.Names...)
	}
	if r.Nodes != nil {
		r.Nodes = append([]proxyNode{}, r.Nodes...)
	}
	return r
}
func (m *lifecycleMirror) Call(q lifecycleEffect) lifecycleReply {

	select {
	case m.events <- lifecycleMirrorEvent{effect: q}:
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
func (m *lifecycleMirror) Release() {}
func newLifecycleMirror(q lifecycleServiceRequest) *lifecycleMirror {
	m := &lifecycleMirror{events: make(chan lifecycleMirrorEvent), replies: make(chan lifecycleReply), cancel: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(m.done)
		defer func() {
			if p := recover(); p != nil {
				if _, ok := p.(subscriptionMirrorCancelled); ok {
					return
				}
				select {
				case m.events <- lifecycleMirrorEvent{terminal: true, panicked: true}:
				case <-m.cancel:
				}
			}
		}()
		r := runGoLifecycleService(q, m)
		select {
		case m.events <- lifecycleMirrorEvent{terminal: true, result: r}:
		case <-m.cancel:
		}
	}()
	return m
}
func (m *lifecycleMirror) close() { m.once.Do(func() { close(m.cancel) }); <-m.done }
func (m *lifecycleMirror) next() (lifecycleMirrorEvent, bool) {
	select {
	case e := <-m.events:
		return e, true
	case <-m.done:
		return lifecycleMirrorEvent{}, false
	}
}
func (m *lifecycleMirror) resume(r lifecycleReply) {
	r = cloneLifecycleReply(r)
	select {
	case m.replies <- r:
	case <-m.done:
	}
}
