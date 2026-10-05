package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type subscriptionProxy struct {
	Name    string `yaml:"name"`
	Type    string `yaml:"type"`
	Network string `yaml:"network"`
	// The node's own host, which is what a check has to resolve: the suffix it
	// lives under is not a name that answers.
	Server string `yaml:"server"`
	TLS    any    `yaml:"tls"`
	WsOpts  struct {
		Path    string         `yaml:"path"`
		Headers map[string]any `yaml:"headers"`
	} `yaml:"ws-opts"`
}

var (
	errSubscriptionNetwork = errors.New("subscription network request failed; check connectivity or firewall")
	errNoUsableNodes       = errors.New("subscription response contained no usable nodes")
)

func decodeSubscriptionBase64(input string) ([]byte, error) {
	input = strings.Join(strings.Fields(input), "")
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if decoded, err := encoding.DecodeString(input); err == nil {
			return decoded, nil
		}
	}
	return nil, errors.New("invalid Base64 subscription")
}

// yamlProfileNodes returns the selectable nodes when body parses as a Clash
// profile. A body that carries only notices yields no nodes, which keeps the
// caller on the last good profile.
func yamlProfileNodes(body []byte) []proxyNode {
	var profile struct {
		Proxies []subscriptionProxy `yaml:"proxies"`
	}
	if yaml.Unmarshal(body, &profile) != nil || len(profile.Proxies) == 0 {
		return nil
	}
	nodes := make([]proxyNode, 0, len(profile.Proxies))
	for _, proxy := range profile.Proxies {
		if proxy.Name == "" || proxy.Type == "" || isInformationalNode(proxy.Name) {
			continue
		}
		wsHost := false
		for key, value := range proxy.WsOpts.Headers {
			if strings.EqualFold(key, "host") && strings.TrimSpace(stringValue(value)) != "" {
				wsHost = true
			}
		}
		nodes = append(nodes, proxyNode{
			Name: proxy.Name, Type: proxy.Type, Network: proxy.Network,
			TLS: tlsEnabled(proxy.TLS), WsHost: wsHost, WsPath: proxy.WsOpts.Path != "",
		})
	}
	return uniqueNodes(nodes)
}

func parseProfileNodes(body []byte) []proxyNode {
	_, nodes, err := parseSubscriptionBodyToProxies(body)
	if err != nil {
		return nil
	}
	return nodes
}

// domesticGroupHints name the services a subscription keeps in a group of
// their own because they are only reachable from outside the country, or
// because the template was written for users who are. Handing them to the proxy
// is the opposite of what the group exists for: 抖音 and B 站 answer a foreign
// address with an error or a much slower path, and Apple's group carries push,
// App Store, updates and iCloud, so proxying it puts half the system through the
// tunnel. Every one of these is a domestic destination for a user sitting in the
// country, which is the user this client is for.
var domesticGroupHints = []string{
	"DIRECT", "国内", "直连", "中国", "大陆", "内地", "本地", "局域网",
	"哔哩", "B站", "BILIBILI", "抖音", "DOUYIN", "快手", "KUAISHOU",
	"腾讯", "TENCENT", "网易", "NETEASE", "阿里", "ALIBABA", "ALIPAY",
	"百度", "BAIDU", "京东", "淘宝", "TAOBAO", "支付宝", "微信", "WECHAT",
	"小米", "XIAOMI", "华为", "HUAWEI", "爱奇艺", "IQIYI", "优酷", "YOUKU",
	"芒果", "咪咕", "央视", "西瓜", "苹果", "APPLE", "ICLOUD",
}

// rejectGroupHints name the ad blockers. A rule the subscription wrote to
// discard an advertisement must not be handed to the proxy instead, which would
// carry the advertisement into the tunnel rather than stopping it.
var rejectGroupHints = []string{
	"REJECT", "广告", "拦截", "屏蔽", "黑洞", "净化", "ADBLOCK", "AD-BLOCK", "BLOCK",
}

