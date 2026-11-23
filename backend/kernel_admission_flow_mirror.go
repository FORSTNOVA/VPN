package main

import "sync"

type kernelAdmissionMirrorEvent struct {
	effect             kernelAdmissionEffect
	result             kernelAdmissionResult
	terminal, panicked bool
}
type kernelAdmissionMirror struct {
	events       chan kernelAdmissionMirrorEvent
	replies      chan kernelAdmissionReply
	cancel, done chan struct{}
	once         sync.Once
}

func cloneKernelAdmissionReply(r kernelAdmissionReply) kernelAdmissionReply {
	if r.Names != nil {
		r.Names = append([]string{}, r.Names...)
	}
	return r
}
func (m *kernelAdmissionMirror) Call(q kernelAdmissionEffect) kernelAdmissionReply {

	select {
	case m.events <- kernelAdmissionMirrorEvent{effect: q}:
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
func (m *kernelAdmissionMirror) Release() {}
func newKernelAdmissionMirror(q kernelAdmissionRequest) *kernelAdmissionMirror {
	m := &kernelAdmissionMirror{events: make(chan kernelAdmissionMirrorEvent), replies: make(chan kernelAdmissionReply), cancel: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(m.done)
		defer func() {
			if p := recover(); p != nil {
				if _, ok := p.(subscriptionMirrorCancelled); ok {
					return
				}
				select {
				case m.events <- kernelAdmissionMirrorEvent{terminal: true, panicked: true}:
				case <-m.cancel:
				}
			}
		}()
		r := runGoKernelAdmissionService(q, m)
		select {
		case m.events <- kernelAdmissionMirrorEvent{terminal: true, result: r}:
		case <-m.cancel:
		}
	}()
	return m
}
func (m *kernelAdmissionMirror) close() { m.once.Do(func() { close(m.cancel) }); <-m.done }
func (m *kernelAdmissionMirror) next() (kernelAdmissionMirrorEvent, bool) {
	select {
	case e := <-m.events:
		return e, true
	case <-m.done:
		return kernelAdmissionMirrorEvent{}, false
	}
}
func (m *kernelAdmissionMirror) resume(r kernelAdmissionReply) {
	r = cloneKernelAdmissionReply(r)
	select {
	case m.replies <- r:
	case <-m.done:
	}
}
