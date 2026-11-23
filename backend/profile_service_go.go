package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// The Go oracle keeps legacy ordering but every external action is an explicit port.
type profileService struct {
	s  *profileServiceState
	fx profileServiceEffects
}

func (a *profileService) port(q profileEffect) profileReply {
	switch q.Kind {
	case "prepare-live-config", "apply-live-config", "save-settings", "save-nodes", "registry":
		return a.fx.Call(q, a.s)
	}
	// Concurrent acquisition workers receive private snapshots and cannot mutate live state.
	view := *a.s
	return a.fx.Call(q, &view)
}
func (a *profileService) profilePath() string    { return a.s.ProfilePath }
func (a *profileService) selectedChoice() string { return a.s.SelectedChoice }
func (a *profileService) ReadFile(p string) ([]byte, error) {
	r := a.port(profileEffect{Kind: "read", Path: p})
	return r.Body, r.Err
}
func (a *profileService) WriteFile(p string, b []byte, m os.FileMode) error {
	return a.port(profileEffect{Kind: "write", Path: p, Body: b, Mode: uint32(m)}).Err
}
func (a *profileService) MkdirAll(p string, m os.FileMode) error {
	return a.port(profileEffect{Kind: "mkdir", Path: p, Mode: uint32(m)}).Err
}
func (a *profileService) Rename(p, q string) error {
	return a.port(profileEffect{Kind: "rename", Path: p, OtherPath: q}).Err
}
func (a *profileService) Remove(p string) error {
	return a.port(profileEffect{Kind: "remove", Path: p}).Err
}
func (a *profileService) parseProfileNodes(b []byte) []proxyNode {
	return a.port(profileEffect{Kind: "parse", Body: b}).Nodes
}
func (a *profileService) ensureClashYAML(b []byte) ([]byte, []proxyNode, error) {
	r := a.port(profileEffect{Kind: "ensure-yaml", Body: b})
	return r.Body, r.Nodes, r.Err
}
func (a *profileService) mergeSubscriptionProfiles(b [][]byte) ([]byte, []proxyNode, error) {
	r := a.port(profileEffect{Kind: "merge-profiles", Parts: b})
	return r.Body, r.Nodes, r.Err
}
func (a *profileService) mergeSubscriptionSources(b []SubscriptionSource) ([]byte, []proxyNode, error) {
	r := a.port(profileEffect{Kind: "merge-sources", Sources: b})
	return r.Body, r.Nodes, r.Err
}
func (a *profileService) downloadProfileWithInfo(u, p string) ([]byte, []proxyNode, *subscriptionUserInfo, error) {
	r := a.port(profileEffect{Kind: "download", URL: u, Proxy: p})
	return r.Body, r.Nodes, r.Info, r.Err
}
func (a *profileService) downloadProfile(u, p string) ([]byte, []proxyNode, error) {
	b, n, _, e := a.downloadProfileWithInfo(u, p)
	return b, n, e
}
func (a *profileService) kernelRunningLocked() bool {
	return a.port(profileEffect{Kind: "running"}).Bool
}
func (a *profileService) proxyState() (bool, string, error) {
	r := a.port(profileEffect{Kind: "proxy-state"})
	return r.Bool, r.Text, r.Err
}
func (a *profileService) applyLiveConfig(c string) error {
	return a.port(profileEffect{Kind: "apply-live-config", Format: c}).Err
}
func (a *profileService) saveSettings() error {
	return a.port(profileEffect{Kind: "save-settings"}).Err
}
func (a *profileService) saveCachedNodesLocked(n []proxyNode) error {
	return a.port(profileEffect{Kind: "save-nodes", Nodes: n}).Err
}
func (a *profileService) nowUnix() int64 { return a.port(profileEffect{Kind: "now"}).Timestamp }
func (a *profileService) logf(f string, v ...any) {
	a.port(profileEffect{Kind: "log", Format: fmt.Sprintf(f, v...)})
}
func (a *profileService) findSubscriptionLocked(id string) (subscriptionEntry, bool) {
	for _, e := range a.s.Subscriptions {
		if e.ID == id {
			return e, true
		}
	}
	return subscriptionEntry{}, false
}
func (a *profileService) findSubscriptionByURL(u string) (subscriptionEntry, bool) {
	for _, e := range a.s.Subscriptions {
		if e.URL == u || e.ID == subscriptionHash(u) {
			return e, true
		}
	}
	return subscriptionEntry{}, false
}
func (a *profileService) recordSubscriptionUseLocked(id string, n int, f string) {
	a.recordSubscriptionUseAndInfoLocked(id, n, f, nil)
}
func (a *profileService) recordSubscriptionUseAndInfoLocked(id string, n int, f string, info *subscriptionUserInfo) {
	a.port(profileEffect{Kind: "registry", Registry: subscriptionServiceRequest{Op: "record-use", ID: id, NodeCount: n, Failure: f, Info: info}})
}
func (a *profileService) recordSubscriptionErrorLocked(id, f string) {
	a.port(profileEffect{Kind: "registry", Registry: subscriptionServiceRequest{Op: "record-error", ID: id, Failure: f}})
}

