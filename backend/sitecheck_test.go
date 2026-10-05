package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// proxyRespondingWith serves the given status for every request, which lets a
// test drive checkSite and checkExitIP without a real proxy in front of them.
func proxyRespondingWith(t *testing.T, handler http.HandlerFunc) *url.URL {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func proxyWithStatus(t *testing.T, status int) *url.URL {
	t.Helper()
	return proxyRespondingWith(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	})
}

func TestCheckSiteMapsStatusCodes(t *testing.T) {
	cases := []struct {
		status int
		state  string
		detail string
	}{
		{200, "ok", "站点已响应"},
		{204, "ok", "站点已响应"},
		{301, "redirect", "站点可达，返回跳转"},
		{302, "redirect", "站点可达，返回跳转"},
		{401, "restricted", "站点已响应，但要求验证或限制访问"},
		{403, "restricted", "站点已响应，但要求验证或限制访问"},
		{429, "restricted", "站点已响应，但要求验证或限制访问"},
		{404, "error", "站点返回错误状态"},
		{500, "error", "站点返回错误状态"},
		{503, "error", "站点返回错误状态"},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			proxyURL := proxyWithStatus(t, tc.status)
			result := checkSite(proxyURL, "Google", "http://probe.invalid/generate_204")
			if result.Name != "Google" {
				t.Errorf("name = %q, want %q", result.Name, "Google")
			}
			if result.State != tc.state {
				t.Errorf("state = %q, want %q", result.State, tc.state)
			}
			if result.Detail != tc.detail {
				t.Errorf("detail = %q, want %q", result.Detail, tc.detail)
			}
			if result.HTTPCode != tc.status {
				t.Errorf("httpCode = %d, want %d", result.HTTPCode, tc.status)
			}
		})
	}
}

func TestCheckSiteReportsUnreachableProxy(t *testing.T) {
	// Nothing listens on port 1, so the proxy hop fails immediately.
	proxyURL, err := url.Parse("http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	result := checkSite(proxyURL, "YouTube", "http://probe.invalid/generate_204")
	if result.State != "error" {
		t.Errorf("state = %q, want %q", result.State, "error")
	}
	if result.Detail != "连接失败，请检查节点或外层网络" {
		t.Errorf("detail = %q, want the connection-failure message", result.Detail)
	}
	if result.HTTPCode != 0 {
		t.Errorf("httpCode = %d, want 0 when nothing responded", result.HTTPCode)
	}
}

func TestCheckSiteRejectsInvalidAddress(t *testing.T) {
	proxyURL := proxyWithStatus(t, http.StatusOK)
	result := checkSite(proxyURL, "ChatGPT", "://bad")
	if result.State != "error" || result.Detail != "检测地址无效" {
		t.Fatalf("got (%q, %q), want an invalid-address error", result.State, result.Detail)
	}
}

func TestCheckExitIP(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		body      string
		wantIPv6  bool
		wantAddr  string
		wantError string
	}{
		{name: "ipv4 answer", status: http.StatusOK, body: `{"ip":"1.2.3.4"}`, wantAddr: "1.2.3.4"},
		{name: "ipv6 answer", status: http.StatusOK, body: `{"ip":"2001:db8::1"}`, wantIPv6: true, wantAddr: "2001:db8::1"},
		{
			name: "ipv4 answer where ipv6 was asked", status: http.StatusOK,
			body: `{"ip":"1.2.3.4"}`, wantIPv6: true, wantError: "出口 IP 类型不符",
		},
		{
			name: "ipv6 answer where ipv4 was asked", status: http.StatusOK,
			body: `{"ip":"2001:db8::1"}`, wantError: "出口 IP 类型不符",
		},
		{name: "unparsable address", status: http.StatusOK, body: `{"ip":"not-an-ip"}`, wantError: "出口 IP 类型不符"},
		{name: "empty address", status: http.StatusOK, body: `{"ip":""}`, wantError: "出口 IP 类型不符"},
		{name: "malformed json", status: http.StatusOK, body: `{`, wantError: "出口 IP 响应无效"},
		{name: "server error", status: http.StatusInternalServerError, body: ``, wantError: "出口 IP 服务未返回地址"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			proxyURL := proxyRespondingWith(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			result := checkExitIP(proxyURL, "http://exit-ip.invalid/", tc.wantIPv6)
			if result.Address != tc.wantAddr {
				t.Errorf("address = %q, want %q", result.Address, tc.wantAddr)
			}
			if result.Error != tc.wantError {
				t.Errorf("error = %q, want %q", result.Error, tc.wantError)
			}
		})
	}
}

