package main

import (
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"sort"
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
	result := pathCheckSettings{Enabled: input.Enabled, IntervalSec: input.IntervalSec}
	if result.IntervalSec == 0 {
		result.IntervalSec = pathCheckDefaultInterval
	}
	if result.IntervalSec < pathCheckMinInterval {
		result.IntervalSec = pathCheckMinInterval
	}
	if result.IntervalSec > pathCheckMaxInterval {
		result.IntervalSec = pathCheckMaxInterval
	}

	seen := map[string]bool{}
	limit := pathCheckMaxTargets - len(pathCheckPresets)
	for _, target := range input.Targets {
		raw := strings.TrimSpace(target.URL)
		if raw == "" {
			continue
		}
		parsed, err := url.ParseRequestURI(raw)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return pathCheckSettings{}, fmt.Errorf("网址无效：%s（只支持 http/https）", raw)
		}
		if seen[raw] {
			continue
		}
		seen[raw] = true
		label := strings.TrimSpace(target.Label)
		if label == "" {
			label = parsed.Hostname()
		}
		if len([]rune(label)) > 40 {
			return pathCheckSettings{}, errors.New("名称最多 40 个字")
		}
		if len(result.Targets) >= limit {
			return pathCheckSettings{}, fmt.Errorf("最多 %d 个检测目标（含 %d 个内置项）",
				pathCheckMaxTargets, len(pathCheckPresets))
		}
		result.Targets = append(result.Targets, pathTarget{
			Label: label, URL: raw, Enabled: target.Enabled, Builtin: isPresetURL(raw),
		})
	}
	for _, preset := range pathCheckPresets {
		if seen[preset.URL] {
			continue
		}
		result.Targets = append(result.Targets, preset)
	}
	return result, nil
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
	targets := make([]pathTarget, 0, len(a.settings.PathChecks.Targets))
	for _, target := range a.settings.PathChecks.Targets {
		if target.Enabled {
			targets = append(targets, target)
		}
	}
	return targets
}

// pathRegionLocked is the region the monitor may switch inside: the locked one
// when there is one, otherwise wherever the node in use was measured.
func (a *app) pathRegionLocked() string {
	if a.lockedRegion != "" {
		return a.lockedRegion
	}
	if record, ok := a.regions[a.activeNodeLocked()]; ok {
		return record.Country
	}
	return ""
}

