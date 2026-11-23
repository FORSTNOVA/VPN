package main

import "sync"

type chinaMirrorEvent struct {
	effect             chinaServiceEffect
	result             chinaServiceResult
	terminal, panicked bool
}
type chinaMirror struct {
	events       chan chinaMirrorEvent
	replies      chan chinaServiceReply
	cancel, done chan struct{}
	once         sync.Once
}

func cloneChinaReply(r chinaServiceReply) chinaServiceReply {
	if r.Body != nil {
		r.Body = append([]byte{}, r.Body...)
	}
	if r.Ranges != nil {
		r.Ranges = append([]string{}, r.Ranges...)
	}
	if r.Names != nil {
		r.Names = append([]string{}, r.Names...)
	}
	return r
}
func (m *chinaMirror) Call(q chinaServiceEffect) chinaServiceReply {

	select {
	case m.events <- chinaMirrorEvent{effect: q}:
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
func (m *chinaMirror) Release() {}
func newChinaMirror(q chinaServiceRequest) *chinaMirror {
	if q.Body != nil {
		q.Body = append([]byte{}, q.Body...)
	}
	if q.Ranges != nil {
		q.Ranges = append([]string{}, q.Ranges...)
	}
	m := &chinaMirror{events: make(chan chinaMirrorEvent), replies: make(chan chinaServiceReply), cancel: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(m.done)
		defer func() {
			if p := recover(); p != nil {
				if _, ok := p.(subscriptionMirrorCancelled); ok {
					return
				}
				select {
				case m.events <- chinaMirrorEvent{terminal: true, panicked: true}:
				case <-m.cancel:
				}
			}
		}()
		r := runGoChinaService(q, m)
		select {
		case m.events <- chinaMirrorEvent{terminal: true, result: r}:
		case <-m.cancel:
		}
	}()
	return m
}
func (m *chinaMirror) close() { m.once.Do(func() { close(m.cancel) }); <-m.done }
func (m *chinaMirror) next() (chinaMirrorEvent, bool) {
	select {
	case e := <-m.events:
		return e, true
	case <-m.done:
		return chinaMirrorEvent{}, false
	}
}
func (m *chinaMirror) resume(r chinaServiceReply) {
	r = cloneChinaReply(r)
	select {
	case m.replies <- r:
	case <-m.done:
	}
}
