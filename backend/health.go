package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"
)

const (
	healthHealthy   = "HEALTHY"
	healthSuspect   = "SUSPECT"
	healthUnhealthy = "UNHEALTHY"
	healthCooldown  = "COOLDOWN"
	healthRecovery  = "RECOVERY"
)

const (
	patrolTargetPrimary   = "https://www.youtube.com/generate_204"
	patrolTargetSecondary = "https://chatgpt.com/"
)

// healthParams mirrors the tunables the design document lists. The defaults
// follow its initial suggestion and are meant to be recalibrated on a real
// network.
type healthParams struct {
	ConnectTimeoutMs  int `json:"connectTimeoutMs"`
	PatrolIntervalSec int `json:"patrolIntervalSec"`
	FailureThreshold  int `json:"failureThreshold"`
	CooldownSec       int `json:"cooldownSec"`
	MinDwellSec       int `json:"minDwellSec"`
	RecoverySuccesses int `json:"recoverySuccesses"`
}

// A sweep is one bulk delay call, and the kernel answers it by testing every
// node of the subscription: the interval below is therefore what the
// subscription's servers see, not a local timer. The design document suggests
// 30 seconds and then says these values have to be calibrated on a real
// network; 60 is that calibration. Every probe is a connection from the user's
// own address, a subscription may limit how many it allows, and half the rate
// halves what this client contributes to that limit. Anything that wants the
// faster cadence can set it back in the interface, where the value is editable
// and clamped to a range the loop can run with.
func defaultHealthParams() healthParams {
	return healthParams{
		ConnectTimeoutMs:  3000,
		PatrolIntervalSec: 60,
		FailureThreshold:  2,
		CooldownSec:       60,
		MinDwellSec:       30,
		RecoverySuccesses: 3,
	}
}

