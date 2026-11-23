package main

import "sync"

type chainMirrorEvent struct {
	effect             chainEffect
	result             chainServiceResult
	terminal, panicked bool
}
type chainMirror struct {
	events       chan chainMirrorEvent
	replies      chan chainReply
	cancel, done chan struct{}
	once         sync.Once
}

func chainCloneProxy(p map[string]any) map[string]any {
	if p == nil {
		return nil
	}
	return cloneManualValue(p).(map[string]any)
}
func chainCloneProxies(ps []map[string]any) []map[string]any {
	if ps == nil {
		return nil
	}
	v := make([]map[string]any, len(ps))
	for i, p := range ps {
		v[i] = chainCloneProxy(p)
	}
	return v
}
func cloneChainReply(r chainReply) chainReply {
	r.State = cloneChainServiceState(r.State)
	if r.Body != nil {
		r.Body = append([]byte{}, r.Body...)
	}
	if r.Entries != nil {
		v := make([]chainProxyEntry, len(r.Entries))
		for i, e := range r.Entries {
			v[i] = chainProxyEntry{Key: e.Key, Proxy: chainCloneProxy(e.Proxy)}
		}
		r.Entries = v
	}
	r.Proxy = chainCloneProxy(r.Proxy)
	if r.Record.Sources != nil {
		r.Record.Sources = append([]string{}, r.Record.Sources...)
	}
	return r
}
func (m *chainMirror) Call(q chainEffect) chainReply {
	if q.Body != nil {
		q.Body = append([]byte{}, q.Body...)
	}
	q.Proxies = chainCloneProxies(q.Proxies)
	q.Next.Hops = appendPreserveChainSlice(q.Next.Hops)

	select {
	case m.events <- chainMirrorEvent{effect: q}:
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
func (m *chainMirror) Release() {}
func newChainMirror(q chainServiceRequest) *chainMirror {
	m := &chainMirror{events: make(chan chainMirrorEvent), replies: make(chan chainReply), cancel: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(m.done)
		defer func() {
			if p := recover(); p != nil {
				if _, ok := p.(subscriptionMirrorCancelled); ok {
					return
				}
				select {
				case m.events <- chainMirrorEvent{terminal: true, panicked: true}:
				case <-m.cancel:
				}
			}
		}()
		r := runGoChainService(q, m)
		select {
		case m.events <- chainMirrorEvent{terminal: true, result: r}:
		case <-m.cancel:
		}
	}()
	return m
}
func (m *chainMirror) close() { m.once.Do(func() { close(m.cancel) }); <-m.done }
func (m *chainMirror) next() (chainMirrorEvent, bool) {
	select {
	case e := <-m.events:
		return e, true
	case <-m.done:
		return chainMirrorEvent{}, false
	}
}
func (m *chainMirror) resume(r chainReply) {
	r = cloneChainReply(r)
	select {
	case m.replies <- r:
	case <-m.done:
	}
}
