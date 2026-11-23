package main

import (
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// The path monitor asks the sites the user actually cares about, through the
// tunnel, on a timer. It exists because mihomo's latency probe can be perfectly
// happy while the path to a site is not: a node that answers in 80 ms is no use
// if YouTube will not load through it.
type pathTarget struct {
	Label   string `json:"label"`
	URL     string `json:"url"`
	Builtin bool   `json:"builtin,omitempty"`
	Enabled bool   `json:"enabled"`
}

type pathCheckSettings struct {
	Enabled     bool         `json:"enabled"`
	IntervalSec int          `json:"intervalSec"`
	Targets     []pathTarget `json:"targets"`
}

// The presets are the sites people buy a subscription for. Two are on by
// default; the rest wait to be ticked, and are always offered even if a saved
// configuration forgot them.
var pathCheckPresets = []pathTarget{
	{Label: "Google", URL: "https://www.google.com/generate_204", Builtin: true, Enabled: true},
	{Label: "YouTube", URL: "https://www.youtube.com/generate_204", Builtin: true, Enabled: true},
	{Label: "GitHub", URL: "https://github.com/", Builtin: true},
	{Label: "Claude", URL: "https://claude.ai/", Builtin: true},
	{Label: "ChatGPT", URL: "https://chatgpt.com/", Builtin: true},
	{Label: "Twitch", URL: "https://www.twitch.tv/", Builtin: true},
}

const (
	pathCheckMinInterval     = 30
	pathCheckMaxInterval     = 3600
	pathCheckDefaultInterval = 120
	pathCheckMaxTargets      = 20
	pathDemandPollInterval   = time.Second
	pathRetryDelay           = 30 * time.Second
)

// pathStatus is what the page shows: the last round's answers and what the
// monitor did about them.
type pathStatus struct {
	At         time.Time         `json:"at,omitempty"`
	Running    bool              `json:"running"`
	ActiveNode string            `json:"activeNode,omitempty"`
	Results    []siteCheckResult `json:"results"`
	Failures   []string          `json:"failures,omitempty"`
	Switched   []string          `json:"switched,omitempty"`
	Note       string            `json:"note,omitempty"`
	AlertID    uint64            `json:"alertId,omitempty"`
	Alert      string            `json:"alert,omitempty"`
	RetryAt    string            `json:"retryAt,omitempty"`
	RetryMode  string            `json:"retryMode,omitempty"`
	Demanded   bool              `json:"demanded,omitempty"`
	Phase      string            `json:"phase,omitempty"`
}

// pathChecker is the round's measurement, injectable so the decision machine can
// be tested without a network.
type pathChecker func(proxyAddress string, targets []pathTarget) []siteCheckResult

func defaultPathChecks() pathCheckSettings {
	return pathCheckSettings{
		Enabled:     false,
		IntervalSec: pathCheckDefaultInterval,
		Targets:     append([]pathTarget(nil), pathCheckPresets...),
	}
}

// pathPassed is the rule the user chose: anything the site answered counts as a
// working path, including the 403 and 429 these sites hand to anything that is
// not a browser.
func pathPassed(result siteCheckResult) bool {
	return result.State != "" && result.State != "error" && result.State != "skipped"
}

func pathFailures(results []siteCheckResult) []string {
	failures := make([]string, 0, len(results))
	for _, result := range results {
		if !pathPassed(result) {
			failures = append(failures, result.Name)
		}
	}
	return failures
}

// normalizePathChecks validates what the page sent and fills in the presets it
// left out, so the built-in targets are always on offer.
func normalizePathChecks(input pathCheckSettings) (pathCheckSettings, error) {
	r := runAppPathMonitorService(nil, pathMonitorServiceRequest{Op: "normalize", Settings: input}, nil, nil)
	return r.Settings, r.Err
}

func isPresetURL(raw string) bool {
	for _, preset := range pathCheckPresets {
		if preset.URL == raw {
			return true
		}
	}
	return false
}

func (a *app) enabledPathTargetsLocked() []pathTarget {
	targets := make([]pathTarget, 0, len(a.pathConfigLocked().Targets))
	for _, target := range a.pathConfigLocked().Targets {
		if target.Enabled {
			targets = append(targets, target)
		}
	}
	return targets
}

// pathRegionLocked is the region the monitor may switch inside: the locked one
// when there is one, otherwise wherever the node in use was measured.
func (a *app) pathRegionLocked() string {
	if a.controlMetaLocked().RuntimeLockedRegion != "" {
		return a.controlMetaLocked().RuntimeLockedRegion
	}
	if record, ok := a.regionRecordLocked(a.activeNodeLocked()); ok {
		return record.Country
	}
	return ""
}

// nextPathCandidateLocked hands out the region's candidates one by one, skipping
// the node in use and anything this round has already tried.
func (a *app) nextPathCandidateLocked(region string, tried map[string]bool) string {
	active := a.activeNodeLocked()
	for _, name := range candidatesForRegion(region, a.healthDomainLocked().Regions, a.healthDomainLocked().Health, time.Now()) {
		if name == active || tried[name] {
			continue
		}
		return name
	}
	return ""
}

// checkPathTargets asks each target through the tunnel, one after another. They
// are sequential on purpose: this runs on a timer, and a burst of parallel
// requests is what makes a weak relay drop connections.
func checkPathTargets(proxyAddress string, targets []pathTarget) []siteCheckResult {
	proxyURL := &url.URL{Scheme: "http", Host: proxyAddress}
	results := make([]siteCheckResult, 0, len(targets))
	for _, target := range targets {
		results = append(results, checkSite(proxyURL, target.Label, target.URL))
	}
	return results
}

// startPathMonitorLocked starts the timer. Like the health patrol it only makes
// sense while connected, so connect starts it and disconnect stops it.
func (a *app) startPathMonitorLocked() {
	runAppPathMonitorService(a, pathMonitorServiceRequest{Op: "start"}, nil, nil)
}

func (a *app) stopPathMonitorLocked() {
	runAppPathMonitorService(a, pathMonitorServiceRequest{Op: "stop"}, nil, nil)
}

func (a *app) pathMonitorLoop(cancel <-chan struct{}, interval time.Duration) {
	runAppPathMonitorService(a, pathMonitorServiceRequest{Op: "loop", Guard: true, Interval: interval}, nil, cancel)
}

type pathConnectionSample struct {
	upload int64
}

// pathAccessDemand watches Mihomo's live connection table rather than polling
// optional sites. A new connection or new upload bytes to an enabled target is
// evidence that an application is trying to open that site.
func (a *app) pathAccessDemand(previous map[string]pathConnectionSample) []string {
	return runAppPathMonitorService(a, pathMonitorServiceRequest{Op: "demand", Previous: previous}, nil, nil).Labels
}

func normalizePathHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}

