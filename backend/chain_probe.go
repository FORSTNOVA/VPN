package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/metacubex/mihomo/component/dialer"
	"maps"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/netip"
	"os"
	"strings"
	"sync"
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
	var policy map[string][]string
	if a.settings.SubscriptionURL != "" {
		profile, _ := os.ReadFile(a.profilePath())
		policy = entryDNS(profile)
	}
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
	ctx, cancel := context.WithTimeout(r.Context(), 24*time.Second)
	defer cancel()
	result := probeResult{Hops: request.Hops, Target: probeTarget, DiagnosticsVersion: 1}
	entryURLs := policy[strings.ToLower(stringValue(proxies[0]["server"]))]
	observation := new(probeObservation)
	result.Delay, result.WarmDelay, err = measureBoth(ctx, entryURLs, proxies, result.Target, observation)
	observation.mu.Lock()
	result.EntryIP = observation.ip
	if result.EntryIP == "" {
		result.EntryIP = observation.attempt
		result.EntryAttempt = result.EntryIP != ""
	}
	if ip, e := netip.ParseAddr(result.EntryIP); e == nil {
		result.EntryFamily = "IPv6"
		if ip.Unmap().Is4() {
			result.EntryFamily = "IPv4"
		}
	}
	if err != nil {
		result.FailureStage = observation.stage
	}
	observation.mu.Unlock()
	if err != nil {
		if result.Delay > 0 {
			result.WarmError = err.Error()
		} else {
			result.Error = err.Error()
		}
	}
	result.Measured = time.Now().UnixMilli()
	path, _ := json.Marshal(request.Hops)
	if a.store == nil {
		http.Error(w, "测试记录数据库未打开", 503)
		return
	}
	details, _ := json.Marshal(result)
	_, err = a.store.db.Exec("INSERT INTO probe_results(path,fingerprint,delay,error,measured,details) VALUES(?,?,?,?,?,?) ON CONFLICT(path) DO UPDATE SET fingerprint=excluded.fingerprint,delay=excluded.delay,error=excluded.error,measured=excluded.measured,details=excluded.details", string(path), fingerprint, result.Delay, result.Error, result.Measured, string(details))
	if err != nil {
		http.Error(w, "未能保存测试记录", 500)
		return
	}
	writeJSON(w, result)
}

const probeTarget = "https://www.gstatic.com/generate_204"