// nextPathCandidateLocked hands out the region's candidates one by one, skipping
// the node in use and anything this round has already tried.
func (a *app) nextPathCandidateLocked(region string, tried map[string]bool) string {
	active := a.activeNodeLocked()
	for _, name := range candidatesForRegion(region, a.regions, a.health, time.Now()) {
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
	if a.pathCancel != nil || !a.settings.PathChecks.Enabled {
		return
	}
	cancel := make(chan struct{})
	a.pathCancel = cancel
	interval := time.Duration(a.settings.PathChecks.IntervalSec) * time.Second
	log.Printf("path monitor: checking Google and YouTube every %s; target access starts recovery", interval)
	go a.pathMonitorLoop(cancel, interval)
}

func (a *app) stopPathMonitorLocked() {
	if a.pathCancel != nil {
		close(a.pathCancel)
		a.pathCancel = nil
	}
	a.pathStatus.Demanded = false
	a.pathStatus.Alert = ""
	a.pathStatus.Phase = ""
	a.clearPathRetryLocked()
}

func (a *app) pathMonitorLoop(cancel <-chan struct{}, interval time.Duration) {
	baselineTicker := time.NewTicker(interval)
	demandTicker := time.NewTicker(pathDemandPollInterval)
	retryTicker := time.NewTicker(time.Second)
	defer baselineTicker.Stop()
	defer demandTicker.Stop()
	defer retryTicker.Stop()
	previous := map[string]pathConnectionSample{}
	lastDemand := map[string]time.Time{}
	go a.runPathBaselineRound(checkPathTargets)
	for {
		select {
		case <-cancel:
			return
		case <-baselineTicker.C:
			a.runPathBaselineRound(checkPathTargets)
		case <-demandTicker.C:
			for _, label := range a.pathAccessDemand(previous) {
				if time.Since(lastDemand[label]) < pathRetryDelay {
					continue
				}
				a.mu.Lock()
				a.mu.Lock()
				if !a.pathBusy && a.kernelRunningLocked() && a.settings.PathChecks.Enabled {
					lastDemand[label] = time.Now()
					a.pathStatus.Demanded = true
					a.pathStatus.Phase = "scanning"
					a.mu.Unlock()
					go a.runPathRecovery(checkPathTargets, "demand")
					break
				}
				a.mu.Unlock()
			}
		case <-retryTicker.C:
			mode := a.takeDuePathRetry(time.Now())
			if mode != "" {
				go a.runPathRecovery(checkPathTargets, mode)
			}
		}
	}
}

type pathConnectionSample struct {
	upload int64
}

// pathAccessDemand watches Mihomo's live connection table rather than polling
// optional sites. A new connection or new upload bytes to an enabled target is
// evidence that an application is trying to open that site.
func (a *app) pathAccessDemand(previous map[string]pathConnectionSample) []string {
	a.mu.Lock()
	if !a.kernelRunningLocked() || !a.settings.PathChecks.Enabled || a.pathBusy {
		a.mu.Unlock()
		return nil
	}
	port, secret := a.ctrlPort, a.ctrlSecret
	targets := otherPathTargets(a.enabledPathTargetsLocked())
	a.mu.Unlock()
	if len(targets) == 0 {
		return nil
	}
	snapshot, err := kernelTrafficAt(port, secret)
	if err != nil {
		return nil
	}
	current := make(map[string]pathConnectionSample, len(snapshot.Connections))
	demanded := make(map[string]bool)
	for _, connection := range snapshot.Connections {
		process := strings.ToLower(connectionProcess(connection.Metadata))
		if strings.Contains(process, "smartvpn") {
			continue
		}
		host := normalizePathHost(connection.Metadata.Host)
		if host == "" {
			continue
		}
		id := connection.ID
		if id == "" {
			id = connection.Start + "|" + host + "|" + string(connection.Metadata.DestinationPort)
		}
		sample := pathConnectionSample{upload: connection.Upload}
		prior, seen := previous[id]
		current[id] = sample
		if seen && connection.Upload <= prior.upload {
			continue
		}
		for _, target := range targets {
			targetURL, parseErr := url.Parse(target.URL)
			if parseErr != nil || !pathHostMatches(host, normalizePathHost(targetURL.Hostname())) {
				continue
			}
			demanded[target.Label] = true
		}
	}
	clear(previous)
	for id, sample := range current {
		previous[id] = sample
	}
	labels := make([]string, 0, len(demanded))
	for label := range demanded {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	return labels
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
	a.mu.Lock()
	if !a.kernelRunningLocked() || a.pathBusy || !a.settings.PathChecks.Enabled {
		a.mu.Unlock()
		return
	}
	a.pathBusy = true
	a.pathStatus.Phase = "baseline"
	proxyAddress := net.JoinHostPort("127.0.0.1", strconv.Itoa(a.mixedPort))
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.pathBusy = false
		a.mu.Unlock()
	}()

	results := check(proxyAddress, baselinePathTargets())
	a.mu.Lock()
	a.recordPathResultsLocked(results)
	if len(pathFailures(results)) == 0 {
		a.pathStatus.Phase = "baseline"
		a.pathStatus.Note = "Google 和 YouTube 联通；其他网址只在检测到访问时启动检测。"
		if a.pathStatus.RetryMode == "scan" {
			a.clearPathRetryLocked()
			a.pathStatus.Alert = ""
			if a.pathStatus.Demanded && len(otherPathTargets(a.enabledPathTargetsLocked())) > 0 {
				a.schedulePathRetryLocked("targets")
			}
		}
		a.mu.Unlock()
		return
	}
	a.pathStatus.Phase = "waiting"
	a.pathStatus.Note = "Google 或 YouTube 不通；30 秒后自动检查同地区节点。"
	if a.pathStatus.RetryAt == "" {
		a.queuePathRetryLocked("scan", "Google 或 YouTube 当前无法连通。30 秒后将自动检测同地区节点。")
	}
	a.mu.Unlock()
}

func (a *app) queuePathRetryLocked(mode, message string) {
	if a.pathStatus.AlertID == 0 || a.pathStatus.RetryAt == "" {
		a.pathStatus.AlertID++
	}
	a.pathStatus.Alert = message
	a.pathStatus.RetryMode = mode
	a.pathStatus.RetryAt = time.Now().Add(pathRetryDelay).UTC().Format(time.RFC3339Nano)
	a.pathStatus.Phase = "waiting"
}

func (a *app) clearPathRetryLocked() {
	a.pathStatus.RetryAt = ""
	a.pathStatus.RetryMode = ""
}

func (a *app) takeDuePathRetry(now time.Time) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pathBusy || !a.settings.PathChecks.Enabled || a.pathStatus.RetryAt == "" {
		return ""
	}
	due, err := time.Parse(time.RFC3339Nano, a.pathStatus.RetryAt)
	if err != nil || now.Before(due) {
		return ""
	}
	mode := a.pathStatus.RetryMode
	a.clearPathRetryLocked()
	a.pathStatus.Phase = "scanning"
	return mode
}

