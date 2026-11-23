package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

type goPathService struct {
	fx                      pathServiceEffects
	s                       *pathServiceState
	locked, round, recovery bool
}

func (a *goPathService) call(q pathServiceEffect) pathServiceReply { return a.fx.Call(q) }
func (a *goPathService) lock()                                     { a.s = a.call(pathServiceEffect{Kind: "lock"}).State; a.locked = true }
func (a *goPathService) unlock()                                   { a.call(pathServiceEffect{Kind: "unlock"}); a.locked = false }
func (a *goPathService) running() bool                             { return a.call(pathServiceEffect{Kind: "running"}).Bool }
func (a *goPathService) now() time.Time                            { return a.call(pathServiceEffect{Kind: "now"}).Now }
func (a *goPathService) set(n, t string, b bool) {
	a.call(pathServiceEffect{Kind: "set", Name: n, Text: t, Flag: b})
	switch n {
	case "busy":
		a.s.Busy = b
		a.round = b
	case "running":
		a.s.Status.Running = b
	case "phase":
		a.s.Status.Phase = t
	case "note":
		a.s.Status.Note = t
	case "alert":
		a.s.Status.Alert = t
	case "demanded":
		a.s.Status.Demanded = b
	case "retry-mode":
		a.s.Status.RetryMode = t
	case "retry-at":
		a.s.Status.RetryAt = t
	case "alert-increment":
		a.s.Status.AlertID++
	case "switched":
		a.s.Status.Switched = nil
	case "append-switch":
		a.s.Status.Switched = append(a.s.Status.Switched, t)
	}
}
func (a *goPathService) active() string {
	r := a.call(pathServiceEffect{Kind: "get", Name: "SmartVPN"})
	if r.Err != nil {
		return ""
	}
	n := r.Text
	if n == autoGroup || n == fallbackGroup {
		r = a.call(pathServiceEffect{Kind: "get", Name: n})
		if r.Err == nil {
			n = r.Text
		}
	}
	return n
}
func (a *goPathService) region() string {
	if a.s.LockedRegion != "" {
		return a.s.LockedRegion
	}
	r := a.call(pathServiceEffect{Kind: "region", Name: a.active()})
	if r.Found {
		return r.Record.Country
	}
	return ""
}
func (a *goPathService) enabled() []pathTarget {
	out := make([]pathTarget, 0, len(a.s.Targets))
	for _, t := range a.s.Targets {
		if t.Enabled {
			out = append(out, t)
		}
	}
	return out
}
func (a *goPathService) check(ts []pathTarget) []siteCheckResult {
	out := make([]siteCheckResult, 0, len(ts))
	for _, t := range ts {
		out = append(out, a.call(pathServiceEffect{Kind: "check", Target: t}).Result)
	}
	return out
}
func (a *goPathService) pool(region string) []string {
	if region == "" {
		return nil
	}
	facts := a.call(pathServiceEffect{Kind: "candidates"}).Candidates
	out := make([]string, 0, len(facts))
	for _, f := range facts {
		if f.Verified && f.Country == region && f.Age <= regionTTL && !f.Cooldown {
			out = append(out, f.Name)
		}
	}
	sort.Strings(out)
	return out
}
func (a *goPathService) record(rs []siteCheckResult) {
	a.call(pathServiceEffect{Kind: "set", Name: "at", Time: a.now()})
	a.call(pathServiceEffect{Kind: "set", Name: "results", Results: rs})
	a.call(pathServiceEffect{Kind: "set", Name: "failures", Strings: pathFailures(rs)})
	a.set("active", a.active(), false)
	a.set("running", "", a.s.Busy)
}
func (a *goPathService) clearRetry() { a.set("retry-at", "", false); a.set("retry-mode", "", false) }
func (a *goPathService) schedule(mode string) {
	a.set("retry-mode", mode, false)
	a.set("retry-at", a.now().Add(pathRetryDelay).UTC().Format(time.RFC3339Nano), false)
	a.set("phase", "waiting", false)
}
func (a *goPathService) queue(mode, msg string) {
	if a.s.Status.AlertID == 0 || a.s.Status.RetryAt == "" {
		a.set("alert-increment", "", false)
	}
	a.set("alert", msg, false)
	a.schedule(mode)
}
func (a *goPathService) finish() {
	a.set("note", "", false)
	a.set("alert", "", false)
	a.set("demanded", "", false)
	a.set("phase", "idle", false)
	a.clearRetry()
}
func (a *goPathService) cleanup() {
	if a.locked {
		a.unlock()
	}
	if a.round {
		a.lock()
		a.set("busy", "", false)
		if a.recovery {
			a.set("running", "", false)
		}
		a.unlock()
	}
}
func (a *goPathService) commitSwitch(target, from, trigger, evidence string) {
	a.set("pin", target, false)
	a.call(pathServiceEffect{Kind: "save"})
	a.call(pathServiceEffect{Kind: "set", Name: "last-switch", Time: a.now()})
	a.set("append-switch", target, false)
	a.call(pathServiceEffect{Kind: "event", Event: switchEvent{Group: "SmartVPN", FromNode: from, ToNode: target, Trigger: trigger, Evidence: evidence}})
}
func (a *goPathService) switchNode(target, region, trigger, evidence string) error {
	a.lock()
	defer a.unlock()
	if !a.running() {
		return errors.New(pathServiceMessage(18))
	}
	if target == a.active() {
		return nil
	}
	r := a.call(pathServiceEffect{Kind: "region", Name: target})
	if !r.Found || r.Record.Status != regionVerified || r.Record.Country != region {
		return errors.New(pathServiceMessage(19))
	}
	if a.call(pathServiceEffect{Kind: "age", Name: target}).Bool {
		return errors.New(pathServiceMessage(19))
	}
	from := a.active()
	if err := a.call(pathServiceEffect{Kind: "put", Name: target}).Err; err != nil {
		return err
	}
	a.commitSwitch(target, from, trigger, evidence)
	return nil
}
func runGoPathService(q pathServiceRequest, fx pathServiceEffects) (result pathServiceResult) {
	defer fx.Release()
	a := &goPathService{fx: fx, recovery: q.Op == "recovery"}
	defer a.cleanup()
	switch q.Op {
	case "baseline":
		a.baseline()
	case "round":
		a.legacy()
	case "recovery":
		a.recover(q.Mode)
	case "switch":
		result.Err = a.switchNode(q.Target, q.Region, q.Trigger, q.Evidence)
	}
	return
}
func (a *goPathService) baseline() {
	a.lock()
	if !a.running() || a.s.Busy || !a.s.Enabled {
		a.unlock()
		return
	}
	a.set("busy", "", true)
	a.set("phase", "baseline", false)
	a.call(pathServiceEffect{Kind: "proxy"})
	a.unlock()
	rs := a.check(baselinePathTargets())
	a.lock()
	a.record(rs)
	if len(pathFailures(rs)) == 0 {
		a.set("phase", "baseline", false)
		a.set("note", pathServiceMessage(0), false)
		if a.s.Status.RetryMode == "scan" {
			a.clearRetry()
			a.set("alert", "", false)
			if a.s.Status.Demanded && len(otherPathTargets(a.enabled())) > 0 {
				a.schedule("targets")
			}
		}
		a.unlock()
		return
	}
	a.set("phase", "waiting", false)
	if a.s.Chain {
		a.clearRetry()
		a.set("note", pathServiceMessage(1), false)
		a.unlock()
		return
	}
	a.set("note", pathServiceMessage(2), false)
	if a.s.Status.RetryAt == "" {
		a.queue("scan", pathServiceMessage(3))
	}
	a.unlock()
}
func (a *goPathService) legacy() {
	a.lock()
	if !a.running() || a.s.Busy {
		a.unlock()
		return
	}
	active := a.active()
	if active == "" || strings.EqualFold(active, blockedChoice) {
		a.unlock()
		return
	}
	targets := a.enabled()
	if len(targets) == 0 {
		a.unlock()
		return
	}
	a.set("busy", "", true)
	a.call(pathServiceEffect{Kind: "proxy"})
	a.unlock()
	rs := a.check(targets)
	a.lock()
	a.record(rs)
	failures := pathFailures(rs)
	a.set("note", "", false)
	a.set("switched", "", false)
	if len(failures) == 0 {
		a.unlock()
		return
	}
	if a.s.Chain {
		a.set("note", pathServiceMessage(4), false)
		a.unlock()
		return
	}
	region := a.region()
	a.unlock()
	if region == "" {
		a.lock()
		a.set("note", pathServiceMessage(5), false)
		a.unlock()
		return
	}
	tried := map[string]bool{}
	for {
		a.lock()
		if !a.running() {
			a.unlock()
			return
		}
		active = a.active()
		target := ""
		for _, n := range a.pool(region) {
			if n != active && !tried[n] {
				target = n
				break
			}
		}
		if target == "" {
			a.set("note", fmt.Sprintf(pathServiceMessage(6), region, strings.Join(failures, pathServiceMessage(7))), false)
			a.unlock()
			return
		}
		from := a.active()
		if err := a.call(pathServiceEffect{Kind: "put", Name: target}).Err; err != nil {
			a.set("note", pathServiceMessage(8)+err.Error(), false)
			a.unlock()
			return
		}
		tried[target] = true
		a.commitSwitch(target, from, "path_check", fmt.Sprintf(pathServiceMessage(9), strings.Join(failures, pathServiceMessage(7))))
		a.unlock()
		rs = a.check(targets)
		a.lock()
		a.record(rs)
		failures = pathFailures(rs)
		a.set("active", a.active(), false)
		a.unlock()
		if len(failures) == 0 {
			return
		}
	}
}
func (a *goPathService) recover(mode string) {
	a.lock()
	if !a.running() || a.s.Busy {
		a.unlock()
		return
	}
	original := a.active()
	if original == "" || strings.EqualFold(original, blockedChoice) {
		a.unlock()
		return
	}
	region := a.region()
	if mode == "demand" || mode == "manual" {
		a.set("demanded", "", true)
	}
	demanded := a.s.Status.Demanded
	extras := otherPathTargets(a.enabled())
	a.call(pathServiceEffect{Kind: "proxy"})
	a.set("busy", "", true)
	a.set("phase", "scanning", false)
	a.set("switched", "", false)
	a.unlock()
	baseTargets := baselinePathTargets()
	if mode == "targets" {
		base := a.check(baseTargets)
		if len(pathFailures(base)) == 0 {
			other := a.check(extras)
			rs := combinePathResults(base, other)
			a.lock()
			a.record(rs)
			if len(pathFailures(rs)) == 0 {
				a.finish()
				a.unlock()
				return
			}
			a.set("note", pathServiceMessage(10), false)
			a.set("alert", "", false)
			a.schedule("targets")
			a.unlock()
			return
		}
		a.lock()
		a.record(base)
		a.set("note", pathServiceMessage(11), false)
		a.queue("scan", pathServiceMessage(3))
		a.unlock()
		return
	}
	if region == "" {
		a.lock()
		a.set("note", pathServiceMessage(5), false)
		a.set("demanded", "", false)
		a.unlock()
		return
	}
	a.lock()
	candidates := a.pool(region)
	a.unlock()
	ordered := []string{original}
	for _, n := range candidates {
		if n != original {
			ordered = append(ordered, n)
		}
	}
	byNode := map[string][]siteCheckResult{}
	var last []siteCheckResult
	for _, candidate := range ordered {
		if candidate != original {
			if a.switchNode(candidate, region, "access-path", pathServiceMessage(12)) != nil {
				continue
			}
		}
		base := a.check(baseTargets)
		byNode[candidate] = base
		if len(pathFailures(base)) != 0 {
			a.lock()
			a.record(base)
			a.unlock()
			last = base
			continue
		}
		if !demanded || len(extras) == 0 {
			a.lock()
			a.record(base)
			a.finish()
			a.set("note", pathServiceMessage(13), false)
			a.unlock()
			return
		}
		other := a.check(extras)
		last = combinePathResults(base, other)
		a.lock()
		a.record(last)
		if len(pathFailures(last)) == 0 {
			a.finish()
			a.unlock()
			return
		}
		a.unlock()
	}
	a.lock()
	active := a.active()
	a.unlock()
	if active != original {
		_ = a.switchNode(original, region, "access-path-restore", pathServiceMessage(14))
	}
	base := byNode[original]
	if base == nil {
		base = a.check(baseTargets)
	}
	rs := base
	if demanded && len(last) > 0 {
		other := otherResultsFrom(last, baseTargets)
		if len(other) > 0 {
			rs = combinePathResults(base, other)
		}
	}
	a.lock()
	a.record(rs)
	if len(pathFailures(base)) == 0 {
		a.set("note", pathServiceMessage(15), false)
		a.set("alert", "", false)
		if demanded {
			a.schedule("targets")
		}
		a.unlock()
		return
	}
	a.set("note", fmt.Sprintf(pathServiceMessage(16), region), false)
	a.queue("scan", pathServiceMessage(17))
	a.unlock()
}
