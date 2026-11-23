package main

import (
	"reflect"
	"sync"
)

// Each state graph has one owner. The mirror blocks at every port until the
// caller sends a deep-copied reply and actual partial state back.
func subscriptionCloneValue(v reflect.Value) reflect.Value {
	if !v.IsValid() {
		return v
	}
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		o := reflect.New(v.Type().Elem())
		o.Elem().Set(subscriptionCloneValue(v.Elem()))
		return o
	case reflect.Interface:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		o := reflect.New(v.Type()).Elem()
		o.Set(subscriptionCloneValue(v.Elem()))
		return o
	case reflect.Map:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		o := reflect.MakeMapWithSize(v.Type(), v.Len())
		it := v.MapRange()
		for it.Next() {
			o.SetMapIndex(it.Key(), subscriptionCloneValue(it.Value()))
		}
		return o
	case reflect.Slice:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		o := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := 0; i < v.Len(); i++ {
			o.Index(i).Set(subscriptionCloneValue(v.Index(i)))
		}
		return o
	case reflect.Struct:
		o := reflect.New(v.Type()).Elem()
		o.Set(v)
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).PkgPath == "" {
				o.Field(i).Set(subscriptionCloneValue(v.Field(i)))
			}
		}
		return o
	default:
		return v
	}
}
func subscriptionCloneState(s subscriptionServiceState) subscriptionServiceState {
	o := subscriptionCloneValue(reflect.ValueOf(s)).Interface().(subscriptionServiceState)
	if s.Settings.GameBypass != nil {
		o.Settings.GameBypass = append([]string{}, s.Settings.GameBypass...)
	}
	return o
}

// UserInfo is a read-only pointer resource within the suspended oracle. Preserve
// its identity; all ordinary mutable state still receives an isolated clone.
func subscriptionCloneResourceValue(v reflect.Value) reflect.Value {
	if v.IsValid() && v.Type() == reflect.TypeOf((*subscriptionUserInfo)(nil)) {
		return v
	}
	if !v.IsValid() {
		return v
	}
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		o := reflect.New(v.Type().Elem())
		o.Elem().Set(subscriptionCloneResourceValue(v.Elem()))
		return o
	case reflect.Interface:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		o := reflect.New(v.Type()).Elem()
		o.Set(subscriptionCloneResourceValue(v.Elem()))
		return o
	case reflect.Map:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		o := reflect.MakeMapWithSize(v.Type(), v.Len())
		it := v.MapRange()
		for it.Next() {
			o.SetMapIndex(it.Key(), subscriptionCloneResourceValue(it.Value()))
		}
		return o
	case reflect.Slice:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		o := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := 0; i < v.Len(); i++ {
			o.Index(i).Set(subscriptionCloneResourceValue(v.Index(i)))
		}
		return o
	case reflect.Struct:
		o := reflect.New(v.Type()).Elem()
		o.Set(v)
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).PkgPath == "" {
				o.Field(i).Set(subscriptionCloneResourceValue(v.Field(i)))
			}
		}
		return o
	default:
		return v
	}
}
func subscriptionCloneResourceState(s subscriptionServiceState) subscriptionServiceState {
	o := subscriptionCloneResourceValue(reflect.ValueOf(s)).Interface().(subscriptionServiceState)
	if s.Settings.GameBypass != nil {
		o.Settings.GameBypass = append([]string{}, s.Settings.GameBypass...)
	}
	return o
}

type subscriptionMirrorReply struct {
	transfer bool
	state    subscriptionServiceState
	reply    subscriptionEffectReply
}
type subscriptionMirrorEvent struct {
	live               subscriptionServiceState
	isolated           bool
	kind, arg          string
	state              subscriptionServiceState
	result             subscriptionServiceResult
	terminal, panicked bool
}
type subscriptionMirror struct {
	transfer bool
	root     *subscriptionServiceState
	events   chan subscriptionMirrorEvent
	replies  chan subscriptionMirrorReply
	cancel   chan struct{}
	done     chan struct{}
	once     sync.Once
}
type subscriptionMirrorCancelled struct{}

func (m *subscriptionMirror) Call(kind, arg string, s *subscriptionServiceState) subscriptionEffectReply {
	e := subscriptionMirrorEvent{kind: kind, arg: arg, state: *s, live: *m.root, isolated: s != m.root}
	if !m.transfer {
		e.state = subscriptionCloneResourceState(*s)
		e.live = subscriptionCloneResourceState(*m.root)
	}
	select {
	case m.events <- e:
	case <-m.cancel:
		panic(subscriptionMirrorCancelled{})
	}
	select {
	case reply := <-m.replies:
		m.transfer = reply.transfer
		if m.transfer {
			*s = reply.state
		} else {
			*s = subscriptionCloneResourceState(reply.state)
		}
		return reply.reply
	case <-m.cancel:
		panic(subscriptionMirrorCancelled{})
	}
}
func newSubscriptionMirror(s subscriptionServiceState, r subscriptionServiceRequest) *subscriptionMirror {
	m := &subscriptionMirror{events: make(chan subscriptionMirrorEvent), replies: make(chan subscriptionMirrorReply), cancel: make(chan struct{}), done: make(chan struct{})}
	state := subscriptionCloneResourceState(s)
	m.root = &state
	request := subscriptionCloneResourceValue(reflect.ValueOf(r)).Interface().(subscriptionServiceRequest)
	go func() {
		defer close(m.done)
		defer func() {
			if p := recover(); p != nil {
				if _, ok := p.(subscriptionMirrorCancelled); ok {
					return
				}
				select {
				case m.events <- subscriptionMirrorEvent{state: subscriptionCloneResourceState(state), terminal: true, panicked: true}:
				case <-m.cancel:
				}
			}
		}()
		result := runGoSubscriptionService(&state, request, m)
		select {
		case m.events <- subscriptionMirrorEvent{state: subscriptionCloneResourceState(state), result: result, terminal: true}:
		case <-m.cancel:
		}
	}()
	return m
}
func (m *subscriptionMirror) close() { m.once.Do(func() { close(m.cancel) }); <-m.done }
func (m *subscriptionMirror) next() (subscriptionMirrorEvent, bool) {
	select {
	case e := <-m.events:
		return e, true
	case <-m.done:
		return subscriptionMirrorEvent{}, false
	}
}
func (m *subscriptionMirror) resume(s subscriptionServiceState, r subscriptionEffectReply, transfer bool) {
	if !transfer {
		s = subscriptionCloneResourceState(s)
		r = subscriptionCloneResourceValue(reflect.ValueOf(r)).Interface().(subscriptionEffectReply)
	}
	select {
	case m.replies <- subscriptionMirrorReply{state: s, reply: r, transfer: transfer}:
	case <-m.done:
	}
}