// beginPathRound takes the snapshot a round needs. The requests themselves run
// without the lock: holding it for seconds of HTTP would freeze the API the
// window is polling.
func (a *app) beginPathRound() ([]pathTarget, string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.kernelRunningLocked() || a.pathBusy {
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
	a.pathBusy = true
	return targets, net.JoinHostPort("127.0.0.1", strconv.Itoa(a.mixedPort)), true
}

// runPathRound is one pass: measure, and when the path is broken walk the
// region's candidates until one carries it. It stops and reports when they run
// out — protected traffic is never blocked for a site being unreachable, because
// one blocked site is not the same thing as a broken tunnel.
func (a *app) runPathRound(check pathChecker) {
	targets, proxyAddress, ok := a.beginPathRound()
	if !ok {
		return
	}
	defer func() {
		a.mu.Lock()
		a.pathBusy = false
		a.mu.Unlock()
	}()

	results := check(proxyAddress, targets)
	a.mu.Lock()
	a.recordPathResultsLocked(results)
	failures := pathFailures(results)
	a.pathStatus.Note = ""
	a.pathStatus.Switched = nil
	if len(failures) == 0 {
		a.mu.Unlock()
		return
	}
	region := a.pathRegionLocked()
	a.mu.Unlock()

	if region == "" {
		a.mu.Lock()
		a.pathStatus.Note = "当前节点的出口地区未知，无法在同地区内切换；先在「地区与健康」验证出口地区。"
		a.mu.Unlock()
		return
	}

	tried := map[string]bool{}
	for {
		a.mu.Lock()
		if !a.kernelRunningLocked() {
			// The connection went away mid-round; there is nothing to switch.
			a.mu.Unlock()
			return
		}
		target := a.nextPathCandidateLocked(region, tried)
		if target == "" {
			a.pathStatus.Note = fmt.Sprintf(
				"同地区（%s）已验证的候选节点都通不过：%s。已停止切换，受保护流量未受影响。",
				region, strings.Join(failures, "、"))
			a.mu.Unlock()
			return
		}
		from := a.activeNodeLocked()
		if err := a.switchGroupLocked(target); err != nil {
			a.pathStatus.Note = "切换失败：" + err.Error()
			a.mu.Unlock()
			return
		}
		tried[target] = true
		a.lockedNode = target
		a.settings.LockedNode = target
		_ = a.saveSettings()
		a.lastSwitch = time.Now()
		a.pathStatus.Switched = append(a.pathStatus.Switched, target)
		a.recordSwitchLocked(switchEvent{
			Group: "SmartVPN", FromNode: from, ToNode: target, Trigger: "path_check",
			Evidence: fmt.Sprintf("%s 通不过，换到这个节点复检", strings.Join(failures, "、")),
		})
		a.mu.Unlock()

		// The re-check is the point: a switch only counts once the path works.
		results = check(proxyAddress, targets)
		a.mu.Lock()
		a.recordPathResultsLocked(results)
		failures = pathFailures(results)
		a.pathStatus.ActiveNode = a.activeNodeLocked()
		a.mu.Unlock()
		if len(failures) == 0 {
			return
		}
	}
}

// runPathRecovery checks the mandatory Google/YouTube baseline before any
// optional site. The broader loop is entered by an observed access attempt, or
// by an explicit user request; timer ticks alone only check the baseline.
func (a *app) runPathRecovery(check pathChecker, mode string) {
	a.mu.Lock()
	if !a.kernelRunningLocked() || a.pathBusy {
		a.mu.Unlock()
		return
	}
	original := a.activeNodeLocked()
	if original == "" || strings.EqualFold(original, blockedChoice) {
		a.mu.Unlock()
		return
	}
	region := a.pathRegionLocked()
	if mode == "demand" || mode == "manual" {
		a.pathStatus.Demanded = true
	}
	demanded := a.pathStatus.Demanded
	extras := otherPathTargets(a.enabledPathTargetsLocked())
	proxyAddress := net.JoinHostPort("127.0.0.1", strconv.Itoa(a.mixedPort))
	a.pathBusy = true
	a.pathStatus.Phase = "scanning"
	a.pathStatus.Switched = nil
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.pathBusy = false
		a.pathStatus.Running = false
		a.mu.Unlock()
	}()

	baselineTargets := baselinePathTargets()
	if mode == "targets" {
		baselineResults := check(proxyAddress, baselineTargets)
		if len(pathFailures(baselineResults)) == 0 {
			otherResults := check(proxyAddress, extras)
			results := combinePathResults(baselineResults, otherResults)
			a.mu.Lock()
			a.recordPathResultsLocked(results)
			if len(pathFailures(results)) == 0 {
				a.finishPathRecoveryLocked()
				a.mu.Unlock()
				return
			}
			a.pathStatus.Note = "Google 和 YouTube 仍联通，保持当前地区和节点；30 秒后继续检测其他网址。"
			a.pathStatus.Alert = ""
			a.schedulePathRetryLocked("targets")
			a.mu.Unlock()
			return
		}
		a.mu.Lock()
		a.recordPathResultsLocked(baselineResults)
		a.pathStatus.Note = "Google 或 YouTube 不通；30 秒后自动检测同地区节点。"
		a.queuePathRetryLocked("scan", "Google 或 YouTube 当前无法连通。30 秒后将自动检测同地区节点。")
		a.mu.Unlock()
		return
	}

	if region == "" {
		a.mu.Lock()
		a.pathStatus.Note = "当前节点的出口地区未知，无法在同地区内切换；先在「地区与健康」验证出口地区。"
		a.pathStatus.Demanded = false
		a.mu.Unlock()
		return
	}

	// Inspect the current node first, then each currently verified node of the
	// same region. Optional sites are never requested on a candidate until both
	// baseline sites answer.
	a.mu.Lock()
	candidates := candidatesForRegion(region, a.regions, a.health, time.Now())
	a.mu.Unlock()
	ordered := []string{original}
	for _, candidate := range candidates {
		if candidate != original {
			ordered = append(ordered, candidate)
		}
	}
	baselineByNode := make(map[string][]siteCheckResult, len(ordered))
	var lastResults []siteCheckResult
	for _, candidate := range ordered {
		if candidate != original {
			if err := a.switchPathNode(candidate, region, "access-path", "Google/YouTube 基线通过后复检目标网址"); err != nil {
				continue
			}
		}
		baseline := check(proxyAddress, baselineTargets)
		baselineByNode[candidate] = baseline
		if len(pathFailures(baseline)) != 0 {
			a.mu.Lock()
			a.recordPathResultsLocked(baseline)
			a.mu.Unlock()
			lastResults = baseline
			continue
		}
		if !demanded || len(extras) == 0 {
			a.mu.Lock()
			a.recordPathResultsLocked(baseline)
			a.finishPathRecoveryLocked()
			a.pathStatus.Note = "Google 和 YouTube 已恢复；其他网址会在检测到访问时检测。"
			a.mu.Unlock()
			return
		}
		otherResults := check(proxyAddress, extras)
		lastResults = combinePathResults(baseline, otherResults)
		a.mu.Lock()
		a.recordPathResultsLocked(lastResults)
		if len(pathFailures(lastResults)) == 0 {
			a.finishPathRecoveryLocked()
			a.mu.Unlock()
			return
		}
		a.mu.Unlock()
	}

	// No candidate carried every selected target. Restore the node the user had
	// chosen if its baseline still works; otherwise show the 30-second recovery
	// prompt and retry the region scan automatically.
	if a.activeNodeLockedSafe() != original {
		_ = a.switchPathNode(original, region, "access-path-restore", "同地区目标均未通过，恢复原节点")
	}
	baseline := baselineByNode[original]
	if baseline == nil {
		baseline = check(proxyAddress, baselineTargets)
	}
	results := baseline
	if demanded && len(lastResults) > 0 {
		other := otherResultsFrom(lastResults, baselineTargets)
		if len(other) > 0 {
			results = combinePathResults(baseline, other)
		}
	}
	a.mu.Lock()
	a.recordPathResultsLocked(results)
	if len(pathFailures(baseline)) == 0 {
		a.pathStatus.Note = "同地区仍有目标网址未全部通过，但 Google 和 YouTube 可用；保持原地区和节点，30 秒后继续检测。"
		a.pathStatus.Alert = ""
		if demanded {
			a.schedulePathRetryLocked("targets")
		}
		a.mu.Unlock()
		return
	}
	a.pathStatus.Note = fmt.Sprintf("同地区（%s）的 Google/YouTube 基线均未通过；保持原节点，30 秒后重试同地区检测。", region)
	a.queuePathRetryLocked("scan", "Google 或 YouTube 当前无法连通。若不操作，30 秒后将自动检测同地区节点。")
	a.mu.Unlock()
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
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.kernelRunningLocked() {
		return errors.New("连接已断开")
	}
	if target != a.activeNodeLocked() {
		if record, ok := a.regions[target]; !ok || record.Status != regionVerified || record.Country != region || time.Since(record.VerifiedAt) > regionTTL {
			return errors.New("候选节点的地区验证已失效")
		}
		from := a.activeNodeLocked()
		if err := a.switchGroupLocked(target); err != nil {
			return err
		}
		a.lockedNode = target
		a.settings.LockedNode = target
		_ = a.saveSettings()
		a.lastSwitch = time.Now()
		a.pathStatus.Switched = append(a.pathStatus.Switched, target)
		a.recordSwitchLocked(switchEvent{
			Group: "SmartVPN", FromNode: from, ToNode: target, Trigger: trigger, Evidence: evidence,
		})
	}
	return nil
}

