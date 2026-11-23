package main

import "sync"

type appMirrorEvent struct {
	effect             appServiceEffect
	result             appServiceResult
	terminal, panicked bool
}
type appMirror struct {
	events       chan appMirrorEvent
	replies      chan appServiceReply
	cancel, done chan struct{}
	once         sync.Once
}

func cloneAppReply(r appServiceReply) appServiceReply {
	if r.Health != nil {
		r.Health = append([]appHealthFact{}, r.Health...)
	}
	if r.Regions != nil {
		r.Regions = append([]appRegionFact{}, r.Regions...)
	}
	return r
}
func (m *appMirror) Call(q appServiceEffect) appServiceReply {

	select {
	case m.events <- appMirrorEvent{effect: q}:
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
func (m *appMirror) Release() {}
func newAppMirror(q appServiceRequest) *appMirror {
	m := &appMirror{events: make(chan appMirrorEvent), replies: make(chan appServiceReply), cancel: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(m.done)
		defer func() {
			if p := recover(); p != nil {
				if _, ok := p.(subscriptionMirrorCancelled); ok {
					return
				}
				select {
				case m.events <- appMirrorEvent{terminal: true, panicked: true}:
				case <-m.cancel:
				}
			}
		}()
		r := runGoAppService(q, m)
		select {
		case m.events <- appMirrorEvent{terminal: true, result: r}:
		case <-m.cancel:
		}
	}()
	return m
}
func (m *appMirror) close() { m.once.Do(func() { close(m.cancel) }); <-m.done }
func (m *appMirror) next() (appMirrorEvent, bool) {
	select {
	case e := <-m.events:
		return e, true
	case <-m.done:
		return appMirrorEvent{}, false
	}
}
func (m *appMirror) resume(r appServiceReply) {
	r = cloneAppReply(r)
	select {
	case m.replies <- r:
	case <-m.done:
	}
}