func (p healthParams) encode() (string, error) {
	body, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func (p *healthParams) decode(body string) error {
	return json.Unmarshal([]byte(body), p)
}

// normalized keeps stored or user-supplied values inside ranges the patrol loop
// can actually run with.
func (p healthParams) normalized() healthParams {
	clamp := func(value, low, high int) int {
		if value < low {
			return low
		}
		if value > high {
			return high
		}
		return value
	}
	return healthParams{
		ConnectTimeoutMs:  clamp(p.ConnectTimeoutMs, 1000, 15000),
		PatrolIntervalSec: clamp(p.PatrolIntervalSec, 10, 600),
		FailureThreshold:  clamp(p.FailureThreshold, 1, 10),
		CooldownSec:       clamp(p.CooldownSec, 10, 3600),
		MinDwellSec:       clamp(p.MinDwellSec, 0, 600),
		RecoverySuccesses: clamp(p.RecoverySuccesses, 1, 10),
	}
}

type nodeHealth struct {
	Name                 string    `json:"name"`
	State                string    `json:"state"`
	ConsecutiveFailures  int       `json:"consecutiveFailures"`
	ConsecutiveSuccesses int       `json:"consecutiveSuccesses"`
	LatencyMs            int       `json:"latencyMs"`
	LastSuccessAt        time.Time `json:"lastSuccessAt,omitempty"`
	CooldownUntil        time.Time `json:"cooldownUntil,omitempty"`
	UpdatedAt            time.Time `json:"updatedAt,omitempty"`
}

func newHealth(name string) nodeHealth {
	return nodeHealth{Name: name, State: healthHealthy}
}

func (h nodeHealth) cooldownActive(now time.Time) bool {
	return !h.CooldownUntil.IsZero() && now.Before(h.CooldownUntil)
}

// nextHealthState folds one probe result into a node's health. It is pure so
// the transition rules stay testable without a running Mihomo.
func nextHealthState(current nodeHealth, ok bool, latencyMs int, now time.Time, params healthParams) nodeHealth {
	next := current
	next.Name = current.Name
	next.UpdatedAt = now

	if ok {
		next.ConsecutiveSuccesses++
		next.ConsecutiveFailures = 0
		next.LastSuccessAt = now
		next.LatencyMs = latencyMs
		switch {
		case current.cooldownActive(now):
			// A cooling node keeps its sentence even when it answers again.
			next.State = healthCooldown
		case current.State == healthCooldown || current.State == healthRecovery:
			if next.ConsecutiveSuccesses >= params.RecoverySuccesses {
				next.State = healthHealthy
			} else {
				next.State = healthRecovery
			}
		default:
			next.State = healthHealthy
		}
		return next
	}

	next.ConsecutiveFailures++
	next.ConsecutiveSuccesses = 0
	next.LatencyMs = 0
	switch {
	case current.cooldownActive(now):
		next.State = healthCooldown
	case next.ConsecutiveFailures >= params.FailureThreshold:
		next.State = healthUnhealthy
	default:
		next.State = healthSuspect
	}
	return next
}

// candidatesForRegion lists the nodes eligible for a locked region: verified in
// that country, still inside the verification lifetime, and not serving a
// cooldown. Anything unverified, stale or in conflict is left out, which is what
// keeps the lock fail closed.
func candidatesForRegion(country string, regions map[string]regionRecord, health map[string]nodeHealth, now time.Time) []string {
	if country == "" {
		return nil
	}
	candidates := make([]string, 0, len(regions))
	for name, record := range regions {
		if record.Status != regionVerified || record.Country != country {
			continue
		}
		if now.Sub(record.VerifiedAt) > regionTTL {
			continue
		}
		if current, ok := health[name]; ok && current.cooldownActive(now) {
			continue
		}
		candidates = append(candidates, name)
	}
	// A stable order keeps the UI list from reshuffling and makes selection
	// deterministic when two nodes report the same latency.
	sort.Strings(candidates)
	return candidates
}

// activeNodeLocked resolves the node the SmartVPN group currently sends traffic
// through, looking one level into the automatic groups.
func (a *app) activeNodeLocked() string {
	group, err := a.mihomoRequest(http.MethodGet, "/proxies/SmartVPN", nil)
	if err != nil {
		return ""
	}
	selected, _ := group["now"].(string)
	if selected == autoGroup || selected == fallbackGroup {
		if nested, nestedErr := a.mihomoRequest(http.MethodGet, "/proxies/"+selected, nil); nestedErr == nil {
			selected, _ = nested["now"].(string)
		}
	}
	return selected
}

func (a *app) switchGroupLocked(target string) error {
	_, err := a.mihomoRequest(http.MethodPut, "/proxies/SmartVPN", map[string]string{"name": target})
	return err
}

func (a *app) recordHealthLocked(record nodeHealth) {
	a.health[record.Name] = record
	if a.store != nil {
		if err := a.store.saveHealth(record); err != nil {
			log.Printf("could not persist node health: %v", err)
		}
	}
}

func (a *app) recordSwitchLocked(event switchEvent) {
	if event.At.IsZero() {
		event.At = time.Now()
	}
	log.Printf("node switch: %s -> %s (%s) %s", event.FromNode, event.ToNode, event.Trigger, event.Evidence)
	if a.store != nil {
		if err := a.store.appendSwitchEvent(event); err != nil {
			log.Printf("could not persist switch event: %v", err)
		}
	}
}

// bestLockedCandidateLocked picks the fastest candidate inside the locked region
// from a set of measured delays, preferring the node already pinned. An empty
// result means the region has nothing usable right now.
func (a *app) bestLockedCandidateLocked(delays map[string]int, now time.Time) (string, int) {
	pool := candidatesForRegion(a.lockedRegion, a.regions, a.health, now)
	nodes := make([]proxyNode, 0, len(pool))
	for _, name := range pool {
		nodes = append(nodes, proxyNode{Name: name})
	}
	return chooseReachableNode(nodes, delays, a.lockedNode)
}

func (a *app) patrolOnce() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.patrolLocked()
}

// patrolLocked runs one health sweep. The caller holds a.mu, matching the rest
// of the service, so a sweep can never race a connect or a group change.
func (a *app) patrolLocked() {
	if !a.kernelRunningLocked() {
		return
	}
	if a.regionJobRunningLocked() {
		return
	}
	now := time.Now()
	active := a.activeNodeLocked()
	if active == "" || active == blockedChoice || active == "DIRECT" {
		if a.blocked {
			a.tryUnblockLocked(now)
		}
		return
	}
	timeout := time.Duration(a.healthParams.ConnectTimeoutMs) * time.Millisecond
	// Mihomo only offers a bulk delay endpoint per group: its per-proxy route
	// cannot address the node names these subscriptions use, which all contain
	// spaces, pipes or emoji. One call covers every node we watch.
	delays, err := a.groupDelaysForURL(patrolTargetPrimary, timeout)
	if err != nil {
		// A group that cannot be measured is not a healthy group. When the whole
		// line is down the bulk call itself has no answer to give, and skipping
		// the sweep would leave the user pinned to a dead node — every request
		// timing out while the UI still reports the node as healthy.
		log.Printf("health sweep could not measure the group (%v); counting it as a failed sweep", err)
		a.applyFailedSweepLocked(active, now)
		return
	}
	a.applyPatrolLocked(active, delays, now)
}

