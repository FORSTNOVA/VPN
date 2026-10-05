package main

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// regionTTL bounds how long a verified exit region stays usable. The design
// document requires stale entries to be re-verified before they can be chosen.
const regionTTL = 24 * time.Hour

type geoSource struct {
	name string
	url  string
}

// All three services answer with the same shape, so one parser covers them and
// a second service is only needed to cross-check the first.
var geoSources = []geoSource{
	{"geojs.io", "https://get.geojs.io/v1/ip/geo.json"},
	{"ipwho.is", "https://ipwho.is/"},
	{"ip.sb", "https://api.ip.sb/geoip"},
}

type geoAnswer struct {
	IP          string `json:"ip"`
	CountryCode string `json:"country_code"`
	Success     *bool  `json:"success"`
}

type regionProbeResult struct {
	ExitIP  string
	Country string
	Status  string
	Sources []string
	Detail  string
}

type regionJob struct {
	Running    bool   `json:"running"`
	Total      int    `json:"total"`
	Done       int    `json:"done"`
	Verified   int    `json:"verified"`
	Skipped    int    `json:"skipped"`
	Failed     int    `json:"failed"`
	StartedAt  string `json:"startedAt,omitempty"`
	FinishedAt string `json:"finishedAt,omitempty"`
	LastError  string `json:"lastError,omitempty"`
}

// timestamp keeps zero times out of the JSON payloads.
func timestamp(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.Format(time.RFC3339)
}

func parseGeoAnswer(body []byte) (string, string, error) {
	var answer geoAnswer
	if err := json.Unmarshal(body, &answer); err != nil {
		return "", "", errors.New("地理位置响应不是有效 JSON")
	}
	if answer.Success != nil && !*answer.Success {
		return "", "", errors.New("地理位置服务拒绝了请求")
	}
	ip := net.ParseIP(strings.TrimSpace(answer.IP))
	if ip == nil {
		return "", "", errors.New("地理位置响应缺少出口 IP")
	}
	country := strings.ToUpper(strings.TrimSpace(answer.CountryCode))
	if !looksLikeCountryCode(country) {
		return "", "", errors.New("地理位置响应缺少国家代码")
	}
	return ip.String(), country, nil
}

func looksLikeCountryCode(code string) bool {
	if len(code) != 2 {
		return false
	}
	for _, letter := range code {
		if letter < 'A' || letter > 'Z' {
			return false
		}
	}
	return true
}

// combineRegionAnswers decides the verification status from the cross-check.
// A single source is not enough to lock a region, so it is reported separately
// from a verified answer.
func combineRegionAnswers(results []regionProbeResult) regionProbeResult {
	combined := regionProbeResult{Sources: make([]string, 0, len(results))}
	switch len(results) {
	case 0:
		combined.Status = regionUnreachable
		combined.Detail = "所有出口查询服务均未响应"
		return combined
	case 1:
		combined.ExitIP = results[0].ExitIP
		combined.Country = results[0].Country
		combined.Sources = results[0].Sources
		combined.Status = regionSingle
		combined.Detail = "仅一个数据源响应，未经交叉核验"
		return combined
	}
	combined.ExitIP = results[0].ExitIP
	combined.Country = results[0].Country
	for _, result := range results {
		combined.Sources = append(combined.Sources, result.Sources...)
	}
	if results[0].Country != results[1].Country {
		combined.Status = regionConflict
		combined.Detail = results[0].Sources[0] + " 与 " + results[1].Sources[0] + " 报告的出口地区不一致"
		return combined
	}
	combined.Status = regionVerified
	combined.Detail = "两个数据源一致"
	return combined
}

func (a *app) queryGeoSource(source geoSource, timeout time.Duration) (string, string, error) {
	proxyURL := &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(a.mixedPort))}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyURL(proxyURL)
	transport.DisableKeepAlives = true
	defer transport.CloseIdleConnections()
	client := http.Client{Timeout: timeout, Transport: transport}
	request, err := http.NewRequest(http.MethodGet, source.url, nil)
	if err != nil {
		return "", "", err
	}
	request.Header.Set("User-Agent", "SmartVPN-region-check/1.0")
	response, err := client.Do(request)
	if err != nil {
		return "", "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", "", errors.New("地理位置服务返回非成功状态")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 64*1024))
	if err != nil {
		return "", "", err
	}
	return parseGeoAnswer(body)
}

// probeRegionLocked asks two independent services for the exit address through
// whichever node the SmartVPN group currently selects.
func (a *app) probeRegionLocked() regionProbeResult {
	timeout := 8 * time.Second
	results := make([]regionProbeResult, 0, 2)
	for _, source := range geoSources {
		exitIP, country, err := a.queryGeoSource(source, timeout)
		if err != nil {
			continue
		}
		results = append(results, regionProbeResult{
			ExitIP: exitIP, Country: country, Sources: []string{source.name},
		})
		if len(results) == 2 {
			break
		}
	}
	return combineRegionAnswers(results)
}

