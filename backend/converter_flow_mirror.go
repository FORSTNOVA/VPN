package main

import "sync"

type converterMirrorEvent struct {
	effect             converterServiceEffect
	result             converterServiceResult
	terminal, panicked bool
}
type converterMirror struct {
	events       chan converterMirrorEvent
	replies      chan converterServiceReply
	cancel, done chan struct{}
	once         sync.Once
}

func cloneConverterReply(r converterServiceReply) converterServiceReply {
	if r.Tokens != nil {
		r.Tokens = append([]int{}, r.Tokens...)
	}
	if r.Fields != nil {
		r.Fields = append([]converterField{}, r.Fields...)
	}
	if r.Names != nil {
		r.Names = append([]string{}, r.Names...)
	}
	return r
}
func cloneConverterEffect(q converterServiceEffect) converterServiceEffect {
	if q.Tokens != nil {
		q.Tokens = append([]int{}, q.Tokens...)
	}
	if q.Fields != nil {
		q.Fields = append([]converterAssignment{}, q.Fields...)
	}
	return q
}
func (m *converterMirror) Call(q converterServiceEffect) converterServiceReply {

	select {
	case m.events <- converterMirrorEvent{effect: cloneConverterEffect(q)}:
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
func (m *converterMirror) Release() {}
func newConverterMirror(q converterServiceRequest) *converterMirror {
	if q.Sources != nil {
		q.Sources = append([]converterSource{}, q.Sources...)
	}
	m := &converterMirror{events: make(chan converterMirrorEvent), replies: make(chan converterServiceReply), cancel: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(m.done)
		defer func() {
			if p := recover(); p != nil {
				if _, ok := p.(subscriptionMirrorCancelled); ok {
					return
				}
				select {
				case m.events <- converterMirrorEvent{terminal: true, panicked: true}:
				case <-m.cancel:
				}
			}
		}()
		r := runGoConverterService(q, m)
		select {
		case m.events <- converterMirrorEvent{terminal: true, result: r}:
		case <-m.cancel:
		}
	}()
	return m
}
func (m *converterMirror) close() { m.once.Do(func() { close(m.cancel) }); <-m.done }
func (m *converterMirror) next() (converterMirrorEvent, bool) {
	select {
	case e := <-m.events:
		return e, true
	case <-m.done:
		return converterMirrorEvent{}, false
	}
}
func (m *converterMirror) resume(r converterServiceReply) {
	r = cloneConverterReply(r)
	select {
	case m.replies <- r:
	case <-m.done:
	}
}
