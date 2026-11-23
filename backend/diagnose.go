package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
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
	r := (*app)(nil).runDiagnoseService(diagnoseServiceRequest{Op: "details", ProxyAddress: proxyAddress}, nil)
	return r.Egress, r.Quality, r.Err
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
	return (*app)(nil).runDiagnoseService(diagnoseServiceRequest{Op: "dns", ProxyAddress: proxyAddress}, nil).DNS
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
	r := a.runDiagnoseService(diagnoseServiceRequest{Op: "diagnose"}, w)
	if r.Err != nil {
		log.Print("diagnostic workflow incomplete")
	}
}