func (a *app) saveRegionLocked(record regionRecord) {
	a.regions[record.Name] = record
	if a.store == nil {
		return
	}
	if err := a.store.saveRegion(record); err != nil {
		log.Printf("could not persist the exit region of %s: %v", record.Name, err)
	}
}

func (a *app) regionJobRunningLocked() bool {
	return a.regionJob != nil && a.regionJob.Running
}

func (a *app) regionJobSnapshotLocked() *regionJob {
	if a.regionJob == nil {
		return &regionJob{}
	}
	snapshot := *a.regionJob
	return &snapshot
}

func (a *app) startRegionVerification(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Refresh bool `json:"refresh"`
	}
	if r.Body != nil {
		// An empty body means "verify what is stale", which is the common case.
		if err := decodeJSON(r.Body, &body); err != nil && !errors.Is(err, io.EOF) {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
	}
	a.mu.Lock()
	if a.settings.SubscriptionURL == "" {
		a.mu.Unlock()
		http.Error(w, "set a subscription URL first", http.StatusBadRequest)
		return
	}
	if a.regionJobRunningLocked() {
		a.mu.Unlock()
		http.Error(w, "an exit region check is already running", http.StatusConflict)
		return
	}
	a.regionJob = &regionJob{Running: true, StartedAt: timestamp(time.Now())}
	a.mu.Unlock()

	go a.runRegionJob(body.Refresh)
	writeJSON(w, map[string]any{"ok": true, "job": a.regionJobSnapshotLocked()})
}

func (a *app) finishRegionJobLocked(err error) {
	if a.regionJob == nil {
		return
	}
	a.regionJob.Running = false
	a.regionJob.FinishedAt = timestamp(time.Now())
	if err != nil {
		a.regionJob.LastError = err.Error()
	}
}

