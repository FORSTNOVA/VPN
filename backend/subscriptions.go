package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type subscriptionUserInfo struct {
	Upload   int64 `json:"upload"`
	Download int64 `json:"download"`
	Total    int64 `json:"total"`
	Expire   int64 `json:"expire"`
}

// A saved subscription. The one in use is still settings.SubscriptionURL, so the
// rest of the pipeline — profile matching, the node cache, fetch and connect —
// is untouched; this list is only the register of what the user has saved.
type subscriptionEntry struct {
	ID             string                `json:"id"`
	URL            string                `json:"url"`
	Label          string                `json:"label"`
	Prefix         string                `json:"prefix,omitempty"`
	Enabled        bool                  `json:"enabled"`
	UpdatedAt      int64                 `json:"updatedAt,omitempty"`
	NodeCount      int                   `json:"nodeCount,omitempty"`
	LastError      string                `json:"lastError,omitempty"`
	UserInfo       *subscriptionUserInfo `json:"userInfo,omitempty"`
	QuotaUpdatedAt int64                 `json:"quotaUpdatedAt,omitempty"`
	QuotaError     string                `json:"quotaError,omitempty"`
}

const (
	subscriptionsKey      = "subscriptions"
	mergedSubscriptionURL = "__merged__"
)

// subscriptionLabel is what the list shows when the user has not renamed an
// entry: the host is enough to tell two providers apart.
func subscriptionLabel(rawURL string) string {
	remoteURLs, inlineLines := categorizeSubscriptionInput(rawURL)
	if len(remoteURLs)+len(inlineLines) > 1 {
		hosts := make([]string, 0, len(remoteURLs)+len(inlineLines))
		for _, u := range remoteURLs {
			parsed, err := url.Parse(u)
			if err == nil && parsed.Hostname() != "" {
				hosts = append(hosts, strings.TrimPrefix(parsed.Hostname(), "www."))
			}
		}
		for _, line := range inlineLines {
			if node, err := parseNodeURI(line); err == nil {
				hosts = append(hosts, node.Name)
			}
		}
		if len(hosts) > 0 {
			return strings.Join(hosts, " + ") + " (合并订阅)"
		}
		return fmt.Sprintf("合并订阅 (%d 个源)", len(remoteURLs)+len(inlineLines))
	}
	if len(inlineLines) == 1 && len(remoteURLs) == 0 {
		if node, err := parseNodeURI(inlineLines[0]); err == nil {
			return node.Name
		}
		return "直连节点"
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Hostname() == "" {
		return rawURL
	}
	return strings.TrimPrefix(parsed.Hostname(), "www.")
}

func (a *app) loadSubscriptionsLocked() {
	a.subscriptions = nil
	if a.store != nil {
		if body, found, err := a.store.getValue(subscriptionsKey); err == nil && found {
			var stored []subscriptionEntry
			if json.Unmarshal([]byte(body), &stored) != nil {
				log.Printf("stored subscription list was unreadable and is ignored")
			} else {
				var rawEntries []map[string]any
				_ = json.Unmarshal([]byte(body), &rawEntries)
				for i, entry := range stored {
					if entry.URL == "" {
						continue
					}
					// The list is stored in the platform's credential form, like
					// the active subscription: on Android the URL is sealed with
					// the keystore, and a value that cannot be opened is one whose
					// key is gone.
					plain, err := openSecret(entry.URL)
					if err != nil {
						log.Printf("could not read a stored subscription URL, dropping that entry: %v", err)
						continue
					}
					entry.URL = plain
					if entry.ID == "" {
						entry.ID = subscriptionHash(entry.URL)
					}
					if entry.Label == "" {
						entry.Label = subscriptionLabel(entry.URL)
					}
					// Default enabled to true if previously saved without this field
					if i < len(rawEntries) {
						if _, hasEnabled := rawEntries[i]["enabled"]; !hasEnabled {
							entry.Enabled = true
						}
					} else {
						entry.Enabled = true
					}
					a.subscriptions = append(a.subscriptions, entry)
				}
			}
		}
	}
	// A URL saved before this list existed still belongs in it.
	a.ensureSubscriptionLocked(a.settings.SubscriptionURL)
	a.startAutoMergeLoopLocked()
}

// ensureSubscriptionLocked registers a URL without changing which one is active.
func (a *app) ensureSubscriptionLocked(rawURL string) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" || rawURL == mergedSubscriptionURL {
		return
	}
	for _, entry := range a.subscriptions {
		if entry.URL == rawURL {
			return
		}
	}
	a.subscriptions = append(a.subscriptions, subscriptionEntry{
		ID:      subscriptionHash(rawURL),
		URL:     rawURL,
		Label:   subscriptionLabel(rawURL),
		Enabled: true,
	})
	_ = a.saveSubscriptionsLocked()
}

