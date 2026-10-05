package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The check page follows one shape borrowed from the IP toolboxes: a few sections
// of facts, each row carrying its own verdict, produced by a single request. This
// file assembles that report; the probes themselves live next to the features
// they belong to.
type egressFacts struct {
	IPv4        string `json:"ipv4,omitempty"`
	IPv6        string `json:"ipv6,omitempty"`
	Country     string `json:"country,omitempty"`
	CountryCode string `json:"countryCode,omitempty"`
	City        string `json:"city,omitempty"`
	ASN         string `json:"asn,omitempty"`
	ISP         string `json:"isp,omitempty"`
	Org         string `json:"org,omitempty"`
	Error       string `json:"error,omitempty"`
}

type ipQualityFacts struct {
	IPType     string `json:"ipType"`
	FraudScore int    `json:"fraudScore"`
	RiskLevel  string `json:"riskLevel"`
	IsProxy    bool   `json:"isProxy"`
	IsVPN      bool   `json:"isVpn"`
	IsTor      bool   `json:"isTor"`
	IsHosting  bool   `json:"isHosting"`
	IsNative   bool   `json:"isNative"`
	WebRTCLeak bool   `json:"webrtcLeak"`
	DNSLeak    bool   `json:"dnsLeak"`
}

type dnsFacts struct {
	Resolver string `json:"resolver,omitempty"`
	Geo      string `json:"geo,omitempty"`
	Country  string `json:"country,omitempty"`
	// Whether the resolver that answered belongs to the same country as the exit
	// address. An unknown country on either side is not a match.
	MatchesExit bool   `json:"matchesExit"`
	Error       string `json:"error,omitempty"`
}

type udpFacts struct {
	Relay  udpRelayResult `json:"relay"`
	Mapped string         `json:"mapped,omitempty"`
	// Whether the address a STUN server saw is the exit address. When it is not,
	// the datagrams are leaving through something other than the tunnel.
	MatchesExit bool   `json:"matchesExit"`
	Error       string `json:"error,omitempty"`
}

type diagnoseReport struct {
	Node      string            `json:"node"`
	Temporary bool              `json:"temporary"`
	Egress    egressFacts       `json:"egress"`
	DNS       dnsFacts          `json:"dns"`
	UDP       udpFacts          `json:"udp"`
	Quality   ipQualityFacts    `json:"quality"`
	Sites     []siteCheckResult `json:"sites"`
}

// egressSources are tried in order; the second is a fallback for the first
// changing shape. Both are HTTPS, which matters: these answers must not be
// rewritable by the node they are fetched through.
var egressSources = []struct {
	url   string
	parse func([]byte) (egressFacts, error)
}{
	{"https://ipwho.is/", parseIpwhois},
	{"https://api.ip.sb/geoip", parseIpSb},
}

func diagnosableClient(proxyAddress string, timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	if parsed, err := url.Parse("http://" + proxyAddress); err == nil {
		transport.Proxy = http.ProxyURL(parsed)
	}
	transport.DisableKeepAlives = true
	return &http.Client{Timeout: timeout, Transport: transport}
}

func fetchBody(client *http.Client, address string) ([]byte, error) {
	request, err := http.NewRequest(http.MethodGet, address, nil)
	if err != nil {
		return nil, errors.New("检测地址无效")
	}
	request.Header.Set("User-Agent", subscriptionUserAgent)
	response, err := client.Do(request)
	if err != nil {
		return nil, errors.New("请求没有到达")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("服务返回 HTTP %d", response.StatusCode)
	}
	return io.ReadAll(io.LimitReader(response.Body, 64*1024))
}

// fetchEgressDetails asks a geolocation service, through the node, what it sees.
// It is the only way to learn the operator and the exit country of the address
// the tunnel actually presents.
func fetchEgressDetails(proxyAddress string) (egressFacts, ipQualityFacts, error) {
	client := diagnosableClient(proxyAddress, 8*time.Second)
	var lastErr error
	for _, source := range egressSources {
		body, err := fetchBody(client, source.url)
		if err != nil {
			lastErr = err
			continue
		}
		facts, err := source.parse(body)
		if err != nil {
			lastErr = err
			continue
		}
		quality := parseIpQuality(body)
		return facts, quality, nil
	}
	if lastErr == nil {
		lastErr = errors.New("没有可用的出口信息源")
	}
	return egressFacts{}, ipQualityFacts{}, lastErr
}