func (a *profileService) profileCachePath(id string) string {
	return filepath.Join(a.s.Home, "subscriptions", id+".yaml")
}

func (a *profileService) cacheProfileForID(id string, body []byte, format string) {
	if body == nil || id == "" {
		return
	}
	path := a.profileCachePath(id)
	if a.MkdirAll(filepath.Dir(path), 0700) != nil {
		return
	}
	if a.WriteFile(path, body, 0600) != nil {
		return
	}
	_ = a.WriteFile(path+".format", []byte(format), 0600)
}

func (a *profileService) cacheProfileLocked(body []byte, format string) {
	if body == nil || a.s.Settings.SubscriptionURL == "" {
		return
	}
	a.cacheProfileForID(subscriptionHash(a.s.Settings.SubscriptionURL), body, format)
}

func (a *profileService) useCachedProfileLocked(id string) error {
	body, err := a.ReadFile(a.profileCachePath(id))
	if err != nil {
		return errors.New("没有这个订阅的缓存副本")
	}
	if len(a.parseProfileNodes(body)) == 0 {
		return errors.New("缓存副本里没有可用节点")
	}
	entry, ok := a.findSubscriptionLocked(id)
	if !ok {
		return errors.New("没有这个订阅")
	}
	path := a.profilePath()
	if err := a.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return errors.New("无法创建本地配置目录")
	}
	temp := path + ".tmp"
	if err := a.WriteFile(temp, body, 0600); err != nil {
		return errors.New("无法写入缓存副本")
	}
	if err := a.Rename(temp, path); err != nil {
		_ = a.Remove(temp)
		return errors.New("无法启用缓存副本")
	}
	sourceContent := subscriptionHash(entry.URL)
	if entry.URL == mergedSubscriptionURL {
		sourceContent = mergedSubscriptionURL
	}
	if err := a.WriteFile(path+".source", []byte(sourceContent), 0600); err != nil {
		return errors.New("无法记录配置来源")
	}
	format, readErr := a.ReadFile(a.profileCachePath(id) + ".format")
	if readErr == nil {
		_ = a.WriteFile(path+".format", format, 0600)
	}
	nodes := a.parseProfileNodes(body)
	a.labelSingleSource(nodes)
	for i := range nodes {
		nodes[i].Selected = nodes[i].Name == a.s.Settings.SelectedNode
	}
	a.s.CachedNodes = append([]proxyNode(nil), nodes...)
	markSettingsCachedNodes(&a.s.settingsServiceState)
	if err := a.saveCachedNodesLocked(nodes); err != nil {
		return errors.New("无法保存缓存副本的节点列表")
	}
	return nil
}

func (a *profileService) cachedSources() []SubscriptionSource {
	var sources []SubscriptionSource
	for _, entry := range a.s.Subscriptions {
		if !entry.Enabled || strings.TrimSpace(entry.URL) == "" {
			continue
		}
		body, err := a.ReadFile(a.profileCachePath(entry.ID))
		if err != nil || len(a.parseProfileNodes(body)) == 0 {
			continue
		}
		sources = append(sources, SubscriptionSource{
			ID: entry.ID, Label: entry.Label, Prefix: entry.Prefix, Body: body,
		})
	}
	return sources
}