// applyPatrolLocked folds one group measurement into every node the sweep is
// responsible for: the node currently in use plus the locked-region candidates.
func (a *app) applyPatrolLocked(active string, delays map[string]int, now time.Time) {
	watched := append([]string{active}, candidatesForRegion(a.lockedRegion, a.regions, a.health, now)...)
	// A sweep that reached anything has put the measurement machinery back in
	// order, so an anomaly reported by an earlier one no longer describes now.
	for _, name := range watched {
		if delays[name] > 0 {
			a.sweepNote = ""
			break
		}
	}
	a.recordSweepLocked(watched, delays, now)
	a.failoverIfUnhealthyLocked(active, now)
}

// applyFailedSweepLocked records a sweep whose measurement could not be taken.
// Only the node in use is marked: nothing was learned about the candidates, so
// their earlier measurements stay valid and can still serve as a failover
// target instead of being condemned along with the measurement that failed.
func (a *app) applyFailedSweepLocked(active string, now time.Time) {
	a.recordSweepLocked([]string{active}, map[string]int{active: 0}, now)
	a.failoverIfUnhealthyLocked(active, now)
}

func (a *app) recordSweepLocked(watched []string, delays map[string]int, now time.Time) {
	seen := map[string]bool{}
	for _, name := range watched {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		delay := delays[name]
		record := a.health[name]
		if record.Name == "" {
			record = newHealth(name)
		}
		previous := record.State
		next := nextHealthState(record, delay > 0, delay, now, a.healthParams)
		a.recordHealthLocked(next)
		if previous != healthUnhealthy && next.State == healthUnhealthy {
			log.Printf("node %s became %s after a failed sweep", name, next.State)
		}
	}
}

// failoverIfUnhealthyLocked moves traffic off the node in use once it is marked
// unhealthy, unless there is no locked region to stay inside.
func (a *app) failoverIfUnhealthyLocked(active string, now time.Time) {
	if a.health[active].State != healthUnhealthy || a.lockedRegion == "" {
		return
	}
	if !a.dwellElapsedLocked(now) {
		return
	}
	a.failoverLocked(active, now, fmt.Sprintf("%s failed %d consecutive sweeps",
		active, a.health[active].ConsecutiveFailures))
}

func (a *app) dwellElapsedLocked(now time.Time) bool {
	if a.lastSwitch.IsZero() {
		return true
	}
	return now.Sub(a.lastSwitch) >= time.Duration(a.healthParams.MinDwellSec)*time.Second
}

// failoverLocked switches to a verified node of the same region. When the pool
// is exhausted it blocks protected traffic rather than crossing regions.
func (a *app) failoverLocked(failed string, now time.Time, evidence string) {
	target, delay := a.pickFailoverTargetLocked(now)
	if target == "" {
		a.blockLocked(failed, now, "同地区已无可用节点")
		return
	}
	if err := a.switchGroupLocked(target); err != nil {
		a.blockLocked(failed, now, "无法切换到同地区备用节点")
		return
	}
	next := a.health[failed]
	next.Name = failed
	next.State = healthCooldown
	next.CooldownUntil = now.Add(time.Duration(a.healthParams.CooldownSec) * time.Second)
	next.ConsecutiveSuccesses = 0
	next.UpdatedAt = now
	a.recordHealthLocked(next)

	if err := a.saveSettings(); err != nil {
		log.Printf("could not persist the failover selection: %v", err)
	}
	a.lastSwitch = now
	a.blocked = false
	a.blockReason = ""
	a.lockedNode = target
	a.settings.LockedNode = target
	a.recordSwitchLocked(switchEvent{
		At: now, Group: "SmartVPN", FromNode: failed, ToNode: target,
		Trigger:  "unhealthy",
		Evidence: fmt.Sprintf("%s; %s answered in %d ms", evidence, target, delay),
	})
}

// bestKnownCandidateLocked picks the fastest candidate of the locked region
// using the latencies the sweep already measured. It never contacts the kernel,
// so it also works while disconnected.
func (a *app) bestKnownCandidateLocked(now time.Time) string {
	pool := candidatesForRegion(a.lockedRegion, a.regions, a.health, now)
	if len(pool) == 0 {
		return ""
	}
	nodes := make([]proxyNode, 0, len(pool))
	delays := map[string]int{}
	for _, name := range pool {
		nodes = append(nodes, proxyNode{Name: name})
		if health, ok := a.health[name]; ok && health.LatencyMs > 0 {
			delays[name] = health.LatencyMs
		}
	}
	if best, _ := chooseReachableNode(nodes, delays, ""); best != "" {
		return best
	}
	return pool[0]
}