// mapGroupTarget decides where a subscription rule that names a group of its own
// should send the traffic here. The client keeps one proxy group, so a
// subscription's groups cannot be reproduced faithfully; the honest
// approximation is to read what the group's name says it is for. Anything that
// does not say domestic or blocker keeps the old behaviour and goes to the
// proxy, which is the safe direction for a rule whose meaning is unknown.
func mapGroupTarget(target string) string {
	upper := strings.ToUpper(strings.TrimSpace(target))
	if upper == "" {
		return "SmartVPN"
	}
	// PASS means "this rule decides nothing, keep looking", which is what
	// dropping it does; the caller skips an empty result.
	if upper == "PASS" {
		return ""
	}
	for _, hint := range rejectGroupHints {
		if strings.Contains(upper, hint) {
			return "REJECT"
		}
	}
	for _, hint := range domesticGroupHints {
		if strings.Contains(upper, hint) {
			return "DIRECT"
		}
	}
	return "SmartVPN"
}

// Keep only local domain and IP rules from a subscription. Remote group names
// are mapped by mapGroupTarget; the subscription cannot replace the local
// controller.
func subscriptionRules(body []byte) []string {
	var profile struct {
		Rules []string `yaml:"rules"`
	}
	if yaml.Unmarshal(body, &profile) != nil {
		return nil
	}
	allowed := map[string]bool{
		"DOMAIN": true, "DOMAIN-SUFFIX": true, "DOMAIN-KEYWORD": true,
		"IP-CIDR": true, "IP-CIDR6": true,
	}
	rules := make([]string, 0, len(profile.Rules))
	for _, rule := range profile.Rules {
		parts := strings.Split(rule, ",")
		if len(parts) < 3 || !allowed[strings.TrimSpace(parts[0])] {
			continue
		}
		matcher := strings.TrimSpace(parts[1])
		if matcher == "" || strings.ContainsAny(matcher, "\r\n") {
			continue
		}
		target := mapGroupTarget(parts[2])
		if target == "" {
			continue
		}
		clean := strings.TrimSpace(parts[0]) + "," + matcher + "," + target
		if len(parts) > 3 && strings.TrimSpace(parts[3]) == "no-resolve" {
			clean += ",no-resolve"
		}
		rules = append(rules, clean)
		if len(rules) == 3000 {
			break
		}
	}
	return rules
}

// ruleTarget reads the outbound a generated rule names.
func ruleTarget(rule string) string {
	parts := strings.Split(rule, ",")
	if len(parts) < 3 {
		return ""
	}
	return strings.TrimSpace(parts[2])
}

// splitRulesByTarget separates the rules that decide a connection themselves
// from the ones that hand it to the proxy, keeping the subscription's order
// inside each part. Everything that has to happen before traffic reaches the
// proxy — the country's own addresses going direct, QUIC being refused so a
// client that cannot relay it falls back to TCP — belongs between the two, and
// a rule the subscription wrote for the proxy must not be able to claim a
// packet first.
func splitRulesByTarget(rules []string) (decided, proxied []string) {
	for _, rule := range rules {
		switch ruleTarget(rule) {
		case "DIRECT", "REJECT":
			decided = append(decided, rule)
		default:
			proxied = append(proxied, rule)
		}
	}
	return decided, proxied
}

func stringValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

func tlsEnabled(value any) bool {
	switch item := value.(type) {
	case bool:
		return item
	case string:
		return item != "" && item != "none" && item != "false" && item != "0"
	default:
		return false
	}
}

func uniqueNodes(nodes []proxyNode) []proxyNode {
	seen := make(map[string]bool, len(nodes))
	result := make([]proxyNode, 0, len(nodes))
	for _, node := range nodes {
		if seen[node.Name] {
			continue
		}
		seen[node.Name] = true
		result = append(result, node)
	}
	return result
}

func (a *app) profilePath() string {
	return filepath.Join(a.home, "providers", "subscription.yaml")
}

func clashFormatURL(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	query := u.Query()
	if _, hasSub := query["sub"]; !hasSub || query.Has("cla") {
		return "", false
	}
	query.Del("sub")
	query.Set("cla", "1")
	u.RawQuery = query.Encode()
	return u.String(), true
}