func pathHostMatches(host, target string) bool {
	return target != "" && (host == target || strings.HasSuffix(host, "."+target))
}

func baselinePathTargets() []pathTarget {
	return append([]pathTarget(nil), pathCheckPresets[:2]...)
}

func otherPathTargets(targets []pathTarget) []pathTarget {
	other := make([]pathTarget, 0, len(targets))
	for _, target := range targets {
		if target.URL == pathCheckPresets[0].URL || target.URL == pathCheckPresets[1].URL {
			continue
		}
		other = append(other, target)
	}
	return other
}

func combinePathResults(baseline, other []siteCheckResult) []siteCheckResult {
	combined := make([]siteCheckResult, 0, len(baseline)+len(other))
	combined = append(combined, baseline...)
	combined = append(combined, other...)
	return combined
}

func (a *app) runPathBaselineRound(check pathChecker) {
	runAppPathService(a, check, pathServiceRequest{Op: "baseline"})
}

func (a *app) queuePathRetryLocked(mode, message string) {
	if a.pathStatusLocked().AlertID == 0 || a.pathStatusLocked().RetryAt == "" {
		a.applyPathCommandLocked(servicePathCommand{Action: pathStateIncrementAlert})
	}
	a.applyPathCommandLocked(servicePathCommand{Action: pathStateAlert, Text: message})
	a.applyPathCommandLocked(servicePathCommand{Action: pathStateRetryMode, Text: mode})
	a.applyPathCommandLocked(servicePathCommand{Action: pathStateRetryAt, Text: time.Now().Add(pathRetryDelay).UTC().Format(time.RFC3339Nano)})
	a.applyPathCommandLocked(servicePathCommand{Action: pathStatePhase, Text: "waiting"})
}

func (a *app) clearPathRetryLocked() {
	a.applyPathCommandLocked(servicePathCommand{Action: pathStateRetryAt})
	a.applyPathCommandLocked(servicePathCommand{Action: pathStateRetryMode})
}

func (a *app) takeDuePathRetry(now time.Time) string {
	return runAppPathMonitorService(a, pathMonitorServiceRequest{Op: "retry", Now: now}, nil, nil).Mode
}