// pickFailoverTargetLocked chooses the fastest ready node from the locked
// region, preferring one already known to be healthy.
func (a *app) pickFailoverTargetLocked(now time.Time) (string, int) {
	pool := candidatesForRegion(a.lockedRegion, a.regions, a.health, now)
	if len(pool) == 0 {
		return "", 0
	}
	timeout := time.Duration(a.healthParams.ConnectTimeoutMs) * time.Millisecond
	nodes := make([]proxyNode, 0, len(pool))
	delays := map[string]int{}
	for _, name := range pool {
		if health, ok := a.health[name]; ok && health.State == healthUnhealthy {
			continue
		}
		nodes = append(nodes, proxyNode{Name: name})
		if health, ok := a.health[name]; ok && health.LatencyMs > 0 {
			delays[name] = health.LatencyMs
		}
	}
	if best, delay := chooseReachableNode(nodes, delays, ""); best != "" {
		return best, delay
	}
	// Nothing has a fresh measurement; ask Mihomo for the whole group once.
	fresh, err := a.groupDelaysForURL(patrolTargetPrimary, timeout)
	if err != nil {
		return "", 0
	}
	return chooseReachableNode(nodes, fresh, "")
}

// confirmProbeTimeout is short on purpose. It is spent while the service's lock
// is held, and all it has to outlast is a node that is really carrying traffic:
// the sweep gives each node less time than this to answer its own probe.
const confirmProbeTimeout = 5 * time.Second

// confirmTrafficFlowsLocked asks whether a request can still get through, using
// the path an application takes: the local mixed port, so the kernel routes it
// exactly as it routes the user's own traffic, and an HTTPS address that is not
// the one the sweep measures, so a broken test target cannot stand in for a dead
// region — and a captive portal cannot answer in its place.
//
// It answers a narrower question than a sweep and answers it independently,
// which is what makes it usable as a second opinion: the sweep reports what the
// kernel's delay probe saw, this reports what the tunnel actually carried.
func (a *app) confirmTrafficFlowsLocked() bool {
	if a.confirmProbe != nil {
		return a.confirmProbe()
	}
	if a.blocked {
		// The group already points at REJECT, so a request would be refused by
		// our own rule and the answer would say nothing about the region.
		// Recovering from a block is the unblock path's job, not this one's.
		return false
	}
	if !a.kernelRunningLocked() || a.mixedPort == 0 {
		return false
	}
	proxyURL := &url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort("127.0.0.1", strconv.Itoa(a.mixedPort)),
	}
	return pathPassed(checkSiteWithin(proxyURL, "通路确认", cloudflareTraceURL, confirmProbeTimeout))
}

// noteSweepAnomalyLocked records that the region measured as dead while a real
// request still got through, so the page can say what happened rather than
// leaving the user to guess why the app did not fail over.
func (a *app) noteSweepAnomalyLocked(failed string) {
	if a.sweepNote == "" {
		log.Printf("the locked region measured as exhausted while a request through %s still got through; not blocking",
			failed)
	}
	a.sweepNote = "同地区节点在延迟测试里全部不通，但经当前节点的实际请求仍然通行，因此没有阻断；以实测为准。"
}

// blockLocked points the group at REJECT so protected traffic fails closed
// instead of falling through to DIRECT — once an independent probe agrees that
// traffic really cannot flow.
//
// The verdict it acts on comes from one bulk delay measurement, and a
// measurement can fail for its own reasons: the kernel's test target, its
// resolver, or the delay API itself can break while the tunnel is perfectly
// usable. A region whose nodes are all reported dead while a request through the
// node in use still answers is a broken measurement, not a broken region, and
// blocking there would turn the app's own diagnostic trouble into the user's
// outage. Nothing leaks by waiting: the group is left where it is, on a concrete
// node of the locked region, and the region is still never crossed.
func (a *app) blockLocked(failed string, now time.Time, reason string) {
	if a.confirmTrafficFlowsLocked() {
		a.noteSweepAnomalyLocked(failed)
		return
	}
	if err := a.switchGroupLocked(blockedChoice); err != nil {
		log.Printf("could not block traffic: %v", err)
		return
	}
	a.blocked = true
	a.blockReason = reason
	a.lastSwitch = now
	a.sweepNote = ""
	a.recordSwitchLocked(switchEvent{
		At: now, Group: "SmartVPN", FromNode: failed, ToNode: blockedChoice,
		Trigger: "region_exhausted", Evidence: reason,
	})
}

