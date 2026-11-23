package main

import (
	"errors"
	"fmt"
	"reflect"
	"sync/atomic"
)

var profileFlowRecoveries atomic.Uint64
var profileFlowRecoveryReason atomic.Value

func profileInternalError() profileServiceResult {
	e := errors.New("profile pipeline interrupted")
	return profileServiceResult{Err: e, Acquisition: profileAcquisitionResult{Err: e}, Status: 500}
}

// The only external-effect executor is this driver. The suspended Go oracle
// guards each planned action and owns opaque state/compensation during migration.
func runRustProfileServiceWithStart(s *profileServiceState, r profileServiceRequest, fx profileServiceEffects, start profileFlowStart) (result profileServiceResult, handled bool) {
	a := &profileArena{}
	input, err := profileStartInput(*s, r, a)
	if err != nil {
		return result, false
	}
	session, err := start(input)
	if err != nil {
		return result, false
	}
	defer session.close()
	initial := *s
	if r.Op != "acquire-entry" {
		initial = profileCloneState(*s)
	}
	mirror := newProfileMirror(*s, r)
	defer mirror.close()
	started := false
	last := uint64(0)
	var pending *profileMirrorEvent
	defer func() {
		if recover() != nil {
			result = profileInternalError()
			handled = started
		}
	}()
	take := func() (profileMirrorEvent, bool) {
		if pending != nil {
			e := *pending
			pending = nil
			return e, true
		}
		return mirror.next()
	}
	// Return a completion closure, not a live reply graph. marshal before resume.
	execute := func(e profileMirrorEvent) (profileReply, func()) {
		*s = e.live
		started = true
		if e.isolated {
			isolated := e.state
			reply := fx.Call(e.effect, &isolated)
			return reply, func() { mirror.resume(isolated, reply) }
		}
		reply := fx.Call(e.effect, s)
		state := *s
		return reply, func() { mirror.resume(state, reply) }
	}
	finish := func() (profileServiceResult, bool) {
		profileFlowRecoveries.Add(1)
		if !started {
			return profileServiceResult{}, false
		}
		for i := 0; i < 1000000; i++ {
			e, ok := take()
			if !ok {
				return profileInternalError(), true
			}
			if e.terminal {
				*s = e.state
				if e.panicked {
					return profileInternalError(), true
				}
				return e.result, true
			}
			_, resume := execute(e)
			resume()
		}
		return profileInternalError(), true
	}
	for i := 0; i < 1000000; i++ {
		raw, step, err := session.next()
		if err != nil || step != last+1 {
			return finish()
		}
		f, err := decodeProfileFrame(raw)
		if err != nil {
			return finish()
		}
		reply := profileReplyBytes(profileWireReply{ok: true})
		switch f.kind {
		case 13:
			if f.a < 0 || f.a >= int64(len(initial.Subscriptions)) {
				return finish()
			}
			v, ok := a.get(f.b)
			if !ok {
				return finish()
			}
			entry := initial.Subscriptions[f.a]
			token := a.put(profileValue{source: SubscriptionSource{ID: entry.ID, Label: entry.Label, Prefix: entry.Prefix, Body: v.body}})
			reply = profileReplyBytes(profileWireReply{ok: true, token: token})
		case 15, 16, 23, 25:
			var ok bool
			reply, ok = profilePureFrame(f, initial, r, a)
			if !ok {
				return finish()
			}
		case 34: // From-cache state is owned by mirror.
		case 32: // Metadata mutation; trusted mirror owns opaque actual state.
		case 33:
			v, ok := a.get(f.b)
			if !ok {
				return finish()
			}
			nodes := append([]proxyNode(nil), v.nodes...)
			label := ""
			if f.c != 0 {
				label = f.text
			}
			annotateProfileNodes(nodes, label, &f.data)
			v.nodes = nodes
			a.values[f.b-1] = v
		case 63:
			e, ok := take()
			if !ok {
				return finish()
			}
			if !e.terminal || e.panicked || !profileResultMatches(f, e.result, a, r.Op, initial.SelectedChoice, e.state, r) {
				if r.Op != "acquire-entry" {
					profileFlowRecoveryReason.Store(fmt.Sprintf("result code=%d token=%d items=%d terminal=%v", f.a, f.b, len(f.items), e.terminal))
				}
				pending = &e
				return finish()
			}
			*s = e.state
			return e.result, true
		default:
			q, ok := profilePort(initial, r, f, a)
			if !ok {
				return finish()
			}
			e, ok := take()
			if !ok {
				return finish()
			}
			if e.terminal || !reflect.DeepEqual(q, e.effect) {
				profileFlowRecoveryReason.Store(fmt.Sprintf("frame=%d expected=%s got=%s equal=%v", f.kind, e.effect.Kind, q.Kind, reflect.DeepEqual(q, e.effect)))
				pending = &e
				return finish()
			}
			out, resume := execute(e)
			reply = profileEncodeReply(f, out, a)
			resume()
		}
		last = step
		if err := session.advance(step, reply); err != nil {
			return finish()
		}
	}
	return finish()
}
func profileResultMatches(f profileFrame, r profileServiceResult, a *profileArena, op, selected string, final profileServiceState, request profileServiceRequest) bool {
	if op == "proxies" || op == "download" || op == "fetch" || op == "fetch-single" || op == "merge-all" || op == "refresh-single" || op == "merge" || op == "auto-tick" || op == "acquire-entry" {
		return profileBResultMatches(f, r, a, op, selected, final, request)
	}
	if f.a == 0 {
		if r.Err != nil {
			return false
		}
	} else {
		if r.Err == nil {
			return false
		}
		if f.a == 10 && f.text != r.Err.Error() {
			return false
		}
		if f.a < 1 || f.a > 12 {
			return false
		}
	}
	if f.a != 0 {
		if f.a == 11 {
			return r.Err.Error() == fmt.Sprintf("合并会移除当前线路 %s，请勾选其订阅或先切换线路", selected)
		}
		if f.a == 12 {
			want := fmt.Sprintf("内核未确认合并配置：%s", f.text)
			if f.data != "" {
				want += "\n" + f.data
			}
			return r.Err.Error() == want
		}
		if f.a <= 9 {
			return r.Err.Error() == profileKnownError(f.a)
		}
		return true
	}
	if op == "write-merged" || op == "merge-caches" {
		if f.b == 0 {
			return r.Nodes == nil
		}
		v, ok := a.get(f.b)
		if !ok || !reflect.DeepEqual(v.nodes, r.Nodes) {
			return false
		}
	}
	if op == "cached-sources" {
		var sources []SubscriptionSource
		for _, n := range f.items {
			v, ok := a.get(n)
			if !ok {
				return false
			}
			sources = append(sources, v.source)
		}
		if !reflect.DeepEqual(sources, r.Sources) {
			return false
		}
	}
	return true
}

func profileKnownError(code int64) string {
	switch code {
	case 1:
		return "没有这个订阅的缓存副本"
	case 2:
		return "缓存副本里没有可用节点"
	case 3:
		return "没有这个订阅"
	case 4:
		return "无法创建本地配置目录"
	case 5:
		return "无法写入缓存副本"
	case 6:
		return "无法启用缓存副本"
	case 7:
		return "无法记录配置来源"
	case 8:
		return "无法保存缓存副本的节点列表"
	case 9:
		return "没有可用的订阅缓存，请执行「一键合并更新」"
	}
	return ""
}