func (a *app) saveSubscriptionsLocked() error {
	if a.store == nil {
		return nil
	}
	stored := make([]subscriptionEntry, 0, len(a.subscriptions))
	for _, entry := range a.subscriptions {
		sealed, err := sealSecret(entry.URL)
		if err != nil {
			return err
		}
		entry.URL = sealed
		stored = append(stored, entry)
	}
	body, err := json.Marshal(stored)
	if err != nil {
		return err
	}
	return a.store.setValue(subscriptionsKey, string(body))
}

func (a *app) findSubscriptionLocked(id string) (subscriptionEntry, bool) {
	for _, entry := range a.subscriptions {
		if entry.ID == id {
			return entry, true
		}
	}
	return subscriptionEntry{}, false
}

func (a *app) findSubscriptionByURL(rawURL string) (subscriptionEntry, bool) {
	for _, entry := range a.subscriptions {
		if entry.URL == rawURL || entry.ID == subscriptionHash(rawURL) {
			return entry, true
		}
	}
	return subscriptionEntry{}, false
}

// profileCachePath is where a subscription's last good profile is kept, so
// switching back to it does not depend on the network that just failed.
func (a *app) profileCachePath(id string) string {
	return filepath.Join(a.home, "subscriptions", id+".yaml")
}

func (a *app) cacheProfileForID(id string, body []byte, format string) {
	if body == nil || id == "" {
		return
	}
	path := a.profileCachePath(id)
	if os.MkdirAll(filepath.Dir(path), 0700) != nil {
		return
	}
	if os.WriteFile(path, body, 0600) != nil {
		return
	}
	_ = os.WriteFile(path+".format", []byte(format), 0600)
}

func (a *app) cacheProfileLocked(body []byte, format string) {
	if body == nil || a.settings.SubscriptionURL == "" {
		return
	}
	a.cacheProfileForID(subscriptionHash(a.settings.SubscriptionURL), body, format)
}

// useCachedProfileLocked puts a subscription's cached profile in place and marks
// it as belonging to that subscription, which is what makes the rest of the
// pipeline accept it.
func (a *app) useCachedProfileLocked(id string) error {
	body, err := os.ReadFile(a.profileCachePath(id))
	if err != nil {
		return errors.New("没有这个订阅的缓存副本")
	}
	if len(parseProfileNodes(body)) == 0 {
		return errors.New("缓存副本里没有可用节点")
	}
	entry, ok := a.findSubscriptionLocked(id)
	if !ok {
		return errors.New("没有这个订阅")
	}
	path := a.profilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return errors.New("无法创建本地配置目录")
	}
	temp := path + ".tmp"
	if err := os.WriteFile(temp, body, 0600); err != nil {
		return errors.New("无法写入缓存副本")
	}
	if err := os.Rename(temp, path); err != nil {
		_ = os.Remove(temp)
		return errors.New("无法启用缓存副本")
	}
	sourceContent := subscriptionHash(entry.URL)
	if entry.URL == mergedSubscriptionURL {
		sourceContent = mergedSubscriptionURL
	}
	if err := os.WriteFile(path+".source", []byte(sourceContent), 0600); err != nil {
		return errors.New("无法记录配置来源")
	}
	format, readErr := os.ReadFile(a.profileCachePath(id) + ".format")
	if readErr == nil {
		_ = os.WriteFile(path+".format", format, 0600)
	}
	nodes := parseProfileNodes(body)
	for i := range nodes {
		nodes[i].Selected = nodes[i].Name == a.settings.SelectedNode
	}
	a.cachedNodes = append([]proxyNode(nil), nodes...)
	if err := a.saveCachedNodesLocked(nodes); err != nil {
		return errors.New("无法保存缓存副本的节点列表")
	}
	return nil
}

