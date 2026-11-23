package main

import "sync"

type kernelMirrorEvent struct {
	effect             kernelEffect
	result             kernelServiceResult
	terminal, panicked bool
}
type kernelMirror struct {
	events       chan kernelMirrorEvent
	replies      chan kernelReply
	cancel, done chan struct{}
	once         sync.Once
}

func cloneKernelReply(r kernelReply) kernelReply {
	if r.State != nil {
		v := *r.State
		r.State = &v
	}
	if r.Body != nil {
		r.Body = append([]byte{}, r.Body...)
	}
	return r
}
func (m *kernelMirror) Call(q kernelEffect) kernelReply {
	if q.Body != nil {
		q.Body = append([]byte{}, q.Body...)
	}
	select {
	case m.events <- kernelMirrorEvent{effect: q}:
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
func (m *kernelMirror) Release() {}
func newKernelMirror(q kernelServiceRequest) *kernelMirror {
	m := &kernelMirror{events: make(chan kernelMirrorEvent), replies: make(chan kernelReply), cancel: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(m.done)
		defer func() {
			if p := recover(); p != nil {
				if _, ok := p.(subscriptionMirrorCancelled); ok {
					return
				}
				select {
				case m.events <- kernelMirrorEvent{terminal: true, panicked: true}:
				case <-m.cancel:
				}
			}
		}()
		r := runGoKernelService(q, m)
		select {
		case m.events <- kernelMirrorEvent{terminal: true, result: r}:
		case <-m.cancel:
		}
	}()
	return m
}
func (m *kernelMirror) close() { m.once.Do(func() { close(m.cancel) }); <-m.done }
func (m *kernelMirror) next() (kernelMirrorEvent, bool) {
	select {
	case e := <-m.events:
		return e, true
	case <-m.done:
		return kernelMirrorEvent{}, false
	}
}
func (m *kernelMirror) resume(r kernelReply) {
	r = cloneKernelReply(r)
	select {
	case m.replies <- r:
	case <-m.done:
	}
}
