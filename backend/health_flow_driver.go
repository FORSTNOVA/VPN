package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"sync/atomic"
	"time"
)

// Counts abnormal native continuation exits, not normal deterministic completion.
var healthFlowRecoveries atomic.Uint64

type healthFlowSession interface {
	next() ([]byte, uint64, error)
	advance(uint64, []byte) error
	close()
}
type healthFlowStart func([]byte) (healthFlowSession, error)

func healthFlowInt(b []byte, n int64) []byte { return binary.LittleEndian.AppendUint64(b, uint64(n)) }
func healthFlowBool(b []byte, v bool) []byte {
	if v {
		return append(b, 1)
	}
	return append(b, 0)
}

type healthFlowReader struct {
	b   []byte
	p   int
	bad bool
}

func (d *healthFlowReader) byte() byte {
	if d.p >= len(d.b) {
		d.bad = true
		return 0
	}
	v := d.b[d.p]
	d.p++
	return v
}
func (d *healthFlowReader) integer() int64 {
	if len(d.b)-d.p < 8 {
		d.bad = true
		return 0
	}
	v := int64(binary.LittleEndian.Uint64(d.b[d.p:]))
	d.p += 8
	return v
}
func (d *healthFlowReader) boolean() bool {
	v := d.byte()
	if v > 1 {
		d.bad = true
	}
	return v == 1
}

type healthFlowEffect struct {
	op, kind           byte
	node, from, target int64
	values             [4]int64
	success, cooldown  bool
	delay              int64
}

func decodeHealthFlowEffect(b []byte, n int) (healthFlowEffect, error) {
	d := healthFlowReader{b: b}
	e := healthFlowEffect{op: d.byte(), node: -1, from: -1, target: -1}
	switch e.op {
	case 1, 3, 4, 6, 9, 10:
	case 2:
		e.node = d.integer()
		for i := range e.values {
			e.values[i] = d.integer()
		}
		e.success = d.boolean()
		e.cooldown = d.boolean()
		if e.node < 0 || e.values[0] < 1 || e.values[0] > 5 {
			d.bad = true
		}
	case 5:
		e.target = d.integer()
	case 7:
		e.kind = d.byte()
		e.target = d.integer()
	case 8:
		e.kind = d.byte()
		e.from = d.integer()
		e.target = d.integer()
		e.delay = d.integer()
	default:
		d.bad = true
	}
	for _, i := range []int64{e.node, e.from, e.target} {
		if i < -1 || i >= int64(n) {
			d.bad = true
		}
	}
	if e.op == 7 || e.op == 8 {
		if e.kind < 1 || e.kind > 4 {
			d.bad = true
		}
		if (e.kind == 1 || e.kind == 3) != (e.target >= 0) {
			d.bad = true
		}
	}
	if d.bad || d.p != len(b) {
		return e, errors.New("invalid health continuation")
	}
	return e, nil
}
func healthFlowSnapshot(s *healthServiceState, r healthServiceRequest) ([]byte, []string, []bool, error) {
	if r.Kind != "patrol" && r.Kind != "failed" && r.Kind != "unblock" {
		return nil, nil, nil, errors.New("unsupported use case")
	}
	if _, ok := s.Regions[""]; ok {
		return nil, nil, nil, errors.New("empty region identity")
	}
	nameset := map[string]bool{}
	for name, h := range s.Health {
		nameset[name] = true
		if h.Name != "" && h.Name != name {
			return nil, nil, nil, errors.New("noncanonical health record")
		}
		if h.Name == "" && (h.State != "" || h.ConsecutiveFailures != 0 || h.ConsecutiveSuccesses != 0 || h.LatencyMs != 0 || !h.LastSuccessAt.IsZero() || !h.CooldownUntil.IsZero() || !h.UpdatedAt.IsZero()) {
			return nil, nil, nil, errors.New("noncanonical empty health record")
		}
	}
	for name := range s.Regions {
		nameset[name] = true
	}
	if r.Active != "" {
		nameset[r.Active] = true
	}
	delete(nameset, "")
	names := make([]string, 0, len(nameset))
	for name := range nameset {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) > 100000 {
		return nil, nil, nil, errors.New("snapshot too large")
	}
	b := healthFlowInt(nil, int64(len(names)))
	active := int64(-1)
	eligible := make([]bool, len(names))
	for i, name := range names {
		if name == r.Active {
			active = int64(i)
		}
		h := s.Health[name]
		record := s.Regions[name]
		match := record.Country == s.LockedRegion
		verified := record.Status == regionVerified
		fresh := r.Now.Sub(record.VerifiedAt) <= regionTTL
		cooling := h.cooldownActive(r.Now)
		eligible[i] = s.LockedRegion != "" && match && verified && fresh && !cooling
		b = healthFlowBool(b, match)
		b = healthFlowBool(b, verified)
		b = healthFlowBool(b, fresh)
		b = healthFlowBool(b, cooling)
		state := int64(0)
		for j, v := range []string{healthHealthy, healthSuspect, healthUnhealthy, healthCooldown, healthRecovery} {
			if h.State == v {
				state = int64(j + 1)
			}
		}
		b = healthFlowInt(b, state)
		b = healthFlowInt(b, int64(h.ConsecutiveFailures))
		b = healthFlowInt(b, int64(h.ConsecutiveSuccesses))
		b = healthFlowInt(b, int64(h.LatencyMs))
	}
	b = healthFlowInt(b, active)
	b = healthFlowBool(b, s.ChainEnabled)
	b = healthFlowBool(b, s.LockedRegion != "")
	b = healthFlowBool(b, s.LastSwitch.IsZero() || r.Now.Sub(s.LastSwitch) >= time.Duration(s.Params.MinDwellSec)*time.Second)
	b = healthFlowBool(b, s.Blocked)
	b = healthFlowInt(b, int64(s.Params.FailureThreshold))
	b = healthFlowInt(b, int64(s.Params.RecoverySuccesses))
	if r.Kind == "unblock" {
		b = append(b, 3)
	} else {
		b = append(b, 0)
		b = healthFlowBool(b, r.Kind == "failed")
		b = healthFlowInt(b, int64(len(names)))
		for _, name := range names {
			b = healthFlowInt(b, int64(r.Delays[name]))
		}
	}
	return b, names, eligible, nil
}

