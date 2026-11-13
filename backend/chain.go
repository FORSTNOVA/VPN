package main

import (
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const chainExit = "SmartVPNChain"

type proxyChain struct {
	Name    string   `json:"name,omitempty"`
	Enabled bool     `json:"enabled"`
	Hops    []string `json:"hops,omitempty"`
}

// Build private copies. A provider refresh or an ordinary single-node
// connection must never inherit the dialer of a chain member.
func chainProxies(hops []string, profile []byte, manual []manualNode) ([]map[string]any, error) {
	if len(hops) < 2 || len(hops) > 8 {
		return nil, errors.New("请选择 2 至 8 个不同的节点")
	}
	return prefixProxies(hops, profile, manual)
}

func prefixProxies(hops []string, profile []byte, manual []manualNode) ([]map[string]any, error) {
	index, err := proxyIndex(profile, manual)
	if err != nil {
		return nil, err
	}
	return compilePrefix(hops, index)
}

func proxyIndex(profile []byte, manual []manualNode) (map[string][]map[string]any, error) {
	var source struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	if err := yaml.Unmarshal(profile, &source); err != nil {
		return nil, errors.New("无法读取订阅节点")
	}
	for _, m := range manual {
		source.Proxies = append(source.Proxies, m.Proxy)
	}
	byName := make(map[string][]map[string]any)
	for _, p := range source.Proxies {
		name := stringValue(p["name"])
		byName[name] = append(byName[name], p)
	}
	return byName, nil
}
func compilePrefix(hops []string, byName map[string][]map[string]any) ([]map[string]any, error) {
	if len(hops) < 1 || len(hops) > 8 {
		return nil, errors.New("请选择 1 至 8 个节点")
	}
	seen := make(map[string]bool)
	var result []map[string]any
	var previous string
	for i, name := range hops {
		if seen[name] {
			return nil, fmt.Errorf("第 %d 跳重复选择了同一个节点", i+1)
		}
		seen[name] = true
		if name == "" || len(byName[name]) != 1 {
			return nil, fmt.Errorf("第 %d 跳节点缺失或重名，请重新选择", i+1)
		}
		p := maps.Clone(byName[name][0])
		if stringValue(p["dialer-proxy"]) != "" {
			return nil, fmt.Errorf("第 %d 跳已有前置代理，请选择独立节点", i+1)
		}
		if stringValue(p["server"]) == "" || stringValue(p["type"]) == "" {
			return nil, fmt.Errorf("第 %d 跳缺少代理服务器配置", i+1)
		}
		alias := fmt.Sprintf("SmartVPNChainHop%d", i+1)
		if i == len(hops)-1 {
			alias = chainExit
		}
		if len(byName[alias]) != 0 {
			return nil, fmt.Errorf("节点名称与链路保留名称 %s 冲突", alias)
		}
		p["name"] = alias
		if i > 0 {
			p["dialer-proxy"] = previous
		}
		result = append(result, p)
		previous = alias
	}
	return result, nil
}

func (a *app) buildChain(chain proxyChain, profile []byte) ([]byte, error) {
	if !chain.Enabled {
		return nil, nil
	}
	if a.settings.SubscriptionURL == "" {
		profile = nil
	}
	proxies, err := chainProxies(chain.Hops, profile, a.manualNodes)
	if err != nil {
		return nil, err
	}
	if a.lockedRegion != "" {
		exit := a.regions[chain.Hops[len(chain.Hops)-1]]
		if exit.Status != regionVerified || exit.Country != a.lockedRegion || time.Since(exit.VerifiedAt) > regionTTL {
			return nil, errors.New("链路出口未验证为已锁定地区，请先解除地区锁或选择已验证的同地区出口")
		}
	}
	return yaml.Marshal(map[string]any{"proxies": proxies})
}

func (a *app) updateChain(w http.ResponseWriter, r *http.Request) {
	var next proxyChain
	if err := decodeJSON(r.Body, &next); err != nil || len(next.Hops) > 8 {
		http.Error(w, "无效的链式代理设置", http.StatusBadRequest)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.kernelRunningLocked() || a.regionJobRunningLocked() {
		http.Error(w, "请先断开连接并结束地区检测，再修改链式代理", http.StatusConflict)
		return
	}
	if next.Enabled {
		if next.Name != "" && !slices.Equal(next.Hops, a.settings.Chains[next.Name]) {
			http.Error(w, "代理链已修改，请重新选择", 409)
			return
		}
		profile, _ := os.ReadFile(a.profilePath())
		if _, err := a.buildChain(next, profile); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	old := a.settings.Chain
	a.settings.Chain = next
	if err := a.saveSettings(); err != nil {
		a.settings.Chain = old
		http.Error(w, "未能保存链式代理", http.StatusInternalServerError)
		return
	}
	writeJSON(w, next)
}

func (a *app) saveChainProfile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name, Original string
		Hops           []string
	}
	if decodeJSON(r.Body, &req) != nil {
		http.Error(w, "无效的代理链", 400)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len([]rune(req.Name)) > 80 {
		http.Error(w, "名称须为 1 至 80 个字符", 400)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	old := a.settings.Chains
	if req.Original != "" && old[req.Original] == nil {
		http.Error(w, "原代理链已不存在", 409)
		return
	}
	if req.Name != req.Original && old[req.Name] != nil {
		http.Error(w, "代理链名称已存在", 409)
		return
	}
	profile, _ := os.ReadFile(a.profilePath())
	if a.settings.SubscriptionURL == "" {
		profile = nil
	}
	if _, err := chainProxies(req.Hops, profile, a.manualNodes); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	previous := a.settings.Chain
	if previous.Enabled && req.Original != "" && previous.Name == req.Original {
		if a.kernelRunningLocked() || a.regionJobRunningLocked() {
			http.Error(w, "请先断开连接并结束地区检测，再修改当前使用的代理链", 409)
			return
		}
		next := proxyChain{Name: req.Name, Enabled: true, Hops: req.Hops}
		if _, err := a.buildChain(next, profile); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		a.settings.Chain = next
	}
	profiles := maps.Clone(old)
	if profiles == nil {
		profiles = make(map[string][]string)
	}
	delete(profiles, req.Original)
	profiles[req.Name] = req.Hops
	a.settings.Chains = profiles
	if a.saveSettings() != nil {
		a.settings.Chains = old
		a.settings.Chain = previous
		http.Error(w, "保存失败", 500)
		return
	}
	writeJSON(w, map[string]bool{"saved": true})
}
