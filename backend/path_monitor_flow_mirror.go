package main

import "sync"

type pathMonitorMirrorEvent struct {
	effect             pathMonitorServiceEffect
	result             pathMonitorServiceResult
	terminal, panicked bool
}
type pathMonitorMirror struct {
	events       chan pathMonitorMirrorEvent
	replies      chan pathMonitorServiceReply
	cancel, done chan struct{}
	once         sync.Once
}

func clonePathMonitorReply(r pathMonitorServiceReply) pathMonitorServiceReply {
	r.State = clonePathMonitorState(r.State)
	if r.Samples != nil {
		v := make(map[string]pathConnectionSample, len(r.Samples))
		for k, x := range r.Samples {
			v[k] = x
		}
		r.Samples = v
	}
	if r.Connections != nil {
		r.Connections = append([]pathMonitorConnection{}, r.Connections...)
	}
	return r
}
func (m *pathMonitorMirror) Call(q pathMonitorServiceEffect) pathMonitorServiceReply {

	select {
	case m.events <- pathMonitorMirrorEvent{effect: q}:
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
func (m *pathMonitorMirror) Release() {}
func newPathMonitorMirror(q pathMonitorServiceRequest) *pathMonitorMirror {
	if q.Previous != nil {
		old := q.Previous
		q.Previous = make(map[string]pathConnectionSample, len(old))
		for k, v := range old {
			q.Previous[k] = v
		}
	}
	q.Settings.Targets = append([]pathTarget(nil), q.Settings.Targets...)
	m := &pathMonitorMirror{events: make(chan pathMonitorMirrorEvent), replies: make(chan pathMonitorServiceReply), cancel: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(m.done)
		defer func() {
			if p := recover(); p != nil {
				if _, ok := p.(subscriptionMirrorCancelled); ok {
					return
				}
				select {
				case m.events <- pathMonitorMirrorEvent{terminal: true, panicked: true}:
				case <-m.cancel:
				}
			}
		}()
		r := runGoPathMonitorService(q, m)
		select {
		case m.events <- pathMonitorMirrorEvent{terminal: true, result: r}:
		case <-m.cancel:
		}
	}()
	return m
}
func (m *pathMonitorMirror) close() { m.once.Do(func() { close(m.cancel) }); <-m.done }
func (m *pathMonitorMirror) next() (pathMonitorMirrorEvent, bool) {
	select {
	case e := <-m.events:
		return e, true
	case <-m.done:
		return pathMonitorMirrorEvent{}, false
	}
}
func (m *pathMonitorMirror) resume(r pathMonitorServiceReply) {
	r = clonePathMonitorReply(r)
	select {
	case m.replies <- r:
	case <-m.done:
	}
}