func (a *profileService) replaceSubscriptionProfile(path string, body []byte) error {
	tmp := path + ".tmp"
	if err := a.WriteFile(tmp, body, 0600); err != nil {
		return err
	}
	if err := a.Rename(tmp, path); err != nil {
		_ = a.Remove(tmp)
		return err
	}
	return nil
}

func (a *profileService) writeMergedSourcesLocked(sources []SubscriptionSource) ([]proxyNode, error) {
	mergedYAML, mergedNodes, err := a.mergeSubscriptionSources(sources)
	if err != nil {
		return nil, err
	}

	path := a.profilePath()
	if err := a.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, errors.New("无法创建本地配置目录")
	}
	var liveConfig string
	var previous []byte
	running := a.kernelRunningLocked()
	if running {
		if selected := a.selectedChoice(); selected != "" && selected != autoGroup && selected != fallbackGroup && selected != chainExit && selected != "REJECT" {
			found := false
			for _, node := range mergedNodes {
				found = found || node.Name == selected
			}
			for _, node := range a.s.ManualNodes {
				found = found || node.Name == selected
			}
			if !found {
				return nil, fmt.Errorf("合并会移除当前线路 %s，请勾选其订阅或先切换线路", selected)
			}
		}
		// A missing provider is expected when the connection started with only manual nodes.
		previous, err = a.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		config := a.port(profileEffect{Kind: "prepare-live-config", Body: mergedYAML})
		if config.Err != nil {
			return nil, config.Err
		}
		liveConfig = config.Text
	}
	if err := a.replaceSubscriptionProfile(path, mergedYAML); err != nil {
		return nil, err
	}
	if running {
		// Updating just the provider misses merged rules, DNS policies, and groups
		// created when a manual-only connection gains its first subscription.
		if err := a.applyLiveConfig(liveConfig); err != nil {
			var restoreErr error
			if previous == nil {
				restoreErr = a.Remove(path)
			} else {
				restoreErr = a.replaceSubscriptionProfile(path, previous)
			}
			return nil, errors.Join(fmt.Errorf("内核未确认合并配置：%w", err), restoreErr)
		}
	}
	_ = a.WriteFile(path+".source", []byte(mergedSubscriptionURL), 0600)
	_ = a.WriteFile(path+".format", []byte("merged"), 0600)

	a.s.Settings.SubscriptionURL = mergedSubscriptionURL
	markSettingsControlIntent(&a.s.settingsServiceState, controlSubscriptionURL)
	_ = a.saveSettings()

	for i := range mergedNodes {
		mergedNodes[i].Selected = mergedNodes[i].Name == a.s.Settings.SelectedNode
	}
	a.s.CachedNodes = append([]proxyNode(nil), mergedNodes...)
	markSettingsCachedNodes(&a.s.settingsServiceState)
	_ = a.saveCachedNodesLocked(mergedNodes)
	return mergedNodes, nil
}

func (a *profileService) mergeEnabledSubscriptionCachesLocked() ([]proxyNode, error) {
	sources := a.cachedSources()
	if len(sources) == 0 {
		return nil, errors.New("没有可用的订阅缓存，请执行「一键合并更新」")
	}
	return a.writeMergedSourcesLocked(sources)
}

