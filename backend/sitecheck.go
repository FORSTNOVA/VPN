package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type siteCheckResult struct {
	Name      string `json:"name"`
	State     string `json:"state"`
	HTTPCode  int    `json:"httpCode,omitempty"`
	LatencyMs int64  `json:"latencyMs,omitempty"`
	Detail    string `json:"detail"`
}

type exitIPResult struct {
	Address string `json:"address,omitempty"`
	Error   string `json:"error,omitempty"`
}

var checkTargets = []struct{ name, address string }{
	{"Google", "https://www.google.com/generate_204"},
	{"YouTube", "https://www.youtube.com/generate_204"},
	{"ChatGPT", "https://chatgpt.com/"},
	{"Gemini", "https://gemini.google.com/app"},
	{"GitHub", "https://api.github.com/zen"},
	{"Cloudflare", "https://www.cloudflare.com/cdn-cgi/trace"},
	{"Netflix", "https://www.netflix.com/title/80018499"},
}

const cloudflareTraceURL = "https://www.cloudflare.com/cdn-cgi/trace"

// ensureKernelForCheckLocked starts the kernel when a check needs one, and pins
// the selector to the user's choice so the check measures the node that would
// actually be used. The returned flag tells the caller to stop it again.
func (a *app) ensureKernelForCheckLocked() (bool, error) {
	if a.kernelRunningLocked() {
		return false, nil
	}
	if err := a.startMihomoLocked(); err != nil {
		return false, err
	}
	choice := a.selectedChoice()
	if choice == autoGroup || choice == fallbackGroup {
		_, _ = a.mihomoRequest(http.MethodPut, "/proxies/SmartVPN", map[string]string{"name": choice})
	} else if choice != "" {
		nodes, err := a.listNodes()
		if err == nil {
			for _, node := range nodes {
				if node.Name == choice {
					_, _ = a.mihomoRequest(http.MethodPut, "/proxies/SmartVPN", map[string]string{"name": node.Name})
					break
				}
			}
		}
	}
	return true, nil
}

func checkExitIP(proxyURL *url.URL, address string, wantIPv6 bool) exitIPResult {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyURL(proxyURL)
	transport.DisableKeepAlives = true
	defer transport.CloseIdleConnections()
	client := http.Client{Timeout: 8 * time.Second, Transport: transport}
	response, err := client.Get(address)
	if err != nil {
		return exitIPResult{Error: "出口 IP 查询失败"}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return exitIPResult{Error: "出口 IP 服务未返回地址"}
	}
	var answer struct {
		IP string `json:"ip"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 256)).Decode(&answer); err != nil {
		return exitIPResult{Error: "出口 IP 响应无效"}
	}
	ip := net.ParseIP(answer.IP)
	if ip == nil || (ip.To4() == nil) != wantIPv6 {
		return exitIPResult{Error: "出口 IP 类型不符"}
	}
	return exitIPResult{Address: ip.String()}
}

func checkCloudflareExitIP(proxyURL *url.URL, address string) exitIPResult {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyURL(proxyURL)
	transport.DisableKeepAlives = true
	defer transport.CloseIdleConnections()
	client := http.Client{Timeout: 8 * time.Second, Transport: transport}
	response, err := client.Get(address)
	if err != nil {
		return exitIPResult{Error: "出口 IPv4 查询失败"}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return exitIPResult{Error: "出口 IPv4 服务未返回地址"}
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 2048))
	if err != nil {
		return exitIPResult{Error: "出口 IPv4 响应无效"}
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "ip=") {
			continue
		}
		ip := net.ParseIP(strings.TrimSpace(strings.TrimPrefix(line, "ip=")))
		if ip != nil && ip.To4() != nil {
			return exitIPResult{Address: ip.String()}
		}
	}
	return exitIPResult{Error: "出口 IPv4 响应无效"}
}

// checkSite asks one site through the proxy, with the timeout the check page
// wants for a page a person is waiting on.
func checkSite(proxyURL *url.URL, name, address string) siteCheckResult {
	return checkSiteWithin(proxyURL, name, address, 8*time.Second)
}

// checkSiteWithin is the same question with a timeout the caller chooses. The
// health machine asks with a shorter one: it asks from inside the service's
// lock, where every page that polls is waiting on the answer.
func checkSiteWithin(proxyURL *url.URL, name, address string, timeout time.Duration) siteCheckResult {
	result := siteCheckResult{Name: name}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyURL(proxyURL)
	transport.DisableKeepAlives = true
	defer transport.CloseIdleConnections()
	client := http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	request, err := http.NewRequest(http.MethodGet, address, nil)
	if err != nil {
		result.State, result.Detail = "error", "检测地址无效"
		return result
	}
	request.Header.Set("User-Agent", "SmartVPN-site-check/1.0")
	start := time.Now()
	response, err := client.Do(request)
	result.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		result.State = "error"
		var networkError net.Error
		switch {
		case errors.Is(err, context.DeadlineExceeded), errors.As(err, &networkError) && networkError.Timeout():
			result.Detail = "连接超时"
		case strings.Contains(strings.ToLower(err.Error()), "tls"):
			result.Detail = "TLS 握手失败"
		default:
			result.Detail = "连接失败，请检查节点或外层网络"
		}
		return result
	}
	defer response.Body.Close()
	result.HTTPCode = response.StatusCode
	switch {
	case response.StatusCode >= 200 && response.StatusCode < 300:
		result.State, result.Detail = "ok", "站点已响应"
	case response.StatusCode >= 300 && response.StatusCode < 400:
		result.State, result.Detail = "redirect", "站点可达，返回跳转"
	case response.StatusCode == 401 || response.StatusCode == 403 || response.StatusCode == 429:
		result.State, result.Detail = "restricted", "站点已响应，但要求验证或限制访问"
	default:
		result.State, result.Detail = "error", "站点返回错误状态"
	}
	return result
}
