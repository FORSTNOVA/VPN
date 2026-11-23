package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"

	"net/http"
	"os"
	"path/filepath"

	"runtime"
	"sort"
	"strconv"
	"strings"

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
	if runtime.GOOS == "android" {
		return healthParams{
			ConnectTimeoutMs:  6000,
			PatrolIntervalSec: 60,
			FailureThreshold:  3,
			CooldownSec:       60,
			MinDwellSec:       30,
			RecoverySuccesses: 3,
		}
	}
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
func (p healthParams) normalizedGo() healthParams {
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
func nextHealthStateGo(current nodeHealth, ok bool, latencyMs int, now time.Time, params healthParams) nodeHealth {
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
	if a.rememberingNode && !a.chainEnabledLocked() && a.store != nil && selected != "" &&
		selected != autoGroup && selected != fallbackGroup && selected != blockedChoice && selected != chainExit && selected != a.lastSavedNode {
		if err := a.store.setValue("lastConnectedNode", selected); err == nil {
			a.lastSavedNode = selected
		}
	}
	return selected
}

func (a *app) switchGroupLocked(target string) error {
	_, err := a.mihomoRequest(http.MethodPut, "/proxies/SmartVPN", map[string]string{"name": target})
	return err
}

func (a *app) recordHealthLocked(record nodeHealth) {
	a.writeHealthRecordLocked(record.Name, record)
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
	pool := candidatesForRegion(a.controlMetaLocked().RuntimeLockedRegion, a.healthDomainLocked().Regions, a.healthDomainLocked().Health, now)
	nodes := make([]proxyNode, 0, len(pool))
	for _, name := range pool {
		nodes = append(nodes, proxyNode{Name: name})
	}
	return chooseReachableNode(nodes, delays, a.controlMetaLocked().RuntimeLockedNode)
}

func (a *app) patrolOnce() {
	if r := a.runAppControl(appServiceRequest{Op: "once"}); r.Err != nil {
		log.Printf("health patrol stopped: state unavailable: %v", r.Err)
	}
}

// patrolLocked runs one health sweep. The caller holds a.mu, matching the rest
// of the service, so a sweep can never race a connect or a group change.
func (a *app) patrolLocked() {
	if r := a.runAppControl(appServiceRequest{Op: "sweep"}); r.Err != nil {
		panic(r.Err)
	}
}

// applyPatrolLocked folds one group measurement into every node the sweep is
// responsible for: the node currently in use plus the locked-region candidates.
func (a *app) applyPatrolLocked(active string, delays map[string]int, now time.Time) {
	runAppHealthService(a, "patrol", active, delays, now)
}

// applyFailedSweepLocked records a sweep whose measurement could not be taken.
// Only the node in use is marked: nothing was learned about the candidates, so
// their earlier measurements stay valid and can still serve as a failover
// target instead of being condemned along with the measurement that failed.
func (a *app) applyFailedSweepLocked(active string, now time.Time) {
	runAppHealthService(a, "failed", active, nil, now)
}

func (a *app) recordSweepLocked(watched []string, delays map[string]int, now time.Time) {
	seen := map[string]bool{}
	for _, name := range watched {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		delay := delays[name]
		record := a.healthValueLocked(name)
		if record.Name == "" {
			record = newHealth(name)
		}
		previous := record.State
		next := nextHealthState(record, delay > 0, delay, now, a.healthMetaLocked().Params)
		a.recordHealthLocked(next)
		if previous != healthUnhealthy && next.State == healthUnhealthy {
			log.Printf("node %s became %s after a failed sweep", name, next.State)
		}
	}
}

// failoverIfUnhealthyLocked moves traffic off the node in use once it is marked
// unhealthy, unless there is no locked region to stay inside.
func (a *app) failoverIfUnhealthyLocked(active string, now time.Time) {
	if a.chainEnabledLocked() {
		return
	}
	if a.healthValueLocked(active).State != healthUnhealthy || a.controlMetaLocked().RuntimeLockedRegion == "" {
		return
	}
	if !a.dwellElapsedLocked(now) {
		return
	}
	a.failoverLocked(active, now, fmt.Sprintf("%s failed %d consecutive sweeps",
		active, a.healthValueLocked(active).ConsecutiveFailures))
}

func (a *app) dwellElapsedLocked(now time.Time) bool {
	if a.healthMetaLocked().LastSwitch.IsZero() {
		return true
	}
	return now.Sub(a.healthMetaLocked().LastSwitch) >= time.Duration(a.healthMetaLocked().Params.MinDwellSec)*time.Second
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
	next := a.healthValueLocked(failed)
	next.Name = failed
	next.State = healthCooldown
	next.CooldownUntil = now.Add(time.Duration(a.healthMetaLocked().Params.CooldownSec) * time.Second)
	next.ConsecutiveSuccesses = 0
	next.UpdatedAt = now
	a.recordHealthLocked(next)

	if err := a.saveSettings(); err != nil {
		log.Printf("could not persist the failover selection: %v", err)
	}
	a.setHealthLastSwitchLocked(now)
	a.setHealthBlockedLocked(false)
	a.setHealthReasonLocked("")
	a.patchControlLocked(controlRuntimeLockedNode, serviceControlState{RuntimeLockedNode: target})
	a.patchControlLocked(controlLockedNode, serviceControlState{LockedNode: target})
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
	pool := candidatesForRegion(a.controlMetaLocked().RuntimeLockedRegion, a.healthDomainLocked().Regions, a.healthDomainLocked().Health, now)
	if len(pool) == 0 {
		return ""
	}
	nodes := make([]proxyNode, 0, len(pool))
	delays := map[string]int{}
	for _, name := range pool {
		nodes = append(nodes, proxyNode{Name: name})
		if health, ok := a.healthRecordLocked(name); ok && health.LatencyMs > 0 {
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
	pool := candidatesForRegion(a.controlMetaLocked().RuntimeLockedRegion, a.healthDomainLocked().Regions, a.healthDomainLocked().Health, now)
	if len(pool) == 0 {
		return "", 0
	}
	timeout := time.Duration(a.healthMetaLocked().Params.ConnectTimeoutMs) * time.Millisecond
	nodes := make([]proxyNode, 0, len(pool))
	delays := map[string]int{}
	for _, name := range pool {
		if health, ok := a.healthRecordLocked(name); ok && health.State == healthUnhealthy {
			continue
		}
		nodes = append(nodes, proxyNode{Name: name})
		if health, ok := a.healthRecordLocked(name); ok && health.LatencyMs > 0 {
			delays[name] = health.LatencyMs
		}
	}
	if best, delay := chooseReachableNode(nodes, delays, ""); best != "" {
		return best, delay
	}
	// Nothing has a fresh measurement; ask Mihomo for the whole group once.
	fresh, err := a.groupDelaysForURLLocked(patrolTargetPrimary, timeout)
	if err != nil {
		return "", 0
	}
	return chooseReachableNode(nodes, fresh, "")
}

// confirmProbeTimeout is short on purpose. It is spent while the service's lock
// is held, and all it has to outlast is a node that is really carrying traffic:
// the sweep gives each node less time than this to answer its own probe.
const confirmProbeTimeout = 5 * time.Second

var confirmProbeTargets = []struct {
	name string
	url  string
}{
	{"Cloudflare Trace", cloudflareTraceURL},
	{"Google 204", "https://connectivitycheck.gstatic.com/generate_204"},
	{"Cloudflare 204", "https://cp.cloudflare.com/generate_204"},
}

// confirmTrafficFlowsLocked asks whether a request can still get through, using
// the path an application takes: the local mixed port, so the kernel routes it
// exactly as it routes the user's own traffic, and an HTTPS address that is not
// the one the sweep measures, so a broken test target cannot stand in for a dead
// region — and a captive portal cannot answer in its place.
//
// It answers a narrower question than a sweep and answers it independently,
// which is what makes it usable as a second opinion: the sweep reports what the
// kernel's delay probe saw, this reports what the tunnel actually carried.
// To avoid false fail-closed triggers when a single probe target is blocked or throttled,
// multiple independent targets are checked.
func (a *app) confirmTrafficFlowsLocked() bool {
	q := adaptiveServiceRequest{Op: "confirm"}
	v := runAdaptiveWithEffects(q, newAdaptiveEffects(nil, a, q))
	if v.Err != nil {
		panic(v.Err)
	}
	return v.Bool
}

// noteSweepAnomalyLocked records that the region measured as dead while a real
// request still got through, so the page can say what happened rather than
// leaving the user to guess why the app did not fail over.
func (a *app) noteSweepAnomalyLocked(failed string) {
	q := adaptiveServiceRequest{Op: "note"}
	e := newAdaptiveEffects(nil, a, q)
	e.failed = failed
	if v := runAdaptiveWithEffects(q, e); v.Err != nil {
		panic(v.Err)
	}
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
	q := adaptiveServiceRequest{Op: "block"}
	e := newAdaptiveEffects(nil, a, q)
	e.failed, e.now, e.reason = failed, now, reason
	if v := runAdaptiveWithEffects(q, e); v.Err != nil {
		panic(v.Err)
	}
}

func (a *app) tryUnblockLocked(now time.Time) {
	runAppHealthService(a, "unblock", "", nil, now)
}

func (a *app) startPatrolLocked() {
	if r := a.runAppControl(appServiceRequest{Op: "start"}); r.Err != nil {
		panic(r.Err)
	}
}

func (a *app) stopPatrolLocked() {
	if r := a.runAppControl(appServiceRequest{Op: "stop"}); r.Err != nil {
		panic(r.Err)
	}
}

func (a *app) patrolLoop(cancel <-chan struct{}, interval time.Duration) {
	q := appServiceRequest{Op: "loop", Interval: interval}
	e := newAppControlEffects(a, q)
	e.cancel = cancel
	if r := runAppControlWithEffects(q, e); r.Err != nil {
		log.Printf("health patrol loop stopped: state unavailable: %v", r.Err)
	}
}

func (a *app) healthSnapshotLocked() map[string]any {
	return a.healthSnapshotFromDomainLocked(a.healthDomainLocked())
}
func (a *app) healthSnapshotFromDomainLocked(h healthDomainProjection) map[string]any {
	q := appServiceRequest{Op: "snapshot"}
	e := newAppControlEffects(a, q)
	e.h = h
	e.haveHealth = true
	r := runAppControlWithEffects(q, e)
	if r.Err != nil {
		panic(r.Err)
	}
	return appHealthProjection(h, r.Plan)
}

func (a *app) updateHealthParams(w http.ResponseWriter, r *http.Request) {
	tracked := &authorityHTTPWriter{ResponseWriter: w}
	defer tracked.recoverFailure()
	var body healthParams
	if err := decodeJSON(r.Body, &body); err != nil {
		http.Error(tracked, "invalid health parameters", http.StatusBadRequest)
		return
	}
	q := appServiceRequest{Op: "params", Params: body}
	e := newAppControlEffects(a, q)
	e.w = tracked
	runAppControlWithEffects(q, e)
}

func (a *app) patrolNow(w http.ResponseWriter, _ *http.Request) {
	q := appServiceRequest{Op: "patrol"}
	e := newAppControlEffects(a, q)
	e.w = w
	runAppControlWithEffects(q, e)
}

func (a *app) switchEvents(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("include") == "logs" {
		a.switchEventsWithLogs(w, r)
		return
	}
	q := appServiceRequest{Op: "events", Limit: r.URL.Query().Get("limit")}
	e := newAppControlEffects(a, q)
	e.w = w
	runAppControlWithEffects(q, e)
}

func (a *app) switchEventsWithLogs(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	events := []appEventPayload{}
	if a.store != nil {
		if rows, err := a.store.recentSwitchEvents(limit); err == nil {
			events = make([]appEventPayload, 0, len(rows))
			for _, v := range rows {
				events = append(events, appEventPayload{v.At.Format(time.RFC3339), v.Group, v.FromNode, v.ToNode, v.Trigger, v.Evidence})
			}
		}
	}
	serviceLogs := tailSanitizedLog(filepath.Join(a.home, "service.log"), 150, a.redactor)
	kernelLogs := tailSanitizedLog(filepath.Join(a.home, "mihomo.log"), 150, a.redactor)
	writeJSON(w, map[string]any{
		"events":      events,
		"serviceLogs": serviceLogs,
		"kernelLogs":  kernelLogs,
	})
}

func tailSanitizedLog(path string, maxLines int, redactor *logRedactor) []string {
	if path == "" || maxLines <= 0 {
		return []string{}
	}
	f, err := os.Open(path)
	if err != nil {
		return []string{}
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() == 0 {
		return []string{}
	}
	const maxBytes = 64 << 10
	offset := int64(0)
	if info.Size() > maxBytes {
		offset = info.Size() - maxBytes
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return []string{}
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxBytes))
	if err != nil {
		return []string{}
	}
	if redactor != nil {
		redactor.mu.Lock()
		secrets := redactor.secrets
		redactor.mu.Unlock()
		for _, secret := range secrets {
			if len(secret) > 0 {
				raw = bytes.ReplaceAll(raw, secret, []byte(redacted))
			}
		}
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if offset > 0 && len(lines) > 1 {
		lines = lines[1:]
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	if len(out) > maxLines {
		out = out[len(out)-maxLines:]
	}
	return out
}

var errNoLockableRegion = errors.New("no verified node matches that region")