func (a *app) profileMatchesSubscription() bool {
	profile, err := os.ReadFile(a.profilePath())
	if err != nil || len(parseProfileNodes(profile)) == 0 {
		return false
	}
	actual, err := os.ReadFile(a.profilePath() + ".source")
	if err != nil {
		return false
	}
	actualStr := strings.TrimSpace(string(actual))
	if a.settings.SubscriptionURL == mergedSubscriptionURL {
		return actualStr == mergedSubscriptionURL
	}
	return actualStr == subscriptionHash(a.settings.SubscriptionURL)
}

func parseSubscriptionUserInfo(header string) *subscriptionUserInfo {
	if strings.TrimSpace(header) == "" {
		return nil
	}
	info := &subscriptionUserInfo{}
	parts := strings.Split(header, ";")
	found := false
	for _, part := range parts {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		k := strings.ToLower(strings.TrimSpace(kv[0]))
		v := strings.TrimSpace(kv[1])
		val, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			continue
		}
		found = true
		switch k {
		case "upload":
			info.Upload = val
		case "download":
			info.Download = val
		case "total":
			info.Total = val
		case "expire":
			info.Expire = val
		}
	}
	if !found {
		return nil
	}
	return info
}

func downloadProfileWithInfo(subscriptionURL, proxyAddress string) ([]byte, []proxyNode, *subscriptionUserInfo, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if proxyAddress == "direct" {
		transport.Proxy = nil
	} else if proxyAddress != "" {
		pURL := proxyAddress
		if !strings.Contains(pURL, "://") {
			pURL = "http://" + pURL
		}
		proxyURL, err := url.Parse(pURL)
		if err != nil {
			return nil, nil, nil, errors.New("invalid local proxy address")
		}
		transport.Proxy = http.ProxyURL(proxyURL)
	} else {
		transport.Proxy = http.ProxyFromEnvironment
	}
	timeout := 15 * time.Second
	if proxyAddress == "direct" {
		timeout = 7 * time.Second
	}
	client := http.Client{Timeout: timeout, Transport: transport}
	request, err := http.NewRequest(http.MethodGet, subscriptionURL, nil)
	if err != nil {
		return nil, nil, nil, errors.New("invalid subscription URL")
	}
	request.Header.Set("User-Agent", subscriptionUserAgent)
	response, err := client.Do(request)
	if err != nil {
		return nil, nil, nil, errSubscriptionNetwork
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, nil, nil, errors.New("subscription server returned a non-success status")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 10*1024*1024+1))
	if err != nil || len(body) > 10*1024*1024 {
		return nil, nil, nil, errors.New("subscription response could not be read or is too large")
	}
	nodes := parseProfileNodes(body)
	if len(nodes) == 0 {
		return nil, nil, nil, errNoUsableNodes
	}
	var userInfo *subscriptionUserInfo
	if uHeader := response.Header.Get("Subscription-Userinfo"); uHeader != "" {
		userInfo = parseSubscriptionUserInfo(uHeader)
	}
	return body, nodes, userInfo, nil
}

func downloadProfile(subscriptionURL, proxyAddress string) ([]byte, []proxyNode, error) {
	body, nodes, _, err := downloadProfileWithInfo(subscriptionURL, proxyAddress)
	return body, nodes, err
}

