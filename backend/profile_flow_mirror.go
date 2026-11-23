package main

import (
	"reflect"
	"sync"
)

// Ownership is transferred at each unbuffered handshake. After resume, callers
// must not read any shared state or reply graph until receiving the next event.
type profileMirrorEvent struct {
	effect                       profileEffect
	state, live                  profileServiceState
	isolated, terminal, panicked bool
	result                       profileServiceResult
}
type profileMirrorReply struct {
	state profileServiceState
	reply profileReply
}
type profileMirror struct {
	root         *profileServiceState
	events       chan profileMirrorEvent
	replies      chan profileMirrorReply
	cancel, done chan struct{}
	once         sync.Once
}

func profileCloneState(s profileServiceState) profileServiceState {
	v := subscriptionCloneValue(reflect.ValueOf(s)).Interface().(profileServiceState)
	v.settingsServiceState = subscriptionCloneState(s.settingsServiceState)
	return v
}
func (m *profileMirror) Call(q profileEffect, s *profileServiceState) profileReply {
	e := profileMirrorEvent{effect: q, state: *s, live: *m.root, isolated: s != m.root}
	select {
	case m.events <- e:
	case <-m.cancel:
		panic(subscriptionMirrorCancelled{})
	}
	select {
	case v := <-m.replies:
		*s = v.state
		return v.reply
	case <-m.cancel:
		panic(subscriptionMirrorCancelled{})
	}
}
func newProfileMirror(s profileServiceState, r profileServiceRequest) *profileMirror {
	m := &profileMirror{events: make(chan profileMirrorEvent), replies: make(chan profileMirrorReply), cancel: make(chan struct{}), done: make(chan struct{})}
	state := s
	if r.Op != "acquire-entry" {
		state = profileCloneState(s)
	}
	m.root = &state
	request := subscriptionCloneValue(reflect.ValueOf(r)).Interface().(profileServiceRequest)
	go func() {
		defer close(m.done)
		defer func() {
			if p := recover(); p != nil {
				if _, ok := p.(subscriptionMirrorCancelled); ok {
					return
				}
				select {
				case m.events <- profileMirrorEvent{state: state, terminal: true, panicked: true}:
				case <-m.cancel:
				}
			}
		}()
		result := runGoProfileService(&state, request, m)
		select {
		case m.events <- profileMirrorEvent{state: state, result: result, terminal: true}:
		case <-m.cancel:
		}
	}()
	return m
}
func (m *profileMirror) close() { m.once.Do(func() { close(m.cancel) }); <-m.done }
func (m *profileMirror) next() (profileMirrorEvent, bool) {
	select {
	case e := <-m.events:
		return e, true
	case <-m.done:
		return profileMirrorEvent{}, false
	}
}
func (m *profileMirror) resume(s profileServiceState, r profileReply) {
	select {
	case m.replies <- profileMirrorReply{s, r}:
	case <-m.done:
	}
}