type probeResult struct {
	EntryAttempt       bool     `json:"entryAttempt,omitempty"`
	DiagnosticsVersion int      `json:"diagnosticsVersion,omitempty"`
	EntryIP            string   `json:"entryIP,omitempty"`
	EntryFamily        string   `json:"entryFamily,omitempty"`
	FailureStage       string   `json:"failureStage,omitempty"`
	Target             string   `json:"target"`
	WarmDelay          int64    `json:"warmDelay,omitempty"`
	WarmError          string   `json:"warmError,omitempty"`
	Hops               []string `json:"hops"`
	Delay              int64    `json:"delay"`
	Error              string   `json:"error,omitempty"`
	Measured           int64    `json:"measured"`
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
	rows, err := a.store.db.Query("SELECT path,fingerprint,delay,error,measured,details FROM probe_results ORDER BY measured DESC")
	if err != nil {
		http.Error(w, "读取测试记录失败", 500)
		return
	}
	defer rows.Close()
	results := []probeResult{}
	for rows.Next() {
		var result probeResult
		var path, signature, details string
		if err := rows.Scan(&path, &signature, &result.Delay, &result.Error, &result.Measured, &details); err != nil {
			http.Error(w, "读取测试记录失败", 500)
			return
		}
		result.Target = probeTarget
		if details != "" {
			if err := json.Unmarshal([]byte(details), &result); err != nil {
				http.Error(w, "测速记录格式错误", 500)
				return
			}
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
	cold, _, err := measureBoth(ctx, nil, proxies, target, new(probeObservation))
	if cold > 0 {
		return cold, nil
	}
	return cold, err
}
func measureBoth(ctx context.Context, entryURLs []string, proxies []map[string]any, target string, obs *probeObservation) (int64, int64, error) {
	started := time.Now()
	var previous C.Dialer
	obs.setStage("加载节点配置")
	for i, p := range proxies {
		delete(p, "dialer-proxy") // Use this request's own preceding adapter, independent of the live selector.
		if i == 0 {
			opts := []dialer.Option{}
			if iface := stringValue(p["interface-name"]); iface != "" {
				opts = append(opts, dialer.WithInterface(iface))
			}
			if mark := intValue(p["routing-mark"]); mark != 0 {
				opts = append(opts, dialer.WithRoutingMark(mark))
			}
			if tlsEnabled(p["tfo"]) {
				opts = append(opts, dialer.WithTFO(true))
			}
			if tlsEnabled(p["mptcp"]) {
				opts = append(opts, dialer.WithMPTCP(true))
			}
			switch strings.ToLower(stringValue(p["ip-version"])) {
			case "ipv4":
				opts = append(opts, dialer.WithOnlySingleStack(true))
			case "ipv6":
				opts = append(opts, dialer.WithOnlySingleStack(false))
			case "ipv4-prefer":
				opts = append(opts, dialer.WithPreferIPv4())
			case "ipv6-prefer":
				opts = append(opts, dialer.WithPreferIPv6())
			}
			iface := stringValue(p["interface-name"])
			rs, err := newEntryResolver(entryURLs, iface, stringValue(p["server"]))
			if err != nil {
				obs.setStage("入口 DNS 配置")
				return 0, 0, err
			}
			if rs != nil {
				opts = append(opts, dialer.WithResolver(rs))
				defer rs.ResetConnection()
				// Mihomo otherwise resolves dynamic ECH via its process-wide DNS.
				// Supply a copy only to this probe, using the same network as its A/AAAA lookup.
				if ech, ok := p["ech-opts"].(map[string]any); ok && tlsEnabled(ech["enable"]) && stringValue(ech["config"]) == "" {
					name := stringValue(ech["query-server-name"])
					if name == "" {
						name = stringValue(p["servername"])
					}
					if name == "" {
						name = stringValue(p["sni"])
					}
					if name == "" {
						name = stringValue(p["server"])
					}
					obs.setStage("入口 ECH 解析")
					lookup, cancel := context.WithDeadline(ctx, started.Add(12*time.Second))
					config, err := rs.ResolveECH(lookup, name)
					cancel()
					if err != nil {
						return 0, 0, errors.New("无法从入口 DNS 获取 ECH 配置")
					}
					p = maps.Clone(p)
					ech = maps.Clone(ech)
					ech["config"] = base64.StdEncoding.EncodeToString(config)
					p["ech-opts"] = ech
				}
			}
			previous = &observedEntry{Dialer: dialer.NewDialer(opts...), obs: obs}
		}
		node, err := adapter.ParseProxy(p, adapter.WithDialerForAPI(previous))
		if err != nil {
			return 0, 0, errors.New("内核无法加载该节点配置")
		}
		defer node.Close()
		previous = proxydialer.New(node, false)
	}
	if previous == nil {
		return 0, 0, errors.New("未选择节点")
	}
	tr := &http.Transport{DialContext: previous.DialContext}
	defer tr.CloseIdleConnections()
	client := http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return timedProbeRequests(ctx, &client, target, obs, 12*time.Second, started)
}

type probeObservation struct {
	mu                 sync.Mutex
	stage, ip, attempt string
}

func (o *probeObservation) setStage(s string) { o.mu.Lock(); o.stage = s; o.mu.Unlock() }
func (o *probeObservation) address(address string, connected bool) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if connected {
		o.ip = ip.Unmap().String()
	} else {
		o.attempt = ip.Unmap().String()
	}
}

type observedEntry struct {
	C.Dialer
	obs *probeObservation
}

func (d *observedEntry) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	d.obs.setStage("入口解析或拨号")
	trace := &httptrace.ClientTrace{ConnectStart: func(_, addr string) { d.obs.address(addr, false); d.obs.setStage("入口拨号（尝试地址）") }}
	conn, err := d.Dialer.DialContext(httptrace.WithClientTrace(ctx, trace), network, address)
	if err != nil {
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) || strings.Contains(err.Error(), "dns resolve failed") {
			d.obs.setStage("入口 DNS 解析")
		}
		return nil, err
	}
	d.obs.address(conn.RemoteAddr().String(), true)
	d.obs.setStage("代理协议握手或链路转发")
	return conn, nil
}
func (d *observedEntry) ListenPacket(ctx context.Context, network, address string, remote netip.AddrPort) (net.PacketConn, error) {
	d.obs.address(remote.String(), false)
	d.obs.setStage("入口 UDP 建连（尝试地址）")
	return d.Dialer.ListenPacket(ctx, network, address, remote)
}

func timeProbeRequests(ctx context.Context, client *http.Client, target string, obs *probeObservation, budget time.Duration) (int64, int64, error) {
	return timedProbeRequests(ctx, client, target, obs, budget, time.Now())
}
func timedProbeRequests(ctx context.Context, client *http.Client, target string, obs *probeObservation, budget time.Duration, started time.Time) (int64, int64, error) {
	request := func(start time.Time) (int64, bool, error) {
		ctx, cancel := context.WithDeadline(ctx, start.Add(budget))
		defer cancel()
		reused := false
		obs.setStage("建立代理连接")
		trace := &httptrace.ClientTrace{
			TLSHandshakeStart: func() { obs.setStage("目标 TLS 握手") },
			TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
				if err == nil {
					obs.setStage("发送 HTTPS 请求")
				}
			},
			GotConn: func(info httptrace.GotConnInfo) { reused = info.Reused; obs.setStage("发送 HTTPS 请求") },
			WroteRequest: func(info httptrace.WroteRequestInfo) {
				if info.Err == nil {
					obs.setStage("等待目标响应头")
				}
			},
		}
		req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), "GET", target, nil)
		if err != nil {
			return 0, false, err
		}
		resp, err := client.Do(req)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return 0, false, errors.New("请求超时")
			}
			return 0, false, errors.New("连接或请求失败")
		}
		elapsed := max(1, time.Since(start).Milliseconds())
		resp.Body.Close()
		if resp.StatusCode != 204 {
			obs.setStage("校验 HTTP 状态")
			return 0, false, fmt.Errorf("HTTP %d，预期 204", resp.StatusCode)
		}
		return elapsed, reused, nil
	}
	first, _, err := request(started)
	if err != nil {
		return 0, 0, err
	}
	warm, reused, err := request(time.Now())
	if err != nil {
		return first, 0, err
	}
	if !reused {
		obs.setStage("连接复用")
		return first, 0, errors.New("服务器未复用首次连接")
	}
	return first, warm, nil
}