func (a *app) proxyAddressesForDownloadLocked() []string {
	var list []string
	if a.kernelRunningLocked() && a.mixedPort > 0 {
		list = append(list, net.JoinHostPort("127.0.0.1", strconv.Itoa(a.mixedPort)))
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

func (a *app) downloadSubscriptionWithFallback(subscriptionURL string) ([]byte, []proxyNode, *subscriptionUserInfo, error) {
	candidates := []string{subscriptionURL}
	if alt, ok := clashFormatURL(subscriptionURL); ok {
		candidates = append(candidates, alt)
	}
	proxies := a.proxyAddressesForDownloadLocked()
	var lastErr error
	for _, proxy := range proxies {
		for _, u := range candidates {
			body, nodes, uinfo, err := downloadProfileWithInfo(u, proxy)
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

// fetchProfileLocked keeps the last good profile when a provider returns notices.
// The caller holds a.mu while changing the selected subscription or node list.
func (a *app) fetchProfileLocked() ([]proxyNode, error) {
	if a.settings.SubscriptionURL == "" {
		return nil, errors.New("set a subscription URL first")
	}
	if a.settings.SubscriptionURL == mergedSubscriptionURL {
		return a.mergeAllEnabledSubscriptionsLocked()
	}
	remoteURLs, inlineLines := categorizeSubscriptionInput(a.settings.SubscriptionURL)
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
		clashBody, clashNodes, err := ensureClashYAML(inlineBody)
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
				response, parsed, err := downloadProfile(attempt.address, proxyAddress)
				if err == nil {
					clashBody, clashNodes, convErr := ensureClashYAML(response)
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
			mergedBody, mergedNodes, err := mergeSubscriptionProfiles(downloaded)
			if err == nil && len(mergedNodes) > 0 {
				body = mergedBody
				nodes = mergedNodes
				chosenFormat = fmt.Sprintf("merged-%d", len(downloaded))
			}
		}
	}

	if chosenFormat == "" {
		if a.profileMatchesSubscription() {
			profile, readErr := os.ReadFile(a.profilePath())
			if readErr == nil {
				nodes = parseProfileNodes(profile)
				for i := range nodes {
					nodes[i].Selected = nodes[i].Name == a.settings.SelectedNode
				}
				a.cachedNodes = append([]proxyNode(nil), nodes...)
				a.profileFromCache = true
				return nodes, nil
			}
		}
		if entry, ok := a.findSubscriptionByURL(a.settings.SubscriptionURL); ok {
			if cached, readErr := os.ReadFile(a.profileCachePath(entry.ID)); readErr == nil && len(cached) > 0 {
				nodes = parseProfileNodes(cached)
				if len(nodes) > 0 {
					_ = a.useCachedProfileLocked(entry.ID)
					a.profileFromCache = true
					return nodes, nil
				}
			}
		}
		if profile, readErr := os.ReadFile(a.profilePath()); readErr == nil && len(profile) > 0 {
			nodes = parseProfileNodes(profile)
			if len(nodes) > 0 {
				for i := range nodes {
					nodes[i].Selected = nodes[i].Name == a.settings.SelectedNode
				}
				a.cachedNodes = append([]proxyNode(nil), nodes...)
				a.profileFromCache = true
				return nodes, nil
			}
		}
		if len(a.cachedNodes) > 0 {
			a.profileFromCache = true
			return a.cachedNodes, nil
		}
		if hadNoNodes {
			return nil, errNoUsableNodes
		}
		return nil, errSubscriptionNetwork
	}
	path := a.profilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, errors.New("could not create the local profile directory")
	}
	tempPath := path + ".tmp"
	if err := os.WriteFile(tempPath, body, 0600); err != nil {
		return nil, errors.New("could not save the local profile")
	}
	if err := os.Rename(tempPath, path); err != nil {
		_ = os.Remove(tempPath)
		return nil, errors.New("could not update the local profile")
	}
	if err := os.WriteFile(path+".source", []byte(subscriptionHash(a.settings.SubscriptionURL)), 0600); err != nil {
		return nil, errors.New("could not save the profile source")
	}
	if err := os.WriteFile(path+".format", []byte(chosenFormat), 0600); err != nil {
		return nil, errors.New("could not save the profile format")
	}
	// A fresh copy is what a later switch falls back to, and the subscription
	// list shows when it was last good.
	a.profileFromCache = false
	a.cacheProfileLocked(body, chosenFormat)
	a.recordSubscriptionUseLocked(subscriptionHash(a.settings.SubscriptionURL), len(nodes), "")
	for i := range nodes {
		nodes[i].Selected = nodes[i].Name == a.settings.SelectedNode
	}
	a.cachedNodes = append([]proxyNode(nil), nodes...)
	if err := a.saveCachedNodesLocked(nodes); err != nil {
		return nil, errors.New("could not save the fetched node list")
	}
	return nodes, nil
}