// clearNodeBoundStateLocked drops everything that described the nodes of the
// subscription we are leaving: node names, health, verified regions and the
// cached list. Keeping them would let an old, unreachable name look verified.
func (a *app) clearNodeBoundStateLocked() {
	a.settings.SelectedNode = ""
	a.settings.SelectionMode = "auto"
	a.settings.LockedRegion = ""
	a.settings.LockedNode = ""
	a.lockedRegion = ""
	a.lockedNode = ""
	a.blocked = false
	a.blockReason = ""
	a.health = map[string]nodeHealth{}
	a.regions = map[string]regionRecord{}
	a.clearCachedNodesLocked()
	if a.store != nil {
		if err := a.store.clearHealth(); err != nil {
			log.Printf("could not clear stored node health: %v", err)
		}
		if err := a.store.clearRegions(); err != nil {
			log.Printf("could not clear stored exit regions: %v", err)
		}
	}
	_ = a.saveSettings()
}

// ---------------------------------------------------------------------------
// HTTP

func (a *app) listSubscriptions(w http.ResponseWriter, _ *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	entries := append([]subscriptionEntry(nil), a.subscriptions...)
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].URL == a.settings.SubscriptionURL {
			return true
		}
		if entries[j].URL == a.settings.SubscriptionURL {
			return false
		}
		return entries[i].Label < entries[j].Label
	})
	writeJSON(w, map[string]any{
		"subscriptions":  entries,
		"activeUrl":      a.settings.SubscriptionURL,
		"isMerged":       a.settings.SubscriptionURL == mergedSubscriptionURL,
		"fromCache":      a.profileFromCache,
		"running":        a.kernelRunningLocked(),
		"autoMergeHours": a.settings.AutoMergeHours,
	})
}

func (a *app) startAutoMergeLoopLocked() {
	if a.autoMergeCancel != nil {
		return
	}
	cancel := make(chan struct{})
	a.autoMergeCancel = cancel
	go func(c <-chan struct{}) {
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-c:
				return
			case <-ticker.C:
				a.mu.Lock()
				a.autoMergeTickLocked()
				a.mu.Unlock()
			}
		}
	}(cancel)
}

func (a *app) stopAutoMergeLoopLocked() {
	if a.autoMergeCancel != nil {
		close(a.autoMergeCancel)
		a.autoMergeCancel = nil
	}
}

func (a *app) autoMergeTickLocked() {
	if a.settings.AutoMergeHours <= 0 {
		return
	}
	var enabledCount int
	var latestUpdate int64
	for _, s := range a.subscriptions {
		if s.Enabled && strings.TrimSpace(s.URL) != "" {
			enabledCount++
			if s.UpdatedAt > latestUpdate {
				latestUpdate = s.UpdatedAt
			}
		}
	}
	if enabledCount == 0 {
		return
	}
	intervalSec := int64(a.settings.AutoMergeHours) * 3600
	if latestUpdate > 0 && time.Now().Unix()-latestUpdate < intervalSec {
		return
	}
	log.Printf("auto-merge: triggering scheduled subscription merge (interval: %dh)", a.settings.AutoMergeHours)
	nodes, err := a.mergeAllEnabledSubscriptionsLocked()
	if err != nil {
		log.Printf("auto-merge failed: %v", err)
		return
	}
	log.Printf("auto-merge completed: merged %d nodes", len(nodes))
	if a.kernelRunningLocked() {
		_, _ = a.mihomoRequest(http.MethodPut, "/providers/proxies/SmartVPNSubscription", nil)
	}
}

