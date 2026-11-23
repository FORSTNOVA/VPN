package main

import "sync"

type subscriptionHTTPMirrorEvent struct {
	effect             subscriptionHTTPServiceEffect
	result             subscriptionHTTPServiceResult
	terminal, panicked bool
}
type subscriptionHTTPMirror struct {
	events       chan subscriptionHTTPMirrorEvent
	replies      chan subscriptionHTTPServiceReply
	cancel, done chan struct{}
	once         sync.Once
}

func cloneSubscriptionHTTPReply(r subscriptionHTTPServiceReply) subscriptionHTTPServiceReply {
	if r.Names != nil {
		r.Names = append([]string{}, r.Names...)
	}
	if r.Nodes != nil {
		r.Nodes = append([]subscriptionHTTPNodeFact{}, r.Nodes...)
	}
	return r
}
func (m *subscriptionHTTPMirror) Call(q subscriptionHTTPServiceEffect) subscriptionHTTPServiceReply {

	select {
	case m.events <- subscriptionHTTPMirrorEvent{effect: q}:
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
func (m *subscriptionHTTPMirror) Release() {}
func newSubscriptionHTTPMirror(q subscriptionHTTPServiceRequest) *subscriptionHTTPMirror {
	m := &subscriptionHTTPMirror{events: make(chan subscriptionHTTPMirrorEvent), replies: make(chan subscriptionHTTPServiceReply), cancel: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(m.done)
		defer func() {
			if p := recover(); p != nil {
				if _, ok := p.(subscriptionMirrorCancelled); ok {
					return
				}
				select {
				case m.events <- subscriptionHTTPMirrorEvent{terminal: true, panicked: true}:
				case <-m.cancel:
				}
			}
		}()
		r := runGoSubscriptionHTTPService(q, m)
		select {
		case m.events <- subscriptionHTTPMirrorEvent{terminal: true, result: r}:
		case <-m.cancel:
		}
	}()
	return m
}
func (m *subscriptionHTTPMirror) close() { m.once.Do(func() { close(m.cancel) }); <-m.done }
func (m *subscriptionHTTPMirror) next() (subscriptionHTTPMirrorEvent, bool) {
	select {
	case e := <-m.events:
		return e, true
	case <-m.done:
		return subscriptionHTTPMirrorEvent{}, false
	}
}
func (m *subscriptionHTTPMirror) resume(r subscriptionHTTPServiceReply) {
	r = cloneSubscriptionHTTPReply(r)
	select {
	case m.replies <- r:
	case <-m.done:
	}
}