// False is allowed only before any state mutation or external effect.
func runRustHealthServiceWithStart(s *healthServiceState, r healthServiceRequest, fx healthServiceEffects, start healthFlowStart) bool {
	input, names, eligible, err := healthFlowSnapshot(s, r)
	if err != nil {
		return false
	}
	session, err := start(input)
	if err != nil {
		return false
	}
	defer session.close()
	index := map[string]int64{}
	for i, name := range names {
		index[name] = int64(i)
	}
	effect := func(op byte) healthFlowEffect { return healthFlowEffect{op: op, node: -1, from: -1, target: -1} }
	queue := []healthFlowEffect{}
	active, hasActive := index[r.Active]
	if r.Kind != "unblock" {
		watched := []int64{}
		if hasActive {
			watched = append(watched, active)
		}
		if r.Kind == "patrol" && !s.ChainEnabled {
			for i, ok := range eligible {
				if ok && (!hasActive || int64(i) != active) {
					watched = append(watched, int64(i))
				}
			}
		}
		positive := false
		for _, i := range watched {
			if r.Kind == "patrol" && r.Delays[names[i]] > 0 {
				positive = true
			}
		}
		if positive {
			queue = append(queue, effect(1))
		}
		for _, i := range watched {
			h := s.Health[names[i]]
			if h.Name == "" {
				h = newHealth(names[i])
			}
			delay := r.Delays[names[i]]
			if r.Kind == "failed" {
				delay = 0
			}
			next := nextHealthStateGo(h, delay > 0, delay, r.Now, s.Params)
			code := int64(0)
			for j, state := range []string{healthHealthy, healthSuspect, healthUnhealthy, healthCooldown, healthRecovery} {
				if next.State == state {
					code = int64(j + 1)
				}
			}
			e := effect(2)
			e.node = i
			e.values = [4]int64{code, int64(next.ConsecutiveFailures), int64(next.ConsecutiveSuccesses), int64(next.LatencyMs)}
			e.success = delay > 0
			queue = append(queue, e)
		}
	}
	started, broken, tail := false, false, false
	last := uint64(0)
	phase := "pick"
	blockKind := byte(2)
	committed := byte(0)
	chosenDelay := int64(0)
	stop := func() bool {
		healthFlowRecoveries.Add(1)
		if started {
			fx.Logf("health continuation stopped; completed effects were not replayed")
		}
		return started
	}
	// This guard computes only a trusted expected continuation. External operations
	// happen solely after a full Rust frame matches it. After a confirmed PUT the
	// remaining deterministic local completion is retained independently of the DLL.
	pick := func(fresh map[string]int, useFresh bool) (int64, int64, bool) {
		pool := []proxyNode{}
		delays := map[string]int{}
		poolCount := 0
		for i, ok := range eligible {
			if !ok {
				continue
			}
			poolCount++
			name := names[i]
			h := s.Health[name]
			if h.State == healthUnhealthy {
				continue
			}
			pool = append(pool, proxyNode{Name: name})
			if useFresh {
				delays[name] = fresh[name]
			} else if h.LatencyMs > 0 {
				delays[name] = h.LatencyMs
			}
		}
		best, d := chooseReachableNodeGo(pool, delays, "")
		if best != "" {
			return index[best], int64(d), false
		}
		return -1, 0, poolCount > 0 && !useFresh
	}
	choose := func(target, delay int64, measure bool) {
		chosenDelay = delay
		if target >= 0 {
			e := effect(5)
			e.target = target
			queue = append(queue, e)
			phase = "switch"
		} else if measure {
			queue = append(queue, effect(3))
			phase = "measure"
		} else if r.Kind == "unblock" {
			phase = "done"
		} else {
			queue = append(queue, effect(4))
			phase = "confirm"
		}
	}
	for count := 0; count < 300100; count++ {
		if len(queue) == 0 {
			if tail {
				return true
			}
			if phase == "pick" {
				if r.Kind != "unblock" && (s.ChainEnabled || s.LockedRegion == "" || s.Health[r.Active].State != healthUnhealthy || (!s.LastSwitch.IsZero() && r.Now.Sub(s.LastSwitch) < time.Duration(s.Params.MinDwellSec)*time.Second)) {
					phase = "done"
				} else {
					target, delay, measure := pick(nil, false)
					choose(target, delay, measure)
				}
			}
			if len(queue) == 0 && phase == "done" {
				queue = append(queue, effect(10))
			}
		}
		if len(queue) == 0 {
			return stop()
		}
		expected := queue[0]
		var step uint64
		if !broken {
			raw, n, e := session.next()
			step = n
			actual, decodeErr := decodeHealthFlowEffect(raw, len(names))
			if e != nil || decodeErr != nil || step != last+1 || actual != expected {
				if !tail {
					return stop()
				}
				broken = true
				healthFlowRecoveries.Add(1)
				fx.Logf("health continuation failed after a confirmed switch; finishing local completion without repeating network effects")
			}
		}
		if expected.op == 10 {
			return true
		}
		started = true
		queue = queue[1:]
		e := expected
		reply := []byte{0}
		switch e.op {
		case 1:
			s.SweepNote = ""
			if authority, ok := fx.(interface{ CommitControl(healthServiceState) }); ok {
				authority.CommitControl(*s)
			}
		case 2:
			name := names[e.node]
			h := s.Health[name]
			if h.Name == "" {
				h = newHealth(name)
			}
			previous := h.State
			h.Name = name
			h.State = []string{healthHealthy, healthSuspect, healthUnhealthy, healthCooldown, healthRecovery}[e.values[0]-1]
			h.ConsecutiveFailures = int(e.values[1])
			h.ConsecutiveSuccesses = int(e.values[2])
			h.LatencyMs = int(e.values[3])
			h.UpdatedAt = r.Now
			if e.success {
				h.LastSuccessAt = r.Now
			}
			if e.cooldown {
				h.CooldownUntil = r.Now.Add(time.Duration(s.Params.CooldownSec) * time.Second)
			}
			if s.Health == nil {
				s.Health = map[string]nodeHealth{}
			}
			s.Health[name] = h
			if authority, ok := fx.(interface{ CommitHealth(nodeHealth) }); ok {
				authority.CommitHealth(h)
			}
			if s.StoreAvailable {
				if err := fx.SaveHealth(h); err != nil {
					fx.Logf("could not persist node health: %v", err)
				}
			}
			if !e.cooldown && previous != healthUnhealthy && h.State == healthUnhealthy {
				fx.Logf("node %s became %s after a failed sweep", name, h.State)
			}
		case 3:
			delays, err := fx.MeasureGroup(time.Duration(s.Params.ConnectTimeoutMs) * time.Millisecond)
			reply = healthFlowBool([]byte{2}, err == nil)
			reply = healthFlowInt(reply, int64(len(names)))
			for _, name := range names {
				reply = healthFlowInt(reply, int64(delays[name]))
			}
			target, delay := int64(-1), int64(0)
			if err == nil {
				target, delay, _ = pick(delays, true)
			}
			choose(target, delay, false)
		case 4:
			passed := fx.ConfirmTraffic()
			reply = healthFlowBool([]byte{1}, passed)
			if passed {
				queue = append(queue, effect(9))
				phase = "done"
			} else {
				queue = append(queue, effect(5))
				phase = "switch"
			}
		case 5:
			target := blockedChoice
			if e.target >= 0 {
				target = names[e.target]
			}
			err := fx.SwitchGroup(target)
			reply = healthFlowBool([]byte{1}, err == nil)
			if err != nil {
				if e.target < 0 {
					fx.Logf("could not block traffic: %v", err)
					phase = "done"
				} else if r.Kind == "unblock" {
					phase = "done"
				} else {
					blockKind = 4
					queue = append(queue, effect(4))
					phase = "confirm"
				}
				break
			}
			tail = true
			kind := blockKind
			if e.target >= 0 {
				kind = 1
				if r.Kind == "unblock" {
					kind = 3
				}
			}
			commit := effect(7)
			commit.kind = kind
			commit.target = e.target
			event := effect(8)
			event.kind = kind
			event.target = e.target
			event.delay = chosenDelay
			if hasActive {
				event.from = active
			}
			if kind == 2 || kind == 4 {
				event.delay = 0
			}
			if kind == 3 {
				event.from = -1
			}
			if kind == 1 {
				h := s.Health[r.Active]
				cool := effect(2)
				cool.node = active
				cool.cooldown = true
				cool.values = [4]int64{4, int64(h.ConsecutiveFailures), 0, int64(h.LatencyMs)}
				queue = append(queue, cool, effect(6), commit, event)
			} else if kind == 3 {
				queue = append(queue, commit, effect(6), event)
			} else {
				queue = append(queue, commit, event)
			}
		case 6:
			if err := fx.SaveSettings(*s); err != nil && committed != 3 {
				fx.Logf("could not persist the failover selection: %v", err)
			}
		case 7:
			committed = e.kind
			s.LastSwitch = r.Now
			if e.kind == 2 || e.kind == 4 {
				s.Blocked = true
				s.BlockReason = healthFlowBlockReason(e.kind)
				s.SweepNote = ""
			} else {
				s.Blocked = false
				s.BlockReason = ""
				s.LockedNode = names[e.target]
				s.SettingsLockedNode = names[e.target]
				if authority, ok := fx.(interface{ CommitPin(healthServiceState) }); ok {
					authority.CommitPin(*s)
				}
				if e.kind == 3 {
					s.SweepNote = ""
				}
			}
			if authority, ok := fx.(interface{ CommitControl(healthServiceState) }); ok {
				authority.CommitControl(*s)
			}
		case 8:
			if authority, ok := fx.(interface{ CommitControl(healthServiceState) }); ok {
				authority.CommitControl(*s)
			}
			from := r.Active
			if e.from >= 0 {
				from = names[e.from]
			}
			target := blockedChoice
			if e.target >= 0 {
				target = names[e.target]
			}
			event := switchEvent{At: r.Now, Group: "SmartVPN", FromNode: from, ToNode: target}
			switch e.kind {
			case 1:
				event.Trigger = "unhealthy"
				event.Evidence = fmt.Sprintf("%s failed %d consecutive sweeps; %s answered in %d ms", r.Active, s.Health[r.Active].ConsecutiveFailures, target, e.delay)
			case 3:
				event.FromNode = blockedChoice
				event.Trigger = "region_recovered"
				event.Evidence = fmt.Sprintf("%s answered in %d ms", target, e.delay)
			case 2, 4:
				event.Trigger = "region_exhausted"
				event.Evidence = healthFlowBlockReason(e.kind)
			}
			fx.Logf("node switch: %s -> %s (%s) %s", event.FromNode, event.ToNode, event.Trigger, event.Evidence)
			if s.StoreAvailable {
				if err := fx.AppendSwitchEvent(event); err != nil {
					fx.Logf("could not persist switch event: %v", err)
				}
			}
		case 9:
			if s.SweepNote == "" {
				fx.Logf("the locked region measured as exhausted while a request through %s still got through; not blocking", r.Active)
			}
			s.SweepNote = healthFlowAnomaly
			if authority, ok := fx.(interface{ CommitControl(healthServiceState) }); ok {
				authority.CommitControl(*s)
			}
		}
		if !broken {
			last = step
			if err := session.advance(step, reply); err != nil {
				if !tail {
					return stop()
				}
				broken = true
				healthFlowRecoveries.Add(1)
				fx.Logf("health continuation failed after a confirmed switch; finishing local completion without repeating network effects")
			}
		}
	}
	return stop()
}

const healthFlowAnomaly = "同地区节点在延迟测试里全部不通，但经当前节点的实际请求仍然通行，因此没有阻断；以实测为准。"

func healthFlowBlockReason(kind byte) string {
	if kind == 4 {
		return "无法切换到同地区备用节点"
	}
	return "同地区已无可用节点"
}