func TestCheckExitIPReportsUnreachableProxy(t *testing.T) {
	proxyURL, err := url.Parse("http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	result := checkExitIP(proxyURL, "http://exit-ip.invalid/", false)
	if result.Error != "出口 IP 查询失败" {
		t.Fatalf("error = %q, want the lookup-failure message", result.Error)
	}
}

func TestCheckCloudflareExitIP(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		body      string
		wantAddr  string
		wantError string
	}{
		{name: "ipv4 trace", status: http.StatusOK, body: "fl=abc\nip=1.2.3.4\nwarp=off\n", wantAddr: "1.2.3.4"},
		{name: "ipv6 trace is not an ipv4 answer", status: http.StatusOK, body: "ip=2001:db8::1\n", wantError: "出口 IPv4 响应无效"},
		{name: "trace without an ip line", status: http.StatusOK, body: "fl=abc\nwarp=off\n", wantError: "出口 IPv4 响应无效"},
		{name: "server error", status: http.StatusServiceUnavailable, body: "", wantError: "出口 IPv4 服务未返回地址"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			proxyURL := proxyRespondingWith(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			result := checkCloudflareExitIP(proxyURL, "http://trace.invalid/cdn-cgi/trace")
			if result.Address != tc.wantAddr {
				t.Errorf("address = %q, want %q", result.Address, tc.wantAddr)
			}
			if result.Error != tc.wantError {
				t.Errorf("error = %q, want %q", result.Error, tc.wantError)
			}
		})
	}
}

func TestCheckCloudflareExitIPReportsUnreachableProxy(t *testing.T) {
	proxyURL, err := url.Parse("http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	result := checkCloudflareExitIP(proxyURL, "http://trace.invalid/cdn-cgi/trace")
	if result.Error != "出口 IPv4 查询失败" {
		t.Fatalf("error = %q, want the lookup-failure message", result.Error)
	}
}

func TestCheckTargetsStayOnHTTPS(t *testing.T) {
	if len(checkTargets) == 0 {
		t.Fatal("the protected site list must not be empty")
	}
	names := map[string]bool{}
	for _, target := range checkTargets {
		names[target.name] = true
		parsed, err := url.Parse(target.address)
		if err != nil {
			t.Errorf("%s has an invalid address: %v", target.name, err)
			continue
		}
		if parsed.Scheme != "https" {
			t.Errorf("%s must be probed over https, got %q", target.name, parsed.Scheme)
		}
	}
	for _, required := range []string{"Google", "YouTube", "ChatGPT", "Gemini"} {
		if !names[required] {
			t.Errorf("%s must stay in the protected site list", required)
		}
	}
}

// The confirmation probe asks from inside the service's lock, where every page
// that polls is waiting, so its timeout is the caller's to choose.
func TestCheckSiteWithinGivesUpOnItsOwnTimeout(t *testing.T) {
	proxy := proxyRespondingWith(t, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusNoContent)
	})
	started := time.Now()
	result := checkSiteWithin(proxy, "慢站点", "https://example.test/", 100*time.Millisecond)
	elapsed := time.Since(started)

	if result.State != "error" {
		t.Fatalf("state = %q, want an error", result.State)
	}
	if result.Detail != "连接超时" {
		t.Fatalf("detail = %q, want the timeout verdict", result.Detail)
	}
	if elapsed > 400*time.Millisecond {
		t.Fatalf("the call waited %v, want it to give up at its own timeout", elapsed)
	}
}
