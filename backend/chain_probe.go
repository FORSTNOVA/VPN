package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/metacubex/mihomo/adapter"
	"github.com/metacubex/mihomo/component/proxydialer"
	C "github.com/metacubex/mihomo/constant"
)

func (a *app) probeChain(w http.ResponseWriter, r *http.Request) {
	var request struct{ Hops []string }
	if decodeJSON(r.Body, &request) != nil {
		http.Error(w, "无效节点列表", 400)
		return
	}
	a.mu.Lock()
	index, err := a.currentProxyIndexLocked()
	a.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	proxies, err := compilePrefix(request.Hops, index)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	fingerprint := probeFingerprint(proxies)
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	result := probeResult{Hops: request.Hops}
	result.Delay, err = measurePrefix(ctx, proxies, "https://www.gstatic.com/generate_204")
	if err != nil {
		result.Error = err.Error()
	}
	result.Measured = time.Now().UnixMilli()
	path, _ := json.Marshal(request.Hops)
	if a.store == nil {
		http.Error(w, "测试记录数据库未打开", 503)
		return
	}
	_, err = a.store.db.Exec("INSERT INTO probe_results(path,fingerprint,delay,error,measured) VALUES(?,?,?,?,?) ON CONFLICT(path) DO UPDATE SET fingerprint=excluded.fingerprint,delay=excluded.delay,error=excluded.error,measured=excluded.measured", string(path), fingerprint, result.Delay, result.Error, result.Measured)
	if err != nil {
		http.Error(w, "未能保存测试记录", 500)
		return
	}
	writeJSON(w, result)
}

type probeResult struct {
	Hops     []string `json:"hops"`
	Delay    int64    `json:"delay"`
	Error    string   `json:"error,omitempty"`
	Measured int64    `json:"measured"`
}

func (a *app) currentProxyIndexLocked() (map[string][]map[string]any, error) {
	var profile []byte
	if a.settings.SubscriptionURL != "" {
		profile, _ = os.ReadFile(a.profilePath())
	}
	return proxyIndex(profile, a.manualNodes)
}

func probeFingerprint(proxies []map[string]any) string {
	body, _ := json.Marshal(proxies)
	return fmt.Sprintf("%x", sha256.Sum256(body))
}

func (a *app) listProbeResults(w http.ResponseWriter, _ *http.Request) {
	if a.store == nil {
		http.Error(w, "测试记录数据库未打开", 503)
		return
	}
	a.mu.Lock()
	index, err := a.currentProxyIndexLocked()
	a.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	rows, err := a.store.db.Query("SELECT path,fingerprint,delay,error,measured FROM probe_results ORDER BY measured DESC")
	if err != nil {
		http.Error(w, "读取测试记录失败", 500)
		return
	}
	defer rows.Close()
	results := []probeResult{}
	for rows.Next() {
		var result probeResult
		var path, signature string
		if err := rows.Scan(&path, &signature, &result.Delay, &result.Error, &result.Measured); err != nil {
			http.Error(w, "读取测试记录失败", 500)
			return
		}
		if json.Unmarshal([]byte(path), &result.Hops) != nil {
			continue
		}
		config, err := compilePrefix(result.Hops, index)
		if err == nil && probeFingerprint(config) == signature {
			results = append(results, result)
		}
	}
	if rows.Err() != nil {
		http.Error(w, "读取测试记录失败", 500)
		return
	}
	writeJSON(w, map[string]any{"results": results})
}

func measurePrefix(ctx context.Context, proxies []map[string]any, target string) (int64, error) {
	var previous C.Dialer
	for _, p := range proxies {
		delete(p, "dialer-proxy") // Use this request's own preceding adapter, independent of the live selector.
		node, err := adapter.ParseProxy(p, adapter.WithDialerForAPI(previous))
		if err != nil {
			return 0, errors.New("内核无法加载该节点配置")
		}
		defer node.Close()
		previous = proxydialer.New(node, false)
	}
	if previous == nil {
		return 0, errors.New("未选择节点")
	}
	tr := &http.Transport{DialContext: previous.DialContext, DisableKeepAlives: true}
	defer tr.CloseIdleConnections()
	client := http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, "GET", target, nil)
	if err != nil {
		return 0, err
	}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0, errors.New("此路径未能完成 HTTPS 请求，请检查节点、上游转发能力或测试站点可达性")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 204 {
		return 0, errors.New("测试站点未返回预期的 HTTP 204")
	}
	return max(1, time.Since(start).Milliseconds()), nil
}