// beginPathRound takes the snapshot a round needs. The requests themselves run
// without the lock: holding it for seconds of HTTP would freeze the API the
// window is polling.
func (a *app) beginPathRound() ([]pathTarget, string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.kernelRunningLocked() || a.pathStateLocked().Busy {
		return nil, "", false
	}
	active := a.activeNodeLocked()
	if active == "" || strings.EqualFold(active, blockedChoice) {
		return nil, "", false
	}
	targets := a.enabledPathTargetsLocked()
	if len(targets) == 0 {
		return nil, "", false
	}
	a.applyPathCommandLocked(servicePathCommand{Action: pathStateBeginRound})
	return targets, net.JoinHostPort("127.0.0.1", strconv.Itoa(a.mixedPort)), true
}

// runPathRound is one pass: measure, and when the path is broken walk the
// region's candidates until one carries it. It stops and reports when they run
// out — protected traffic is never blocked for a site being unreachable, because
// one blocked site is not the same thing as a broken tunnel.
func (a *app) runPathRound(check pathChecker) {
	runAppPathService(a, check, pathServiceRequest{Op: "round"})
}

// runPathRecovery checks the mandatory Google/YouTube baseline before any
// optional site. The broader loop is entered by an observed access attempt, or
// by an explicit user request; timer ticks alone only check the baseline.
func (a *app) runPathRecovery(check pathChecker, mode string) {
	runAppPathService(a, check, pathServiceRequest{Op: "recovery", Mode: mode})
}

func otherResultsFrom(results []siteCheckResult, baseline []pathTarget) []siteCheckResult {
	baselineNames := map[string]bool{}
	for _, target := range baseline {
		baselineNames[target.Label] = true
	}
	other := make([]siteCheckResult, 0, len(results))
	for _, result := range results {
		if !baselineNames[result.Name] {
			other = append(other, result)
		}
	}
	return other
}

func (a *app) switchPathNode(target, region, trigger, evidence string) error {
	return runAppPathService(a, nil, pathServiceRequest{Op: "switch", Target: target, Region: region, Trigger: trigger, Evidence: evidence}).Err
}

func (a *app) activeNodeLockedSafe() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.activeNodeLocked()
}

func (a *app) finishPathRecoveryLocked() {
	a.applyPathCommandLocked(servicePathCommand{Action: pathStateNote})
	a.applyPathCommandLocked(servicePathCommand{Action: pathStateAlert})
	a.applyPathCommandLocked(servicePathCommand{Action: pathStateDemanded})
	a.applyPathCommandLocked(servicePathCommand{Action: pathStatePhase, Text: "idle"})
	a.clearPathRetryLocked()
}

func (a *app) schedulePathRetryLocked(mode string) {
	a.applyPathCommandLocked(servicePathCommand{Action: pathStateRetryMode, Text: mode})
	a.applyPathCommandLocked(servicePathCommand{Action: pathStateRetryAt, Text: time.Now().Add(pathRetryDelay).UTC().Format(time.RFC3339Nano)})
	a.applyPathCommandLocked(servicePathCommand{Action: pathStatePhase, Text: "waiting"})
}

func (a *app) recordPathResultsLocked(results []siteCheckResult) {
	a.pathClockCommandLocked(pathStateAt, time.Now(), "", 0)
	a.applyPathCommandLocked(servicePathCommand{Action: pathStateResults, Results: results})
	a.applyPathCommandLocked(servicePathCommand{Action: pathStateFailures, Strings: pathFailures(results)})
	a.applyPathCommandLocked(servicePathCommand{Action: pathStateActiveNode, Text: a.activeNodeLocked()})
	a.applyPathCommandLocked(servicePathCommand{Action: pathStateRunning, Flag: a.pathStateLocked().Busy})
}

// ---------------------------------------------------------------------------
// HTTP

func (a *app) pathChecks(w http.ResponseWriter, _ *http.Request) {
	runAppPathMonitorService(a, pathMonitorServiceRequest{Op: "snapshot"}, w, nil)
}

func (a *app) updatePathChecks(w http.ResponseWriter, r *http.Request) {
	var body pathCheckSettings
	if err := decodeJSON(r.Body, &body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	runAppPathMonitorService(a, pathMonitorServiceRequest{Op: "update", Settings: body}, w, nil)
}

// runPathChecks starts a round and answers at once: a round can take a while, and
// the page polls the status anyway.
func (a *app) runPathChecks(w http.ResponseWriter, _ *http.Request) {
	runAppPathMonitorService(a, pathMonitorServiceRequest{Op: "manual"}, w, nil)
}

func (a *app) cancelPathRecovery(w http.ResponseWriter, _ *http.Request) {
	runAppPathMonitorService(a, pathMonitorServiceRequest{Op: "cancel"}, w, nil)
}
