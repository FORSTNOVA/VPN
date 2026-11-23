package main

import (
	"errors"
	"strings"
)

func diagnoseQualityFromFacts(f diagnoseQualityInput) ipQualityFacts {
	if !f.Valid {
		return ipQualityFacts{IPType: diagnoseQualityUnknown(), RiskLevel: diagnoseQualityUnknown()}
	}
	if !f.Hosting {
		for _, word := range []string{"cloudflare", "amazon", "digitalocean", "microsoft", "google", "oracle", "alibaba", "tencent", "hetzner", "ovh", "vultr", "linode", "choopa", "m247", "data center", "hosting"} {
			if strings.Contains(f.Lower, word) {
				f.Hosting = true
				break
			}
		}
	}
	score := 5
	kind := diagnoseQualityResidential()
	if f.Hosting {
		score = 25
		kind = diagnoseQualityHosting()
	}
	if f.Proxy || f.VPN {
		score += 30
	}
	if f.Tor {
		score += 50
	}
	if f.Anonymous {
		score += 15
	}
	if score > 100 {
		score = 100
	}
	risk := diagnoseQualityLow()
	if score > 60 {
		risk = diagnoseQualityHigh()
	} else if score > 25 {
		risk = diagnoseQualityMedium()
	}
	return ipQualityFacts{IPType: kind, FraudScore: score, RiskLevel: risk, IsProxy: f.Proxy, IsVPN: f.VPN, IsTor: f.Tor, IsHosting: f.Hosting, IsNative: !f.Hosting}
}
func runGoDiagnoseService(q diagnoseServiceRequest, fx diagnoseServiceEffects) (result diagnoseServiceResult) {
	defer fx.Release()
	defer func() {
		if f, ok := fx.(interface {
			FinalizeDiagnoseResult(diagnoseServiceRequest, diagnoseServiceResult) diagnoseServiceResult
		}); ok {
			result = f.FinalizeDiagnoseResult(q, result)
		}
	}()
	call := fx.Call
	initial := "snapshot"
	if q.Op == "diagnose" {
		initial = "lock"
	}
	s := call(diagnoseEffect{Kind: initial}).State
	ensure := func() (bool, error) {
		if call(diagnoseEffect{Kind: "running"}).Bool {
			return false, nil
		}
		r := call(diagnoseEffect{Kind: "start"})
		if r.Err != nil {
			return false, r.Err
		}
		s = call(diagnoseEffect{Kind: "choice"}).State
		choice := s.Choice
		if choice == chainExit && s.Chain {
			if err := call(diagnoseEffect{Kind: "put", Name: choice}).Err; err != nil {
				call(diagnoseEffect{Kind: "stop"})
				return false, err
			}
		} else if choice == autoGroup || choice == fallbackGroup {
			call(diagnoseEffect{Kind: "put", Name: choice})
		} else if choice != "" {
			r := call(diagnoseEffect{Kind: "list"})
			if r.Err == nil {
				for _, name := range r.Names {
					if name == choice {
						call(diagnoseEffect{Kind: "put", Name: name})
						break
					}
				}
			}
		}
		return true, nil
	}
	details := func() (egressFacts, ipQualityFacts, error) {
		var last error
		for i := 0; i < s.DetailsCount; i++ {
			r := call(diagnoseEffect{Kind: "detail", Index: i})
			if r.Err != nil {
				last = r.Err
				continue
			}
			quality := diagnoseQualityFromFacts(call(diagnoseEffect{Kind: "quality"}).Quality)
			return r.Egress, quality, nil
		}
		if last == nil {
			last = errors.New(diagnoseNoDetailsMessage())
		}
		return egressFacts{}, ipQualityFacts{}, last
	}
	dns := func() dnsFacts {
		r := call(diagnoseEffect{Kind: "resolver"})
		if r.Err != nil {
			return dnsFacts{Error: r.Err.Error()}
		}
		f := r.DNS
		if r.Bool {
			if country := call(diagnoseEffect{Kind: "country", Address: f.Resolver}); country.Err == nil {
				f.Country = country.Address
			}
		}
		return f
	}
	stun := func() (string, int64, error) {
		if call(diagnoseEffect{Kind: "stun-build"}).Err != nil {
			return "", 0, errors.New(diagnoseSTUNBuildMessage())
		}
		var last error
		for i := 0; i < s.STUNCount; i++ {
			r := call(diagnoseEffect{Kind: "stun-exchange", Index: i})
			if r.Err != nil {
				last = r.Err
				continue
			}
			return r.Mapped, r.Elapsed, nil
		}
		if last == nil {
			last = errors.New(diagnoseSTUNEmptyMessage())
		}
		return "", 0, last
	}
	switch q.Op {
	case "ensure":
		temporary, err := ensure()
		return diagnoseServiceResult{Temporary: temporary, Err: err}
	case "details":
		f, quality, err := details()
		return diagnoseServiceResult{Egress: f, Quality: quality, Err: err}
	case "dns":
		return diagnoseServiceResult{DNS: dns()}
	case "stun":
		mapped, elapsed, err := stun()
		return diagnoseServiceResult{Mapped: mapped, Elapsed: elapsed, Err: err}
	}
	temporary, err := ensure()
	respond := func(status int, message string, report diagnoseReport) diagnoseServiceResult {
		call(diagnoseEffect{Kind: "response", Status: status, Message: message, Report: report})
		if temporary {
			call(diagnoseEffect{Kind: "stop"})
		}
		call(diagnoseEffect{Kind: "unlock"})
		return diagnoseServiceResult{Status: status, Message: message, Report: report}
	}
	if err != nil {
		return respond(502, err.Error(), diagnoseReport{})
	}
	selected := ""
	r := call(diagnoseEffect{Kind: "get", Path: "/proxies/SmartVPN"})
	if r.Err == nil {
		selected, _ = r.Value.(string)
		if selected == autoGroup || selected == fallbackGroup {
			nested := call(diagnoseEffect{Kind: "get", Path: "/proxies/" + selected})
			if nested.Err == nil {
				selected, _ = nested.Value.(string)
			}
		}
	}
	if selected == "" || strings.EqualFold(selected, blockedChoice) {
		return respond(409, "select a subscription node before checking", diagnoseReport{})
	}
	call(diagnoseEffect{Kind: "context"})
	report := diagnoseReport{Node: selected, Temporary: temporary, Sites: make([]siteCheckResult, len(s.SiteNames))}
	report.Egress.IPv4 = call(diagnoseEffect{Kind: "ipv4"}).Address
	if report.Egress.IPv4 == "" {
		report.Egress.IPv4 = call(diagnoseEffect{Kind: "trace4"}).Address
	}
	if report.Egress.IPv4 == "" {
		report.Egress.Error = diagnoseExitFailureMessage()
		for i, name := range s.SiteNames {
			report.Sites[i] = siteCheckResult{Name: name, State: "skipped", Detail: diagnoseSkippedMessage()}
		}
		return respond(200, "", report)
	}
	report.Egress.IPv6 = call(diagnoseEffect{Kind: "ipv6"}).Address
	if f, quality, err := details(); err != nil {
		report.Egress.Error = err.Error()
	} else {
		if report.Egress.IPv4 == "" && f.IPv4 != "" {
			report.Egress.IPv4 = f.IPv4
		}
		if report.Egress.IPv6 == "" && f.IPv6 != "" {
			report.Egress.IPv6 = f.IPv6
		}
		report.Egress.Country = f.Country
		report.Egress.CountryCode = f.CountryCode
		report.Egress.City = f.City
		report.Egress.ASN = f.ASN
		report.Egress.ISP = f.ISP
		report.Egress.Org = f.Org
		report.Quality = quality
	}
	report.DNS = dns()
	report.UDP.Relay = call(diagnoseEffect{Kind: "relay"}).Relay
	if mapped, _, err := stun(); err != nil {
		report.UDP.Error = err.Error()
	} else {
		report.UDP.Mapped = mapped
	}
	indices := make([]int, len(s.SiteNames))
	for i := range indices {
		indices[i] = i
	}
	call(diagnoseEffect{Kind: "sites-start", Indices: indices})
	report.Sites = call(diagnoseEffect{Kind: "sites-join"}).Sites
	v := call(diagnoseEffect{Kind: "verdict", Report: report})
	report.DNS.MatchesExit = v.DNSMatch
	report.UDP.MatchesExit = v.UDPMatch
	report.Quality.WebRTCLeak = report.UDP.Mapped != "" && !report.UDP.MatchesExit
	report.Quality.DNSLeak = report.DNS.Country != "" && report.Egress.CountryCode != "" && !report.DNS.MatchesExit
	return respond(200, "", report)
}