func (a *app) activeNodeLockedSafe() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.activeNodeLocked()
}

func (a *app) finishPathRecoveryLocked() {
	a.pathStatus.Note = ""
	a.pathStatus.Alert = ""
	a.pathStatus.Demanded = false
	a.pathStatus.Phase = "idle"
	a.clearPathRetryLocked()
}

func (a *app) schedulePathRetryLocked(mode string) {
	a.pathStatus.RetryMode = mode
	a.pathStatus.RetryAt = time.Now().Add(pathRetryDelay).UTC().Format(time.RFC3339Nano)
	a.pathStatus.Phase = "waiting"
}

func (a *app) recordPathResultsLocked(results []siteCheckResult) {
	a.pathStatus.At = time.Now()
	a.pathStatus.Results = append([]siteCheckResult(nil), results...)
	a.pathStatus.Failures = pathFailures(results)
	a.pathStatus.ActiveNode = a.activeNodeLocked()
	a.pathStatus.Running = a.pathBusy
}

// ---------------------------------------------------------------------------
// HTTP

func (a *app) pathChecks(w http.ResponseWriter, _ *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	written := a.pathStatus
	written.Running = a.pathBusy
	writeJSON(w, map[string]any{
		"settings": a.settings.PathChecks,
		"status":   written,
		"limits": map[string]any{
			"minIntervalSec": pathCheckMinInterval,
			"maxIntervalSec": pathCheckMaxInterval,
			"maxTargets":     pathCheckMaxTargets,
		},
	})
}

