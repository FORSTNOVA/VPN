package main

import "sync"

type regionJobMirrorEvent struct {
	effect             regionJobEffect
	result             regionJobServiceResult
	terminal, panicked bool
}
type regionJobMirror struct {
	events       chan regionJobMirrorEvent
	replies      chan regionJobReply
	cancel, done chan struct{}
	once         sync.Once
}

func cloneRegionJobReply(r regionJobReply) regionJobReply {
	if r.State != nil {
		v := *r.State
		v.Sources = append([]string(nil), v.Sources...)
		r.State = &v
	}
	r.Names = append([]string(nil), r.Names...)
	if r.Delays != nil {
		v := make(map[string]int, len(r.Delays))
		for k, n := range r.Delays {
			v[k] = n
		}
		r.Delays = v
	}
	if r.Record.Sources != nil {
		r.Record.Sources = append([]string{}, r.Record.Sources...)
	}
	if r.Job != nil {
		v := *r.Job
		r.Job = &v
	}
	return r
}
func (m *regionJobMirror) Call(q regionJobEffect) regionJobReply {
	if q.Record.Sources != nil {
		q.Record.Sources = append([]string{}, q.Record.Sources...)
	}
	select {
	case m.events <- regionJobMirrorEvent{effect: q}:
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
func (m *regionJobMirror) Release() {}
func newRegionJobMirror(q regionJobServiceRequest) *regionJobMirror {
	m := &regionJobMirror{events: make(chan regionJobMirrorEvent), replies: make(chan regionJobReply), cancel: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(m.done)
		defer func() {
			if p := recover(); p != nil {
				if _, ok := p.(subscriptionMirrorCancelled); ok {
					return
				}
				select {
				case m.events <- regionJobMirrorEvent{terminal: true, panicked: true}:
				case <-m.cancel:
				}
			}
		}()
		r := runGoRegionJobService(q, m)
		select {
		case m.events <- regionJobMirrorEvent{terminal: true, result: r}:
		case <-m.cancel:
		}
	}()
	return m
}
func (m *regionJobMirror) close() { m.once.Do(func() { close(m.cancel) }); <-m.done }
func (m *regionJobMirror) next() (regionJobMirrorEvent, bool) {
	select {
	case e := <-m.events:
		return e, true
	case <-m.done:
		return regionJobMirrorEvent{}, false
	}
}
func (m *regionJobMirror) resume(r regionJobReply) {
	r = cloneRegionJobReply(r)
	select {
	case m.replies <- r:
	case <-m.done:
	}
}
