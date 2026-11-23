package main

import "sync"

type quotaMirrorEvent struct {
	effect             quotaEffect
	result             quotaServiceResult
	terminal, panicked bool
}
type quotaMirror struct {
	events       chan quotaMirrorEvent
	replies      chan quotaReply
	cancel, done chan struct{}
	once         sync.Once
}

func cloneQuotaReply(r quotaReply) quotaReply {
	r.Entries = cloneQuotaEntries(r.Entries)
	r.Strings = append([]string(nil), r.Strings...)
	if r.Info != nil {
		v := *r.Info
		r.Info = &v
	}
	r.Entry = cloneQuotaEntries([]subscriptionEntry{r.Entry})[0]
	return r
}
func (m *quotaMirror) Call(q quotaEffect) quotaReply {
	if q.Info != nil {
		v := *q.Info
		q.Info = &v
	}
	select {
	case m.events <- quotaMirrorEvent{effect: q}:
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

// The mirror never owns the real mutex. Only the driver releases actual ownership.
func (m *quotaMirror) Release() {}
func newQuotaMirror(q quotaServiceRequest) *quotaMirror {
	m := &quotaMirror{events: make(chan quotaMirrorEvent), replies: make(chan quotaReply), cancel: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(m.done)
		defer func() {
			if p := recover(); p != nil {
				if _, ok := p.(subscriptionMirrorCancelled); ok {
					return
				}
				select {
				case m.events <- quotaMirrorEvent{terminal: true, panicked: true}:
				case <-m.cancel:
				}
			}
		}()
		r := runGoQuotaService(q, m)
		select {
		case m.events <- quotaMirrorEvent{terminal: true, result: r}:
		case <-m.cancel:
		}
	}()
	return m
}
func (m *quotaMirror) close() { m.once.Do(func() { close(m.cancel) }); <-m.done }
func (m *quotaMirror) next() (quotaMirrorEvent, bool) {
	select {
	case e := <-m.events:
		return e, true
	case <-m.done:
		return quotaMirrorEvent{}, false
	}
}
func (m *quotaMirror) resume(r quotaReply) {
	r = cloneQuotaReply(r)
	select {
	case m.replies <- r:
	case <-m.done:
	}
}