func (a *profileService) fetchSingleSubscriptionLocked(id string) (*subscriptionEntry, error) {
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
		body, nodes, err = a.ensureClashYAML(inlineBody)
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
			body, nodes, err = a.mergeSubscriptionProfiles(parts)
		} else {
			err = errors.New("无法拉取该订阅节点")
		}
	}

	if err != nil || len(nodes) == 0 {
		if cached, readErr := a.ReadFile(a.profileCachePath(id)); readErr == nil && len(cached) > 0 {
			cNodes := a.parseProfileNodes(cached)
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

func (a *profileService) mergeAllEnabledSubscriptionsLocked() ([]proxyNode, error) {
	var enabledEntries []subscriptionEntry
	for _, sub := range a.s.Subscriptions {
		if sub.Enabled && strings.TrimSpace(sub.URL) != "" {
			enabledEntries = append(enabledEntries, sub)
		}
	}
	if len(enabledEntries) == 0 {
		return nil, errors.New("没有已启用的订阅源，请至少勾选启用一个订阅")
	}

	results := a.port(profileEffect{Kind: "acquire-batch", Entries: enabledEntries}).Results

	var sources []SubscriptionSource
	for i, r := range results {
		entry := enabledEntries[i]
		if len(r.Body) > 0 && len(r.Nodes) > 0 {
			sources = append(sources, SubscriptionSource{
				ID:     entry.ID,
				Label:  entry.Label,
				Prefix: entry.Prefix,
				Body:   r.Body,
			})
			a.cacheProfileForID(entry.ID, r.Body, "yaml")
			lastErr := ""
			if r.Err != nil {
				lastErr = r.Err.Error()
			}
			a.recordSubscriptionUseAndInfoLocked(entry.ID, len(r.Nodes), lastErr, r.Info)
		} else {
			lastErr := "未能获取到可用节点"
			if r.Err != nil {
				lastErr = r.Err.Error()
			}
			a.recordSubscriptionErrorLocked(entry.ID, lastErr)
		}
	}

	if len(sources) == 0 {
		return nil, errors.New("所有已启用的订阅均拉取失败且无可用本地缓存")
	}

	return a.writeMergedSourcesLocked(sources)
}

func (a *profileService) autoMergeTickLocked() {
	if a.s.Settings.AutoMergeHours <= 0 {
		return
	}
	var enabledCount int
	var latestUpdate int64
	for _, s := range a.s.Subscriptions {
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
	intervalSec := int64(a.s.Settings.AutoMergeHours) * 3600
	if latestUpdate > 0 && a.nowUnix()-latestUpdate < intervalSec {
		return
	}
	a.logf("auto-merge: triggering scheduled subscription merge (interval: %dh)", a.s.Settings.AutoMergeHours)
	nodes, err := a.mergeAllEnabledSubscriptionsLocked()
	if err != nil {
		a.logf("auto-merge failed: %v", err)
		return
	}
	a.logf("auto-merge completed: merged %d nodes", len(nodes))
}

func (a *profileService) labelSingleSource(nodes []proxyNode) {
	if a.s.Settings.SubscriptionURL == "" || a.s.Settings.SubscriptionURL == mergedSubscriptionURL {
		return
	}
	label := "当前订阅"
	for _, entry := range a.s.Subscriptions {
		if entry.URL == a.s.Settings.SubscriptionURL && entry.Label != "" {
			label = entry.Label
			break
		}
	}
	annotateProfileNodes(nodes, label, nil)
}

func (a *profileService) profileMatchesSubscription() bool {
	profile, err := a.ReadFile(a.profilePath())
	if err != nil || len(a.parseProfileNodes(profile)) == 0 {
		return false
	}
	actual, err := a.ReadFile(a.profilePath() + ".source")
	if err != nil {
		return false
	}
	actualStr := strings.TrimSpace(string(actual))
	if a.s.Settings.SubscriptionURL == mergedSubscriptionURL {
		return actualStr == mergedSubscriptionURL
	}
	return actualStr == subscriptionHash(a.s.Settings.SubscriptionURL)
}

func (a *profileService) proxyAddressesForDownloadLocked() []string {
	var list []string
	if a.kernelRunningLocked() && a.s.MixedPort > 0 {
		list = append(list, net.JoinHostPort("127.0.0.1", strconv.Itoa(a.s.MixedPort)))
		list = append(list, "direct")
		return list
	}
	list = append(list, "direct")
	if enabled, srv, err := a.proxyState(); err == nil && enabled && strings.TrimSpace(srv) != "" {
		list = append(list, strings.TrimSpace(srv))
	}
	list = append(list, "")
	return list
}

func (a *profileService) downloadSubscriptionWithFallback(subscriptionURL string) ([]byte, []proxyNode, *subscriptionUserInfo, error) {
	candidates := []string{subscriptionURL}
	if alt, ok := clashFormatURL(subscriptionURL); ok {
		candidates = append(candidates, alt)
	}
	proxies := a.proxyAddressesForDownloadLocked()
	var lastErr error
	for _, proxy := range proxies {
		for _, u := range candidates {
			body, nodes, uinfo, err := a.downloadProfileWithInfo(u, proxy)
			if err == nil && len(nodes) > 0 {
				return body, nodes, uinfo, nil
			}
			if err != nil {
				lastErr = err
			}
		}
	}
	if lastErr != nil {
		return nil, nil, nil, lastErr
	}
	return nil, nil, nil, errSubscriptionNetwork
}

func (a *profileService) fetchProfileLocked() ([]proxyNode, error) {
	if a.s.Settings.SubscriptionURL == "" {
		return nil, errors.New("set a subscription URL first")
	}
	if a.s.Settings.SubscriptionURL == mergedSubscriptionURL {
		return a.mergeAllEnabledSubscriptionsLocked()
	}
	remoteURLs, inlineLines := categorizeSubscriptionInput(a.s.Settings.SubscriptionURL)
	if len(remoteURLs) == 0 && len(inlineLines) == 0 {
		return nil, errors.New("set a subscription URL first")
	}

	var body []byte
	var nodes []proxyNode
	chosenFormat := ""
	hadNoNodes := false

	if len(remoteURLs) == 0 && len(inlineLines) > 0 {
		// Pure inline node link(s) pasted directly into the subscription box
		inlineBody := []byte(strings.Join(inlineLines, "\n"))
		clashBody, clashNodes, err := a.ensureClashYAML(inlineBody)
		if err == nil && len(clashNodes) > 0 {
			body = clashBody
			nodes = clashNodes
			chosenFormat = "inline"
		} else {
			hadNoNodes = true
		}
	} else if len(remoteURLs) == 1 && len(inlineLines) == 0 {
		// Single remote HTTP(S) subscription
		targetURL := remoteURLs[0]
		type candidate struct{ address, format string }
		candidates := []candidate{{targetURL, "original"}}
		if alternate, ok := clashFormatURL(targetURL); ok {
			candidates = append(candidates, candidate{alternate, "cla=1"})
		}
		for _, proxyAddress := range a.proxyAddressesForDownloadLocked() {
			for _, attempt := range candidates {
				response, parsed, err := a.downloadProfile(attempt.address, proxyAddress)
				if err == nil {
					clashBody, clashNodes, convErr := a.ensureClashYAML(response)
					if convErr == nil && len(clashNodes) > 0 {
						body, nodes, chosenFormat = clashBody, clashNodes, attempt.format
					} else {
						body, nodes, chosenFormat = response, parsed, attempt.format
					}
					break
				}
				if errors.Is(err, errNoUsableNodes) {
					hadNoNodes = true
				}
			}
			if chosenFormat != "" {
				break
			}
		}
	} else {
		// Multiple remote subscriptions and/or mixed remote + inline nodes
		var downloaded [][]byte
		for _, u := range remoteURLs {
			resp, _, _, err := a.downloadSubscriptionWithFallback(u)
			if err == nil && len(resp) > 0 {
				downloaded = append(downloaded, resp)
			} else if errors.Is(err, errNoUsableNodes) {
				hadNoNodes = true
			}
		}
		if len(inlineLines) > 0 {
			downloaded = append(downloaded, []byte(strings.Join(inlineLines, "\n")))
		}
		if len(downloaded) > 0 {
			mergedBody, mergedNodes, err := a.mergeSubscriptionProfiles(downloaded)
			if err == nil && len(mergedNodes) > 0 {
				body = mergedBody
				nodes = mergedNodes
				chosenFormat = fmt.Sprintf("merged-%d", len(downloaded))
			}
		}
	}

	if chosenFormat == "" {
		if a.profileMatchesSubscription() {
			profile, readErr := a.ReadFile(a.profilePath())
			if readErr == nil {
				nodes = a.parseProfileNodes(profile)
				for i := range nodes {
					nodes[i].Selected = nodes[i].Name == a.s.Settings.SelectedNode
				}
				a.labelSingleSource(nodes)
				a.s.CachedNodes = append([]proxyNode(nil), nodes...)
				markSettingsCachedNodes(&a.s.settingsServiceState)
				a.s.ProfileFromCache = true
				markCatalogFromCache(&a.s.settingsServiceState, true)
				return nodes, nil
			}
		}
		if entry, ok := a.findSubscriptionByURL(a.s.Settings.SubscriptionURL); ok {
			if cached, readErr := a.ReadFile(a.profileCachePath(entry.ID)); readErr == nil && len(cached) > 0 {
				nodes = a.parseProfileNodes(cached)
				if len(nodes) > 0 {
					_ = a.useCachedProfileLocked(entry.ID)
					a.s.ProfileFromCache = true
					markCatalogFromCache(&a.s.settingsServiceState, true)
					return nodes, nil
				}
			}
		}
		if profile, readErr := a.ReadFile(a.profilePath()); readErr == nil && len(profile) > 0 {
			nodes = a.parseProfileNodes(profile)
			if len(nodes) > 0 {
				for i := range nodes {
					nodes[i].Selected = nodes[i].Name == a.s.Settings.SelectedNode
				}
				a.s.CachedNodes = append([]proxyNode(nil), nodes...)
				markSettingsCachedNodes(&a.s.settingsServiceState)
				a.s.ProfileFromCache = true
				markCatalogFromCache(&a.s.settingsServiceState, true)
				return nodes, nil
			}
		}
		if len(a.s.CachedNodes) > 0 {
			a.s.ProfileFromCache = true
			markCatalogFromCache(&a.s.settingsServiceState, true)
			return a.s.CachedNodes, nil
		}
		if hadNoNodes {
			return nil, errNoUsableNodes
		}
		return nil, errSubscriptionNetwork
	}
	path := a.profilePath()
	if err := a.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, errors.New("could not create the local profile directory")
	}
	tempPath := path + ".tmp"
	if err := a.WriteFile(tempPath, body, 0600); err != nil {
		return nil, errors.New("could not save the local profile")
	}
	if err := a.Rename(tempPath, path); err != nil {
		_ = a.Remove(tempPath)
		return nil, errors.New("could not update the local profile")
	}
	if err := a.WriteFile(path+".source", []byte(subscriptionHash(a.s.Settings.SubscriptionURL)), 0600); err != nil {
		return nil, errors.New("could not save the profile source")
	}
	if err := a.WriteFile(path+".format", []byte(chosenFormat), 0600); err != nil {
		return nil, errors.New("could not save the profile format")
	}
	// A fresh copy is what a later switch falls back to, and the subscription
	// list shows when it was last good.
	a.s.ProfileFromCache = false
	markCatalogFromCache(&a.s.settingsServiceState, false)
	a.cacheProfileLocked(body, chosenFormat)
	a.recordSubscriptionUseLocked(subscriptionHash(a.s.Settings.SubscriptionURL), len(nodes), "")
	for i := range nodes {
		nodes[i].Selected = nodes[i].Name == a.s.Settings.SelectedNode
	}
	a.labelSingleSource(nodes)
	a.s.CachedNodes = append([]proxyNode(nil), nodes...)
	markSettingsCachedNodes(&a.s.settingsServiceState)
	if err := a.saveCachedNodesLocked(nodes); err != nil {
		return nil, errors.New("could not save the fetched node list")
	}
	return nodes, nil
}

func runGoProfileService(s *profileServiceState, q profileServiceRequest, fx profileServiceEffects) (r profileServiceResult) {
	a := profileService{s, fx}
	r.Status = 200
	switch q.Op {
	case "acquire-entry":
		r.Acquisition = a.acquireEntry(q.Entry)
	case "cache-id":
		a.cacheProfileForID(q.ID, q.Body, q.Format)
	case "cache-active":
		a.cacheProfileLocked(q.Body, q.Format)
	case "use-cache":
		r.Err = a.useCachedProfileLocked(q.ID)
	case "cached-sources":
		r.Sources = a.cachedSources()
	case "replace":
		r.Err = a.replaceSubscriptionProfile(q.Path, q.Body)
	case "write-merged":
		r.Nodes, r.Err = a.writeMergedSourcesLocked(q.Sources)
	case "merge-caches":
		r.Nodes, r.Err = a.mergeEnabledSubscriptionCachesLocked()
	case "proxies":
		r.Proxies = a.proxyAddressesForDownloadLocked()
	case "download":
		r.Body, r.Nodes, r.Info, r.Err = a.downloadSubscriptionWithFallback(q.URL)
	case "fetch":
		r.Nodes, r.Err = a.fetchProfileLocked()
	case "fetch-single":
		r.Entry, r.Err = a.fetchSingleSubscriptionLocked(q.ID)
	case "merge-all":
		r.Nodes, r.Err = a.mergeAllEnabledSubscriptionsLocked()
	case "auto-tick":
		a.autoMergeTickLocked()
	case "merge":
		r.Nodes, r.Err = a.mergeAllEnabledSubscriptionsLocked()
		if r.Err != nil {
			r.Status = 502
			return
		}
		s.ProfileFromCache = false
		markCatalogFromCache(&s.settingsServiceState, false)
		r.Data = map[string]any{"ok": true, "nodes": len(r.Nodes)}
	case "refresh-single":
		id := strings.TrimSpace(q.ID)
		if id == "" {
			r.Status = 400
			r.Err = errors.New("missing subscription id")
			return
		}
		r.Entry, r.Err = a.fetchSingleSubscriptionLocked(id)
		if r.Err != nil {
			r.Status = 502
			return
		}
		if s.Settings.SubscriptionURL == mergedSubscriptionURL && r.Entry.Enabled {
			_, r.Err = a.mergeEnabledSubscriptionCachesLocked()
			if r.Err != nil {
				r.Status = 502
				return
			}
		}
		r.Data = map[string]any{"ok": true, "subscription": r.Entry}
	default:
		panic("unknown profile operation: " + q.Op)
	}
	return
}

func (a *profileService) acquireEntry(entry subscriptionEntry) profileAcquisitionResult {
	remoteURLs, inlineLines := categorizeSubscriptionInput(entry.URL)
	if len(remoteURLs) == 0 && len(inlineLines) > 0 {
		inlineBody := []byte(strings.Join(inlineLines, "\n"))
		cBody, cNodes, err := a.ensureClashYAML(inlineBody)
		return profileAcquisitionResult{ID: entry.ID, Body: cBody, Nodes: cNodes, Err: err}
	}
	if len(remoteURLs) == 1 && len(inlineLines) == 0 {
		u := remoteURLs[0]
		body, nodes, uinfo, err := a.downloadSubscriptionWithFallback(u)
		if err != nil {
			if cached, readErr := a.ReadFile(a.profileCachePath(entry.ID)); readErr == nil && len(cached) > 0 {
				cNodes := a.parseProfileNodes(cached)
				if len(cNodes) > 0 {
					return profileAcquisitionResult{ID: entry.ID, Body: cached, Nodes: cNodes, Info: uinfo, Err: errors.New("网络获取失败，使用历史缓存")}
				}
			}
			return profileAcquisitionResult{ID: entry.ID, Err: err}
		}
		return profileAcquisitionResult{ID: entry.ID, Body: body, Nodes: nodes, Info: uinfo, Err: nil}
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
		mBody, mNodes, err := a.mergeSubscriptionProfiles(parts)
		return profileAcquisitionResult{ID: entry.ID, Body: mBody, Nodes: mNodes, Info: uinfo, Err: err}
	} else {
		if cached, readErr := a.ReadFile(a.profileCachePath(entry.ID)); readErr == nil && len(cached) > 0 {
			cNodes := a.parseProfileNodes(cached)
			if len(cNodes) > 0 {
				return profileAcquisitionResult{ID: entry.ID, Body: cached, Nodes: cNodes, Err: errors.New("网络获取失败，使用历史缓存")}
			}
		}
		return profileAcquisitionResult{ID: entry.ID, Err: errors.New("未能获取到节点")}
	}
}