func (a *app) updatePathChecks(w http.ResponseWriter, r *http.Request) {
	var body pathCheckSettings
	if err := decodeJSON(r.Body, &body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	normalized, err := normalizePathChecks(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	a.settings.PathChecks = normalized
	if err := a.saveSettings(); err != nil {
		http.Error(w, "could not save the path check settings", http.StatusInternalServerError)
		return
	}
	// The timer follows the settings: a change takes effect now rather than at
	// the next launch.
	a.stopPathMonitorLocked()
	a.startPathMonitorLocked()
	writeJSON(w, map[string]any{"ok": true, "settings": normalized})
}

// runPathChecks starts a round and answers at once: a round can take a while, and
// the page polls the status anyway.
func (a *app) runPathChecks(w http.ResponseWriter, _ *http.Request) {
	a.mu.Lock()
	running := a.kernelRunningLocked()
	a.mu.Unlock()
	if !running {
		http.Error(w, "连接后再检测通路", http.StatusConflict)
		return
	}
	go a.runPathRecovery(checkPathTargets, "manual")
	writeJSON(w, map[string]any{"ok": true})
}

func (a *app) cancelPathRecovery(w http.ResponseWriter, _ *http.Request) {
	a.mu.Lock()
	a.pathStatus.Demanded = false
	a.pathStatus.Alert = ""
	a.pathStatus.Note = "自动恢复检测已取消；Google 和 YouTube 仍按间隔检查。"
	a.clearPathRetryLocked()
	a.pathStatus.Phase = "baseline"
	a.mu.Unlock()
	writeJSON(w, map[string]any{"ok": true})
}