func (a *app) setAutoMergeHours(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Hours int `json:"hours"`
	}
	if err := decodeJSON(r.Body, &body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if body.Hours < 0 || body.Hours > 720 {
		http.Error(w, "hours must be between 0 and 720", http.StatusBadRequest)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.settings.AutoMergeHours = body.Hours
	if err := a.saveSettings(); err != nil {
		http.Error(w, "could not save settings", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "autoMergeHours": a.settings.AutoMergeHours})
}

// activateSubscription switches to a saved subscription
func (a *app) activateSubscription(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if err := decodeJSON(r.Body, &body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.kernelRunningLocked() {
		http.Error(w, "断开连接后再切换订阅", http.StatusConflict)
		return
	}
	if body.ID == mergedSubscriptionURL {
		nodes, err := a.mergeAllEnabledSubscriptionsLocked()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, map[string]any{
			"ok": true, "fromCache": false, "nodes": len(nodes),
			"label": "多订阅合并", "subscriptionUrl": mergedSubscriptionURL,
		})
		return
	}
	entry, ok := a.findSubscriptionLocked(body.ID)
	if !ok {
		http.Error(w, "没有这个订阅", http.StatusNotFound)
		return
	}
	if entry.URL == a.settings.SubscriptionURL && a.profileMatchesSubscription() {
		writeJSON(w, map[string]any{"ok": true, "fromCache": a.profileFromCache})
		return
	}

	a.settings.SubscriptionURL = entry.URL
	a.clearNodeBoundStateLocked()
	a.profileFromCache = false

	fetched, err := a.fetchProfileLocked()
	fromCache := a.profileFromCache
	if err != nil {
		if cacheErr := a.useCachedProfileLocked(entry.ID); cacheErr != nil {
			a.recordSubscriptionErrorLocked(entry.ID, err.Error())
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		fromCache = true
		a.profileFromCache = true
	}
	nodeCount := len(fetched)
	if fromCache {
		nodeCount = len(a.cachedNodes)
	}
	a.recordSubscriptionUseLocked(entry.ID, nodeCount, "")

	writeJSON(w, map[string]any{
		"ok": true, "fromCache": fromCache, "nodes": nodeCount,
		"label": entry.Label, "subscriptionUrl": entry.URL,
	})
}

func (a *app) recordSubscriptionUseLocked(id string, nodeCount int, failure string) {
	a.recordSubscriptionUseAndInfoLocked(id, nodeCount, failure, nil)
}

func (a *app) recordSubscriptionUseAndInfoLocked(id string, nodeCount int, failure string, info *subscriptionUserInfo) {
	for i := range a.subscriptions {
		if a.subscriptions[i].ID != id {
			continue
		}
		a.subscriptions[i].UpdatedAt = time.Now().Unix()
		a.subscriptions[i].NodeCount = nodeCount
		a.subscriptions[i].LastError = failure
		if info != nil {
			a.subscriptions[i].UserInfo = info
			a.subscriptions[i].QuotaUpdatedAt = time.Now().Unix()
			a.subscriptions[i].QuotaError = ""
		}
	}
	_ = a.saveSubscriptionsLocked()
}

func (a *app) recordSubscriptionErrorLocked(id, failure string) {
	for i := range a.subscriptions {
		if a.subscriptions[i].ID == id {
			a.subscriptions[i].LastError = failure
		}
	}
	_ = a.saveSubscriptionsLocked()
}

func (a *app) fetchSingleSubscriptionLocked(id string) (*subscriptionEntry, error) {
	entry, ok := a.findSubscriptionLocked(id)
	if !ok {
		return nil, errors.New("没有这个订阅")
	}
	remoteURLs, inlineLines := categorizeSubscriptionInput(entry.URL)
	var body []byte
	var nodes []proxyNode
	var uinfo *subscriptionUserInfo
	var err error

	if len(remoteURLs) == 0 && len(inlineLines) > 0 {
		inlineBody := []byte(strings.Join(inlineLines, "\n"))
		body, nodes, err = ensureClashYAML(inlineBody)
	} else if len(remoteURLs) == 1 && len(inlineLines) == 0 {
		u := remoteURLs[0]
		body, nodes, uinfo, err = a.downloadSubscriptionWithFallback(u)
	} else {
		var parts [][]byte
		for _, ru := range remoteURLs {
			b, _, info, dErr := a.downloadSubscriptionWithFallback(ru)
			if dErr == nil && len(b) > 0 {
				parts = append(parts, b)
				if uinfo == nil && info != nil {
					uinfo = info
				}
			}
		}
		if len(inlineLines) > 0 {
			parts = append(parts, []byte(strings.Join(inlineLines, "\n")))
		}
		if len(parts) > 0 {
			body, nodes, err = mergeSubscriptionProfiles(parts)
		} else {
			err = errors.New("无法拉取该订阅节点")
		}
	}

	if err != nil || len(nodes) == 0 {
		if cached, readErr := os.ReadFile(a.profileCachePath(id)); readErr == nil && len(cached) > 0 {
			cNodes := parseProfileNodes(cached)
			if len(cNodes) > 0 {
				a.recordSubscriptionUseAndInfoLocked(id, len(cNodes), "网络连接失败，已使用本地历史缓存", uinfo)
				updated, _ := a.findSubscriptionLocked(id)
				return &updated, nil
			}
		}
		lastErr := "拉取失败"
		if err != nil {
			lastErr = err.Error()
		}
		a.recordSubscriptionErrorLocked(id, lastErr)
		return nil, errors.New(lastErr)
	}

	a.cacheProfileForID(id, body, "yaml")
	a.recordSubscriptionUseAndInfoLocked(id, len(nodes), "", uinfo)
	updated, _ := a.findSubscriptionLocked(id)
	return &updated, nil
}

func (a *app) mergeAllEnabledSubscriptionsLocked() ([]proxyNode, error) {
	var enabledEntries []subscriptionEntry
	for _, sub := range a.subscriptions {
		if sub.Enabled && strings.TrimSpace(sub.URL) != "" {
			enabledEntries = append(enabledEntries, sub)
		}
	}
	if len(enabledEntries) == 0 {
		return nil, errors.New("没有已启用的订阅源，请至少勾选启用一个订阅")
	}

	type fetchResult struct {
		id       string
		body     []byte
		nodes    []proxyNode
		userInfo *subscriptionUserInfo
		err      error
	}

	results := make([]fetchResult, len(enabledEntries))
	var wg sync.WaitGroup

	for i, sub := range enabledEntries {
		wg.Add(1)
		go func(idx int, entry subscriptionEntry) {
			defer wg.Done()
			remoteURLs, inlineLines := categorizeSubscriptionInput(entry.URL)
			if len(remoteURLs) == 0 && len(inlineLines) > 0 {
				inlineBody := []byte(strings.Join(inlineLines, "\n"))
				cBody, cNodes, err := ensureClashYAML(inlineBody)
				results[idx] = fetchResult{id: entry.ID, body: cBody, nodes: cNodes, err: err}
				return
			}
			if len(remoteURLs) == 1 && len(inlineLines) == 0 {
				u := remoteURLs[0]
				body, nodes, uinfo, err := a.downloadSubscriptionWithFallback(u)
				if err != nil {
					if cached, readErr := os.ReadFile(a.profileCachePath(entry.ID)); readErr == nil && len(cached) > 0 {
						cNodes := parseProfileNodes(cached)
						if len(cNodes) > 0 {
							results[idx] = fetchResult{id: entry.ID, body: cached, nodes: cNodes, userInfo: uinfo, err: errors.New("网络获取失败，使用历史缓存")}
							return
						}
					}
					results[idx] = fetchResult{id: entry.ID, err: err}
					return
				}
				results[idx] = fetchResult{id: entry.ID, body: body, nodes: nodes, userInfo: uinfo, err: nil}
				return
			}
			var parts [][]byte
			var uinfo *subscriptionUserInfo
			for _, ru := range remoteURLs {
				b, _, info, dErr := a.downloadSubscriptionWithFallback(ru)
				if dErr == nil && len(b) > 0 {
					parts = append(parts, b)
					if uinfo == nil && info != nil {
						uinfo = info
					}
				}
			}
			if len(inlineLines) > 0 {
				parts = append(parts, []byte(strings.Join(inlineLines, "\n")))
			}
			if len(parts) > 0 {
				mBody, mNodes, err := mergeSubscriptionProfiles(parts)
				results[idx] = fetchResult{id: entry.ID, body: mBody, nodes: mNodes, userInfo: uinfo, err: err}
			} else {
				if cached, readErr := os.ReadFile(a.profileCachePath(entry.ID)); readErr == nil && len(cached) > 0 {
					cNodes := parseProfileNodes(cached)
					if len(cNodes) > 0 {
						results[idx] = fetchResult{id: entry.ID, body: cached, nodes: cNodes, err: errors.New("网络获取失败，使用历史缓存")}
						return
					}
				}
				results[idx] = fetchResult{id: entry.ID, err: errors.New("未能获取到节点")}
			}
		}(i, sub)
	}

	wg.Wait()

	var sources []SubscriptionSource
	for i, r := range results {
		entry := enabledEntries[i]
		if len(r.body) > 0 && len(r.nodes) > 0 {
			sources = append(sources, SubscriptionSource{
				ID:     entry.ID,
				Label:  entry.Label,
				Prefix: entry.Prefix,
				Body:   r.body,
			})
			a.cacheProfileForID(entry.ID, r.body, "yaml")
			lastErr := ""
			if r.err != nil {
				lastErr = r.err.Error()
			}
			a.recordSubscriptionUseAndInfoLocked(entry.ID, len(r.nodes), lastErr, r.userInfo)
		} else {
			lastErr := "未能获取到可用节点"
			if r.err != nil {
				lastErr = r.err.Error()
			}
			a.recordSubscriptionErrorLocked(entry.ID, lastErr)
		}
	}

	if len(sources) == 0 {
		return nil, errors.New("所有已启用的订阅均拉取失败且无可用本地缓存")
	}

	return a.writeMergedSourcesLocked(sources)
}

// mergeEnabledSubscriptionCachesLocked reapplies the current source selection
// without downloading again. Every source included in a successful merge has
// its own last-good profile, which lets a single-source refresh update the live
// aggregate while offline sources keep their prior nodes.
func (a *app) mergeEnabledSubscriptionCachesLocked() ([]proxyNode, error) {
	var sources []SubscriptionSource
	for _, entry := range a.subscriptions {
		if !entry.Enabled || strings.TrimSpace(entry.URL) == "" {
			continue
		}
		body, err := os.ReadFile(a.profileCachePath(entry.ID))
		if err != nil || len(parseProfileNodes(body)) == 0 {
			continue
		}
		sources = append(sources, SubscriptionSource{
			ID: entry.ID, Label: entry.Label, Prefix: entry.Prefix, Body: body,
		})
	}
	if len(sources) == 0 {
		return nil, errors.New("没有可用的订阅缓存，请执行「一键合并更新」")
	}
	return a.writeMergedSourcesLocked(sources)
}

func (a *app) writeMergedSourcesLocked(sources []SubscriptionSource) ([]proxyNode, error) {
	mergedYAML, mergedNodes, err := mergeSubscriptionSources(sources)
	if err != nil {
		return nil, err
	}

	path := a.profilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, errors.New("无法创建本地配置目录")
	}
	tempPath := path + ".tmp"
	if err := os.WriteFile(tempPath, mergedYAML, 0600); err != nil {
		return nil, errors.New("无法保存合并配置")
	}
	if err := os.Rename(tempPath, path); err != nil {
		_ = os.Remove(tempPath)
		return nil, errors.New("无法更新合并配置")
	}
	_ = os.WriteFile(path+".source", []byte(mergedSubscriptionURL), 0600)
	_ = os.WriteFile(path+".format", []byte("merged"), 0600)

	a.settings.SubscriptionURL = mergedSubscriptionURL
	_ = a.saveSettings()

	for i := range mergedNodes {
		mergedNodes[i].Selected = mergedNodes[i].Name == a.settings.SelectedNode
	}
	a.cachedNodes = append([]proxyNode(nil), mergedNodes...)
	_ = a.saveCachedNodesLocked(mergedNodes)
	return mergedNodes, nil
}

func (a *app) addSubscription(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL     string `json:"url"`
		Label   string `json:"label"`
		Prefix  string `json:"prefix"`
		Enabled *bool  `json:"enabled"`
	}
	if err := decodeJSON(r.Body, &body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	rawURL := strings.TrimSpace(body.URL)
	if rawURL == "" {
		http.Error(w, "订阅地址不能为空", http.StatusBadRequest)
		return
	}
	if err := validateSubscriptionURL(rawURL); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	id := subscriptionHash(rawURL)
	for _, entry := range a.subscriptions {
		if entry.ID == id || entry.URL == rawURL {
			http.Error(w, "该订阅已存在", http.StatusConflict)
			return
		}
	}

	label := strings.TrimSpace(body.Label)
	if label == "" {
		label = subscriptionLabel(rawURL)
	}
	enabled := true
	if body.Enabled != nil {
		enabled = *body.Enabled
	}

	newEntry := subscriptionEntry{
		ID:      id,
		URL:     rawURL,
		Label:   label,
		Prefix:  strings.TrimSpace(body.Prefix),
		Enabled: enabled,
	}
	a.subscriptions = append(a.subscriptions, newEntry)
	if err := a.saveSubscriptionsLocked(); err != nil {
		http.Error(w, "保存订阅列表失败", http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]any{"ok": true, "subscription": newEntry})
}

func (a *app) updateSubscription(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID      string `json:"id"`
		URL     string `json:"url,omitempty"`
		Label   string `json:"label"`
		Prefix  string `json:"prefix"`
		Enabled *bool  `json:"enabled"`
	}
	if err := decodeJSON(r.Body, &body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	id := strings.TrimSpace(body.ID)
	if id == "" {
		http.Error(w, "missing subscription id", http.StatusBadRequest)
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	found := false
	var updated subscriptionEntry
	for i := range a.subscriptions {
		if a.subscriptions[i].ID == id {
			if strings.TrimSpace(body.Label) != "" {
				a.subscriptions[i].Label = strings.TrimSpace(body.Label)
			}
			a.subscriptions[i].Prefix = strings.TrimSpace(body.Prefix)
			if body.Enabled != nil {
				a.subscriptions[i].Enabled = *body.Enabled
			}
			if strings.TrimSpace(body.URL) != "" {
				newURL := strings.TrimSpace(body.URL)
				if err := validateSubscriptionURL(newURL); err == nil {
					a.subscriptions[i].URL = newURL
				}
			}
			updated = a.subscriptions[i]
			found = true
			break
		}
	}
	if !found {
		http.Error(w, "没有这个订阅", http.StatusNotFound)
		return
	}
	if err := a.saveSubscriptionsLocked(); err != nil {
		http.Error(w, "无法保存订阅列表", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "subscription": updated})
}

func (a *app) mergeSubscriptions(w http.ResponseWriter, _ *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.kernelRunningLocked() {
		http.Error(w, "断开连接后再进行合并更新", http.StatusConflict)
		return
	}

	nodes, err := a.mergeAllEnabledSubscriptionsLocked()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	a.profileFromCache = false

	writeJSON(w, map[string]any{
		"ok":    true,
		"nodes": len(nodes),
	})
}

func (a *app) refreshSingleSubscription(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if err := decodeJSON(r.Body, &body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	id := strings.TrimSpace(body.ID)
	if id == "" {
		http.Error(w, "missing subscription id", http.StatusBadRequest)
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	updated, err := a.fetchSingleSubscriptionLocked(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if a.settings.SubscriptionURL == mergedSubscriptionURL && updated.Enabled {
		if _, err := a.mergeEnabledSubscriptionCachesLocked(); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
	}
	writeJSON(w, map[string]any{"ok": true, "subscription": updated})
}

// refreshSubscriptionQuota reads only Subscription-Userinfo. It deliberately
// leaves profile caches and the running Mihomo configuration untouched.
func (a *app) refreshSubscriptionQuota(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if err := decodeJSON(r.Body, &body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	id := strings.TrimSpace(body.ID)
	if id == "" {
		http.Error(w, "missing subscription id", http.StatusBadRequest)
		return
	}

	a.mu.Lock()
	entry, ok := a.findSubscriptionLocked(id)
	if !ok {
		a.mu.Unlock()
		http.Error(w, "没有这个订阅", http.StatusNotFound)
		return
	}
	remoteURLs, _ := categorizeSubscriptionInput(entry.URL)
	proxies := a.proxyAddressesForDownloadLocked()
	a.mu.Unlock()

	if len(remoteURLs) == 0 {
		a.recordSubscriptionQuotaError(id, entry.URL, "此订阅没有可刷新的远程流量信息", http.StatusBadRequest, w)
		return
	}

	var userInfo *subscriptionUserInfo
	respondedWithoutInfo := false
	for _, remoteURL := range remoteURLs {
		candidates := []string{remoteURL}
		if alt, ok := clashFormatURL(remoteURL); ok {
			candidates = append(candidates, alt)
		}
		for _, proxy := range proxies {
			for _, candidate := range candidates {
				info, err := fetchSubscriptionUserInfo(candidate, proxy)
				if err == nil {
					userInfo = info
					break
				}
				if errors.Is(err, errNoSubscriptionUserInfo) {
					respondedWithoutInfo = true
				}
			}
			if userInfo != nil {
				break
			}
		}
		if userInfo != nil {
			break
		}
	}

	if userInfo == nil {
		message := "无法连接订阅服务，流量信息未刷新"
		if respondedWithoutInfo {
			message = "订阅服务未返回流量信息"
		}
		a.recordSubscriptionQuotaError(id, entry.URL, message, http.StatusBadGateway, w)
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	for i := range a.subscriptions {
		if a.subscriptions[i].ID != id {
			continue
		}
		if a.subscriptions[i].URL != entry.URL {
			http.Error(w, "订阅已变更，请重新刷新", http.StatusConflict)
			return
		}
		a.subscriptions[i].UserInfo = userInfo
		a.subscriptions[i].QuotaUpdatedAt = time.Now().Unix()
		a.subscriptions[i].QuotaError = ""
		if err := a.saveSubscriptionsLocked(); err != nil {
			http.Error(w, "无法保存流量信息", http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "subscription": a.subscriptions[i]})
		return
	}
	http.Error(w, "订阅已删除，请重新刷新", http.StatusConflict)
}

func (a *app) recordSubscriptionQuotaError(id, originalURL, message string, status int, w http.ResponseWriter) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := range a.subscriptions {
		if a.subscriptions[i].ID != id {
			continue
		}
		if a.subscriptions[i].URL != originalURL {
			http.Error(w, "订阅已变更，请重新刷新", http.StatusConflict)
			return
		}
		a.subscriptions[i].QuotaError = message
		if err := a.saveSubscriptionsLocked(); err != nil {
			http.Error(w, "无法保存流量刷新状态", http.StatusInternalServerError)
			return
		}
		http.Error(w, message, status)
		return
	}
	http.Error(w, "订阅已删除，请重新刷新", http.StatusConflict)
}

func (a *app) renameSubscription(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID    string `json:"id"`
		Label string `json:"label"`
	}
	if err := decodeJSON(r.Body, &body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	label := strings.TrimSpace(body.Label)
	if label == "" || len([]rune(label)) > 40 {
		http.Error(w, "名称需要 1 到 40 个字", http.StatusBadRequest)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	found := false
	for i := range a.subscriptions {
		if a.subscriptions[i].ID == body.ID {
			a.subscriptions[i].Label = label
			found = true
		}
	}
	if !found {
		http.Error(w, "没有这个订阅", http.StatusNotFound)
		return
	}
	if err := a.saveSubscriptionsLocked(); err != nil {
		http.Error(w, "无法保存订阅列表", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (a *app) deleteSubscription(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		http.Error(w, "missing subscription id", http.StatusBadRequest)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.kernelRunningLocked() {
		http.Error(w, "断开连接后再删除订阅", http.StatusConflict)
		return
	}
	entry, ok := a.findSubscriptionLocked(id)
	if !ok {
		http.Error(w, "没有这个订阅", http.StatusNotFound)
		return
	}
	previous := append([]subscriptionEntry(nil), a.subscriptions...)
	kept := make([]subscriptionEntry, 0, len(a.subscriptions))
	for _, item := range a.subscriptions {
		if item.ID != id {
			kept = append(kept, item)
		}
	}
	a.subscriptions = kept
	if a.settings.SubscriptionURL == mergedSubscriptionURL {
		remainingEnabled := false
		for _, item := range a.subscriptions {
			if item.Enabled {
				remainingEnabled = true
				break
			}
		}
		if remainingEnabled {
			if _, err := a.mergeEnabledSubscriptionCachesLocked(); err != nil {
				a.subscriptions = previous
				_ = a.saveSubscriptionsLocked()
				http.Error(w, "无法更新合并节点列表："+err.Error(), http.StatusBadGateway)
				return
			}
		} else {
			// An aggregate with no enabled source must not keep serving nodes from
			// the deleted sources. Manual nodes remain available on their own.
			a.settings.SubscriptionURL = ""
			a.profileFromCache = false
			a.clearNodeBoundStateLocked()
			a.removeConfigLocked()
			_ = os.Remove(a.profilePath())
			_ = os.Remove(a.profilePath() + ".source")
			_ = os.Remove(a.profilePath() + ".format")
		}
	} else if entry.URL == a.settings.SubscriptionURL {
		// Removing the single subscription that was in use leaves the app without
		// one; the nodes it contributed go with it.
		a.settings.SubscriptionURL = ""
		a.profileFromCache = false
		a.clearNodeBoundStateLocked()
		a.removeConfigLocked()
		_ = os.Remove(a.profilePath())
		_ = os.Remove(a.profilePath() + ".source")
		_ = os.Remove(a.profilePath() + ".format")
	}
	if err := a.saveSubscriptionsLocked(); err != nil {
		a.subscriptions = previous
		_ = a.saveSubscriptionsLocked()
		http.Error(w, "无法保存订阅列表", http.StatusInternalServerError)
		return
	}
	_ = os.Remove(a.profileCachePath(id))
	_ = os.Remove(a.profileCachePath(id) + ".format")
	writeJSON(w, map[string]any{"ok": true, "activeUrl": a.settings.SubscriptionURL})
}