func parseIpQuality(body []byte) ipQualityFacts {
	var answer struct {
		Connection struct {
			ASN int    `json:"asn"`
			ISP string `json:"isp"`
			Org string `json:"org"`
		} `json:"connection"`
		Security struct {
			Anonymous bool `json:"anonymous"`
			Proxy     bool `json:"proxy"`
			VPN       bool `json:"vpn"`
			Tor       bool `json:"tor"`
			Hosting   bool `json:"hosting"`
		} `json:"security"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		return ipQualityFacts{IPType: "未知", RiskLevel: "未知"}
	}

	isHosting := answer.Security.Hosting
	isProxy := answer.Security.Proxy
	isVPN := answer.Security.VPN
	isTor := answer.Security.Tor

	ispLower := strings.ToLower(answer.Connection.ISP + " " + answer.Connection.Org)
	if !isHosting && (strings.Contains(ispLower, "cloudflare") ||
		strings.Contains(ispLower, "amazon") ||
		strings.Contains(ispLower, "digitalocean") ||
		strings.Contains(ispLower, "microsoft") ||
		strings.Contains(ispLower, "google") ||
		strings.Contains(ispLower, "oracle") ||
		strings.Contains(ispLower, "alibaba") ||
		strings.Contains(ispLower, "tencent") ||
		strings.Contains(ispLower, "hetzner") ||
		strings.Contains(ispLower, "ovh") ||
		strings.Contains(ispLower, "vultr") ||
		strings.Contains(ispLower, "linode") ||
		strings.Contains(ispLower, "choopa") ||
		strings.Contains(ispLower, "m247") ||
		strings.Contains(ispLower, "data center") ||
		strings.Contains(ispLower, "hosting")) {
		isHosting = true
	}

	score := 5
	ipType := "原生住宅 (Residential / ISP)"
	if isHosting {
		ipType = "机房/数据中心 (Data Center / Hosting)"
		score = 25
	}
	if isProxy || isVPN {
		score += 30
	}
	if isTor {
		score += 50
	}
	if answer.Security.Anonymous {
		score += 15
	}
	if score > 100 {
		score = 100
	}

	riskLevel := "低风险 (良好)"
	if score > 60 {
		riskLevel = "高风险 (已标记)"
	} else if score > 25 {
		riskLevel = "中风险 (机房/代理)"
	}

	return ipQualityFacts{
		IPType:     ipType,
		FraudScore: score,
		RiskLevel:  riskLevel,
		IsProxy:    isProxy,
		IsVPN:      isVPN,
		IsTor:      isTor,
		IsHosting:  isHosting,
		IsNative:   !isHosting,
	}
}

func parseIpwhois(body []byte) (egressFacts, error) {
	var answer struct {
		Success     bool   `json:"success"`
		IP          string `json:"ip"`
		Country     string `json:"country"`
		CountryCode string `json:"country_code"`
		City        string `json:"city"`
		Connection  struct {
			ASN int    `json:"asn"`
			ISP string `json:"isp"`
			Org string `json:"org"`
		} `json:"connection"`
	}
	if err := json.Unmarshal(body, &answer); err != nil || answer.IP == "" {
		return egressFacts{}, errors.New("出口信息响应无法解析")
	}
	asn := ""
	if answer.Connection.ASN > 0 {
		asn = "AS" + strconv.Itoa(answer.Connection.ASN)
	}
	return withAddress(egressFacts{
		Country: answer.Country, CountryCode: answer.CountryCode, City: answer.City,
		ASN: asn, ISP: answer.Connection.ISP, Org: answer.Connection.Org,
	}, answer.IP), nil
}

func parseIpSb(body []byte) (egressFacts, error) {
	var answer struct {
		IP           string `json:"ip"`
		Country      string `json:"country"`
		CountryCode  string `json:"country_code"`
		City         string `json:"city"`
		ASN          any    `json:"asn"`
		ISP          string `json:"isp"`
		Organization string `json:"organization"`
		Org          string `json:"org"`
	}
	if err := json.Unmarshal(body, &answer); err != nil || answer.IP == "" {
		return egressFacts{}, errors.New("出口信息响应无法解析")
	}
	return withAddress(egressFacts{
		Country: answer.Country, CountryCode: answer.CountryCode, City: answer.City,
		ASN: asnLabel(answer.ASN), ISP: answer.ISP,
		Org: firstNonEmpty(answer.Organization, answer.Org),
	}, answer.IP), nil
}

// withAddress files the address under the family it actually belongs to, so a
// node with an IPv6 exit is described correctly.
func withAddress(facts egressFacts, address string) egressFacts {
	if ip := net.ParseIP(strings.TrimSpace(address)); ip != nil && ip.To4() == nil {
		facts.IPv6 = ip.String()
		return facts
	}
	facts.IPv4 = strings.TrimSpace(address)
	return facts
}

// asnLabel accepts the two shapes an ASN arrives in.
func asnLabel(raw any) string {
	switch value := raw.(type) {
	case float64:
		if value > 0 {
			return "AS" + strconv.Itoa(int(value))
		}
	case string:
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return ""
		}
		if strings.HasPrefix(strings.ToUpper(trimmed), "AS") {
			return trimmed
		}
		return "AS" + trimmed
	}
	return ""
}

// checkDNSLeak asks a service that reports which resolver asked for the name we
// just resolved. The endpoint answers with a redirect to a freshly generated
// name, so the answer describes the resolution that has just happened instead of
// one a resolver had cached.
func checkDNSLeak(proxyAddress string) dnsFacts {
	body, err := fetchBody(diagnosableClient(proxyAddress, 8*time.Second),
		"https://edns.ip-api.com/json")
	if err != nil {
		return dnsFacts{Error: err.Error()}
	}
	var answer struct {
		DNS struct {
			IP  string `json:"ip"`
			Geo string `json:"geo"`
		} `json:"dns"`
	}
	if err := json.Unmarshal(body, &answer); err != nil || answer.DNS.IP == "" {
		return dnsFacts{Error: "解析器归属服务没有返回结果"}
	}
	facts := dnsFacts{Resolver: answer.DNS.IP, Geo: answer.DNS.Geo}
	// The verdict compares countries, and the label above is a sentence rather
	// than a code, so the resolver's address is looked up instead.
	if country, err := lookupCountry(proxyAddress, answer.DNS.IP); err == nil {
		facts.Country = country
	}
	return facts
}

// lookupCountry asks the geolocation source about one address, which is how the
// resolver's country is compared with the exit's.
func lookupCountry(proxyAddress, address string) (string, error) {
	if net.ParseIP(strings.TrimSpace(address)) == nil {
		return "", errors.New("地址无效")
	}
	body, err := fetchBody(diagnosableClient(proxyAddress, 8*time.Second),
		"https://ipwho.is/"+strings.TrimSpace(address))
	if err != nil {
		return "", err
	}
	facts, err := parseIpwhois(body)
	if err != nil {
		return "", err
	}
	if facts.CountryCode == "" {
		return "", errors.New("归属地没有给出国家代码")
	}
	return facts.CountryCode, nil
}

// dnsMatchesExit compares the resolver's country with the exit's. An unknown
// country on either side is not a match, and the row says so.
func dnsMatchesExit(resolverCountry, exitCountry string) bool {
	return resolverCountry != "" && exitCountry != "" &&
		strings.EqualFold(resolverCountry, exitCountry)
}

// udpMatchesExit compares the address a STUN server saw with an exit address.
// Only the address: the port is the NAT mapping's.
func udpMatchesExit(mapped, exitAddress string) bool {
	host := hostOf(mapped)
	if host == "" {
		return false
	}
	return host == strings.TrimSpace(exitAddress)
}

func hostOf(address string) string {
	if host, _, err := net.SplitHostPort(strings.TrimSpace(address)); err == nil {
		return host
	}
	return ""
}

// markSitesSkipped fills the site rows for a pass that stopped early, so the
// page can say "not checked" rather than showing a failure it did not measure.
func markSitesSkipped(sites []siteCheckResult) {
	for i, target := range checkTargets {
		sites[i] = siteCheckResult{Name: target.name, State: "skipped", Detail: "出口不通，未检测"}
	}
}

// diagnose runs every check the page shows, once. It is the batch the page is
// built around: one node, one kernel, one report.
func (a *app) diagnose(w http.ResponseWriter, _ *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()

	temporary, err := a.ensureKernelForCheckLocked()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if temporary {
		defer a.stopMihomoLocked()
	}
	selected := a.activeNodeLocked()
	if selected == "" || strings.EqualFold(selected, blockedChoice) {
		http.Error(w, "select a subscription node before checking", http.StatusConflict)
		return
	}

	proxyAddress := net.JoinHostPort("127.0.0.1", strconv.Itoa(a.mixedPort))
	proxyURL := &url.URL{Scheme: "http", Host: proxyAddress}
	report := diagnoseReport{
		Node: selected, Temporary: temporary,
		Sites: make([]siteCheckResult, len(checkTargets)),
	}

	// The sections run one after another rather than all at once. A relay that
	// carries a handful of connections happily starts dropping when a check opens
	// nine at the same moment, and a check that fails because of its own burst is
	// worse than a slow one: one pass then reports the line, not the load.
	report.Egress.IPv4 = checkExitIP(proxyURL, exitIPProbeURL(false), false).Address
	if report.Egress.IPv4 == "" {
		// A Cloudflare trace is a second opinion on the same question.
		report.Egress.IPv4 = checkCloudflareExitIP(proxyURL, cloudflareTraceURL).Address
	}
	if report.Egress.IPv4 == "" {
		// Nothing is leaving through the tunnel, so the rest would only be a row
		// of timeouts. Saying so at once is both faster and more honest.
		report.Egress.Error = "隧道出口不通（请求没有到达）"
		markSitesSkipped(report.Sites)
		writeJSON(w, report)
		return
	}
	report.Egress.IPv6 = checkExitIP(proxyURL, exitIPProbeURL(true), true).Address

	if details, quality, err := fetchEgressDetails(proxyAddress); err != nil {
		report.Egress.Error = err.Error()
	} else {
		// The exit probes are the authority on the address; the details service
		// only fills in what it knows about it, and stands in when they failed.
		if report.Egress.IPv4 == "" && details.IPv4 != "" {
			report.Egress.IPv4 = details.IPv4
		}
		if report.Egress.IPv6 == "" && details.IPv6 != "" {
			report.Egress.IPv6 = details.IPv6
		}
		report.Egress.Country = details.Country
		report.Egress.CountryCode = details.CountryCode
		report.Egress.City = details.City
		report.Egress.ASN = details.ASN
		report.Egress.ISP = details.ISP
		report.Egress.Org = details.Org
		report.Quality = quality
	}

	report.DNS = checkDNSLeak(proxyAddress)

	report.UDP.Relay = probeUDPRelay(proxyAddress, udpRelayTarget, udpRelayTimeout)
	if mapped, _, err := probeUDPEgress(proxyAddress, udpRelayTimeout); err != nil {
		report.UDP.Error = err.Error()
	} else {
		report.UDP.Mapped = mapped
	}

	// The sites are checked concurrently: that is the same width the speed test uses.
	var pending sync.WaitGroup
	for i, target := range checkTargets {
		pending.Add(1)
		go func(i int, name, address string) {
			defer pending.Done()
			report.Sites[i] = checkSite(proxyURL, name, address)
		}(i, target.name, target.address)
	}
	pending.Wait()

	// The verdicts are decided here, once, so the page only has to render them.
	report.DNS.MatchesExit = dnsMatchesExit(report.DNS.Country, report.Egress.CountryCode)
	report.UDP.MatchesExit = report.UDP.Mapped != "" &&
		(udpMatchesExit(report.UDP.Mapped, report.Egress.IPv4) ||
			udpMatchesExit(report.UDP.Mapped, report.Egress.IPv6))

	// WebRTC / STUN 泄露检测：若 STUN 映射地址有效且与 TCP 出口不一致，标记为 WebRTC 泄露
	report.Quality.WebRTCLeak = report.UDP.Mapped != "" && !report.UDP.MatchesExit
	// DNS 跨国泄露检测：若 DNS 解析器非空且与出口国家不匹配，标记为 DNS 泄露
	report.Quality.DNSLeak = report.DNS.Country != "" && !report.DNS.MatchesExit

	if report.Quality.WebRTCLeak || report.Quality.DNSLeak {
		if report.Quality.FraudScore < 50 {
			report.Quality.FraudScore += 20
			if report.Quality.FraudScore > 60 {
				report.Quality.RiskLevel = "高风险 (存在泄露)"
			} else {
				report.Quality.RiskLevel = "中风险 (存在泄露)"
			}
		}
	}

	writeJSON(w, report)
}