func (a *app) tryUnblockLocked(now time.Time) {
	target, delay := a.pickFailoverTargetLocked(now)
	if target == "" {
		return
	}
	if err := a.switchGroupLocked(target); err != nil {
		return
	}
	a.blocked = false
	a.blockReason = ""
	a.sweepNote = ""
	a.lastSwitch = now
	a.lockedNode = target
	a.settings.LockedNode = target
	_ = a.saveSettings()
	a.recordSwitchLocked(switchEvent{
		At: now, Group: "SmartVPN", FromNode: blockedChoice, ToNode: target,
		Trigger:  "region_recovered",
		Evidence: fmt.Sprintf("%s answered in %d ms", target, delay),
	})
}

func (a *app) startPatrolLocked() {
	if a.patrolCancel != nil {
		return
	}
	cancel := make(chan struct{})
	a.patrolCancel = cancel
	interval := time.Duration(a.healthParams.PatrolIntervalSec) * time.Second
	go a.patrolLoop(cancel, interval)
}

func (a *app) stopPatrolLocked() {
	if a.patrolCancel != nil {
		close(a.patrolCancel)
		a.patrolCancel = nil
	}
	if !a.blocked {
		return
	}
	// Leaving the group on REJECT would silently drop traffic on reconnect.
	if a.kernelRunningLocked() {
		if err := a.switchGroupLocked(a.selectedChoice()); err != nil {
			log.Printf("could not clear the fail-closed selection: %v", err)
		}
	}
	a.blocked = false
	a.blockReason = ""
}

func (a *app) patrolLoop(cancel <-chan struct{}, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-cancel:
			return
		case <-ticker.C:
			a.patrolOnce()
		}
	}
}

func (a *app) healthSnapshotLocked() map[string]any {
	counts := map[string]int{
		healthHealthy: 0, healthSuspect: 0, healthUnhealthy: 0,
		healthCooldown: 0, healthRecovery: 0,
	}
	now := time.Now()
	for _, record := range a.health {
		if record.cooldownActive(now) {
			counts[healthCooldown]++
			continue
		}
		counts[record.State]++
	}
	return map[string]any{
		"counts":       counts,
		"params":       a.healthParams,
		"lockedRegion": a.lockedRegion,
		"poolSize":     len(candidatesForRegion(a.lockedRegion, a.regions, a.health, now)),
		"sweepNote":    a.sweepNote,
	}
}

func (a *app) updateHealthParams(w http.ResponseWriter, r *http.Request) {
	var body healthParams
	if err := decodeJSON(r.Body, &body); err != nil {
		http.Error(w, "invalid health parameters", http.StatusBadRequest)
		return
	}
	if body.PatrolIntervalSec != 0 && body.PatrolIntervalSec < 10 {
		http.Error(w, "the patrol interval must be at least 10 seconds", http.StatusBadRequest)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	params := body.normalized()
	a.healthParams = params
	if a.store != nil {
		if err := a.store.saveHealthParams(params); err != nil {
			http.Error(w, "could not save the health parameters", http.StatusInternalServerError)
			return
		}
	}
	if a.patrolCancel != nil && params.PatrolIntervalSec != 0 {
		// Restart the loop so a new interval applies immediately.
		close(a.patrolCancel)
		a.patrolCancel = nil
		a.startPatrolLocked()
	}
	a.startPathMonitorLocked()
	writeJSON(w, map[string]any{"ok": true, "params": a.healthParams})
}

func (a *app) patrolNow(w http.ResponseWriter, _ *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.kernelRunningLocked() {
		http.Error(w, "connect before running a health sweep", http.StatusConflict)
		return
	}
	a.patrolLocked()
	writeJSON(w, map[string]any{"ok": true, "health": a.healthSnapshotLocked()})
}

func (a *app) switchEvents(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 500 {
			limit = parsed
		}
	}
	if a.store == nil {
		http.Error(w, "the local database is not available", http.StatusInternalServerError)
		return
	}
	events, err := a.store.recentSwitchEvents(limit)
	if err != nil {
		http.Error(w, "could not read the switch history", http.StatusInternalServerError)
		return
	}
	type eventPayload struct {
		At       string `json:"at"`
		Group    string `json:"group"`
		FromNode string `json:"fromNode"`
		ToNode   string `json:"toNode"`
		Trigger  string `json:"trigger"`
		Evidence string `json:"evidence"`
	}
	payload := make([]eventPayload, 0, len(events))
	for _, event := range events {
		payload = append(payload, eventPayload{
			At: event.At.Format(time.RFC3339), Group: event.Group,
			FromNode: event.FromNode, ToNode: event.ToNode,
			Trigger: event.Trigger, Evidence: event.Evidence,
		})
	}
	writeJSON(w, map[string]any{"events": payload})
}

var errNoLockableRegion = errors.New("no verified node matches that region")
