package main

import "sync"

type diagnosticMirrorEvent struct {
	effect             diagnoseEffect
	result             diagnoseServiceResult
	terminal, panicked bool
}
type diagnosticMirror struct {
	events       chan diagnosticMirrorEvent
	replies      chan diagnoseReply
	cancel, done chan struct{}
	once         sync.Once
}

func cloneDiagnosticReply(r diagnoseReply) diagnoseReply { return r }
func (m *diagnosticMirror) Call(q diagnoseEffect) diagnoseReply {

	select {
	case m.events <- diagnosticMirrorEvent{effect: q}:
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
func (m *diagnosticMirror) Release() {}
func newDiagnosticMirror(q diagnoseServiceRequest) *diagnosticMirror {
	m := &diagnosticMirror{events: make(chan diagnosticMirrorEvent), replies: make(chan diagnoseReply), cancel: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(m.done)
		defer func() {
			if p := recover(); p != nil {
				if _, ok := p.(subscriptionMirrorCancelled); ok {
					return
				}
				select {
				case m.events <- diagnosticMirrorEvent{terminal: true, panicked: true}:
				case <-m.cancel:
				}
			}
		}()
		r := runGoDiagnoseService(q, m)
		select {
		case m.events <- diagnosticMirrorEvent{terminal: true, result: r}:
		case <-m.cancel:
		}
	}()
	return m
}
func (m *diagnosticMirror) close() { m.once.Do(func() { close(m.cancel) }); <-m.done }
func (m *diagnosticMirror) next() (diagnosticMirrorEvent, bool) {
	select {
	case e := <-m.events:
		return e, true
	case <-m.done:
		return diagnosticMirrorEvent{}, false
	}
}
func (m *diagnosticMirror) resume(r diagnoseReply) {
	r = cloneDiagnosticReply(r)
	select {
	case m.replies <- r:
	case <-m.done:
	}
}
