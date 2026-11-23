package main

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
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
	a.writeRegionRecordLocked(record.Name, record)
	if a.store == nil {
		return
	}
	if err := a.store.saveRegion(record); err != nil {
		log.Printf("could not persist the exit region of %s: %v", record.Name, err)
	}
}

func (a *app) regionJobRunningLocked() bool {
	j := a.healthMetaLocked().Job
	return j != nil && j.Running
}

func (a *app) regionJobSnapshotLocked() *regionJob {
	j := a.healthMetaLocked().Job
	if j == nil {
		return &regionJob{}
	}
	snapshot := *j
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
	result := a.runRegionJobService(regionJobServiceRequest{Op: "start", Refresh: body.Refresh})
	if result.Status != 200 {
		http.Error(w, result.Message, result.Status)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "job": result.Job})
}
func (a *app) finishRegionJobLocked(err error) {
	r := a.runRegionJobService(regionJobServiceRequest{Op: "finish", Err: err})
	if r.Err != nil {
		log.Printf("region job finish incomplete: %v", r.Err)
	}
}

// The original group is restored once, after all node iterations.
func (a *app) runRegionJob(refresh bool) {
	r := a.runRegionJobService(regionJobServiceRequest{Op: "run", Refresh: refresh})
	if r.Err != nil {
		log.Printf("region job incomplete: %v", r.Err)
	}
}

func (a *app) regionReport(w http.ResponseWriter, _ *http.Request) {
	writeRegionServiceResult(w, a.runRegionService(regionServiceRequest{Op: "report"}))
}

func (a *app) lockRegion(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Country string `json:"country"`
	}
	if err := decodeJSON(r.Body, &body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	writeRegionServiceResult(w, a.runRegionService(regionServiceRequest{Op: "lock", Country: body.Country}))
}

// lockRegionToLocked follows the verified exit region of a node, which is how
// the UI keeps the lock in step with the user's chosen node. The node itself
// becomes the pinned selection so automatic groups cannot leave the region.
func (a *app) lockRegionToLocked(node string) {
	if a.chainEnabledLocked() {
		return
	}
	if node == "" {
		return
	}
	record, ok := a.regionRecordLocked(node)
	if !ok || record.Status != regionVerified {
		return
	}
	if time.Since(record.VerifiedAt) > regionTTL {
		return
	}
	if a.controlMetaLocked().RuntimeLockedRegion == record.Country && a.controlMetaLocked().RuntimeLockedNode == node {
		return
	}
	a.patchControlLocked(controlRuntimeLockedRegion, serviceControlState{RuntimeLockedRegion: record.Country})
	a.patchControlLocked(controlRuntimeLockedNode, serviceControlState{RuntimeLockedNode: node})
	a.patchControlLocked(controlLockedRegion, serviceControlState{LockedRegion: record.Country})
	a.patchControlLocked(controlLockedNode, serviceControlState{LockedNode: node})
	if err := a.saveSettings(); err != nil {
		log.Printf("could not persist the locked region: %v", err)
	}
}