// runRegionJob walks the node list one node at a time. It releases the lock
// between nodes so the UI can still poll while a long check runs, and it never
// holds the group switch across a lock release.
func (a *app) runRegionJob(refresh bool) {
	a.mu.Lock()
	if !a.regionJobRunningLocked() {
		a.mu.Unlock()
		return
	}
	temporary := !a.kernelRunningLocked()
	if temporary {
		if err := a.startMihomoLocked(); err != nil {
			a.finishRegionJobLocked(err)
			a.mu.Unlock()
			return
		}
	}
	original := a.activeNodeLocked()
	nodes, err := a.listNodes()
	if err != nil {
		if temporary {
			a.stopMihomoLocked()
		}
		a.finishRegionJobLocked(err)
		a.mu.Unlock()
		return
	}
	delays, _ := a.groupDelays(8 * time.Second)
	a.regionJob.Total = len(nodes)
	a.mu.Unlock()

	for _, node := range nodes {
		a.mu.Lock()
		if !a.regionJobRunningLocked() {
			a.mu.Unlock()
			break
		}
		if !refresh {
			if record, ok := a.regions[node.Name]; ok &&
				record.Status == regionVerified && time.Since(record.VerifiedAt) < regionTTL {
				a.regionJob.Skipped++
				a.regionJob.Done++
				a.mu.Unlock()
				continue
			}
		}
		if delays[node.Name] <= 0 {
			a.saveRegionLocked(regionRecord{
				Name: node.Name, Status: regionUnreachable,
				Detail: "节点未通过延迟测试", VerifiedAt: time.Now(),
			})
			a.regionJob.Failed++
			a.regionJob.Done++
			a.mu.Unlock()
			continue
		}
		if err := a.switchGroupLocked(node.Name); err != nil {
			a.saveRegionLocked(regionRecord{
				Name: node.Name, Status: regionUnreachable,
				Detail: "无法切换到该节点", VerifiedAt: time.Now(),
			})
			a.regionJob.Failed++
			a.regionJob.Done++
			a.mu.Unlock()
			continue
		}
		result := a.probeRegionLocked()
		a.saveRegionLocked(regionRecord{
			Name: node.Name, ExitIP: result.ExitIP, Country: result.Country,
			Status: result.Status, Sources: result.Sources, Detail: result.Detail,
			VerifiedAt: time.Now(),
		})
		if result.Status == regionVerified {
			a.regionJob.Verified++
		} else {
			a.regionJob.Failed++
		}
		a.regionJob.Done++
		a.mu.Unlock()
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if original != "" && original != "REJECT" {
		if err := a.switchGroupLocked(original); err != nil {
			log.Printf("could not restore the node after an exit region check: %v", err)
		}
	}
	if temporary {
		a.stopMihomoLocked()
	}
	a.finishRegionJobLocked(nil)
}

func (a *app) regionReport(w http.ResponseWriter, _ *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	type regionPayload struct {
		Name       string   `json:"name"`
		Country    string   `json:"country,omitempty"`
		ExitIP     string   `json:"exitIp,omitempty"`
		Status     string   `json:"status"`
		Sources    []string `json:"sources,omitempty"`
		Detail     string   `json:"detail,omitempty"`
		VerifiedAt string   `json:"verifiedAt,omitempty"`
		Stale      bool     `json:"stale"`
	}
	names := make([]string, 0, len(a.regions))
	for name := range a.regions {
		names = append(names, name)
	}
	sort.Strings(names)
	payload := make([]regionPayload, 0, len(names))
	now := time.Now()
	for _, name := range names {
		record := a.regions[name]
		entry := regionPayload{
			Name: name, Country: record.Country, ExitIP: record.ExitIP,
			Status: record.Status, Sources: record.Sources, Detail: record.Detail,
			Stale: record.Status == regionVerified && now.Sub(record.VerifiedAt) > regionTTL,
		}
		if !record.VerifiedAt.IsZero() {
			entry.VerifiedAt = record.VerifiedAt.Format(time.RFC3339)
		}
		payload = append(payload, entry)
	}
	pool := candidatesForRegion(a.lockedRegion, a.regions, a.health, now)
	if pool == nil {
		pool = []string{}
	}
	type healthPayload struct {
		State               string `json:"state"`
		LatencyMs           int    `json:"latencyMs"`
		ConsecutiveFailures int    `json:"consecutiveFailures"`
		CooldownUntil       string `json:"cooldownUntil,omitempty"`
	}
	healthPayloads := make(map[string]healthPayload, len(a.health))
	for name, record := range a.health {
		healthPayloads[name] = healthPayload{
			State:               record.State,
			LatencyMs:           record.LatencyMs,
			ConsecutiveFailures: record.ConsecutiveFailures,
			CooldownUntil:       timestamp(record.CooldownUntil),
		}
	}
	writeJSON(w, map[string]any{
		"nodes":        payload,
		"job":          a.regionJobSnapshotLocked(),
		"lockedRegion": a.lockedRegion,
		"pool":         pool,
		"health":       healthPayloads,
		"ttlHours":     int(regionTTL.Hours()),
	})
}

func (a *app) lockRegion(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Country string `json:"country"`
	}
	if err := decodeJSON(r.Body, &body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	country := strings.ToUpper(strings.TrimSpace(body.Country))
	if country != "" && !looksLikeCountryCode(country) {
		http.Error(w, "the region must be a two-letter country code", http.StatusBadRequest)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if country != "" {
		now := time.Now()
		usable := false
		for _, record := range a.regions {
			if record.Status == regionVerified && record.Country == country && now.Sub(record.VerifiedAt) < regionTTL {
				usable = true
				break
			}
		}
		if !usable {
			http.Error(w, errNoLockableRegion.Error(), http.StatusBadRequest)
			return
		}
	}
	a.lockedRegion = country
	a.settings.LockedRegion = country
	if country == "" {
		a.lockedNode = ""
		a.settings.LockedNode = ""
	} else {
		// The pinned node has to belong to the new region, otherwise the group
		// would keep carrying an off-region node under a lock that claims
		// otherwise.
		a.lockedNode = a.bestKnownCandidateLocked(time.Now())
		a.settings.LockedNode = a.lockedNode
	}
	if err := a.saveSettings(); err != nil {
		http.Error(w, "could not save the locked region", http.StatusInternalServerError)
		return
	}
	// A lock taken while a connection is running binds that connection, not only
	// the next one. Leaving the group on the previous region's node would mean
	// the window says the region is locked while traffic still leaves from
	// somewhere else, which is the one thing the lock exists to prevent.
	//
	// Nothing here is measured: the candidate is the best the health records
	// know, and if the new region has none that can carry traffic the group is
	// blocked rather than left where it was. That is the lock's own promise —
	// it fails closed.
	if country != "" && a.kernelRunningLocked() {
		now := time.Now()
		if a.lockedNode == "" {
			a.blockLocked("", now, "锁定地区 "+country+" 目前没有可用节点")
		} else if a.activeNodeLocked() != a.lockedNode {
			if err := a.switchGroupLocked(a.lockedNode); err != nil {
				log.Printf("could not pin the group to %s after locking %s: %v", a.lockedNode, country, err)
			}
		}
	}
	a.recordSwitchLocked(switchEvent{
		At: time.Now(), Group: "SmartVPN", ToNode: "-",
		Trigger:  "region_lock",
		Evidence: "锁定地区：" + country,
	})
	writeJSON(w, map[string]any{
		"ok": true, "lockedRegion": a.lockedRegion,
		"pool": len(candidatesForRegion(a.lockedRegion, a.regions, a.health, time.Now())),
	})
}

// lockRegionToLocked follows the verified exit region of a node, which is how
// the UI keeps the lock in step with the user's chosen node. The node itself
// becomes the pinned selection so automatic groups cannot leave the region.
func (a *app) lockRegionToLocked(node string) {
	if node == "" {
		return
	}
	record, ok := a.regions[node]
	if !ok || record.Status != regionVerified {
		return
	}
	if time.Since(record.VerifiedAt) > regionTTL {
		return
	}
	if a.lockedRegion == record.Country && a.lockedNode == node {
		return
	}
	a.lockedRegion = record.Country
	a.lockedNode = node
	a.settings.LockedRegion = record.Country
	a.settings.LockedNode = node
	if err := a.saveSettings(); err != nil {
		log.Printf("could not persist the locked region: %v", err)
	}
}
