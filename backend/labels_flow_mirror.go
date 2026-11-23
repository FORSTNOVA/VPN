package main

import "sync"

type labelsMirrorEvent struct {
	effect             labelsEffect
	result             labelsServiceResult
	terminal, panicked bool
}
type labelsMirror struct {
	events       chan labelsMirrorEvent
	replies      chan labelsReply
	cancel, done chan struct{}
	once         sync.Once
}

func cloneLabelsReply(r labelsReply) labelsReply { r.State = cloneLabelsState(r.State); return r }
func (m *labelsMirror) Call(q labelsEffect) labelsReply {

	select {
	case m.events <- labelsMirrorEvent{effect: q}:
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
func (m *labelsMirror) Release() {}
func newLabelsMirror(q labelsServiceRequest) *labelsMirror {
	m := &labelsMirror{events: make(chan labelsMirrorEvent), replies: make(chan labelsReply), cancel: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(m.done)
		defer func() {
			if p := recover(); p != nil {
				if _, ok := p.(subscriptionMirrorCancelled); ok {
					return
				}
				select {
				case m.events <- labelsMirrorEvent{terminal: true, panicked: true}:
				case <-m.cancel:
				}
			}
		}()
		r := runGoLabelsService(q, m)
		select {
		case m.events <- labelsMirrorEvent{terminal: true, result: r}:
		case <-m.cancel:
		}
	}()
	return m
}
func (m *labelsMirror) close() { m.once.Do(func() { close(m.cancel) }); <-m.done }
func (m *labelsMirror) next() (labelsMirrorEvent, bool) {
	select {
	case e := <-m.events:
		return e, true
	case <-m.done:
		return labelsMirrorEvent{}, false
	}
}
func (m *labelsMirror) resume(r labelsReply) {
	r = cloneLabelsReply(r)
	select {
	case m.replies <- r:
	case <-m.done:
	}
}
